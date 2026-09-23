package server

import (
	"context"
	"database/sql"
	"time"

	"github.com/scott4game/market-bridge/internal/coverage"
	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/provider"
	_ "modernc.org/sqlite"
)

// HistoryCatalog owns the monotonic revision advertised by go-server. It is
// deliberately independent from ClickHouse so clients can invalidate caches
// even when the server is operating as a provider gateway.
type HistoryCatalog struct {
	db         *sql.DB
	coverage   *coverage.Store
	tailWindow time.Duration
}

func OpenHistoryCatalog(path string) (*HistoryCatalog, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, query := range []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE IF NOT EXISTS history_state (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT OR IGNORE INTO history_state(id,revision,updated_at) VALUES(1,0,0)`,
	} {
		if _, err := db.Exec(query); err != nil {
			db.Close()
			return nil, err
		}
	}
	coverageStore, err := coverage.New(db, "history_coverage_v3", "history_coverage", "history_coverage_v2")
	if err != nil {
		db.Close()
		return nil, err
	}
	return &HistoryCatalog{db: db, coverage: coverageStore}, nil
}

func (c *HistoryCatalog) Missing(ctx context.Context, spec market.DatasetSpec, dataVersion string) ([]market.DatasetSpec, error) {
	return c.coverage.Missing(ctx, spec, dataVersion)
}

func (c *HistoryCatalog) RecordCoverage(ctx context.Context, spec market.DatasetSpec, dataVersion string, bars []market.Bar, emptyTTL time.Duration) error {
	return c.recordMatureCoverage(ctx, spec, dataVersion, bars, emptyTTL, c.tailWindow)
}

func (c *HistoryCatalog) Close() error { return c.db.Close() }

func (c *HistoryCatalog) RunCleanup(ctx context.Context, retention time.Duration) {
	if retention <= 0 {
		retention = 1825 * 24 * time.Hour
	}
	cleanup := func() {
		_ = c.coverage.Cleanup(ctx, time.Now().UTC().Add(-retention))
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

func (c *HistoryCatalog) Current(ctx context.Context) (uint64, time.Time, error) {
	var revision, updated int64
	err := c.db.QueryRowContext(ctx, `SELECT revision,updated_at FROM history_state WHERE id=1`).Scan(&revision, &updated)
	return uint64(revision), time.Unix(updated, 0).UTC(), err
}

func (c *HistoryCatalog) Bump(ctx context.Context) (uint64, error) {
	if _, err := c.db.ExecContext(ctx, `UPDATE history_state SET revision=revision+1,updated_at=? WHERE id=1`, time.Now().Unix()); err != nil {
		return 0, err
	}
	revision, _, err := c.Current(ctx)
	return revision, err
}

// InvalidateRecentUSCoverage discards only recent coverage metadata on startup.
// This retires pre-overlay records that may have confirmed delayed partial bars,
// without causing a complete historical refetch or deleting stored candles.
func (c *HistoryCatalog) InvalidateRecentUSCoverage(ctx context.Context, now time.Time, window time.Duration) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT rowid,symbol,interval,session,from_ms FROM history_coverage_v3 WHERE to_ms>?", now.Add(-24*time.Hour).UnixMilli())
	if err != nil {
		return err
	}
	type change struct {
		id     int64
		cutoff int64
		remove bool
	}
	var changes []change
	for rows.Next() {
		var id, from int64
		var symbol, interval string
		var session market.Session
		if err := rows.Scan(&id, &symbol, &interval, &session, &from); err != nil {
			rows.Close()
			return err
		}
		venue, _ := market.VenueOf(symbol)
		step := market.IntervalDuration(interval)
		if venue != market.VenueUS || step < time.Minute || step > 4*time.Hour {
			continue
		}
		cutoff := provider.TailBucket(now.Add(-24*time.Hour), interval, session).UnixMilli()
		changes = append(changes, change{id: id, cutoff: cutoff, remove: from >= cutoff})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range changes {
		if item.remove {
			_, err = tx.ExecContext(ctx, "DELETE FROM history_coverage_v3 WHERE rowid=?", item.id)
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE history_coverage_v3 SET to_ms=? WHERE rowid=?", item.cutoff, item.id)
		}
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE history_state SET revision=revision+1,updated_at=? WHERE id=1", now.Unix()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.tailWindow = window
	return nil
}

func (c *HistoryCatalog) recordMatureCoverage(ctx context.Context, spec market.DatasetSpec, version string, bars []market.Bar, ttl, window time.Duration) error {
	if window > 0 {
		now := time.Now()
		if provider.USTailEligible(spec, now, window) {
			ttl = 15 * time.Second
		}
		confirmed := make([]market.Bar, 0, len(bars))
		step := market.IntervalDuration(spec.Interval)
		for _, bar := range bars {
			venue, _ := market.VenueOf(bar.Symbol)
			if venue == market.VenueUS && step >= time.Minute && step <= 4*time.Hour && bar.Timestamp.Add(step).After(now.Add(-window)) {
				continue
			}
			confirmed = append(confirmed, bar)
		}
		bars = confirmed
	}
	return c.coverage.Record(ctx, spec, version, bars, ttl)
}
