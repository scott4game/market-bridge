package localclient

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/scott4game/market-bridge/internal/config"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const remoteTestToken = "fixture-remote-token-01234567890123456789"

func TestRemoteAuthenticationAndRoutes(t *testing.T) {
	h := &HTTP{}
	handler := h.RemoteHandler(remoteTestToken)
	for _, path := range []string{"/healthz", "/v1/news/stream", "/v1/live/ws", "/mcp", "/v1/cache/prune"} {
		for _, auth := range []string{"", "Bearer wrong"} {
			r := httptest.NewRequest("GET", "https://client.example"+path, nil)
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("Authorization", auth)
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			r.Header.Set("Origin", "https://untrusted.example")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("%s: %d", path, w.Code)
			}
		}
	}
	for _, tc := range []struct {
		path, method string
		status       int
	}{{"/healthz", "GET", 200}, {"/v1/cache/prune", "POST", 404}, {"/", "GET", 404}, {"/v1/datasets/id/refresh", "POST", 404}, {"/v1/me/indicators", "POST", 404}} {
		r := httptest.NewRequest(tc.method, "https://client.example"+tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+remoteTestToken)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s got %d", tc.path, w.Code)
		}
	}
	local := httptest.NewRecorder()
	h.Handler().ServeHTTP(local, httptest.NewRequest("GET", "http://localhost/healthz", nil))
	if local.Code != 200 {
		t.Fatal(local.Code)
	}
}
func TestRemoteCredentialNeverForwarded(t *testing.T) {
	h := mcpTestHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("inbound credential reached upstream or upstream credential missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	r := httptest.NewRequest("GET", "https://client.example/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+remoteTestToken)
	w := httptest.NewRecorder()
	h.RemoteHandler(remoteTestToken).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), remoteTestToken) {
		t.Fatal("credential leaked")
	}
}

type remoteAuthTransport struct{ base http.RoundTripper }

func (t remoteAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+remoteTestToken)
	r.Host = "client.example"
	return t.base.RoundTrip(r)
}
func TestRemoteMCPAndNews(t *testing.T) {
	h := mcpTestHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	backend := httptest.NewServer(h.RemoteHandler(remoteTestToken))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	srv := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	defer srv.Close()
	client := srv.Client()
	client.Transport = remoteAuthTransport{client.Transport}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "remote-test", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: client}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil || len(list.Tools) == 0 {
		t.Fatalf("MCP list failed %v", err)
	}
	response, err := client.Get(srv.URL + "/v1/news?limit=1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var data map[string]any
	if err := json.NewDecoder(response.Body).Decode(&data); err != nil || response.StatusCode != 200 {
		t.Fatalf("news: %v %d", err, response.StatusCode)
	}
}

func TestRemoteTLSStreams(t *testing.T) {
	h := mcpTestHTTP(t, http.NotFoundHandler())
	h.Live = NewLiveProxy(config.Client{})
	backend := httptest.NewServer(h.RemoteHandler(remoteTestToken))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	srv := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()
	wsURL := "wss" + strings.TrimPrefix(srv.URL, "https") + "/v1/live/ws"
	_, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: srv.Client()})
	if err == nil || resp.StatusCode != 401 {
		t.Fatal("unauthenticated websocket accepted")
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: srv.Client(), HTTPHeader: http.Header{"Authorization": []string{"Bearer " + remoteTestToken}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = conn.Write(ctx, websocket.MessageText, []byte(`{"symbols":["INTC"],"events":["quote"],"status":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = conn.Read(ctx); err != nil {
		t.Fatal("no subscription status", err)
	}
	conn.CloseNow()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/news/stream", nil)
	req.Header.Set("Authorization", "Bearer "+remoteTestToken)
	response, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("SSE failed")
	}
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, "heartbeat") {
		t.Fatal("no SSE heartbeat", err)
	}
}
