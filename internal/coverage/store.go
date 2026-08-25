package coverage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

// Store records which canonical one-minute ranges have been fetched. Empty
// ranges expire so a transient successful-but-empty provider response cannot
// poison the cache forever.
type Store struct {
	db    *sql.DB
	table string
}

func New(db *sql.DB, table, legacyTable string) (*Store, error) {
	if table != "history_coverage_v2" && table != "clickhouse_coverage_v2" {
		return nil, fmt.Errorf("unsupported coverage table %q", table)
	}
	if legacyTable != "" && legacyTable != "history_coverage" && legacyTable != "clickhouse_coverage" {
		return nil, fmt.Errorf("unsupported legacy coverage table %q", legacyTable)
	}
	if legacyTable != "" {
		if _, err := db.Exec(`DROP TABLE IF EXISTS ` + legacyTable); err != nil {
			return nil, err
		}
	}
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS ` + table + ` (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			data_version TEXT NOT NULL,
			symbol TEXT NOT NULL,
			interval TEXT NOT NULL,
			session TEXT NOT NULL,
			adjustment TEXT NOT NULL,
			from_ms INTEGER NOT NULL,
			to_ms INTEGER NOT NULL,
			kind TEXT NOT NULL CHECK(kind IN ('positive','empty')),
			expires_at INTEGER NOT NULL DEFAULT 0,
			completed_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS ` + table + `_lookup ON ` + table + `(data_version,symbol,interval,session,adjustment,from_ms,to_ms)`,
		`CREATE INDEX IF NOT EXISTS ` + table + `_expiry ON ` + table + `(kind,expires_at)`,
	} {
		if _, err := db.Exec(query); err != nil {
			return nil, err
		}
	}
	return &Store{db: db, table: table}, nil
}

// Missing returns uncovered ranges as normalized one-symbol specs.
func (s *Store) Missing(ctx context.Context, spec market.DatasetSpec, dataVersion string) ([]market.DatasetSpec, error) {
	n, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	var missing []market.DatasetSpec
	for _, symbol := range n.Symbols {
		rows, err := s.db.QueryContext(ctx, `SELECT from_ms,to_ms FROM `+s.table+`
			WHERE data_version=? AND symbol=? AND interval=? AND session=? AND adjustment=?
			AND to_ms>? AND from_ms<? AND (kind='positive' OR expires_at>?) ORDER BY from_ms,to_ms`,
			dataVersion, symbol, n.Interval, n.Session, n.Adjustment, n.From.UnixMilli(), n.To.UnixMilli(), now)
		if err != nil {
			return nil, err
		}
		cursor := n.From.UnixMilli()
		end := n.To.UnixMilli()
		for rows.Next() {
			var from, to int64
			if err := rows.Scan(&from, &to); err != nil {
				rows.Close()
				return nil, err
			}
			if from > cursor {
				missing = append(missing, rangeSpec(n, symbol, cursor, min(from, end)))
			}
			if to > cursor {
				cursor = to
			}
			if cursor >= end {
				break
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if cursor < end {
			missing = append(missing, rangeSpec(n, symbol, cursor, end))
		}
	}
	return missing, nil
}

func rangeSpec(base market.DatasetSpec, symbol string, from, to int64) market.DatasetSpec {
	base.Symbols = []string{symbol}
	base.From, base.To = time.UnixMilli(from).UTC(), time.UnixMilli(to).UTC()
	return base
}

func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// Record records a successful provider response. Symbols with no bars are
// negative coverage and expire after emptyTTL.
func (s *Store) Record(ctx context.Context, spec market.DatasetSpec, dataVersion string, bars []market.Bar, emptyTTL time.Duration) error {
	n, err := spec.Normalize()
	if err != nil {
		return err
	}
	positive := make(map[string]bool, len(bars))
	for _, bar := range bars {
		positive[bar.Symbol] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	for _, symbol := range n.Symbols {
		kind, expires := "empty", now.Add(emptyTTL).Unix()
		from, to := n.From.UnixMilli(), n.To.UnixMilli()
		if positive[symbol] {
			kind, expires = "positive", int64(0)
			from, to, err = s.mergePositive(ctx, tx, n, symbol, dataVersion)
			if err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+s.table+`(data_version,symbol,interval,session,adjustment,from_ms,to_ms,kind,expires_at,completed_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			dataVersion, symbol, n.Interval, n.Session, n.Adjustment, from, to, kind, expires, now.Unix()); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM `+s.table+` WHERE kind='empty' AND expires_at<=?`, now.Unix())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) mergePositive(ctx context.Context, tx *sql.Tx, spec market.DatasetSpec, symbol, version string) (int64, int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,from_ms,to_ms FROM `+s.table+` WHERE data_version=? AND symbol=? AND interval=? AND session=? AND adjustment=? AND kind='positive' AND to_ms>=? AND from_ms<=?`,
		version, symbol, spec.Interval, spec.Session, spec.Adjustment, spec.From.UnixMilli(), spec.To.UnixMilli())
	if err != nil {
		return 0, 0, err
	}
	var ids []int64
	from, to := spec.From.UnixMilli(), spec.To.UnixMilli()
	for rows.Next() {
		var id, a, b int64
		if err := rows.Scan(&id, &a, &b); err != nil {
			rows.Close()
			return 0, 0, err
		}
		ids = append(ids, id)
		if a < from {
			from = a
		}
		if b > to {
			to = b
		}
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+s.table+` WHERE id=?`, id); err != nil {
			return 0, 0, err
		}
	}
	return from, to, nil
}

func (s *Store) Cleanup(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM `+s.table+` WHERE kind='empty' AND expires_at<=?`, time.Now().Unix())
	return err
}

// GroupMissing batches symbols whose uncovered ranges and query dimensions are
// identical, keeping provider request counts bounded for multi-symbol specs.
func GroupMissing(specs []market.DatasetSpec) []market.DatasetSpec {
	groups := map[string]market.DatasetSpec{}
	for _, spec := range specs {
		key := fmt.Sprintf("%d:%d:%s:%s:%s", spec.From.UnixMilli(), spec.To.UnixMilli(), spec.Interval, spec.Session, spec.Adjustment)
		if existing, ok := groups[key]; ok {
			existing.Symbols = append(existing.Symbols, spec.Symbols...)
			groups[key] = existing
		} else {
			groups[key] = spec
		}
	}
	out := make([]market.DatasetSpec, 0, len(groups))
	for _, spec := range groups {
		sort.Strings(spec.Symbols)
		out = append(out, spec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From.Equal(out[j].From) {
			return out[i].Symbols[0] < out[j].Symbols[0]
		}
		return out[i].From.Before(out[j].From)
	})
	return out
}
