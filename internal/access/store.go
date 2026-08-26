package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Quotas struct {
	RequestsPerMinute int `json:"requests_per_minute"`
	DatasetsPerMinute int `json:"datasets_per_minute"`
	ConcurrentBuilds  int `json:"concurrent_builds"`
	LiveConnections   int `json:"live_connections"`
	LiveSymbols       int `json:"live_symbols"`
}

func DefaultQuotas(role string) Quotas {
	if role == "admin" {
		return Quotas{RequestsPerMinute: 3000, DatasetsPerMinute: 120, ConcurrentBuilds: 8, LiveConnections: 10, LiveSymbols: 200}
	}
	return Quotas{RequestsPerMinute: 600, DatasetsPerMinute: 20, ConcurrentBuilds: 2, LiveConnections: 3, LiveSymbols: 200}
}

type Principal struct {
	UserID    string    `json:"id"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	KeyID     string    `json:"-"`
	KeyName   string    `json:"key_name"`
	KeyPrefix string    `json:"key_prefix"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Scopes    []string  `json:"scopes"`
	Quotas    Quotas    `json:"quotas"`
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok
}

func (p Principal) HasScope(want string) bool {
	for _, scope := range p.Scopes {
		if scope == want || scope == "admin" {
			return true
		}
	}
	return false
}

type User struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Enabled bool   `json:"enabled"`
}

type APIKey struct {
	Prefix    string    `json:"prefix"`
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
	Revoked   bool      `json:"revoked"`
}

type Store struct {
	db          *sql.DB
	legacyToken string
}

func Open(path, legacyToken string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE, role TEXT NOT NULL CHECK(role IN ('member','admin')), enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS api_keys (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), name TEXT NOT NULL, prefix TEXT NOT NULL UNIQUE, secret_hash BLOB NOT NULL, scopes TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, last_used_at INTEGER, revoked_at INTEGER)`,
		`CREATE INDEX IF NOT EXISTS api_keys_user ON api_keys(user_id)`,
		`CREATE TABLE IF NOT EXISTS quota_overrides (user_id TEXT PRIMARY KEY REFERENCES users(id), requests_per_minute INTEGER, datasets_per_minute INTEGER, concurrent_builds INTEGER, live_connections INTEGER, live_symbols INTEGER)`,
		`CREATE TABLE IF NOT EXISTS user_watchlists (user_id TEXT NOT NULL REFERENCES users(id), symbol TEXT NOT NULL, PRIMARY KEY(user_id,symbol))`,
		`CREATE TABLE IF NOT EXISTS usage_daily (user_id TEXT NOT NULL, local_date TEXT NOT NULL, requests INTEGER NOT NULL DEFAULT 0, datasets INTEGER NOT NULL DEFAULT 0, failures INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(user_id,local_date))`,
		`CREATE TABLE IF NOT EXISTS audit_events (id INTEGER PRIMARY KEY AUTOINCREMENT, occurred_at INTEGER NOT NULL, request_id TEXT NOT NULL, user_id TEXT NOT NULL, key_id TEXT NOT NULL, method TEXT NOT NULL, route TEXT NOT NULL, status INTEGER NOT NULL, duration_ms INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS audit_events_time ON audit_events(occurred_at)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	if legacyToken != "" {
		if _, err := db.Exec(`INSERT OR IGNORE INTO users(id,name,role,enabled,created_at) VALUES('legacy-admin','legacy-admin','admin',1,?)`, time.Now().Unix()); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db, legacyToken: legacyToken}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func scopesForRole(role string) []string {
	scopes := []string{"history:read", "live:read", "profile:read"}
	if role == "admin" {
		scopes = append(scopes, "provider:usage", "admin")
	}
	return scopes
}

func (s *Store) Authenticate(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, errors.New("missing bearer token")
	}
	if s.legacyToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.legacyToken)) == 1 {
		return Principal{UserID: "legacy-admin", Name: "legacy-admin", Role: "admin", KeyID: "legacy", KeyName: "GO_SERVER_TOKEN", KeyPrefix: "legacy", Scopes: scopesForRole("admin"), Quotas: DefaultQuotas("admin")}, nil
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "mbk_") {
		return Principal{}, errors.New("invalid API key")
	}
	prefix := strings.TrimPrefix(parts[0], "mbk_")
	var p Principal
	var enabled int
	var hash []byte
	var scopes string
	var expires int64
	var revoked sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT u.id,u.name,u.role,u.enabled,k.id,k.name,k.secret_hash,k.scopes,k.expires_at,k.revoked_at FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.prefix=?`, prefix).Scan(&p.UserID, &p.Name, &p.Role, &enabled, &p.KeyID, &p.KeyName, &hash, &scopes, &expires, &revoked)
	if err != nil {
		return Principal{}, errors.New("invalid API key")
	}
	want := sha256.Sum256([]byte(token))
	if enabled == 0 || revoked.Valid || expires <= time.Now().Unix() || subtle.ConstantTimeCompare(hash, want[:]) != 1 {
		return Principal{}, errors.New("invalid API key")
	}
	p.KeyPrefix, p.ExpiresAt = prefix, time.Unix(expires, 0).UTC()
	p.Scopes = splitScopes(scopes)
	p.Quotas, err = s.Quotas(ctx, p.UserID, p.Role)
	if err != nil {
		return Principal{}, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at=? WHERE id=? AND (last_used_at IS NULL OR last_used_at<?)`, time.Now().Unix(), p.KeyID, time.Now().Add(-5*time.Minute).Unix())
	return p, nil
}

func splitScopes(v string) []string {
	var out []string
	for _, scope := range strings.Split(v, ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			out = append(out, scope)
		}
	}
	return out
}

func randomHex(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) CreateUser(ctx context.Context, name, role string) (User, error) {
	name, role = strings.TrimSpace(name), strings.ToLower(strings.TrimSpace(role))
	if name == "" || strings.EqualFold(name, "legacy-admin") || (role != "member" && role != "admin") {
		return User{}, errors.New("name and role member|admin are required")
	}
	id, err := randomHex(16)
	if err != nil {
		return User{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO users(id,name,role,enabled,created_at) VALUES(?,?,?,?,?)`, id, name, role, 1, time.Now().Unix())
	return User{ID: id, Name: name, Role: role, Enabled: true}, err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,role,enabled FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var enabled int
		if err := rows.Scan(&u.ID, &u.Name, &u.Role, &enabled); err != nil {
			return nil, err
		}
		u.Enabled = enabled == 1
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Store) SetUserEnabled(ctx context.Context, name string, enabled bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE users SET enabled=? WHERE name=?`, boolInt(enabled), name)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return errors.New("user not found")
	}
	return nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Store) CreateKey(ctx context.Context, userName, keyName string, validFor time.Duration) (string, APIKey, error) {
	if validFor <= 0 {
		validFor = 365 * 24 * time.Hour
	}
	var userID, role string
	var enabled int
	if err := s.db.QueryRowContext(ctx, `SELECT id,role,enabled FROM users WHERE name=?`, userName).Scan(&userID, &role, &enabled); err != nil || enabled == 0 {
		return "", APIKey{}, errors.New("active user not found")
	}
	prefix, err := randomHex(6)
	if err != nil {
		return "", APIKey{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", APIKey{}, err
	}
	token := "mbk_" + prefix + "." + base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	id, err := randomHex(16)
	if err != nil {
		return "", APIKey{}, err
	}
	expires := time.Now().Add(validFor).UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO api_keys(id,user_id,name,prefix,secret_hash,scopes,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`, id, userID, keyName, prefix, hash[:], strings.Join(scopesForRole(role), ","), time.Now().Unix(), expires.Unix())
	return token, APIKey{Prefix: prefix, Name: keyName, ExpiresAt: expires}, err
}

func (s *Store) ListKeys(ctx context.Context, userName string) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT k.prefix,k.name,k.expires_at,k.revoked_at FROM api_keys k JOIN users u ON u.id=k.user_id WHERE u.name=? ORDER BY k.created_at`, userName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []APIKey
	for rows.Next() {
		var k APIKey
		var expires int64
		var revoked sql.NullInt64
		if err := rows.Scan(&k.Prefix, &k.Name, &expires, &revoked); err != nil {
			return nil, err
		}
		k.ExpiresAt, k.Revoked = time.Unix(expires, 0).UTC(), revoked.Valid
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *Store) RevokeKey(ctx context.Context, prefix string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE api_keys SET revoked_at=? WHERE prefix=? AND revoked_at IS NULL`, time.Now().Unix(), strings.TrimPrefix(prefix, "mbk_"))
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return errors.New("active key not found")
	}
	return nil
}

func (s *Store) Quotas(ctx context.Context, userID, role string) (Quotas, error) {
	q := DefaultQuotas(role)
	var a, b, c, d, e sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT requests_per_minute,datasets_per_minute,concurrent_builds,live_connections,live_symbols FROM quota_overrides WHERE user_id=?`, userID).Scan(&a, &b, &c, &d, &e)
	if errors.Is(err, sql.ErrNoRows) {
		return q, nil
	}
	if err != nil {
		return q, err
	}
	if a.Valid {
		q.RequestsPerMinute = int(a.Int64)
	}
	if b.Valid {
		q.DatasetsPerMinute = int(b.Int64)
	}
	if c.Valid {
		q.ConcurrentBuilds = int(c.Int64)
	}
	if d.Valid {
		q.LiveConnections = int(d.Int64)
	}
	if e.Valid {
		q.LiveSymbols = int(e.Int64)
	}
	return q, nil
}

func (s *Store) SetQuotas(ctx context.Context, userName string, q Quotas) error {
	if q.RequestsPerMinute < 1 || q.DatasetsPerMinute < 1 || q.ConcurrentBuilds < 1 || q.LiveConnections < 1 || q.LiveSymbols < 1 {
		return errors.New("all quota values must be positive")
	}
	var userID string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE name=?`, userName).Scan(&userID); err != nil {
		return errors.New("user not found")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO quota_overrides(user_id,requests_per_minute,datasets_per_minute,concurrent_builds,live_connections,live_symbols) VALUES(?,?,?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET requests_per_minute=excluded.requests_per_minute,datasets_per_minute=excluded.datasets_per_minute,concurrent_builds=excluded.concurrent_builds,live_connections=excluded.live_connections,live_symbols=excluded.live_symbols`, userID, q.RequestsPerMinute, q.DatasetsPerMinute, q.ConcurrentBuilds, q.LiveConnections, q.LiveSymbols)
	return err
}

func (s *Store) ClearQuotas(ctx context.Context, userName string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM quota_overrides WHERE user_id=(SELECT id FROM users WHERE name=?)`, userName)
	return err
}

func (s *Store) Watchlist(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT symbol FROM user_watchlists WHERE user_id=? ORDER BY symbol`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var symbols []string
	for rows.Next() {
		var symbol string
		if err := rows.Scan(&symbol); err != nil {
			return nil, err
		}
		symbols = append(symbols, symbol)
	}
	return symbols, rows.Err()
}

func (s *Store) SetWatchlist(ctx context.Context, userID string, symbols []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_watchlists WHERE user_id=?`, userID); err != nil {
		return err
	}
	for _, symbol := range symbols {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_watchlists(user_id,symbol) VALUES(?,?)`, userID, symbol); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type Usage struct {
	Requests int64 `json:"requests"`
	Datasets int64 `json:"datasets"`
	Failures int64 `json:"failures"`
}

func (s *Store) Usage(ctx context.Context, userID string) (Usage, error) {
	var u Usage
	err := s.db.QueryRowContext(ctx, `SELECT requests,datasets,failures FROM usage_daily WHERE user_id=? AND local_date=?`, userID, time.Now().Format("2006-01-02")).Scan(&u.Requests, &u.Datasets, &u.Failures)
	if errors.Is(err, sql.ErrNoRows) {
		return u, nil
	}
	return u, err
}

func (s *Store) RecordRequest(ctx context.Context, p Principal, requestID, method, route string, status int, duration time.Duration, dataset bool) {
	if p.UserID == "" {
		return
	}
	failure, datasets := 0, 0
	if status >= 400 {
		failure = 1
	}
	if dataset {
		datasets = 1
	}
	date := time.Now().Format("2006-01-02")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO usage_daily(user_id,local_date,requests,datasets,failures) VALUES(?,?,?,?,?) ON CONFLICT(user_id,local_date) DO UPDATE SET requests=requests+1,datasets=datasets+excluded.datasets,failures=failures+excluded.failures`, p.UserID, date, 1, datasets, failure); err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(occurred_at,request_id,user_id,key_id,method,route,status,duration_ms) VALUES(?,?,?,?,?,?,?,?)`, time.Now().Unix(), requestID, p.UserID, p.KeyID, method, route, status, duration.Milliseconds())
	}
	if err != nil {
		_ = tx.Rollback()
		return
	}
	_ = tx.Commit()
}

func (s *Store) RunCleanup(ctx context.Context) {
	cleanup := func() {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM audit_events WHERE occurred_at<?`, time.Now().Add(-30*24*time.Hour).Unix())
	}
	cleanup()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

func (s *Store) HasCredential(ctx context.Context) bool {
	if s.legacyToken != "" {
		return true
	}
	var n int
	return s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL AND expires_at>?`, time.Now().Unix()).Scan(&n) == nil && n > 0
}
