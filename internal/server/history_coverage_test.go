package server

import (
	"context"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type emptyUniverseProvider struct{}

func (emptyUniverseProvider) Name() string        { return "empty-universe" }
func (emptyUniverseProvider) DataVersion() string { return "v1" }
func (emptyUniverseProvider) Universe(context.Context) ([]string, error) {
	return []string{"AAPL"}, nil
}
func (emptyUniverseProvider) Bars(context.Context, market.DatasetSpec) ([]market.Bar, error) {
	return nil, nil
}

type noopHistoryWriter struct{}

func (noopHistoryWriter) WriteBars(context.Context, string, market.AdjustmentMode, []market.Bar, uint64) error {
	return nil
}

func TestSyncRecentUniverseUsesConfiguredEmptyCoverageTTL(t *testing.T) {
	store, err := NewStore(t.TempDir(), emptyUniverseProvider{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := OpenHistoryCatalog(t.TempDir() + "/history.db")
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	ttl := 37 * time.Minute
	started := time.Now().Unix()
	if err := store.SyncRecentUniverse(context.Background(), noopHistoryWriter{}, catalog, "v1", 2, ttl); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().Unix()
	var expires int64
	if err := catalog.db.QueryRow(`SELECT expires_at FROM history_coverage_v3 WHERE symbol='AAPL' AND kind='empty'`).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if expires < started+int64(ttl/time.Second) || expires > finished+int64(ttl/time.Second) {
		t.Fatalf("expires_at=%d want between %d and %d", expires, started+int64(ttl/time.Second), finished+int64(ttl/time.Second))
	}
}
