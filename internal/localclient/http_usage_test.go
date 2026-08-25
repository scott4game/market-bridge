package localclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scott4game/market-bridge/internal/config"
)

func TestEmbeddedKLineChartAssets(t *testing.T) {
	handler := (&HTTP{}).Handler()
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/", want: "/klinecharts.min.js"},
		{path: "/", want: "管理指标"},
		{path: "/", want: "WebSocket 实时"},
		{path: "/", want: "symbol-options"},
		{path: "/", want: "<label>股市"},
		{path: "/app.js", want: "applyFormulaIndicators"},
		{path: "/app.js", want: "loadSymbolOptions"},
		{path: "/formula-worker.js", want: "Market Bridge TDX formula worker"},
		{path: "/klinecharts.min.js", want: "KLineChart v10.0.2"},
		{path: "/klinecharts.LICENSE.txt", want: "Apache License"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		body, err := io.ReadAll(recorder.Result().Body)
		if err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK || !strings.Contains(string(body), test.want) {
			t.Fatalf("path=%s status=%d missing=%q", test.path, recorder.Code, test.want)
		}
	}
}

func TestProviderUsageProxyForwardsServerToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider":"massive","totals":{"requests":7}}`))
	}))
	defer upstream.Close()

	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL, ServerToken: "secret", RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	rec := httptest.NewRecorder()
	(&HTTP{Cache: cache}).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/providers/massive/usage", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"provider\":\"massive\",\"totals\":{\"requests\":7}}\n" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUniverseProxyForwardsServerToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/market-history/universe" || r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"symbols":["AAPL","SNDK"]}`))
	}))
	defer upstream.Close()

	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL, ServerToken: "secret", RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	recorder := httptest.NewRecorder()
	(&HTTP{Cache: cache}).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/market-history/universe", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"symbols":["AAPL","SNDK"]}` {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestIndicatorProxyPreservesRevisionAndNoContent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/me/indicators/test" || r.URL.Query().Get("revision") != "7" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL, ServerToken: "secret", RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	recorder := httptest.NewRecorder()
	(&HTTP{Cache: cache}).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/v1/me/indicators/test?revision=7", nil))
	if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestAllowedOriginAcceptsPublicSameOrigin(t *testing.T) {
	if !allowedOrigin("https://stock.hiova.com", "stock.hiova.com") {
		t.Fatal("public same-origin request was rejected")
	}
	if allowedOrigin("https://attacker.example", "stock.hiova.com") {
		t.Fatal("cross-origin request was accepted")
	}
}
