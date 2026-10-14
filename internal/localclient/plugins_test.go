package localclient

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scott4game/market-bridge/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const backendPluginToken = "fixture-private-plugin-token"

func testGateway(url string) *PluginGateway {
	return NewPluginGateway([]config.ClientPlugin{{ID: "analysis", URL: url, Token: backendPluginToken, TimeoutSeconds: 1, Routes: []config.PluginRoute{{Method: "GET", Path: "/v1/capabilities"}, {Method: "GET", Path: "/v1/jobs/{id}"}, {Method: "POST", Path: "/v1/scans"}}}})
}
func TestPluginRemoteAuthIsolationAndPaths(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+backendPluginToken {
			t.Error("wrong backend credential")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-For") != "" {
			t.Error("untrusted headers forwarded")
		}
		if r.Method == "POST" {
			data, _ := io.ReadAll(r.Body)
			if string(data) != `{"symbols":["INTC"]}` {
				t.Error("wrong JSON")
			}
			w.WriteHeader(202)
		}
		_, _ = io.WriteString(w, `{"status":"queued","echo":"`+backendPluginToken+`"}`)
	}))
	defer server.Close()
	h := &HTTP{Plugins: testGateway(server.URL)}
	remote := h.RemoteHandler(remoteTestToken)
	for _, auth := range []string{"", "Bearer wrong"} {
		r := httptest.NewRequest("GET", "https://client.test/v1/plugins/analysis/v1/capabilities", nil)
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		remote.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unauthenticated upstream request")
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/v1/plugins", "", 200}, {"GET", "/v1/plugins/analysis/v1/capabilities", "", 200},
		{"POST", "/v1/plugins/analysis/v1/scans", `{"symbols":["INTC"]}`, 202},
		{"GET", "/v1/plugins/analysis/v1/jobs/job-1", "", 200},
		{"POST", "/v1/plugins/analysis/v1/scans", `not json`, 400},
		{"GET", "/v1/plugins/unknown/v1/capabilities", "", 404},
		{"GET", "/v1/plugins/analysis/v1/delete", "", 404},
		{"DELETE", "/v1/plugins/analysis/v1/jobs/job-1", "", 404},
	} {
		r := httptest.NewRequest(tc.method, "https://client.test"+tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+remoteTestToken)
		r.Header.Set("Cookie", "secret")
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		w := httptest.NewRecorder()
		remote.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), backendPluginToken) || strings.Contains(w.Body.String(), server.URL) {
			t.Fatal("secret or backend URL leaked")
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected upstream calls: %d", calls.Load())
	}
	for _, path := range []string{"/v1/jobs/..", "/v1/jobs/%2e%2e", "/v1/jobs/a/b", "/v1/jobs/a?x=y", "/v1/jobs/a\\b"} {
		if allowedPluginPath(h.Plugins.targets["analysis"].definition.Routes, "GET", path) {
			t.Fatal("unsafe path", path)
		}
	}
}
func TestPluginRedirectErrorsTimeoutAndCancellation(t *testing.T) {
	var redirected atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer sink.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("case") {
		case "redirect":
			http.Redirect(w, r, sink.URL, 302)
		case "rate":
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(429)
			_, _ = io.WriteString(w, "not-json "+backendPluginToken)
		case "partial":
			w.WriteHeader(206)
			_, _ = io.WriteString(w, `{"complete":false,"data_issues":["missing"]}`)
		case "slow":
			<-r.Context().Done()
		default:
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"error":"denied"}`)
		}
	}))
	defer server.Close()
	g := testGateway(server.URL)
	for _, tc := range []struct {
		query  string
		status int
	}{{"case=redirect", 502}, {"case=rate", 429}, {"case=partial", 206}, {"", 403}, {"case=slow", 504}} {
		reply := g.call(context.Background(), "analysis", "GET", "/v1/capabilities", tc.query, nil)
		if reply.status != tc.status {
			t.Fatalf("%s: %d", tc.query, reply.status)
		}
		if strings.Contains(string(reply.value), backendPluginToken) {
			t.Fatal("token leaked")
		}
		if tc.status == 429 && reply.retryAfter != "7" {
			t.Fatal("Retry-After lost")
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("followed redirect")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if reply := g.call(ctx, "analysis", "GET", "/v1/capabilities", "case=slow", nil); reply.status != 499 {
		t.Fatal(reply.status)
	}
}
func TestPluginMCPDiscoveryAndSubmit(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		_, _ = io.WriteString(w, `{"id":"job-1"}`)
	}))
	defer backend.Close()
	h := mcpTestHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	h.Plugins = testGateway(backend.URL)
	srv := httptest.NewServer(h.RemoteHandler(remoteTestToken))
	defer srv.Close()
	client := srv.Client()
	client.Transport = remoteAuthTransport{client.Transport}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "plugins-test", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: client}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range listed.Tools {
		if tool.Name == "submit_plugin" {
			found = true
			if tool.Annotations.ReadOnlyHint {
				t.Fatal("POST advertised read-only")
			}
		}
	}
	if !found {
		t.Fatal("plugin tools missing")
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "submit_plugin", Arguments: map[string]any{"plugin": "analysis", "path": "/v1/scans", "body": map[string]any{"symbols": []string{"INTC"}}}})
	if err != nil || result.IsError {
		t.Fatalf("call: %v %#v", err, result)
	}
	data, _ := json.Marshal(result)
	if !strings.Contains(string(data), "job-1") {
		t.Fatal("job result missing")
	}
}
