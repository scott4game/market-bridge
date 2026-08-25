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
	store, err := New(db, "history_coverage_v2", "history_coverage")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	middle := from.Add(time.Hour)
	to := middle.Add(time.Hour)
	first := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: from, To: middle, Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	price := market.DecimalFromFloat(100)
	bars := []market.Bar{{Symbol: "AAPL", Timestamp: from, Open: price, High: price, Low: price, Close: price, Completed: true}}
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
	store, err := New(db, "clickhouse_coverage_v2", "clickhouse_coverage")
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
	if _, err := db.Exec(`UPDATE clickhouse_coverage_v2 SET expires_at=0`); err != nil {
		t.Fatal(err)
	}
	if missing, _ := store.Missing(context.Background(), spec, "v1"); len(missing) != 1 {
		t.Fatalf("expired negative cache was honored: %+v", missing)
	}
}
