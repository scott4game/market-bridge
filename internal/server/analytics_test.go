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

	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/provider"
)

type analyticsProvider struct {
	groupedCalls int
}

type repairAnalyticsProvider struct{ calls int }

func (p *repairAnalyticsProvider) Name() string        { return "massive" }
func (p *repairAnalyticsProvider) DataVersion() string { return "repair-v1" }
func (p *repairAnalyticsProvider) Bars(_ context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	p.calls++
	bar := market.Bar{Symbol: spec.Symbols[0], Timestamp: spec.From, High: market.DecimalFromFloat(10), Low: market.DecimalFromFloat(8), Close: market.DecimalFromFloat(9), Volume: 100, Session: spec.Session, Completed: true}
	if p.calls > 1 {
		turnover := market.DecimalFromFloat(1000)
		bar.Turnover = &turnover
	}
	return []market.Bar{bar}, nil
}

func (p *analyticsProvider) Name() string        { return "massive" }
func (p *analyticsProvider) DataVersion() string { return "massive-test-v1" }
func (p *analyticsProvider) Bars(_ context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	bars := make([]market.Bar, 0, len(spec.Symbols))
	for index, symbol := range spec.Symbols {
		turnover := market.DecimalFromFloat(float64(1000 + index*500))
		bars = append(bars, market.Bar{
			Symbol: symbol, Timestamp: spec.From, Open: market.DecimalFromFloat(9), High: market.DecimalFromFloat(10),
			Low: market.DecimalFromFloat(8), Close: market.DecimalFromFloat(float64(10 - index)), Volume: 100, Turnover: &turnover,
			Session: spec.Session, Source: "massive", Completed: true,
		})
	}
	return bars, nil
}
func (p *analyticsProvider) Securities(context.Context) ([]provider.Security, error) {
	return []provider.Security{{Symbol: "AAA"}, {Symbol: "BBB"}, {Symbol: "CCC"}}, nil
}
func (p *analyticsProvider) SecurityProfile(_ context.Context, symbol string) (provider.SecurityProfile, error) {
	code, description := "1000", "Sector A"
	if symbol == "CCC" {
		code, description = "2000", "Sector B"
	}
	return provider.SecurityProfile{Symbol: symbol, Name: symbol, Type: "CS", Active: true, Locale: "us", Market: "stocks", SICCode: code, SICDescription: description, Provider: "massive"}, nil
}
func (p *analyticsProvider) GroupedDaily(_ context.Context, _ string) ([]market.Bar, error) {
	p.groupedCalls++
	first, second, third := market.DecimalFromFloat(1000), market.DecimalFromFloat(1000), market.DecimalFromFloat(500)
	ts := time.Date(2026, 9, 3, 4, 0, 0, 0, time.UTC)
	return []market.Bar{
		{Symbol: "AAA", Timestamp: ts, High: market.DecimalFromFloat(10), Low: market.DecimalFromFloat(8), Close: market.DecimalFromFloat(10), Volume: 100, Turnover: &first, Session: market.RegularSession, Completed: true},
		{Symbol: "BBB", Timestamp: ts, High: market.DecimalFromFloat(10), Low: market.DecimalFromFloat(8), Close: market.DecimalFromFloat(8), Volume: 100, Turnover: &second, Session: market.RegularSession, Completed: true},
		{Symbol: "CCC", Timestamp: ts, High: market.DecimalFromFloat(10), Low: market.DecimalFromFloat(8), Close: market.DecimalFromFloat(10), Volume: 50, Turnover: &third, Session: market.RegularSession, Completed: true},
	}, nil
}

func testMarketAnalytics(t *testing.T) (*MarketAnalytics, *analyticsProvider) {
	t.Helper()
	p := &analyticsProvider{}
	store, err := NewStore(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := OpenSecurityProfileCatalog(filepath.Join(t.TempDir(), "profiles.db"), store, time.Hour, 24*time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = profiles.Close() })
	service, err := OpenMarketAnalytics(filepath.Join(t.TempDir(), "analytics.db"), store, nil, nil, profiles, "test-v1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service, p
}

func TestAnalyticsFlowHTTP(t *testing.T) {
	service, _ := testMarketAnalytics(t)
	handler := (&HTTP{Analytics: service}).Handler()
	path := "/v1/market-analytics/flow/AAA?from=2026-09-03T14:30:00Z&to=2026-09-03T14:31:00Z&interval=1m&session=regular"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response FlowResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Proxy || response.Method != "massive_close_location_v1" || len(response.Points) != 1 || response.Points[0].Inflow != "1000.000000" {
		t.Fatalf("response=%+v", response)
	}
}

func TestAnalyticsFlowRefetchesMissingTurnoverWithoutEstimating(t *testing.T) {
	provider := &repairAnalyticsProvider{}
	store, err := NewStore(t.TempDir(), provider)
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenMarketAnalytics(filepath.Join(t.TempDir(), "analytics.db"), store, nil, nil, nil, "repair-v1")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	from := time.Date(2026, 9, 3, 14, 30, 0, 0, time.UTC)
	response, err := service.Flow(context.Background(), market.DatasetSpec{Symbols: []string{"NVDA"}, Interval: "1m", From: from, To: from.Add(time.Minute), Session: market.RegularSession, Adjustment: market.Raw})
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 || len(response.Points) != 1 || response.Points[0].Turnover != "1000.000000" {
		t.Fatalf("calls=%d response=%+v", provider.calls, response)
	}
}

func TestAnalyticsSectorFlowAggregatesPerStockAndCachesGroupedDaily(t *testing.T) {
	service, p := testMarketAnalytics(t)
	first, err := service.SectorFlow(context.Background(), "2026-09-03", 50)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.SectorFlow(context.Background(), "2026-09-03", 50)
	if err != nil {
		t.Fatal(err)
	}
	if p.groupedCalls != 1 || len(first.Sectors) != 2 || len(second.Sectors) != 2 {
		t.Fatalf("calls=%d first=%+v second=%+v", p.groupedCalls, first.Sectors, second.Sectors)
	}
	if first.Sectors[0].SICCode != "2000" || first.Sectors[0].NetFlow != "500.000000" {
		t.Fatalf("ranking=%+v", first.Sectors)
	}
	if first.Sectors[1].SICCode != "1000" || first.Sectors[1].NetFlow != "0.000000" || first.Sectors[1].Inflow != "1000.000000" || first.Sectors[1].Outflow != "1000.000000" {
		t.Fatalf("sector A=%+v", first.Sectors[1])
	}
}

func TestAnalyticsBasketRejectsMoreThanTwoHundredSymbols(t *testing.T) {
	service, _ := testMarketAnalytics(t)
	handler := (&HTTP{Analytics: service}).Handler()
	symbols := make([]string, 201)
	for index := range symbols {
		symbols[index] = "A" + string(rune('A'+index%26)) + string(rune('A'+index/26))
	}
	body, _ := json.Marshal(map[string]any{"symbols": symbols, "interval": "1d", "from": "2026-09-01T00:00:00Z", "to": "2026-09-04T00:00:00Z", "session": "regular"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/market-analytics/basket-flow", strings.NewReader(string(body))))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
