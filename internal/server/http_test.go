package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/provider"
)

type fakeUsageReader struct{ snapshot provider.UsageSnapshot }

type fakeRecentTrades struct {
	symbol string
	count  int32
	rows   []*lbquote.Trade
	err    error
}

func (f *fakeRecentTrades) Trades(_ context.Context, symbol string, count int32) ([]*lbquote.Trade, error) {
	f.symbol, f.count = symbol, count
	return f.rows, f.err
}

type factorProvider struct{ version string }

type securityProvider struct{}

func (securityProvider) Name() string        { return "security-test" }
func (securityProvider) DataVersion() string { return "security-v1" }
func (securityProvider) Bars(context.Context, market.DatasetSpec) ([]market.Bar, error) {
	return nil, nil
}
func (securityProvider) Securities(context.Context) ([]provider.Security, error) {
	return []provider.Security{{Symbol: "AAPL", NameCN: "苹果", NameEN: "Apple Inc."}}, nil
}

type pinnedDatasetProvider struct {
	version string
	used    chan string
}

func (p *pinnedDatasetProvider) Name() string        { return "pinned-test" }
func (p *pinnedDatasetProvider) DataVersion() string { return "bars-v1" }
func (p *pinnedDatasetProvider) Bars(context.Context, market.DatasetSpec) ([]market.Bar, error) {
	p.used <- "unpinned"
	return nil, nil
}
func (p *pinnedDatasetProvider) ForwardAdjustmentFactors(_ context.Context, symbol string) (market.ForwardFactors, error) {
	return market.ForwardFactors{Symbol: symbol, Mode: market.ForwardAdjusted, AsOf: "2026-08-25", Version: p.version}, nil
}
func (p *pinnedDatasetProvider) BarsWithForwardFactors(_ context.Context, _ market.DatasetSpec, curves map[string]market.ForwardFactors) ([]market.Bar, error) {
	p.used <- curves["SNDK"].Version
	return nil, nil
}

func (p *factorProvider) Name() string        { return "factor-test" }
func (p *factorProvider) DataVersion() string { return "bars-v1" }
func (p *factorProvider) Bars(context.Context, market.DatasetSpec) ([]market.Bar, error) {
	return nil, nil
}
func (p *factorProvider) ForwardAdjustmentFactors(context.Context, string) (market.ForwardFactors, error) {
	return market.ForwardFactors{
		Symbol: "SNDK", Mode: market.ForwardAdjusted, AsOf: "2026-08-25", Version: p.version,
		Factors: []market.ForwardFactor{{EffectiveDate: "2026-08-20", Factor: market.DecimalFromFloat(0.9)}},
	}, nil
}

func (f fakeUsageReader) Snapshot(context.Context, string) (provider.UsageSnapshot, error) {
	return f.snapshot, nil
}

func TestMassiveUsageEndpointRequiresToken(t *testing.T) {
	h := (&HTTP{Token: "secret", Usage: fakeUsageReader{snapshot: provider.UsageSnapshot{
		Provider: "massive", UpdatedAt: time.Now().UTC(),
	}}}).Handler()

	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/providers/massive/usage", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/providers/massive/usage", nil)
	req.Header.Set("Authorization", "Bearer secret")
	ok := httptest.NewRecorder()
	h.ServeHTTP(ok, req)
	if ok.Code != http.StatusOK {
		t.Fatalf("authorized status=%d body=%s", ok.Code, ok.Body.String())
	}
	var got provider.UsageSnapshot
	if err := json.NewDecoder(ok.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Provider != "massive" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestMassiveUsageEndpointDisabled(t *testing.T) {
	rec := httptest.NewRecorder()
	(&HTTP{}).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/providers/massive/usage", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRecentTradesEndpointNormalizesLongbridgeResponse(t *testing.T) {
	reader := &fakeRecentTrades{rows: []*lbquote.Trade{{Price: "227.15", Volume: 20, Timestamp: 1770000000, TradeType: "F", Direction: 2, TradeSession: lbquote.TradeSessionNormal}}}
	handler := (&HTTP{Token: "secret", RecentTrades: reader}).Handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/live/trades/AAPL?limit=25", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d", unauthorized.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/live/trades/AAPL?limit=25", nil)
	req.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if reader.symbol != "AAPL.US" || reader.count != 25 {
		t.Fatalf("upstream symbol=%q count=%d", reader.symbol, reader.count)
	}
	var response struct {
		Symbol string        `json:"symbol"`
		Trades []recentTrade `json:"trades"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Symbol != "AAPL" || len(response.Trades) != 1 || response.Trades[0].TradeType != "F" || response.Trades[0].Direction != 2 {
		t.Fatalf("response=%+v", response)
	}
}

func TestRecentTradesEndpointValidatesAvailabilityAndLimit(t *testing.T) {
	for _, test := range []struct {
		name string
		http *HTTP
		path string
		want int
	}{
		{name: "disabled", http: &HTTP{}, path: "/v1/live/trades/AAPL", want: http.StatusServiceUnavailable},
		{name: "invalid limit", http: &HTTP{RecentTrades: &fakeRecentTrades{}}, path: "/v1/live/trades/AAPL?limit=1001", want: http.StatusBadRequest},
		{name: "binance", http: &HTTP{RecentTrades: &fakeRecentTrades{}}, path: "/v1/live/trades/BTCUSDT.BINANCE", want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.http.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			if recorder.Code != test.want {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestUniverseEndpointReturnsNamesAndLegacySymbols(t *testing.T) {
	store, err := NewStore(t.TempDir(), securityProvider{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	(&HTTP{Store: store, DataVersion: "test-v1"}).historyUniverse(recorder, httptest.NewRequest(http.MethodGet, "/v1/market-history/universe", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Symbols    []string            `json:"symbols"`
		Securities []provider.Security `json:"securities"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Symbols) != 1 || response.Symbols[0] != "AAPL" || len(response.Securities) != 1 || response.Securities[0].NameCN != "苹果" {
		t.Fatalf("response=%+v", response)
	}
}

func TestSecurityProfilesEndpointIsProtected(t *testing.T) {
	p := &profileCatalogProvider{
		securities: []provider.Security{{Symbol: "MRNA"}},
		profiles:   map[string]provider.SecurityProfile{"MRNA": validTestProfile("MRNA", "0002", 50)},
		calls:      map[string]int{},
	}
	store, err := NewStore(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := OpenSecurityProfileCatalog(filepath.Join(t.TempDir(), "profiles.db"), store, time.Hour, 30*24*time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	handler := (&HTTP{Token: "secret", SecurityProfiles: catalog}).Handler()
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/market-history/security-profiles", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/market-history/security-profiles", nil)
	req.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "secret") || !strings.Contains(recorder.Body.String(), `"symbol":"MRNA"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestDisabledHistoricalProviderReturnsServiceUnavailable(t *testing.T) {
	store, err := NewStore(t.TempDir(), &provider.Router{US: &provider.Mock{Version: "test-v1"}})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"spec":{"symbols":["700.HK"],"interval":"1d","from":"2026-08-01T00:00:00Z","to":"2026-08-02T00:00:00Z","session":"regular","adjustment":"forward_adjusted"}}`
	recorder := httptest.NewRecorder()
	(&HTTP{Store: store}).historyBars(recorder, httptest.NewRequest(http.MethodPost, "/v1/history/bars", strings.NewReader(body)))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "GO_SERVER_HK_PROVIDER=longbridge") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestForwardAdjustmentEndpointIsProtectedAndVersioned(t *testing.T) {
	p := &factorProvider{version: "factor-v1"}
	store, err := NewStore(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&HTTP{Store: store, Token: "secret"}).Handler()
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/market-history/adjustments/SNDK", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d", unauthorized.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/market-history/adjustments/SNDK", nil)
	req.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var curve market.ForwardFactors
	if err := json.NewDecoder(recorder.Body).Decode(&curve); err != nil {
		t.Fatal(err)
	}
	if curve.Symbol != "SNDK" || curve.AsOf != "2026-08-25" || curve.Version != "factor-v1" || len(curve.Factors) != 1 {
		t.Fatalf("curve=%+v", curve)
	}

	now := time.Now()
	spec := market.DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1d", From: now.Add(-time.Hour), To: now, Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	first, err := store.describe(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	p.version = "factor-v2"
	second, err := store.describe(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.DataVersion == second.DataVersion || !strings.Contains(first.DataVersion, "factor-v1") || !strings.Contains(second.DataVersion, "factor-v2") {
		t.Fatalf("first=%q second=%q", first.DataVersion, second.DataVersion)
	}
}

func TestDatasetBuildUsesFactorsCapturedDuringAdmission(t *testing.T) {
	p := &pinnedDatasetProvider{version: "factor-v1", used: make(chan string, 1)}
	store, err := NewStore(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	spec := market.DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1d", From: now.Add(-24 * time.Hour), To: now, Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	status, err := store.Ensure(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	p.version = "factor-v2"
	select {
	case used := <-p.used:
		if used != "factor-v1" {
			t.Fatalf("build used %q", used)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("build did not start")
	}
	waitForState(t, store, status.DatasetID, "ready")
	manifest, err := store.Manifest(status.DatasetID)
	if err != nil || !strings.Contains(manifest.DataVersion, "factor-v1") {
		t.Fatalf("manifest=%+v err=%v", manifest, err)
	}
}
