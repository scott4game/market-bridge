package localclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/market"
)

func newFlowBasketCache(t *testing.T, serverURL string) *Cache {
	t.Helper()
	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: serverURL, RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return cache
}

func TestFlowBasketCRUDAndRevision(t *testing.T) {
	cache := newFlowBasketCache(t, "http://127.0.0.1:1")
	created, err := cache.CreateFlowBasket(context.Background(), flowBasketMutation{Name: " Chips ", Symbols: []string{"nvda.us", "AMD", "NVDA"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Chips" || len(created.Symbols) != 2 || created.Symbols[0] != "AMD" || created.Symbols[1] != "NVDA" || created.Revision != 1 {
		t.Fatalf("created=%+v", created)
	}
	if _, err := cache.CreateFlowBasket(context.Background(), flowBasketMutation{Name: "chips", Symbols: []string{"AAPL"}}); err != errFlowBasketName {
		t.Fatalf("duplicate error=%v", err)
	}
	updated, err := cache.UpdateFlowBasket(context.Background(), created.ID, flowBasketMutation{Name: "Semis", Symbols: []string{"NVDA"}, Revision: 1})
	if err != nil || updated.Revision != 2 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if _, err := cache.UpdateFlowBasket(context.Background(), created.ID, flowBasketMutation{Name: "Old", Symbols: []string{"NVDA"}, Revision: 1}); err != errFlowBasketConflict {
		t.Fatalf("conflict error=%v", err)
	}
	if err := cache.DeleteFlowBasket(context.Background(), created.ID, 1); err != errFlowBasketConflict {
		t.Fatalf("delete conflict=%v", err)
	}
	if err := cache.DeleteFlowBasket(context.Background(), created.ID, 2); err != nil {
		t.Fatal(err)
	}
}

func TestFlowBasketRejectsOverTwoHundredSymbols(t *testing.T) {
	cache := newFlowBasketCache(t, "http://127.0.0.1:1")
	symbols := make([]string, 201)
	for index := range symbols {
		symbols[index] = "A" + string(rune('A'+index%26)) + string(rune('A'+index/26))
	}
	if _, err := cache.CreateFlowBasket(context.Background(), flowBasketMutation{Name: "Too many", Symbols: symbols}); err == nil {
		t.Fatal("oversized basket was accepted")
	}
}

func TestNamedFlowBasketBuildIsAsynchronousAndDeduplicated(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/storage/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"data_version": "test-v1", "history_revision": 7, "clickhouse": map[string]any{"enabled": false}, "redis": map[string]any{"enabled": false}})
		case "/v1/market-analytics/basket-flow":
			calls.Add(1)
			time.Sleep(50 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"source": "massive", "method": "massive_close_location_v1", "proxy": true, "complete": true,
				"coverage": map[string]any{"total": 1, "evaluated": 1}, "points": []any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	cache := newFlowBasketCache(t, upstream.URL)
	basket, err := cache.CreateFlowBasket(context.Background(), flowBasketMutation{Name: "One", Symbols: []string{"NVDA"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := (&HTTP{Cache: cache}).Handler()
	path := "/v1/me/flow-baskets/" + basket.ID + "/flow?from=2026-09-01T00:00:00Z&to=2026-09-02T00:00:00Z&interval=1d&session=regular"
	for range 2 {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"complete":false`)) {
			t.Fatalf("pending status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if bytes.Contains(recorder.Body.Bytes(), []byte(`"complete":true`)) {
			if calls.Load() != 1 {
				t.Fatalf("upstream calls=%d", calls.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background basket build did not complete")
}

func TestFlowBasketCacheKeyIncludesHistoryIdentity(t *testing.T) {
	basket := flowBasket{ID: "x", Revision: 1, Symbols: []string{"NVDA"}}
	spec := market.DatasetSpec{Symbols: basket.Symbols, Interval: "1d", From: time.Unix(1, 0), To: time.Unix(2, 0), Session: market.RegularSession, Adjustment: market.Raw}
	first, _ := flowBasketCacheKey(basket, spec, "v1")
	second, _ := flowBasketCacheKey(basket, spec, "v2")
	if first == second {
		t.Fatal("data version did not invalidate flow basket cache")
	}
}
