package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

func TestUsageTrackerCountsAndWindows(t *testing.T) {
	loc := time.FixedZone("test-local", 8*60*60)
	u, err := NewUsageTracker(filepath.Join(t.TempDir(), "usage.db"), "stocks_basic", 5, 0, loc)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	now := time.Date(2026, 8, 31, 15, 59, 30, 0, time.UTC) // local 2026-08-31 23:59:30
	u.now = func() time.Time { return now }

	u.Begin("massive", "aggs")(200, nil)
	u.Begin("massive", "aggs")(429, nil)
	u.Begin("massive", "reference")(0, errors.New("network down"))

	s, err := u.Snapshot(context.Background(), "massive")
	if err != nil {
		t.Fatal(err)
	}
	if s.Rolling60s.Used != 3 || s.Rolling60s.Remaining == nil || *s.Rolling60s.Remaining != 2 {
		t.Fatalf("unexpected rolling usage: %+v", s.Rolling60s)
	}
	if s.CurrentMonth.Used != 3 || s.CurrentMonth.Period != "2026-08" || s.CurrentMonth.Limit != nil {
		t.Fatalf("unexpected monthly usage: %+v", s.CurrentMonth)
	}
	if s.Totals.Requests != 3 || s.Totals.Success != 1 || s.Totals.Failed != 2 || len(s.ByEndpoint) != 2 {
		t.Fatalf("unexpected totals: %+v endpoints=%+v", s.Totals, s.ByEndpoint)
	}

	now = now.Add(61 * time.Second)
	s, err = u.Snapshot(context.Background(), "massive")
	if err != nil {
		t.Fatal(err)
	}
	if s.Rolling60s.Used != 0 || s.CurrentMonth.Period != "2026-09" || s.CurrentMonth.Used != 0 {
		t.Fatalf("window/month did not roll over: %+v %+v", s.Rolling60s, s.CurrentMonth)
	}
}

func TestUsageTrackerRecoversInterruptedRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	u, err := NewUsageTracker(path, "stocks_basic", 5, 0, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	_ = u.Begin("massive", "aggs")
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}

	u, err = NewUsageTracker(path, "stocks_basic", 5, 0, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	s, err := u.Snapshot(context.Background(), "massive")
	if err != nil {
		t.Fatal(err)
	}
	if s.Totals.Requests != 1 || s.Totals.Failed != 1 || s.Totals.InFlight != 0 {
		t.Fatalf("interrupted request was not recovered: %+v", s.Totals)
	}
}

func TestMassiveCountsPaginationRequests(t *testing.T) {
	var calls atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			fmt.Fprintf(w, `{"status":"OK","next_url":%q,"results":[{"o":1,"h":2,"l":1,"c":2,"v":10,"t":1786320000000}]}`, upstream.URL+"/page/2")
			return
		}
		fmt.Fprint(w, `{"status":"OK","results":[{"o":2,"h":3,"l":2,"c":3,"v":11,"t":1786406400000}]}`)
	}))
	defer upstream.Close()

	u, err := NewUsageTracker(filepath.Join(t.TempDir(), "usage.db"), "stocks_basic", 5, 0, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	p := &Massive{APIKey: "test", BaseURL: upstream.URL, Version: "test-v1", Usage: u}
	bars, err := p.Bars(context.Background(), market.DatasetSpec{
		Symbols: []string{"AAPL"}, Interval: "1d",
		From: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC),
		Session: market.RegularSession, Adjustment: market.SplitAdjusted,
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := u.Snapshot(context.Background(), "massive")
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 || calls.Load() != 2 || s.Totals.Requests != 2 || s.Totals.Success != 2 {
		t.Fatalf("bars=%d calls=%d usage=%+v", len(bars), calls.Load(), s.Totals)
	}
}
