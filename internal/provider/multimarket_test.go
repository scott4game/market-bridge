package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openapi "github.com/longbridge/openapi-go"
	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/market"
	shopdecimal "github.com/shopspring/decimal"
)

func TestBinanceHistoricalPaginationAndDecimals(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v3/klines" || r.URL.Query().Get("symbol") != "BTCUSDT" || r.URL.Query().Get("interval") != "1m" {
			t.Fatalf("unexpected request %s", r.URL.String())
		}
		start := int64(1735689600000)
		if requests > 1 {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		rows := make([]any, 1000)
		for index := range rows {
			rows[index] = []any{start + int64(index)*60000, "100.1", "101.2", "99.3", "100.4", "0.123456", start + int64(index+1)*60000 - 1, "12.3456", 1, "0", "0", "0"}
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer server.Close()
	from := time.UnixMilli(1735689600000).UTC()
	spec := market.DatasetSpec{Symbols: []string{"BTCUSDT.BINANCE"}, Interval: "1m", From: from, To: from.Add(1001 * time.Minute), Session: market.ContinuousSession, Adjustment: market.Raw}
	bars, err := (&Binance{BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1000 || requests != 2 || bars[0].VolumeDecimal != "0.123456" || bars[0].Turnover == nil {
		t.Fatalf("bars=%d requests=%d first=%+v", len(bars), requests, bars[0])
	}
}

type longbridgeHistoryStub struct {
	calls int
	from  time.Time
	lists map[openapi.Market][]*lbquote.Security
}

func (s *longbridgeHistoryStub) HistoryCandlesticksByOffset(_ context.Context, symbol string, _ lbquote.Period, adjust lbquote.AdjustType, forward bool, cursor *time.Time, count int32, _ ...lbquote.CandlestickRequestOption) ([]*lbquote.Candlestick, error) {
	s.calls++
	if symbol != "700.HK" || adjust != lbquote.AdjustTypeForward || !forward || count != 1000 {
		return nil, fmt.Errorf("unexpected Longbridge request")
	}
	start := s.from
	limit := 1000
	if s.calls == 2 {
		start = s.from.Add(1000 * time.Minute)
		limit = 1
	}
	if s.calls > 2 || cursor == nil {
		return nil, nil
	}
	one := shopdecimal.NewFromInt(1)
	result := make([]*lbquote.Candlestick, limit)
	for index := range result {
		ts := start.Add(time.Duration(index) * time.Minute)
		result[index] = &lbquote.Candlestick{Open: &one, High: &one, Low: &one, Close: &one, Volume: 10, Timestamp: ts.Unix()}
	}
	return result, nil
}

func (s *longbridgeHistoryStub) SecurityList(_ context.Context, market openapi.Market, _ lbquote.SecurityListCategory) ([]*lbquote.Security, error) {
	return s.lists[market], nil
}

func TestLongbridgeHistoricalPaginationAndSuffix(t *testing.T) {
	from := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	stub := &longbridgeHistoryStub{from: from}
	spec := market.DatasetSpec{Symbols: []string{"700.hk"}, Interval: "1m", From: from, To: from.Add(1002 * time.Minute), Adjustment: market.ForwardAdjusted}
	bars, err := (&Longbridge{Quote: stub}).Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1001 || stub.calls != 2 || bars[0].Symbol != "700.HK" {
		t.Fatalf("bars=%d calls=%d first=%+v", len(bars), stub.calls, bars[0])
	}
}

func TestLongbridgeSecuritiesIncludeChineseNamesAcrossMarkets(t *testing.T) {
	stub := &longbridgeHistoryStub{lists: map[openapi.Market][]*lbquote.Security{
		openapi.MarketUS: {{Symbol: "NVDA.US", NameCN: "英伟达", NameEN: "NVIDIA"}},
		openapi.MarketHK: {{Symbol: "700.HK", NameCN: "腾讯控股", NameEN: "Tencent"}},
		openapi.MarketCN: {{Symbol: "600519.SH", NameCN: "贵州茅台"}},
	}}
	securities, err := (&Longbridge{Quote: stub}).Securities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(securities) != 3 || securities[0].Symbol != "600519.SH" || securities[1].Symbol != "700.HK" || securities[2].Symbol != "NVDA" || securities[2].NameCN != "英伟达" {
		t.Fatalf("securities=%+v", securities)
	}
}

type securityRouteProvider struct {
	routeProvider
	securities []Security
}

func (p *securityRouteProvider) Securities(context.Context) ([]Security, error) {
	return append([]Security(nil), p.securities...), nil
}

func TestRouterMergesSecurityNamesWithoutDuplicatingSymbols(t *testing.T) {
	us := &securityRouteProvider{routeProvider: routeProvider{name: "massive", version: "us-v1"}, securities: []Security{{Symbol: "NVDA", NameEN: "NVIDIA Corporation"}}}
	directory := &securityRouteProvider{routeProvider: routeProvider{name: "longbridge", version: "directory-v1"}, securities: []Security{{Symbol: "NVDA.US", NameCN: "英伟达", NameEN: "NVIDIA"}}}
	securities, err := (&Router{US: us, UniverseProviders: []Provider{directory}}).Securities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(securities) != 1 || securities[0].Symbol != "NVDA" || securities[0].NameCN != "英伟达" || securities[0].NameEN != "NVIDIA Corporation" {
		t.Fatalf("securities=%+v", securities)
	}
}

type routeProvider struct {
	name     string
	version  string
	calls    []market.DatasetSpec
	universe []string
}

func (p *routeProvider) Name() string        { return p.name }
func (p *routeProvider) DataVersion() string { return p.version }
func (p *routeProvider) Bars(_ context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	p.calls = append(p.calls, spec)
	return nil, nil
}
func (p *routeProvider) Universe(context.Context) ([]string, error) {
	return append([]string(nil), p.universe...), nil
}

func TestRouterSelectsProviderBySuffix(t *testing.T) {
	us := &routeProvider{name: "massive", version: "us-v1"}
	lb := &routeProvider{name: "longbridge", version: "lb-v1"}
	router := &Router{US: us, Longbridge: lb}
	spec := market.DatasetSpec{Symbols: []string{"AAPL", "700.HK", "600519.SH"}, Interval: "1d", From: time.Now().Add(-24 * time.Hour), To: time.Now(), Session: market.RegularSession, Adjustment: market.Raw}
	description, err := router.Describe(spec)
	if err != nil {
		t.Fatal(err)
	}
	if description.Name != "longbridge+massive" || !strings.Contains(description.DataVersion, "lb-v1") {
		t.Fatalf("description=%+v", description)
	}
	if _, err := router.Bars(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if len(us.calls) != 1 || len(us.calls[0].Symbols) != 1 || len(lb.calls) != 1 || len(lb.calls[0].Symbols) != 2 {
		t.Fatalf("us=%v lb=%v", us.calls, lb.calls)
	}
}

func TestRouterCanListMarketsWithoutEnablingTheirHistoryRoute(t *testing.T) {
	us := &routeProvider{name: "massive", version: "us-v1", universe: []string{"SNDK"}}
	liveDirectory := &routeProvider{name: "longbridge", version: "directory-v1", universe: []string{"700.HK", "600519.SH"}}
	router := &Router{US: us, UniverseProviders: []Provider{liveDirectory}}
	symbols, err := router.Universe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(symbols, ","); got != "600519.SH,700.HK,SNDK" {
		t.Fatalf("universe=%s", got)
	}
	spec := market.DatasetSpec{Symbols: []string{"700.HK"}, Interval: "1d", From: time.Now().Add(-24 * time.Hour), To: time.Now(), Session: market.RegularSession, Adjustment: market.Raw}
	if _, err := router.Bars(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "historical provider is not enabled") {
		t.Fatalf("unexpected history route error: %v", err)
	} else {
		var disabled *HistoricalProviderDisabledError
		if !errors.As(err, &disabled) || disabled.Provider != "Longbridge" || disabled.Venue != market.VenueHK {
			t.Fatalf("history route error is not typed: %v", err)
		}
	}
}
