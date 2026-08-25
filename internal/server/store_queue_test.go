package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type blockingProvider struct {
	release, started chan struct{}
	once             sync.Once
}

type retryProvider struct{ calls atomic.Int32 }

func (*retryProvider) Name() string        { return "retry" }
func (*retryProvider) DataVersion() string { return "v1" }
func (p *retryProvider) Bars(context.Context, market.DatasetSpec) ([]market.Bar, error) {
	if p.calls.Add(1) == 1 {
		return nil, errors.New("temporary failure")
	}
	return nil, nil
}

func TestFailedDatasetCanBeRetriedAndCleansTemporaryDirectory(t *testing.T) {
	root := t.TempDir()
	p := &retryProvider{}
	store, err := NewStoreWithBuildOptions(context.Background(), root, p, 1, 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec("AAPL")
	first, err := store.Ensure(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, store, first.DatasetID, "failed")
	second, err := store.Ensure(context.Background(), spec)
	if err != nil || second.State != "building" {
		t.Fatalf("retry=%+v err=%v", second, err)
	}
	waitForState(t, store, first.DatasetID, "ready")
	if _, err := os.Stat(filepath.Join(root, "datasets", first.DatasetID+".building")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("building directory remains: %v", err)
	}
}

func waitForState(t *testing.T, store *Store, id, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if state := store.Status(id).State; state == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("dataset %s did not reach %s; got %+v", id, want, store.Status(id))
}

type contextProvider struct{}

func (*contextProvider) Name() string        { return "context" }
func (*contextProvider) DataVersion() string { return "v1" }
func (*contextProvider) Bars(ctx context.Context, _ market.DatasetSpec) ([]market.Bar, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDatasetBuildTimeoutReleasesWorker(t *testing.T) {
	store, err := NewStoreWithBuildOptions(context.Background(), t.TempDir(), &contextProvider{}, 1, 1, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.EnsureFor(context.Background(), testSpec("AAPL"), "alice", 1)
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, store, status.DatasetID, "failed")
	if store.ActiveBuilds("alice") != 0 {
		t.Fatal("timed out build remained active")
	}
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
