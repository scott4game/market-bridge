package server

import (
	"context"
	"database/sql"
	"time"

	"github.com/scott4game/market-bridge/internal/coverage"
	"github.com/scott4game/market-bridge/internal/market"
	_ "modernc.org/sqlite"
)

// HistoryCatalog owns the monotonic revision advertised by go-server. It is
// deliberately independent from ClickHouse so clients can invalidate caches
// even when the server is operating as a provider gateway.
type HistoryCatalog struct {
	db       *sql.DB
	coverage *coverage.Store
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
	return c.coverage.Record(ctx, spec, dataVersion, bars, emptyTTL)
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
