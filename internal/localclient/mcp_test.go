package localclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/news"
)

func mcpTestHTTP(t *testing.T, upstream http.Handler) *HTTP {
	t.Helper()
	remote := httptest.NewServer(upstream)
	t.Cleanup(remote.Close)
	c, err := NewCache(config.Client{MCPEnabled: true, CacheDir: t.TempDir(), ServerURL: remote.URL, ServerToken: "test-secret", ParquetTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	store, err := news.OpenStore(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &HTTP{Cache: c, News: NewNewsProxy(config.Client{}, store)}
}

func mcpTestSession(t *testing.T, h *HTTP) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func mcpCall(t *testing.T, s *mcp.ClientSession, name string, args any, wantError bool) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError != wantError {
		t.Fatalf("%s: error=%v content=%v", name, result.IsError, result.Content)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "test-secret") {
		t.Fatal("credential leaked")
	}
	if wantError {
		return nil
	}
	if len(result.Content) == 0 || result.StructuredContent == nil {
		t.Fatal("missing structured or text content")
	}
	var out map[string]any
	raw, _ = json.Marshal(result.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMCPQueryTools(t *testing.T) {
	h := mcpTestHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("missing upstream authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/storage/capabilities":
			w.Write([]byte(`{"clickhouse":{"enabled":true,"healthy":true}}`))
		case "/v1/history/bars":
			w.Write([]byte(`{"source":"mock","warning":"test-secret","bars":[{"symbol":"NVDA","timestamp":"2026-09-07T14:01:00Z"},{"symbol":"NVDA","timestamp":"2026-09-07T14:00:00Z"}]}`))
		case "/v1/providers/status":
			w.Write([]byte(`{"us":{"history_enabled":true}}`))
		case "/v1/live/trades/NVDA":
			if r.URL.Query().Get("limit") != "2" {
				t.Error("trade limit missing")
			}
			w.Write([]byte(`{"trades":[],"symbol":"NVDA"}`))
		case "/v1/options/contracts":
			if r.URL.Query().Get("underlying") != "NVDA" {
				t.Error("underlying missing")
			}
			w.Write([]byte(`{"source":"mock","contracts":[{"ticker":"O:CCC"},{"ticker":"O:AAA"},{"ticker":"O:BBB"}]}`))
		case "/v1/options/bars/O:NVDA":
			w.Write([]byte(`{"source":"mock","bars":[{"timestamp":"2026-09-07T16:00:00+02:00"},{"timestamp":"2026-09-07T14:01:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	for i := 0; i < 2; i++ {
		_, _, err := h.News.Service.Store.Insert(context.Background(), news.Article{ID: strings.Repeat("a", i+1), Kind: news.StockNews, Symbols: []string{"NVDA"}, Title: "test", URL: "https://example.com", PublishedAt: time.Now(), ReceivedAt: time.Now(), Provider: "test"})
		if err != nil {
			t.Fatal(err)
		}
	}
	s := mcpTestSession(t, h)
	list, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 6 {
		t.Fatalf("tools=%d", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if tool.InputSchema == nil || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("invalid tool %s", tool.Name)
		}
	}
	bars := mcpCall(t, s, "get_bars", map[string]any{"symbol": "NVDA", "interval": "1m", "from": "2026-09-07T14:00:00Z", "to": "2026-09-07T14:02:00Z", "adjustment": "raw", "limit": 1}, false)
	if bars["count"] != float64(1) || bars["truncated"] != true || bars["warning"] == nil {
		t.Fatalf("bars=%v", bars)
	}
	if bars["bars"].([]any)[0].(map[string]any)["timestamp"] != "2026-09-07T14:01:00Z" {
		t.Fatal("did not retain latest bar")
	}
	mcpCall(t, s, "get_recent_trades", map[string]any{"symbol": "NVDA", "limit": 2}, false)
	mcpCall(t, s, "get_provider_status", map[string]any{}, false)
	contracts := mcpCall(t, s, "get_option_contracts", map[string]any{"underlying": "NVDA", "limit": 1, "offset": 1}, false)
	if contracts["next_offset"] != float64(2) || contracts["contracts"].([]any)[0].(map[string]any)["ticker"] != "O:BBB" {
		t.Fatalf("contracts=%v", contracts)
	}
	optionBars := mcpCall(t, s, "get_option_bars", map[string]any{"contract": "O:NVDA", "from": "2026-09-07", "to": "2026-09-08", "limit": 1}, false)
	if optionBars["bars"].([]any)[0].(map[string]any)["timestamp"] != "2026-09-07T14:01:00Z" {
		t.Fatal("incorrect timestamp sorting")
	}
	articles := mcpCall(t, s, "get_news", map[string]any{"symbols": []string{"NVDA"}, "limit": 1}, false)
	if articles["next_before_sequence"] == nil {
		t.Fatal("missing news cursor")
	}
	mcpCall(t, s, "get_news", map[string]any{"before_sequence": articles["next_before_sequence"], "limit": 1}, false)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"get_bars", map[string]any{"symbol": "NVDA", "interval": "bad", "from": "2026-09-07T00:00:00Z", "to": "2026-09-08T00:00:00Z"}},
		{"get_recent_trades", map[string]any{"symbol": "NVDA", "limit": 1001}},
		{"get_option_contracts", map[string]any{"underlying": "NVDA", "offset": -1}},
		{"get_option_bars", map[string]any{"contract": "NVDA", "from": "2026-09-07", "to": "2026-09-08"}},
		{"get_news", map[string]any{"after_sequence": 1, "before_sequence": 2}},
	} {
		mcpCall(t, s, tc.name, tc.args, true)
	}
}

func TestMCPAccessAndDisabled(t *testing.T) {
	for _, tc := range []struct {
		name, peer, host, origin string
		enabled                  bool
		allowDocker              bool
		want                     int
	}{
		{"remote", "192.168.1.2:1234", "localhost", "", true, false, 403},
		{"docker", "172.20.0.1:1234", "127.0.0.1:17600", "", true, true, 405},
		{"public", "203.0.113.10:1234", "localhost", "", true, true, 403},
		{"host", "127.0.0.1:1234", "evil.example", "", true, false, 403},
		{"origin", "127.0.0.1:1234", "localhost", "https://evil.example", true, false, 403},
		{"disabled", "127.0.0.1:1234", "localhost", "", false, false, 404},
		{"local", "127.0.0.1:1234", "localhost", "", true, false, 405},
		{"ipv6", "[::1]:1234", "[::1]:17600", "", true, false, 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &HTTP{Cache: &Cache{cfg: config.Client{MCPEnabled: tc.enabled, MCPAllowDocker: tc.allowDocker}}}
			r := httptest.NewRequest("GET", "http://localhost/mcp", nil)
			r.RemoteAddr = tc.peer
			r.Host = tc.host
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			w := httptest.NewRecorder()
			h.Handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestMCPUpstreamErrorsAndCancellation(t *testing.T) {
	for _, status := range []int{401, 503} {
		h := mcpTestHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":"test-secret"}`))
		}))
		mcpCall(t, mcpTestSession(t, h), "get_provider_status", map[string]any{}, true)
	}
	entered, cancelled := make(chan struct{}), make(chan struct{})
	h := mcpTestHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(cancelled) }))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := h.mcpServerJSON(ctx, "/v1/providers/status", nil); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream not called")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation not propagated")
	}
	if err := <-done; err == nil {
		t.Fatal("expected cancellation error")
	}
	h.News = nil
	mcpCall(t, mcpTestSession(t, h), "get_news", map[string]any{}, true)
}

func TestMCPCancelToolRequest(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan struct{})
	h := mcpTestHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(cancelled)
	}))
	s := mcpTestSession(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "get_provider_status", Arguments: map[string]any{}})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not reach upstream")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP cancellation did not reach upstream")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancelled call")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call did not finish")
	}
}

func TestMCPEmptyNewsAndSchema(t *testing.T) {
	h := mcpTestHTTP(t, http.NotFoundHandler())
	s := mcpTestSession(t, h)
	out := mcpCall(t, s, "get_news", map[string]any{}, false)
	if rows, ok := out["news"].([]any); !ok || len(rows) != 0 {
		t.Fatalf("empty news=%v", out)
	}
	for _, args := range []map[string]any{{"limit": 1}, {"symbol": "NVDA", "limit": "two"}} {
		result, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_recent_trades", Arguments: args})
		if err == nil && !result.IsError {
			t.Fatal("invalid schema accepted")
		}
	}
}
