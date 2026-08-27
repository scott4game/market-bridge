package news

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func testArticle(id string, sequence int64, symbol string, kind Kind) Article {
	return Article{ID: id, Sequence: sequence, Kind: kind, Symbols: []string{symbol}, Title: "Title " + id, Summary: "summary", URL: "https://example.com/" + id, Publisher: "Example", PublishedAt: time.Now().UTC(), ReceivedAt: time.Now().UTC(), Provider: "test"}
}

func TestStoreDeduplicatesFiltersAndCleans(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	old := testArticle("old", 0, "AAPL", StockNews)
	old.ReceivedAt = time.Now().Add(-31 * 24 * time.Hour)
	inserted, ok, err := store.Insert(ctx, old)
	if err != nil || !ok || inserted.Sequence == 0 {
		t.Fatalf("insert=%+v ok=%v err=%v", inserted, ok, err)
	}
	if _, ok, err := store.Insert(ctx, old); err != nil || ok {
		t.Fatalf("duplicate ok=%v err=%v", ok, err)
	}
	priority := old
	priority.Kind = PressRelease
	if _, ok, err := store.Insert(ctx, priority); err != nil || ok {
		t.Fatalf("cross-feed duplicate ok=%v err=%v", ok, err)
	}
	priorityRows, _ := store.List(ctx, Query{Kinds: []Kind{PressRelease}, Symbols: []string{"AAPL"}, Limit: 10})
	if len(priorityRows) != 1 || priorityRows[0].Kind != PressRelease {
		t.Fatalf("press release priority=%+v", priorityRows)
	}
	newer, ok, err := store.Insert(ctx, testArticle("new", 0, "NVDA", PressRelease))
	if err != nil || !ok || newer.Sequence <= inserted.Sequence {
		t.Fatalf("newer=%+v ok=%v err=%v", newer, ok, err)
	}
	rows, err := store.List(ctx, Query{Symbols: []string{"NVDA"}, Kinds: []Kind{PressRelease}, Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ID != "new" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if n, err := store.Cleanup(ctx, time.Now().Add(-30*24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("cleanup=%d err=%v", n, err)
	}
}

func TestFMPMapsStableNewsAndHonorsRetryAfter(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("apikey") != "secret" || r.URL.Path != "/stable/news/stock-latest" {
			t.Fatalf("request=%s", r.URL.String())
		}
		if requests == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"symbol": "AAPL", "publishedDate": "2026-08-27 10:20:30", "publisher": "Wire", "title": "Apple update", "text": "Details", "url": "https://example.com/apple?utm_source=x"}})
	}))
	defer upstream.Close()
	provider := &FMP{APIKey: "secret", BaseURL: upstream.URL, Client: upstream.Client()}
	if _, err := provider.Latest(context.Background(), StockNews, 0, 100); err == nil {
		t.Fatal("expected rate limit error")
	} else if retry := err.(*UpstreamError).RetryAfter; retry != 7*time.Second {
		t.Fatalf("retry=%v", retry)
	}
	rows, err := provider.Latest(context.Background(), StockNews, 0, 100)
	if err != nil || len(rows) != 1 || rows[0].Symbols[0] != "AAPL" || rows[0].ID == "" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestNewsWebSocketReplaysThenPushes(t *testing.T) {
	store := openTestStore(t)
	service := NewService(store, nil, time.Minute, 30*24*time.Hour)
	first, _, _ := store.Insert(context.Background(), testArticle("first", 0, "AAPL", StockNews))
	server := httptest.NewServer(http.HandlerFunc(service.ServeWS))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	payload, _ := json.Marshal(map[string]any{"symbols": []string{"AAPL"}, "after_sequence": first.Sequence, "status": true})
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
	_, raw, err := conn.Read(ctx)
	if err != nil || !strings.Contains(string(raw), `"type":"status"`) {
		t.Fatalf("status=%s err=%v", raw, err)
	}
	second, _, _ := store.Insert(context.Background(), testArticle("second", 0, "AAPL", PressRelease))
	service.Broadcast(second)
	_, raw, err = conn.Read(ctx)
	if err != nil || !strings.Contains(string(raw), `"id":"second"`) {
		t.Fatalf("event=%s err=%v", raw, err)
	}
}

func TestNewsHTTPRejectsInvalidFilters(t *testing.T) {
	service := NewService(openTestStore(t), nil, time.Minute, 30*24*time.Hour)
	for _, target := range []string{"/v1/news?symbols=BTCUSDT.BINANCE", "/v1/news?kinds=unknown", "/v1/news?after_sequence=1&before_sequence=2"} {
		recorder := httptest.NewRecorder()
		service.ListHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("target=%s status=%d body=%s", target, recorder.Code, recorder.Body.String())
		}
	}
}
