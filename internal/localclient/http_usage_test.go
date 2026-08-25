package localclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/market"
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

func TestEmbeddedMarketDefaultsUseExpectedAdjustments(t *testing.T) {
	raw, err := ui.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{
		".BINANCE')) return { session: 'continuous', adjustment: 'raw'",
		".HK')) return { session: 'regular', adjustment: 'forward_adjusted'",
		".SH') || upper.endsWith('.SZ')) return { session: 'regular', adjustment: 'forward_adjusted'",
		"return { session: 'regular', adjustment: 'forward_adjusted', market: '美股'",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("missing market default %q", want)
		}
	}
}

func TestForwardFactorsAreCachedUntilNextNewYorkDayAndVersionCacheKeys(t *testing.T) {
	requests := 0
	factorVersion := "factor-v1"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/market-history/adjustments/SNDK" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		requests++
		_ = json.NewEncoder(w).Encode(market.ForwardFactors{Symbol: "SNDK", Mode: market.ForwardAdjusted, AsOf: time.Now().Format("2006-01-02"), Version: factorVersion})
	}))
	defer upstream.Close()
	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	now := time.Now()
	spec := market.DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1d", From: now.Add(-time.Hour), To: now, Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	first, err := cache.semanticCacheVersion(context.Background(), spec, "base")
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.semanticCacheVersion(context.Background(), spec, "base")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || first != second || !strings.Contains(first, factorVersion) {
		t.Fatalf("requests=%d first=%q second=%q", requests, first, second)
	}
	location, _ := time.LoadLocation("America/New_York")
	cache.factorMu.Lock()
	cached := cache.factorCache["SNDK"]
	delete(cache.factorCache, "SNDK")
	cache.factorMu.Unlock()
	if cached.expiresAt.In(location).Hour() != 0 || cached.expiresAt.In(location).Minute() != 0 {
		t.Fatalf("expires_at=%s", cached.expiresAt.In(location))
	}
	factorVersion = "factor-v2"
	third, err := cache.semanticCacheVersion(context.Background(), spec, "base")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || third == first || !strings.Contains(third, factorVersion) {
		t.Fatalf("requests=%d first=%q third=%q", requests, first, third)
	}
}

func TestSchemaV1LocalDatasetDoesNotMatchV2Request(t *testing.T) {
	cache, err := NewCache(config.Client{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	now := time.Now()
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1d", From: now.Add(-time.Hour), To: now, Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	v1Key, err := spec.Hash("1", "request")
	if err != nil {
		t.Fatal(err)
	}
	v2Key, err := spec.Hash(market.SchemaVersion, "request")
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(market.Manifest{DatasetID: "old-dataset", Spec: spec, SchemaVersion: "1", DataVersion: "request"})
	if _, err := cache.db.Exec(`INSERT INTO datasets(id,spec_hash,manifest_json,last_accessed_at,state) VALUES(?,?,?,?,?)`, "old-dataset", v1Key, manifest, now.UnixMilli(), "ready"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cache.localManifest(context.Background(), v2Key); err != nil || ok {
		t.Fatalf("v1 dataset matched v2 key: ok=%v err=%v", ok, err)
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
