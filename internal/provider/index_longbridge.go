package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/market"
)

type LongbridgeIndex struct {
	Quote   LongbridgeHistoryClient
	Version string
}

func (p *LongbridgeIndex) Name() string { return "longbridge-index" }
func (p *LongbridgeIndex) DataVersion() string {
	if p.Version == "" {
		return "longbridge-index-v1"
	}
	return p.Version
}

type longbridgeIndexRoute struct {
	symbol   string
	timezone string
}

var longbridgeIndexRoutes = map[string]longbridgeIndexRoute{
	"I:IXIC":   {symbol: ".IXIC.US", timezone: "America/New_York"},
	"I:DJI":    {symbol: ".DJI.US", timezone: "America/New_York"},
	"I:HSI":    {symbol: "HSI.HK", timezone: "Asia/Hong_Kong"},
	"I:HSCEI":  {symbol: "HSCEI.HK", timezone: "Asia/Hong_Kong"},
	"I:HSTECH": {symbol: "HSTECH.HK", timezone: "Asia/Hong_Kong"},
}

func (p *LongbridgeIndex) Bars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	if p.Quote == nil {
		return nil, fmt.Errorf("Longbridge quote context is not configured")
	}
	normalized, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	period, err := mapLongbridgePeriod(normalized.Interval)
	if err != nil {
		return nil, err
	}
	var bars []market.Bar
	for _, symbol := range normalized.Symbols {
		route, ok := longbridgeIndexRoutes[symbol]
		if !ok {
			return nil, unsupportedIndexSymbol("Longbridge", symbol, longbridgeIndexRoutes)
		}
		location, loadErr := time.LoadLocation(route.timezone)
		if loadErr != nil {
			return nil, loadErr
		}
		part, fetchErr := fetchLongbridgeHistory(ctx, p.Quote, normalized, period, lbquote.AdjustTypeNo, symbol, route.symbol, location)
		if fetchErr != nil {
			return nil, fetchErr
		}
		bars = append(bars, part...)
	}
	market.SortBars(bars)
	return bars, nil
}

func unsupportedIndexSymbol[T any](providerName, symbol string, routes map[string]T) error {
	supported := make([]string, 0, len(routes))
	for value := range routes {
		supported = append(supported, value)
	}
	sort.Strings(supported)
	return fmt.Errorf("%s index provider does not support %s; supported symbols: %s", providerName, symbol, strings.Join(supported, ", "))
}
