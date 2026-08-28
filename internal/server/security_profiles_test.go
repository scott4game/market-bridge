package server

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/provider"
)

type profileCatalogProvider struct {
	mu         sync.Mutex
	securities []provider.Security
	profiles   map[string]provider.SecurityProfile
	calls      map[string]int
	fail       bool
}

func (p *profileCatalogProvider) Name() string        { return "profile-test" }
func (p *profileCatalogProvider) DataVersion() string { return "profile-test-v1" }
func (p *profileCatalogProvider) Bars(context.Context, market.DatasetSpec) ([]market.Bar, error) {
	return nil, nil
}
func (p *profileCatalogProvider) Securities(context.Context) ([]provider.Security, error) {
	return p.securities, nil
}
func (p *profileCatalogProvider) SecurityProfile(_ context.Context, symbol string) (provider.SecurityProfile, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[symbol]++
	if p.fail {
		return provider.SecurityProfile{}, errors.New("upstream unavailable")
	}
	profile, ok := p.profiles[symbol]
	if !ok {
		return provider.SecurityProfile{}, errors.New("not found")
	}
	return profile, nil
}

func TestSecurityProfileCatalogCachesAndFiltersUniverse(t *testing.T) {
	p := &profileCatalogProvider{
		securities: []provider.Security{{Symbol: "MRNA"}, {Symbol: "AAPL"}, {Symbol: "700.HK"}},
		profiles: map[string]provider.SecurityProfile{
			"AAPL": validTestProfile("AAPL", "0001", 100),
			"MRNA": validTestProfile("MRNA", "0002", 50),
		},
		calls: map[string]int{},
	}
	store, err := NewStore(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := OpenSecurityProfileCatalog(filepath.Join(t.TempDir(), "profiles.db"), store, time.Hour, 30*24*time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	first, err := catalog.Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := catalog.Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Complete || first.Source != "massive" || len(first.Profiles) != 2 || second.Source != "cache" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if p.calls["AAPL"] != 1 || p.calls["MRNA"] != 1 || p.calls["700.HK"] != 0 {
		t.Fatalf("calls=%v", p.calls)
	}
}

func TestSecurityProfileCatalogReturnsStaleCacheOnRefreshFailure(t *testing.T) {
	p := &profileCatalogProvider{
		securities: []provider.Security{{Symbol: "MRNA"}},
		profiles:   map[string]provider.SecurityProfile{"MRNA": validTestProfile("MRNA", "0002", 50)},
		calls:      map[string]int{},
	}
	store, err := NewStore(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := OpenSecurityProfileCatalog(filepath.Join(t.TempDir(), "profiles.db"), store, time.Nanosecond, time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	if _, err := catalog.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	p.fail = true
	response, err := catalog.Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if response.Complete || len(response.Errors) != 1 || len(response.Profiles) != 1 || !response.Profiles[0].Stale {
		t.Fatalf("response=%+v", response)
	}
}

func validTestProfile(symbol, cik string, marketCap float64) provider.SecurityProfile {
	return provider.SecurityProfile{Symbol: symbol, Name: symbol + " Inc.", CIK: cik, Type: "CS", Active: true, Locale: "us", Market: "stocks", MarketCap: marketCap, SICCode: "2834", Provider: "massive"}
}
