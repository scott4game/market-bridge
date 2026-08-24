package provider

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type UsagePlan struct {
	Name              string `json:"name"`
	RequestsPerMinute *int   `json:"requests_per_minute"`
	RequestsPerMonth  *int   `json:"requests_per_month"`
}

type UsageWindow struct {
	Used      int64    `json:"used"`
	Limit     *int     `json:"limit"`
	Remaining *int64   `json:"remaining"`
	Percent   *float64 `json:"percent"`
}

type MonthlyUsage struct {
	Period    string   `json:"period"`
	Timezone  string   `json:"timezone"`
	UTCOffset string   `json:"utc_offset"`
	Used      int64    `json:"used"`
	Limit     *int     `json:"limit"`
	Remaining *int64   `json:"remaining"`
	Percent   *float64 `json:"percent"`
}

type UsageTotals struct {
	Requests int64 `json:"requests"`
	Success  int64 `json:"success"`
	Failed   int64 `json:"failed"`
	InFlight int64 `json:"in_flight"`
}

type EndpointUsage struct {
	Endpoint string `json:"endpoint"`
	Requests int64  `json:"requests"`
	Success  int64  `json:"success"`
	Failed   int64  `json:"failed"`
}

type TrackingStatus struct {
	Healthy   bool      `json:"healthy"`
	StartedAt time.Time `json:"started_at"`
	LastError *string   `json:"last_error"`
}

type UsageSnapshot struct {
	Provider     string          `json:"provider"`
	Plan         UsagePlan       `json:"plan"`
	Rolling60s   UsageWindow     `json:"rolling_60s"`
	CurrentMonth MonthlyUsage    `json:"current_month"`
	Totals       UsageTotals     `json:"totals"`
	ByEndpoint   []EndpointUsage `json:"by_endpoint"`
	Tracking     TrackingStatus  `json:"tracking"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// UsageTracker records actual outbound provider HTTP attempts. Detailed events
// are short lived; compact daily rows retain monthly and lifetime totals.
type UsageTracker struct {
	db       *sql.DB
	plan     UsagePlan
	location *time.Location
	now      func() time.Time
	mu       sync.RWMutex
	healthy  bool
	lastErr  string
}

func NewUsageTracker(path, planName string, perMinute, perMonth int, location *time.Location) (*UsageTracker, error) {
	if location == nil {
		location = time.Local
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	plan := UsagePlan{Name: planName, RequestsPerMinute: positiveLimit(perMinute), RequestsPerMonth: positiveLimit(perMonth)}
	u := &UsageTracker{db: db, plan: plan, location: location, now: time.Now, healthy: true}
	for _, q := range []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE IF NOT EXISTS request_events (id INTEGER PRIMARY KEY AUTOINCREMENT, provider TEXT NOT NULL, endpoint TEXT NOT NULL, started_at_ms INTEGER NOT NULL, completed_at_ms INTEGER, outcome TEXT NOT NULL, http_status INTEGER NOT NULL DEFAULT 0)`,
		`CREATE INDEX IF NOT EXISTS request_events_started ON request_events(provider, started_at_ms)`,
		`CREATE TABLE IF NOT EXISTS request_daily (provider TEXT NOT NULL, local_date TEXT NOT NULL, endpoint TEXT NOT NULL, requests INTEGER NOT NULL DEFAULT 0, success INTEGER NOT NULL DEFAULT 0, failed INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(provider, local_date, endpoint))`,
		`CREATE TABLE IF NOT EXISTS usage_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	now := u.now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT OR IGNORE INTO usage_meta(key,value) VALUES('tracking_started_at',?)`, now); err != nil {
		db.Close()
		return nil, err
	}
	if err := u.recoverInterrupted(); err != nil {
		db.Close()
		return nil, err
	}
	return u, nil
}

func positiveLimit(v int) *int {
	if v <= 0 {
		return nil
	}
	x := v
	return &x
}

func (u *UsageTracker) Close() error { return u.db.Close() }

// Begin persists quota consumption before the request leaves the process.
// The returned function must be called once with the final HTTP status/error.
func (u *UsageTracker) Begin(providerName, endpoint string) func(int, error) {
	now := u.now()
	date := now.In(u.location).Format("2006-01-02")
	tx, err := u.db.Begin()
	if err != nil {
		u.degrade(err)
		return func(int, error) {}
	}
	result, err := tx.Exec(`INSERT INTO request_events(provider,endpoint,started_at_ms,outcome) VALUES(?,?,?,'in_flight')`, providerName, endpoint, now.UnixMilli())
	if err == nil {
		_, err = tx.Exec(`INSERT INTO request_daily(provider,local_date,endpoint,requests) VALUES(?,?,?,1) ON CONFLICT(provider,local_date,endpoint) DO UPDATE SET requests=requests+1`, providerName, date, endpoint)
	}
	if err != nil {
		_ = tx.Rollback()
		u.degrade(err)
		return func(int, error) {}
	}
	if err = tx.Commit(); err != nil {
		u.degrade(err)
		return func(int, error) {}
	}
	id, _ := result.LastInsertId()
	var once sync.Once
	return func(status int, requestErr error) {
		once.Do(func() { u.finish(id, providerName, date, endpoint, status, requestErr) })
	}
}

func (u *UsageTracker) finish(id int64, providerName, date, endpoint string, status int, requestErr error) {
	outcome := "success"
	column := "success"
	if requestErr != nil || status < 200 || status >= 300 {
		outcome, column = "failed", "failed"
	}
	tx, err := u.db.Begin()
	if err == nil {
		_, err = tx.Exec(`UPDATE request_events SET completed_at_ms=?,outcome=?,http_status=? WHERE id=?`, u.now().UnixMilli(), outcome, status, id)
	}
	if err == nil {
		_, err = tx.Exec(fmt.Sprintf(`UPDATE request_daily SET %s=%s+1 WHERE provider=? AND local_date=? AND endpoint=?`, column, column), providerName, date, endpoint)
	}
	if err != nil {
		if tx != nil {
			_ = tx.Rollback()
		}
		u.degrade(err)
		return
	}
	if err = tx.Commit(); err != nil {
		u.degrade(err)
	}
}

func (u *UsageTracker) recoverInterrupted() error {
	rows, err := u.db.Query(`SELECT provider,endpoint,COUNT(*) FROM request_events WHERE outcome='in_flight' GROUP BY provider,endpoint`)
	if err != nil {
		return err
	}
	type interrupted struct {
		provider, endpoint string
		count              int64
	}
	var items []interrupted
	for rows.Next() {
		var x interrupted
		if err := rows.Scan(&x.provider, &x.endpoint, &x.count); err != nil {
			rows.Close()
			return err
		}
		items = append(items, x)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	tx, err := u.db.Begin()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE request_events SET completed_at_ms=?,outcome='failed' WHERE outcome='in_flight'`, u.now().UnixMilli()); err != nil {
		_ = tx.Rollback()
		return err
	}
	// Daily request totals were already persisted at start. Attribute interrupted
	// outcomes to today's recovery record; they remain visible as failures.
	date := u.now().In(u.location).Format("2006-01-02")
	for _, x := range items {
		if _, err = tx.Exec(`INSERT INTO request_daily(provider,local_date,endpoint,failed) VALUES(?,?,?,?) ON CONFLICT(provider,local_date,endpoint) DO UPDATE SET failed=failed+excluded.failed`, x.provider, date, x.endpoint, x.count); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (u *UsageTracker) Snapshot(ctx context.Context, providerName string) (UsageSnapshot, error) {
	now := u.now()
	localNow := now.In(u.location)
	period := localNow.Format("2006-01")
	startDate := period + "-01"
	var rolling, month int64
	if err := u.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_events WHERE provider=? AND started_at_ms>=?`, providerName, now.Add(-60*time.Second).UnixMilli()).Scan(&rolling); err != nil {
		return UsageSnapshot{}, err
	}
	if err := u.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(requests),0) FROM request_daily WHERE provider=? AND local_date>=? AND local_date<?`, providerName, startDate, nextMonth(localNow)).Scan(&month); err != nil {
		return UsageSnapshot{}, err
	}
	var totals UsageTotals
	if err := u.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(requests),0),COALESCE(SUM(success),0),COALESCE(SUM(failed),0) FROM request_daily WHERE provider=?`, providerName).Scan(&totals.Requests, &totals.Success, &totals.Failed); err != nil {
		return UsageSnapshot{}, err
	}
	if err := u.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_events WHERE provider=? AND outcome='in_flight'`, providerName).Scan(&totals.InFlight); err != nil {
		return UsageSnapshot{}, err
	}
	rows, err := u.db.QueryContext(ctx, `SELECT endpoint,SUM(requests),SUM(success),SUM(failed) FROM request_daily WHERE provider=? GROUP BY endpoint ORDER BY endpoint`, providerName)
	if err != nil {
		return UsageSnapshot{}, err
	}
	endpoints := make([]EndpointUsage, 0)
	for rows.Next() {
		var x EndpointUsage
		if err := rows.Scan(&x.Endpoint, &x.Requests, &x.Success, &x.Failed); err != nil {
			rows.Close()
			return UsageSnapshot{}, err
		}
		endpoints = append(endpoints, x)
	}
	if err := rows.Close(); err != nil {
		return UsageSnapshot{}, err
	}
	var startedRaw string
	if err := u.db.QueryRowContext(ctx, `SELECT value FROM usage_meta WHERE key='tracking_started_at'`).Scan(&startedRaw); err != nil {
		return UsageSnapshot{}, err
	}
	started, _ := time.Parse(time.RFC3339Nano, startedRaw)
	_, offset := localNow.Zone()
	s := UsageSnapshot{
		Provider: providerName, Plan: u.plan,
		Rolling60s:   usageWindow(rolling, u.plan.RequestsPerMinute),
		CurrentMonth: MonthlyUsage{Period: period, Timezone: u.location.String(), UTCOffset: formatOffset(offset), Used: month, Limit: u.plan.RequestsPerMonth},
		Totals:       totals, ByEndpoint: endpoints, Tracking: u.tracking(started), UpdatedAt: now.UTC(),
	}
	s.CurrentMonth.Remaining, s.CurrentMonth.Percent = remaining(month, s.CurrentMonth.Limit)
	// Raw events are only needed for the rolling window and interrupted requests.
	_, _ = u.db.ExecContext(ctx, `DELETE FROM request_events WHERE started_at_ms<? AND outcome!='in_flight'`, now.Add(-24*time.Hour).UnixMilli())
	return s, nil
}

func usageWindow(used int64, limit *int) UsageWindow {
	r, p := remaining(used, limit)
	return UsageWindow{Used: used, Limit: limit, Remaining: r, Percent: p}
}

func remaining(used int64, limit *int) (*int64, *float64) {
	if limit == nil {
		return nil, nil
	}
	r := int64(*limit) - used
	if r < 0 {
		r = 0
	}
	p := float64(used) * 100 / float64(*limit)
	return &r, &p
}

func nextMonth(t time.Time) string {
	return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location()).Format("2006-01-02")
}

func formatOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign, seconds = "-", -seconds
	}
	return fmt.Sprintf("%s%02d:%02d", sign, seconds/3600, seconds%3600/60)
}

func (u *UsageTracker) degrade(err error) {
	u.mu.Lock()
	u.healthy = false
	u.lastErr = err.Error()
	u.mu.Unlock()
}

func (u *UsageTracker) tracking(started time.Time) TrackingStatus {
	u.mu.RLock()
	defer u.mu.RUnlock()
	var last *string
	if u.lastErr != "" {
		x := u.lastErr
		last = &x
	}
	return TrackingStatus{Healthy: u.healthy, StartedAt: started, LastError: last}
}
