package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type indexRouteRecorder struct {
	name  string
	specs []market.DatasetSpec
	err   error
}

func (p *indexRouteRecorder) Name() string        { return p.name }
func (p *indexRouteRecorder) DataVersion() string { return p.name + "-v1" }
func (p *indexRouteRecorder) Bars(_ context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	p.specs = append(p.specs, spec)
	var bars []market.Bar
	if p.err == nil {
		for _, s := range spec.Symbols {
			bars = append(bars, market.Bar{Symbol: s, Source: p.name})
		}
	}
	return bars, p.err
}

func TestIndexSymbolRouting(t *testing.T) {
	lb, fmp, fallback := &indexRouteRecorder{name: "longbridge-index"}, &indexRouteRecorder{name: "fmp-index"}, &indexRouteRecorder{name: "mock"}
	r := &Router{Index: fallback, IndexRoutes: map[string]Provider{"I:HSI": lb, "I:VIX": fmp, "I:SPX": nil}, IndexVersion: "routes-v1"}
	now := time.Now()
	spec := market.DatasetSpec{Symbols: []string{"I:HSI", "I:VIX", "I:DJI"}, Interval: "1m", From: now.Add(-time.Hour), To: now}
	bars, err := r.Bars(context.Background(), spec)
	if err != nil || len(bars) != 3 || len(lb.specs) != 1 || len(fmp.specs) != 1 || len(fallback.specs) != 1 {
		t.Fatalf("bars=%v err=%v", bars, err)
	}
	if lb.specs[0].Symbols[0] != "I:HSI" || fmp.specs[0].Symbols[0] != "I:VIX" || fallback.specs[0].Symbols[0] != "I:DJI" {
		t.Fatal("incorrect routing")
	}
	description, err := r.Describe(spec)
	if err != nil || !strings.Contains(description.DataVersion, "routes-v1") || !strings.Contains(description.Name, "longbridge-index") {
		t.Fatalf("description=%+v err=%v", description, err)
	}
	fmp.err = errors.New("upstream unavailable")
	spec.Symbols = []string{"I:VIX"}
	if _, err = r.Bars(context.Background(), spec); err == nil || len(fallback.specs) != 1 || len(lb.specs) != 1 {
		t.Fatal("failure must not fall back")
	}
	spec.Symbols = []string{"I:SPX"}
	if _, err = r.Bars(context.Background(), spec); !IsHistoricalProviderDisabled(err) {
		t.Fatalf("explicit disabled: %v", err)
	}
	r.Index = nil
	spec.Symbols = []string{"I:DJI"}
	if _, err = r.Bars(context.Background(), spec); !IsHistoricalProviderDisabled(err) {
		t.Fatalf("default disabled: %v", err)
	}
	spec.Symbols = []string{"I:HSI"}
	if _, err = r.Bars(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
}

func TestValidateIndexRoute(t *testing.T) {
	for _, tc := range []struct {
		name, symbol string
		valid        bool
	}{{"longbridge", "I:HSI", true}, {"longbridge", "I:VIX", false}, {"fmp", "I:VIX", true}, {"fmp", "I:HSTECH", false}, {"massive", "I:SPX", true}} {
		if err := ValidateIndexRoute(tc.name, tc.symbol); (err == nil) != tc.valid {
			t.Errorf("%+v err=%v", tc, err)
		}
	}
}

func TestIndexRouteHistoryPolicies(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	lb := &indexRouteRecorder{name: "longbridge-index"}
	fmp := &indexRouteRecorder{name: "fmp-index", err: errors.New("permission denied")}
	r := &Router{IndexRoutes: map[string]Provider{"I:HSI": lb, "I:VIX": fmp}, HistoryMaxYears: map[string]int{"longbridge": 2, "fmp": 3}, Now: func() time.Time { return now }}
	spec := market.DatasetSpec{Symbols: []string{"I:HSI", "I:VIX"}, Interval: "1d", From: now.AddDate(-8, 0, 0), To: now}
	if _, err := r.Bars(context.Background(), spec); err == nil {
		t.Fatal("expected upstream failure")
	}
	if len(lb.specs) != 2 || !lb.specs[1].From.Equal(now.AddDate(-2, 0, 0)) || len(fmp.specs) != 1 {
		t.Fatalf("lb=%v fmp=%v", lb.specs, fmp.specs)
	}
	spec.Symbols = []string{"I:VIX"}
	if _, err := r.Bars(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "cooling down") || len(fmp.specs) != 1 {
		t.Fatalf("cooldown err=%v calls=%d", err, len(fmp.specs))
	}
	now = now.Add(11 * time.Minute)
	fmp.err = nil
	if _, err := r.Bars(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if len(fmp.specs) != 4 || !fmp.specs[3].From.Equal(spec.To.AddDate(-3, 0, 0)) {
		t.Fatalf("fmp history policy=%v", fmp.specs)
	}
}

func TestIndexRouteCacheIdentity(t *testing.T) {
	p := &Mock{Version: "same-provider-version"}
	r := &Router{Index: p, IndexVersion: "route-config-a"}
	now := time.Now()
	spec := market.DatasetSpec{Symbols: []string{"I:HSI"}, Interval: "1d", From: now.AddDate(0, 0, -2), To: now}
	a, err := r.Describe(spec)
	if err != nil {
		t.Fatal(err)
	}
	r.IndexVersion = "route-config-b"
	b, err := r.Describe(spec)
	if err != nil {
		t.Fatal(err)
	}
	keyA, err := spec.Hash(market.SchemaVersion, a.DataVersion)
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := spec.Hash(market.SchemaVersion, b.DataVersion)
	if err != nil || keyA == keyB {
		t.Fatalf("cache keys unchanged: %s %s err=%v", keyA, keyB, err)
	}
}
