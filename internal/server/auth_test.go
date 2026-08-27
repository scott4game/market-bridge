package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/scott4game/market-bridge/internal/access"
	"github.com/scott4game/market-bridge/internal/live"
	"github.com/scott4game/market-bridge/internal/provider"
)

func TestMultiUserHTTPAuthenticationAndProfile(t *testing.T) {
	ctx := context.Background()
	auth, err := access.Open(filepath.Join(t.TempDir(), "auth.db"), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	_, err = auth.CreateUser(ctx, "alice", "member")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.CreateKey(ctx, "alice", "laptop", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	data, err := NewStore(t.TempDir(), &provider.Mock{Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	h := (&HTTP{Store: data, Access: auth, Limiter: access.NewLimiter()}).Handler()

	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	profile := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(profile, req)
	if profile.Code != http.StatusOK {
		t.Fatalf("profile status=%d body=%s", profile.Code, profile.Body.String())
	}
	var p access.Principal
	if err := json.Unmarshal(profile.Body.Bytes(), &p); err != nil || p.Name != "alice" {
		t.Fatalf("profile=%+v err=%v", p, err)
	}

	forbidden := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/providers/massive/usage", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(forbidden, req)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("provider usage status=%d", forbidden.Code)
	}

	newsUnavailable := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/news", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(newsUnavailable, req)
	if newsUnavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("news status=%d body=%s", newsUnavailable.Code, newsUnavailable.Body.String())
	}

	watchlist := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v1/me/watchlist", bytes.NewBufferString(`{"symbols":["AAPL"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(watchlist, req)
	if watchlist.Code != http.StatusOK {
		t.Fatalf("watchlist status=%d body=%s", watchlist.Code, watchlist.Body.String())
	}

	secondWatchlist := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v1/me/watchlist", bytes.NewBufferString(`{"symbols":["TSLA"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(secondWatchlist, req)
	if secondWatchlist.Code != http.StatusOK || !strings.Contains(secondWatchlist.Body.String(), `"subscription_mode":"on_demand"`) {
		t.Fatalf("on-demand watchlist status=%d body=%s", secondWatchlist.Code, secondWatchlist.Body.String())
	}

}

func TestAuthenticatedLiveWebSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	auth, err := access.Open(filepath.Join(t.TempDir(), "auth.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	_, _ = auth.CreateUser(ctx, "streamer", "member")
	token, _, err := auth.CreateKey(ctx, "streamer", "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	limiter := access.NewLimiter()
	hub, err := live.NewHub(live.MockSource{}, live.NopSink{})
	if err != nil {
		t.Fatal(err)
	}
	hub.ConfigureAccess(auth, limiter)
	go hub.Run(ctx)
	data, _ := NewStore(t.TempDir(), &provider.Mock{Version: "v1"})
	srv := httptest.NewServer((&HTTP{Store: data, Access: auth, Limiter: limiter, Live: hub}).Handler())
	defer srv.Close()
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/live/ws", &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"symbols":["AAPL"],"events":["bar"]}`)); err != nil {
		t.Fatal(err)
	}
	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()
	if _, _, err := conn.Read(readCtx); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyTokenRetainsAdminAccess(t *testing.T) {
	auth, err := access.Open(filepath.Join(t.TempDir(), "auth.db"), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	data, _ := NewStore(t.TempDir(), &provider.Mock{Version: "v1"})
	h := (&HTTP{Store: data, Access: auth, Limiter: access.NewLimiter()}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer legacy")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
