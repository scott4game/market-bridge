package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type memoryBarCache struct {
	mu   sync.Mutex
	data map[string][]market.Bar
}

func (c *memoryBarCache) Get(_ context.Context, key string) ([]market.Bar, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	bars, ok := c.data[key]
	return append([]market.Bar(nil), bars...), ok, nil
}

func (c *memoryBarCache) Set(_ context.Context, key string, bars []market.Bar, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data == nil {
		c.data = map[string][]market.Bar{}
	}
	c.data[key] = append([]market.Bar(nil), bars...)
	return nil
}

func (c *memoryBarCache) Healthy(context.Context) error { return nil }

type redisCountingProvider struct {
	mu    sync.Mutex
	calls int
}

type redisTestClickHouse struct {
	mu    sync.Mutex
	bars  []market.Bar
	reads int
}

func (c *redisTestClickHouse) Healthy(context.Context) error { return nil }
func (c *redisTestClickHouse) QueryBars(_ context.Context, _ market.DatasetSpec) ([]market.Bar, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	return append([]market.Bar(nil), c.bars...), nil
}
func (c *redisTestClickHouse) WriteBars(context.Context, string, market.AdjustmentMode, []market.Bar, uint64) error {
	return nil
}

func (p *redisCountingProvider) Name() string        { return "counting" }
func (p *redisCountingProvider) DataVersion() string { return "counting-v1" }
func (p *redisCountingProvider) Bars(_ context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	price := market.DecimalFromFloat(10)
	return []market.Bar{{Symbol: spec.Symbols[0], Timestamp: spec.From, Open: price, High: price, Low: price, Close: price, Completed: true}}, nil
}

func TestProviderBarsUseSharedRedisCache(t *testing.T) {
	provider := &redisCountingProvider{}
	store, err := NewStore(t.TempDir(), provider)
	if err != nil {
		t.Fatal(err)
	}
	store.ConfigureBarCache(&memoryBarCache{}, time.Hour, 15*time.Minute, 730*24*time.Hour)
	from := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: from, To: from.Add(time.Minute), Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	if _, cached, err := store.ProviderBarsCached(context.Background(), spec); err != nil || cached {
		t.Fatalf("first request cached=%t err=%v", cached, err)
	}
	if _, cached, err := store.ProviderBarsCached(context.Background(), spec); err != nil || !cached {
		t.Fatalf("second request cached=%t err=%v", cached, err)
	}
	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 1 {
		t.Fatalf("provider calls=%d", calls)
	}
}

func TestStorageCapabilitiesExposeRemoteRedis(t *testing.T) {
	store, err := NewStore(t.TempDir(), &redisCountingProvider{})
	if err != nil {
		t.Fatal(err)
	}
	handler := (&HTTP{Store: store, RedisEnabled: true, Redis: &memoryBarCache{}, DataVersion: "v1"}).Handler()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/storage/capabilities", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Redis struct {
			Enabled bool `json:"enabled"`
			Healthy bool `json:"healthy"`
		} `json:"redis"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Redis.Enabled || !payload.Redis.Healthy {
		t.Fatalf("redis capability=%+v", payload.Redis)
	}
}

func TestServerRedisCachesCanonicalClickHouseResponse(t *testing.T) {
	store, err := NewStore(t.TempDir(), &redisCountingProvider{})
	if err != nil {
		t.Fatal(err)
	}
	cache := &memoryBarCache{}
	store.ConfigureBarCache(cache, time.Hour, 15*time.Minute, 730*24*time.Hour)
	catalog, err := OpenHistoryCatalog(t.TempDir() + "/history.db")
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	from := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: from, To: from.Add(time.Minute), Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	price := market.DecimalFromFloat(10)
	bars := []market.Bar{{Symbol: "AAPL", Timestamp: from, Open: price, High: price, Low: price, Close: price, Completed: true}}
	if err := catalog.RecordCoverage(context.Background(), spec, "v1:"+market.KlineStorageVersion, bars, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	clickhouse := &redisTestClickHouse{bars: bars}
	handler := (&HTTP{Store: store, RedisEnabled: true, Redis: cache, ClickHouseEnabled: true, ClickHouse: clickhouse, HistoryCatalog: catalog, DataVersion: "v1"}).Handler()
	body, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		t.Fatal(err)
	}
	for requestNumber, expectedSource := range []string{"server-clickhouse", "server-redis"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/history/bars", bytes.NewReader(body)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("request %d: status=%d body=%s", requestNumber+1, recorder.Code, recorder.Body.String())
		}
		var payload struct {
			Source string `json:"source"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Source != expectedSource {
			t.Fatalf("request %d: source=%q, want %q", requestNumber+1, payload.Source, expectedSource)
		}
	}
	clickhouse.mu.Lock()
	reads := clickhouse.reads
	clickhouse.mu.Unlock()
	if reads != 1 {
		t.Fatalf("ClickHouse reads=%d, want 1", reads)
	}
}

func TestClickHouseLayoutVersionInvalidatesOldCoverage(t *testing.T) {
	provider := &redisCountingProvider{}
	store, err := NewStore(t.TempDir(), provider)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := OpenHistoryCatalog(t.TempDir() + "/history.db")
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	from := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: from, To: from.Add(time.Minute), Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	if err := catalog.RecordCoverage(context.Background(), spec, "v1", []market.Bar{{Symbol: "AAPL", Timestamp: from, Completed: true}}, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	handler := (&HTTP{Store: store, ClickHouseEnabled: true, ClickHouse: &redisTestClickHouse{}, HistoryCatalog: catalog, DataVersion: "v1"}).Handler()
	body, _ := json.Marshal(map[string]any{"spec": spec})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/history/bars", bytes.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 1 {
		t.Fatalf("provider calls=%d, want 1", calls)
	}
}
