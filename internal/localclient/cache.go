package localclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/redis/go-redis/v9"
	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/market"
	_ "modernc.org/sqlite"
)

type Cache struct {
	cfg      config.Client
	db       *sql.DB
	redis    *redis.Client
	http     *http.Client
	mu       sync.Mutex
	inflight map[string]*flight
	active   map[string]int
}

type flight struct {
	done     chan struct{}
	manifest market.Manifest
	err      error
}

func NewCache(cfg config.Client) (*Cache, error) {
	root, err := filepath.Abs(cfg.CacheDir)
	if err != nil {
		return nil, err
	}
	if root == string(filepath.Separator) || root == "." {
		return nil, errors.New("unsafe cache directory")
	}
	cfg.CacheDir = root
	if err := os.MkdirAll(filepath.Join(root, "datasets"), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "cache.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE IF NOT EXISTS datasets (id TEXT PRIMARY KEY, spec_hash TEXT NOT NULL, manifest_json BLOB NOT NULL, last_accessed_at INTEGER NOT NULL, state TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS datasets_spec ON datasets(spec_hash, last_accessed_at)`,
	}
	for _, q := range stmts {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	c := &Cache{cfg: cfg, db: db, http: &http.Client{Timeout: 2 * time.Minute}, inflight: map[string]*flight{}, active: map[string]int{}}
	if cfg.RedisEnabled {
		c.redis = redis.NewClient(&redis.Options{Addr: cfg.RedisAddress, Username: cfg.RedisUsername, Password: cfg.RedisPassword, DB: cfg.RedisDB, DialTimeout: 300 * time.Millisecond, ReadTimeout: 500 * time.Millisecond, WriteTimeout: 500 * time.Millisecond})
	}
	if err := c.recoverInterrupted(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Cache) recoverInterrupted() error {
	root := filepath.Join(c.cfg.CacheDir, "datasets")
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".downloading") {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return err
			}
		}
	}
	rows, err := c.db.Query(`SELECT id FROM datasets WHERE state='deleting'`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := os.RemoveAll(filepath.Join(root, id)); err != nil {
			return err
		}
		if _, err := c.db.Exec(`DELETE FROM datasets WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cache) Close() error {
	if c.redis != nil {
		_ = c.redis.Close()
	}
	return c.db.Close()
}

func (c *Cache) Bars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, string, error) {
	spec, err := spec.Normalize()
	if err != nil {
		return nil, "", err
	}
	key, err := spec.Hash(market.SchemaVersion, "request")
	if err != nil {
		return nil, "", err
	}
	if c.redis != nil {
		if b, err := c.redis.Get(ctx, "bars:"+key).Bytes(); err == nil {
			var bars []market.Bar
			if json.Unmarshal(b, &bars) == nil {
				_ = c.touchBySpec(ctx, key)
				return bars, "redis", nil
			}
		}
	}
	m, ok, err := c.localManifest(ctx, key)
	if err != nil {
		return nil, "", err
	}
	source := "parquet"
	if !ok {
		m, err = c.ensureRemote(ctx, key, spec)
		if err != nil {
			return nil, "", err
		}
		source = "go-server"
	}
	c.retain(m.DatasetID)
	defer c.release(m.DatasetID)
	bars, err := c.readManifest(m)
	if err != nil {
		return nil, "", err
	}
	market.SortBars(bars)
	_ = c.touch(ctx, m.DatasetID)
	if c.redis != nil {
		if b, e := json.Marshal(bars); e == nil {
			_ = c.redis.Set(ctx, "bars:"+key, b, c.cfg.RedisTTL).Err()
		}
	}
	return bars, source, nil
}

func (c *Cache) localManifest(ctx context.Context, specHash string) (market.Manifest, bool, error) {
	var raw []byte
	var last int64
	err := c.db.QueryRowContext(ctx, `SELECT manifest_json,last_accessed_at FROM datasets WHERE spec_hash=? AND state='ready' ORDER BY last_accessed_at DESC LIMIT 1`, specHash).Scan(&raw, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return market.Manifest{}, false, nil
	}
	if err != nil {
		return market.Manifest{}, false, err
	}
	if c.cfg.ParquetTTL > 0 && time.Unix(last, 0).Add(c.cfg.ParquetTTL).Before(time.Now()) {
		return market.Manifest{}, false, nil
	}
	var m market.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, false, err
	}
	for _, p := range m.Partitions {
		if _, err := os.Stat(c.partitionPath(m.DatasetID, p.Name)); err != nil {
			return m, false, nil
		}
	}
	return m, true, nil
}

func (c *Cache) ensureRemote(ctx context.Context, key string, spec market.DatasetSpec) (market.Manifest, error) {
	c.mu.Lock()
	if f, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return market.Manifest{}, ctx.Err()
		case <-f.done:
			return f.manifest, f.err
		}
	}
	f := &flight{done: make(chan struct{})}
	c.inflight[key] = f
	c.mu.Unlock()
	f.manifest, f.err = c.download(ctx, key, spec)
	close(f.done)
	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
	return f.manifest, f.err
}

func (c *Cache) download(ctx context.Context, key string, spec market.DatasetSpec) (market.Manifest, error) {
	b, _ := json.Marshal(spec)
	req, err := c.request(ctx, http.MethodPost, "/v1/datasets", bytes.NewReader(b))
	if err != nil {
		return market.Manifest{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return market.Manifest{}, err
	}
	var st market.DatasetStatus
	err = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if err != nil {
		return market.Manifest{}, err
	}
	if resp.StatusCode/100 != 2 {
		return market.Manifest{}, fmt.Errorf("go-server returned %d: %s", resp.StatusCode, st.Error)
	}
	for st.State == "building" {
		select {
		case <-ctx.Done():
			return market.Manifest{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if err := c.getJSON(ctx, "/v1/datasets/"+st.DatasetID, &st); err != nil {
			return market.Manifest{}, err
		}
	}
	if st.State != "ready" {
		return market.Manifest{}, fmt.Errorf("dataset %s: %s", st.State, st.Error)
	}
	var m market.Manifest
	if err := c.getJSON(ctx, "/v1/datasets/"+st.DatasetID+"/manifest", &m); err != nil {
		return m, err
	}
	tmpRoot := filepath.Join(c.cfg.CacheDir, "datasets", st.DatasetID+".downloading")
	_ = os.RemoveAll(tmpRoot)
	if err := os.MkdirAll(filepath.Join(tmpRoot, "files"), 0o755); err != nil {
		return m, err
	}
	for _, p := range m.Partitions {
		path := filepath.Join(tmpRoot, "files", filepath.FromSlash(p.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return m, err
		}
		if err := c.downloadFile(ctx, "/v1/datasets/"+st.DatasetID+"/files/"+p.Name, path, p.SHA256); err != nil {
			return m, err
		}
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpRoot, "manifest.json"), mb, 0o644); err != nil {
		return m, err
	}
	final := filepath.Join(c.cfg.CacheDir, "datasets", st.DatasetID)
	_ = os.RemoveAll(final)
	if err := os.Rename(tmpRoot, final); err != nil {
		return m, err
	}
	now := time.Now().Unix()
	if _, err := c.db.ExecContext(ctx, `INSERT OR REPLACE INTO datasets(id,spec_hash,manifest_json,last_accessed_at,state) VALUES(?,?,?,?,?)`, st.DatasetID, key, mb, now, "ready"); err != nil {
		return m, err
	}
	return m, nil
}

func (c *Cache) readManifest(m market.Manifest) ([]market.Bar, error) {
	var out []market.Bar
	for _, p := range m.Partitions {
		rows, err := parquet.ReadFile[market.ParquetBar](c.partitionPath(m.DatasetID, p.Name))
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, market.FromParquetBar(r))
		}
	}
	return out, nil
}
func (c *Cache) partitionPath(id, name string) string {
	return filepath.Join(c.cfg.CacheDir, "datasets", id, "files", filepath.FromSlash(name))
}
func (c *Cache) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.cfg.ServerURL, "/")+path, body)
	if err == nil && c.cfg.ServerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.ServerToken)
	}
	return req, err
}
func (c *Cache) getJSON(ctx context.Context, path string, v any) error {
	req, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("go-server returned %d: %s", resp.StatusCode, b)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *Cache) ProviderUsage(ctx context.Context) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.getJSON(ctx, "/v1/providers/massive/usage", &raw)
	return raw, err
}
func (c *Cache) downloadFile(ctx context.Context, urlPath, path, want string) error {
	req, err := c.request(ctx, http.MethodGet, urlPath, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, hash), resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch: got %s want %s", got, want)
	}
	return nil
}
func (c *Cache) touch(ctx context.Context, id string) error {
	_, err := c.db.ExecContext(ctx, `UPDATE datasets SET last_accessed_at=? WHERE id=?`, time.Now().Unix(), id)
	return err
}
func (c *Cache) touchBySpec(ctx context.Context, key string) error {
	_, err := c.db.ExecContext(ctx, `UPDATE datasets SET last_accessed_at=? WHERE spec_hash=?`, time.Now().Unix(), key)
	return err
}
func (c *Cache) retain(id string) { c.mu.Lock(); c.active[id]++; c.mu.Unlock() }
func (c *Cache) release(id string) {
	c.mu.Lock()
	c.active[id]--
	if c.active[id] <= 0 {
		delete(c.active, id)
	}
	c.mu.Unlock()
}

type CacheEntry struct {
	DatasetID    string    `json:"dataset_id"`
	SpecHash     string    `json:"spec_hash,omitempty"`
	LastAccessed time.Time `json:"last_accessed"`
	State        string    `json:"state"`
}

func (c *Cache) List(ctx context.Context) ([]CacheEntry, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id,spec_hash,last_accessed_at,state FROM datasets ORDER BY last_accessed_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CacheEntry
	for rows.Next() {
		var x CacheEntry
		var ts int64
		if err := rows.Scan(&x.DatasetID, &x.SpecHash, &ts, &x.State); err != nil {
			return nil, err
		}
		x.LastAccessed = time.Unix(ts, 0).UTC()
		out = append(out, x)
	}
	return out, rows.Err()
}
func (c *Cache) Prune(ctx context.Context, expiredOnly bool) (int, error) {
	entries, err := c.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if expiredOnly && (c.cfg.ParquetTTL <= 0 || e.LastAccessed.Add(c.cfg.ParquetTTL).After(time.Now())) {
			continue
		}
		c.mu.Lock()
		busy := c.active[e.DatasetID] > 0
		c.mu.Unlock()
		if busy {
			continue
		}
		if _, err := c.db.ExecContext(ctx, `UPDATE datasets SET state='deleting' WHERE id=?`, e.DatasetID); err != nil {
			return n, err
		}
		path := filepath.Join(c.cfg.CacheDir, "datasets", e.DatasetID)
		if err := os.RemoveAll(path); err != nil {
			return n, err
		}
		if c.redis != nil {
			_ = c.redis.Del(ctx, "bars:"+e.SpecHash).Err()
		}
		if _, err := c.db.ExecContext(ctx, `DELETE FROM datasets WHERE id=?`, e.DatasetID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (c *Cache) Manifest(ctx context.Context, id string) (market.Manifest, error) {
	var raw []byte
	if err := c.db.QueryRowContext(ctx, `SELECT manifest_json FROM datasets WHERE id=? AND state='ready'`, id).Scan(&raw); err != nil {
		return market.Manifest{}, err
	}
	var m market.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	return m, nil
}

func (c *Cache) Delete(ctx context.Context, id string) error {
	c.mu.Lock()
	busy := c.active[id] > 0
	c.mu.Unlock()
	if busy {
		return errors.New("dataset is in use")
	}
	var key string
	if err := c.db.QueryRowContext(ctx, `SELECT spec_hash FROM datasets WHERE id=?`, id).Scan(&key); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `UPDATE datasets SET state='deleting' WHERE id=?`, id); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(c.cfg.CacheDir, "datasets", id)); err != nil {
		return err
	}
	if c.redis != nil {
		_ = c.redis.Del(ctx, "bars:"+key).Err()
	}
	_, err := c.db.ExecContext(ctx, `DELETE FROM datasets WHERE id=?`, id)
	return err
}
func (c *Cache) RunCleanup(ctx context.Context) {
	if c.cfg.ParquetTTL <= 0 {
		return
	}
	ticker := time.NewTicker(c.cfg.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = c.Prune(ctx, true)
		}
	}
}
