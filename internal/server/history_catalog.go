package server

import (
	"context"
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

// HistoryCatalog owns the monotonic revision advertised by go-server. It is
// deliberately independent from ClickHouse so clients can invalidate caches
// even when the server is operating as a provider gateway.
type HistoryCatalog struct{ db *sql.DB }

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
		`CREATE TABLE IF NOT EXISTS history_coverage (spec_hash TEXT PRIMARY KEY, completed_at INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(query); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &HistoryCatalog{db: db}, nil
}

func (c *HistoryCatalog) HasCoverage(ctx context.Context, specHash string) (bool, error) {
	var exists int
	err := c.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM history_coverage WHERE spec_hash=?)`, specHash).Scan(&exists)
	return exists == 1, err
}

func (c *HistoryCatalog) RecordCoverage(ctx context.Context, specHash string) error {
	_, err := c.db.ExecContext(ctx, `INSERT OR REPLACE INTO history_coverage(spec_hash,completed_at) VALUES(?,?)`, specHash, time.Now().Unix())
	return err
}

func (c *HistoryCatalog) Close() error { return c.db.Close() }

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
