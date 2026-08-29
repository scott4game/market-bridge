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
		{path: "/", want: "/history.js"},
		{path: "/", want: "/live-market.js"},
		{path: "/", want: "管理指标"},
		{path: "/", want: "WebSocket 实时"},
		{path: "/", want: "逐笔成交"},
		{path: "/", want: "实时盘口"},
		{path: "/", want: "最新新闻"},
		{path: "/news.js", want: "/v1/news/ws"},
		{path: "/", want: "symbol-options"},
		{path: "/", want: "<label>股市"},
		{path: "/app.js", want: "applyFormulaIndicators"},
		{path: "/app.js", want: "loadSymbolOptions"},
		{path: "/history.js", want: "MAX_BARS_PER_REQUEST"},
		{path: "/live-market.js", want: "mergeInitialAndBuffered"},
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

func TestKLinePageUsesLazyHistoryWithoutDateControls(t *testing.T) {
	raw, err := ui.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if strings.Contains(source, `id="from"`) || strings.Contains(source, `id="to"`) {
		t.Fatal("date controls remain in page")
	}
	if !strings.Contains(source, `/history.js`) {
		t.Fatal("history policy is not loaded")
	}
	if strings.Contains(source, `<select id="interval"`) {
		t.Fatal("period dropdown remains in page")
	}
	workspacePosition := strings.Index(source, `<section class="market-workspace">`)
	indicatorPosition := strings.Index(source, `<section class="indicator-toolbar"`)
	if workspacePosition < 0 || indicatorPosition < 0 || workspacePosition > indicatorPosition {
		t.Fatal("market workspace must appear above the indicator toolbar")
	}
	for _, interval := range []string{"1m", "3m", "5m", "10m", "15m", "30m", "1h", "2h", "3h", "4h", "1d", "1w", "1mo", "1y"} {
		if !strings.Contains(source, `data-interval="`+interval+`"`) {
			t.Fatalf("period button %s is missing", interval)
		}
	}
}

func TestEmbeddedUIUsesProviderCapabilitiesAndSignedHistogramColors(t *testing.T) {
	raw, err := ui.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{
		"providerStatus?.hk?.history_enabled === true",
		"providerStatus?.ashare?.history_enabled === true",
		"服务端未启用该市场的历史行情 Provider",
		"slice(0, 18)",
		"Number(row[`o_${output.name}`]) >= 0 ? RED : GREEN",
		"Y_AXIS_ZOOM_STORAGE_KEY",
		"scrollZoomEnabled: yAxisZoomEnabled",
		"/v1/me/indicators/reset-display",
		"upColor: RED, downColor: GREEN",
		"$('query').requestSubmit()",
		"events: ['bar', 'quote', 'trade', 'depth']",
		"consumeQuote(payload.quote)",
		"/v1/live/trades/",
		"fmp-news-status",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("embedded app is missing %q", want)
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

func TestForwardFactorCacheVersionBindsVersionsToSymbols(t *testing.T) {
	versions := map[string]string{"AAPL": "factor-a", "SNDK": "factor-b"}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		symbol := strings.TrimPrefix(r.URL.Path, "/v1/market-history/adjustments/")
		_ = json.NewEncoder(w).Encode(market.ForwardFactors{Symbol: symbol, Mode: market.ForwardAdjusted, AsOf: "2026-08-26", Version: versions[symbol]})
	}))
	defer upstream.Close()
	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	now := time.Now()
	spec := market.DatasetSpec{Symbols: []string{"AAPL", "SNDK"}, Interval: "1d", From: now.Add(-time.Hour), To: now, Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	first, err := cache.semanticCacheVersion(context.Background(), spec, "base")
	if err != nil {
		t.Fatal(err)
	}
	versions["AAPL"], versions["SNDK"] = versions["SNDK"], versions["AAPL"]
	cache.factorMu.Lock()
	cache.factorCache = map[string]cachedForwardFactors{}
	cache.factorMu.Unlock()
	second, err := cache.semanticCacheVersion(context.Background(), spec, "base")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("symbol/version swap collided: %q", first)
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

func TestSecurityProfilesProxyForwardsServerToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/market-history/security-profiles" || r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"complete":true,"profiles":[{"symbol":"MRNA"}]}`))
	}))
	defer upstream.Close()

	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL, ServerToken: "secret", RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	recorder := httptest.NewRecorder()
	(&HTTP{Cache: cache}).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/market-history/security-profiles", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"complete":true,"profiles":[{"symbol":"MRNA"}]}` {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestOptionsProxyForwardsQueryAndServerToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/options/contracts":
			if r.URL.Query().Get("underlying") != "NVDA" || r.URL.Query().Get("type") != "put" {
				http.Error(w, "unexpected query", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"source":"cache","count":1,"contracts":[{"ticker":"O:NVDA261002P00190000"}]}`))
		case "/v1/options/bars/O:NVDA261002P00190000":
			_, _ = w.Write([]byte(`{"source":"cache","count":0,"bars":[]}`))
		case "/v1/providers/massive-options/usage":
			_, _ = w.Write([]byte(`{"provider":"massive_options","totals":{"requests":2}}`))
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL, ServerToken: "secret", RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	recorder := httptest.NewRecorder()
	(&HTTP{Cache: cache}).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/options/contracts?underlying=NVDA&type=put", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "O:NVDA261002P00190000") {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	for _, path := range []string{"/v1/options/bars/O:NVDA261002P00190000?from=2026-08-01&to=2026-08-29", "/v1/providers/massive-options/usage"} {
		recorder = httptest.NewRecorder()
		(&HTTP{Cache: cache}).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("path=%s status=%d body=%q", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestIndicatorsAreStoredLocallyWithoutCallingServer(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		http.Error(w, "indicator data must remain local", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL, ServerToken: "secret", RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	handler := (&HTTP{Cache: cache}).Handler()

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/me/indicators", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"storage":"local"`) || !strings.Contains(list.Body.String(), `"template_key":"ma-v1"`) {
		t.Fatalf("status=%d body=%q", list.Code, list.Body.String())
	}

	created := httptest.NewRecorder()
	body := `{"name":"Private Formula","pane":"main","formula":"M:MA(CLOSE,N);","parameters":[{"name":"N","default":5,"min":1,"max":500,"step":1,"value":5}],"enabled":true,"sort_order":100}`
	handler.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/v1/me/indicators", strings.NewReader(body)))
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"kind":"personal"`) {
		t.Fatalf("status=%d body=%q", created.Code, created.Body.String())
	}
	reset := httptest.NewRecorder()
	handler.ServeHTTP(reset, httptest.NewRequest(http.MethodPost, "/v1/me/indicators/reset-display", nil))
	if reset.Code != http.StatusOK || !strings.Contains(reset.Body.String(), `"storage":"local"`) || strings.Contains(reset.Body.String(), `"enabled":true`) {
		t.Fatalf("status=%d body=%q", reset.Code, reset.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("local indicator API called go-server %d times", upstreamCalls)
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
