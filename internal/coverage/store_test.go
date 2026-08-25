package coverage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
	_ "modernc.org/sqlite"
)

func TestCoverageReturnsOnlyMissingTail(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/coverage.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	store, err := New(db, "history_coverage_v3", "history_coverage", "history_coverage_v2")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	middle := from.Add(time.Hour)
	to := middle.Add(time.Hour)
	first := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: from, To: middle, Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	price := market.DecimalFromFloat(100)
	bars := []market.Bar{{Symbol: "AAPL", Timestamp: middle.Add(-time.Minute), Open: price, High: price, Low: price, Close: price, Completed: true}}
	if err := store.Record(context.Background(), first, "v1", bars, time.Minute); err != nil {
		t.Fatal(err)
	}
	whole := first
	whole.To = to
	missing, err := store.Missing(context.Background(), whole, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || !missing[0].From.Equal(middle) || !missing[0].To.Equal(to) {
		t.Fatalf("missing=%+v", missing)
	}
}

func TestEmptyCoverageExpires(t *testing.T) {
	db, _ := sql.Open("sqlite", t.TempDir()+"/coverage.db")
	defer db.Close()
	db.SetMaxOpenConns(1)
	store, err := New(db, "clickhouse_coverage_v3", "clickhouse_coverage", "clickhouse_coverage_v2")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: from, To: from.Add(time.Hour), Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	if err := store.Record(context.Background(), spec, "v1", nil, time.Hour); err != nil {
		t.Fatal(err)
	}
	if missing, _ := store.Missing(context.Background(), spec, "v1"); len(missing) != 0 {
		t.Fatalf("negative cache was not honored: %+v", missing)
	}
	if _, err := db.Exec(`UPDATE clickhouse_coverage_v3 SET expires_at=0`); err != nil {
		t.Fatal(err)
	}
	if missing, _ := store.Missing(context.Background(), spec, "v1"); len(missing) != 1 {
		t.Fatalf("expired negative cache was honored: %+v", missing)
	}
}

func TestPositiveCoverageStopsAtLastCompletedBarAndWatermark(t *testing.T) {
	db, _ := sql.Open("sqlite", t.TempDir()+"/coverage.db")
	defer db.Close()
	db.SetMaxOpenConns(1)
	store, err := New(db, "history_coverage_v3")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 10, 0, 30, 0, time.UTC)
	store.now = func() time.Time { return now }
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: from, To: to, Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	price := market.DecimalFromFloat(100)
	bars := []market.Bar{
		{Symbol: "AAPL", Timestamp: now.Add(-time.Minute), Open: price, High: price, Low: price, Close: price, Completed: true},
		{Symbol: "AAPL", Timestamp: now.Truncate(time.Minute), Open: price, High: price, Low: price, Close: price, Completed: true},
	}
	if err := store.Record(context.Background(), spec, "v1", bars, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE history_coverage_v3 SET expires_at=0 WHERE kind='empty'`); err != nil {
		t.Fatal(err)
	}
	missing, err := store.Missing(context.Background(), spec, "v1")
	if err != nil {
		t.Fatal(err)
	}
	watermark := now.Truncate(time.Minute)
	if len(missing) != 1 || !missing[0].From.Equal(watermark) || !missing[0].To.Equal(to) {
		t.Fatalf("missing=%+v", missing)
	}
}

func TestCoverageTracksEachSymbolLastBarIndependently(t *testing.T) {
	db, _ := sql.Open("sqlite", t.TempDir()+"/coverage.db")
	defer db.Close()
	db.SetMaxOpenConns(1)
	store, err := New(db, "history_coverage_v3")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	from, to := now.Add(-3*time.Hour), now.Add(-time.Hour)
	spec := market.DatasetSpec{Symbols: []string{"NVDA", "AAPL"}, Interval: "1m", From: from, To: to, Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	price := market.DecimalFromFloat(100)
	bars := []market.Bar{
		{Symbol: "AAPL", Timestamp: from.Add(time.Hour), Open: price, High: price, Low: price, Close: price, Completed: true},
		{Symbol: "NVDA", Timestamp: from.Add(90 * time.Minute), Open: price, High: price, Low: price, Close: price, Completed: true},
		{Symbol: "MSFT", Timestamp: to.Add(-time.Minute), Open: price, High: price, Low: price, Close: price, Completed: true},
	}
	if err := store.Record(context.Background(), spec, "v1", bars, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE history_coverage_v3 SET expires_at=0 WHERE kind='empty'`); err != nil {
		t.Fatal(err)
	}
	missing, err := store.Missing(context.Background(), spec, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 2 || missing[0].Symbols[0] != "AAPL" || !missing[0].From.Equal(from.Add(61*time.Minute)) || missing[1].Symbols[0] != "NVDA" || !missing[1].From.Equal(from.Add(91*time.Minute)) {
		t.Fatalf("missing=%+v", missing)
	}
}

func TestCoverageCleanupFollowsRetentionCutoff(t *testing.T) {
	db, _ := sql.Open("sqlite", t.TempDir()+"/coverage.db")
	defer db.Close()
	db.SetMaxOpenConns(1)
	store, err := New(db, "history_coverage_v3")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	_, err = db.Exec(`INSERT INTO history_coverage_v3(data_version,symbol,interval,session,adjustment,from_ms,to_ms,kind,expires_at,completed_at) VALUES
		('v1','OLD','1m','regular','split_adjusted',?,?,'positive',0,0),
		('v1','AAPL','1m','regular','split_adjusted',?,?,'positive',0,0),
		('v1','EMPTY','1m','regular','split_adjusted',?,?,'empty',0,0)`,
		now.Add(-3*time.Hour).UnixMilli(), now.Add(-2*time.Hour).UnixMilli(),
		now.Add(-2*time.Hour).UnixMilli(), now.UnixMilli(),
		now.Add(-time.Hour).UnixMilli(), now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	cutoff := now.Add(-time.Hour)
	if err := store.Cleanup(context.Background(), cutoff); err != nil {
		t.Fatal(err)
	}
	var from int64
	if err := db.QueryRow(`SELECT from_ms FROM history_coverage_v3 WHERE symbol='AAPL'`).Scan(&from); err != nil || from != cutoff.UnixMilli() {
		t.Fatalf("trimmed from=%d err=%v", from, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM history_coverage_v3`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("remaining=%d err=%v", count, err)
	}
}

func TestCoverageV3DropsLegacyMetadata(t *testing.T) {
	db, _ := sql.Open("sqlite", t.TempDir()+"/coverage.db")
	defer db.Close()
	for _, table := range []string{"history_coverage", "history_coverage_v2"} {
		if _, err := db.Exec(`CREATE TABLE ` + table + ` (id INTEGER)`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := New(db, "history_coverage_v3", "history_coverage", "history_coverage_v2"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"history_coverage", "history_coverage_v2"} {
		var count int
		err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count)
		if err != nil || count != 0 {
			t.Fatalf("legacy table %s remains: count=%d err=%v", table, count, err)
		}
	}
}
