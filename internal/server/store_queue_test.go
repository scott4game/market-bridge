package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type blockingProvider struct {
	release, started chan struct{}
	once             sync.Once
}

func (*blockingProvider) Name() string        { return "blocking" }
func (*blockingProvider) DataVersion() string { return "v1" }
func (p *blockingProvider) Bars(context.Context, market.DatasetSpec) ([]market.Bar, error) {
	p.once.Do(func() { close(p.started) })
	<-p.release
	return nil, nil
}

func testSpec(symbol string) market.DatasetSpec {
	return market.DatasetSpec{Symbols: []string{symbol}, Interval: "1m", From: time.Now().Add(-time.Hour), To: time.Now(), Session: market.RegularSession, Adjustment: market.SplitAdjusted}
}

func TestStoreBuildQuotaDedupAndQueueCapacity(t *testing.T) {
	p := &blockingProvider{release: make(chan struct{}), started: make(chan struct{})}
	store, err := NewStoreWithOptions(t.TempDir(), p, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec("AAPL")
	admissions := 0
	admit := func() bool { admissions++; return true }
	first, err := store.EnsureForAdmission(context.Background(), spec, "alice", 1, admit)
	if err != nil {
		t.Fatal(err)
	}
	<-p.started
	duplicate, err := store.EnsureForAdmission(context.Background(), spec, "alice", 1, admit)
	if err != nil || duplicate.DatasetID != first.DatasetID {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	if admissions != 1 {
		t.Fatalf("deduplicated request consumed admission: %d", admissions)
	}
	if _, err := store.EnsureFor(context.Background(), testSpec("NVDA"), "alice", 1); !errors.Is(err, ErrBuildQuota) {
		t.Fatalf("quota err=%v", err)
	}
	if _, err := store.EnsureFor(context.Background(), testSpec("NVDA"), "bob", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureFor(context.Background(), testSpec("AMD"), "bob", 3); !errors.Is(err, ErrBuildQueueFull) {
		t.Fatalf("queue err=%v", err)
	}
	close(p.release)
	deadline := time.Now().Add(2 * time.Second)
	for store.ActiveBuilds("alice")+store.ActiveBuilds("bob") > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if active := store.ActiveBuilds("alice") + store.ActiveBuilds("bob"); active != 0 {
		t.Fatalf("builds did not finish: %d", active)
	}
}
