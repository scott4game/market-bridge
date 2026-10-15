package localclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scott4game/market-bridge/internal/config"
)

func TestMCPGatewayDiscoveryAuthenticationAndErrorPreservation(t *testing.T) {
	upstream := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls atomic.Int32
	upstream.AddTool(&mcp.Tool{Name: "compute", InputSchema: map[string]any{"type": "object"}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			calls.Add(1)
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "missing data"}}, StructuredContent: map[string]any{"status": "data_insufficient", "b": nil}}, nil
		})
	upstream.AddTool(&mcp.Tool{Name: "not_allowed", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.Error("unlisted tool reached")
		return nil, nil
	})
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-backend-fixture-token" || r.Header.Get("Cookie") != "" {
			t.Error("credentials leaked or missing")
			http.Error(w, "denied", 401)
			return
		}
		transport.ServeHTTP(w, r)
	}))
	defer backend.Close()
	g := NewMCPGateway([]config.MCPBackend{{ID: "analysis", URL: backend.URL, Token: "private-backend-fixture-token", TimeoutSeconds: 5, Tools: []config.MCPAllowedTool{{Name: "compute", Policy: "analysis"}}}})
	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: backend.URL, MCPEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	h := &HTTP{Cache: cache, MCPBackends: g}
	remote := httptest.NewServer(h.RemoteHandler("remote-fixture-token-1234567890123456"))
	defer remote.Close()
	req, _ := http.NewRequest("POST", remote.URL+"/mcp", strings.NewReader(`{}`))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("remote auth bypass")
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: remote.URL + "/mcp", HTTPClient: &http.Client{Transport: backendTransport{token: "remote-fixture-token-1234567890123456"}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var advertised *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "analysis__compute" {
			advertised = tool
		}
		if tool.Name == "analysis__not_allowed" {
			t.Fatal("allowlist bypass")
		}
	}
	if advertised == nil || advertised.Annotations.ReadOnlyHint || advertised.Meta["gateway/policy"] != "analysis" {
		t.Fatal("upstream hint overrode admin policy", advertised)
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "analysis__compute", Arguments: map[string]any{}})
	if err != nil || !result.IsError || calls.Load() != 1 {
		t.Fatal(result, err, calls.Load())
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), `"b":null`) || !strings.Contains(string(raw), "missing data") {
		t.Fatal(string(raw))
	}
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "analysis__compute", Meta: mcp.Meta{"gateway/fingerprint": "old"}, Arguments: map[string]any{}})
	if err != nil || !result.IsError || calls.Load() != 1 {
		t.Fatal("changed catalog called backend")
	}
	backend.Close()
	g.backends[0].mu.Lock()
	g.backends[0].expires = time.Time{}
	g.backends[0].mu.Unlock()
	tools, err = session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	market := false
	for _, tool := range tools.Tools {
		if tool.Name == "analysis__compute" {
			t.Fatal("stale backend advertised")
		}
		if tool.Name == "get_bars" {
			market = true
		}
	}
	if !market {
		t.Fatal("backend failure removed market tools")
	}
}
