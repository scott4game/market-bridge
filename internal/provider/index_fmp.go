package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type FMPIndex struct {
	APIKey  string
	BaseURL string
	Version string
	HTTP    *http.Client
}

func (p *FMPIndex) Name() string { return "fmp-index" }
func (p *FMPIndex) DataVersion() string {
	if p.Version == "" {
		return "fmp-index-v1"
	}
	return p.Version
}

type fmpIndexRoute struct {
	symbol   string
	timezone string
}

var fmpIndexRoutes = map[string]fmpIndexRoute{
	"I:VIX":  {symbol: "^VIX", timezone: "America/New_York"},
	"I:NDX":  {symbol: "^NDX", timezone: "America/New_York"},
	"I:SPX":  {symbol: "^GSPC", timezone: "America/New_York"},
	"I:IXIC": {symbol: "^IXIC", timezone: "America/New_York"},
	"I:DJI":  {symbol: "^DJI", timezone: "America/New_York"},
	"I:HSI":  {symbol: "^HSI", timezone: "Asia/Hong_Kong"},
}

type fmpInterval struct {
	upstream string
	factor   int
	daily    bool
}

func mapFMPIndexInterval(interval string) (fmpInterval, error) {
	switch interval {
	case "1m":
		return fmpInterval{upstream: "1min", factor: 1}, nil
	case "3m":
		return fmpInterval{upstream: "1min", factor: 3}, nil
	case "5m":
		return fmpInterval{upstream: "5min", factor: 1}, nil
	case "10m":
		return fmpInterval{upstream: "5min", factor: 2}, nil
	case "15m":
		return fmpInterval{upstream: "15min", factor: 1}, nil
	case "30m":
		return fmpInterval{upstream: "30min", factor: 1}, nil
	case "1h":
		return fmpInterval{upstream: "1hour", factor: 1}, nil
	case "2h":
		return fmpInterval{upstream: "1hour", factor: 2}, nil
	case "3h":
		return fmpInterval{upstream: "1hour", factor: 3}, nil
	case "4h":
		return fmpInterval{upstream: "4hour", factor: 1}, nil
	case "1d", "1w", "1mo", "1y":
		return fmpInterval{daily: true, factor: 1}, nil
	default:
		return fmpInterval{}, fmt.Errorf("unsupported FMP index interval %q", interval)
	}
}

func (p *FMPIndex) Bars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	if strings.TrimSpace(p.APIKey) == "" {
		return nil, fmt.Errorf("FMP_API_KEY is required")
	}
	normalized, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	interval, err := mapFMPIndexInterval(normalized.Interval)
	if err != nil {
		return nil, err
	}
	var bars []market.Bar
	for _, symbol := range normalized.Symbols {
		route, ok := fmpIndexRoutes[symbol]
		if !ok {
			return nil, unsupportedIndexSymbol("FMP", symbol, fmpIndexRoutes)
		}
		location, loadErr := time.LoadLocation(route.timezone)
		if loadErr != nil {
			return nil, loadErr
		}
		fetchSpec := normalized
		if interval.daily && normalized.Interval != "1d" {
			fetchSpec.From = startOfUSCalendarPeriod(normalized.From, normalized.Interval, location)
		}
		part, fetchErr := p.fetchBars(ctx, fetchSpec, interval, symbol, route.symbol, location)
		if fetchErr != nil {
			return nil, fetchErr
		}
		if interval.factor > 1 {
			part, fetchErr = aggregateBars(part, normalized.Interval, interval.factor, location)
			if fetchErr != nil {
				return nil, fetchErr
			}
		}
		if interval.daily && normalized.Interval != "1d" {
			part, fetchErr = aggregateUSCalendar(part, normalized.Interval, location)
			if fetchErr != nil {
				return nil, fetchErr
			}
		}
		bars = append(bars, filterRequestedRange(part, normalized.From, normalized.To)...)
	}
	bars = deduplicateBars(bars)
	market.SortBars(bars)
	return bars, nil
}

type fmpPrice struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

func (p *FMPIndex) fetchBars(ctx context.Context, spec market.DatasetSpec, interval fmpInterval, publicSymbol, upstreamSymbol string, location *time.Location) ([]market.Bar, error) {
	baseURL := strings.TrimRight(p.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://financialmodelingprep.com"
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid FMP base URL")
	}
	path := "/stable/historical-price-eod/full"
	if !interval.daily {
		path = "/stable/historical-chart/" + interval.upstream
	}
	u, err := url.Parse(baseURL + path)
	if err != nil || u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) {
		return nil, fmt.Errorf("invalid FMP index endpoint")
	}
	q := u.Query()
	q.Set("symbol", upstreamSymbol)
	q.Set("from", spec.From.In(location).Format("2006-01-02"))
	q.Set("to", spec.To.In(location).Format("2006-01-02"))
	q.Set("apikey", p.APIKey)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "market-bridge")
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("FMP index request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		message := fmpErrorMessage(body, p.APIKey)
		return nil, fmt.Errorf("FMP index status %d: %s", resp.StatusCode, message)
	}
	var rows []fmpPrice
	if err := json.Unmarshal(body, &rows); err != nil {
		if message := fmpErrorMessage(body, p.APIKey); message != "upstream error" {
			return nil, fmt.Errorf("FMP index: %s", message)
		}
		return nil, fmt.Errorf("decode FMP index response: %w", err)
	}
	bars := make([]market.Bar, 0, len(rows))
	for _, row := range rows {
		ts, parseErr := parseFMPPriceTime(row.Date, location)
		if parseErr != nil {
			return nil, fmt.Errorf("FMP index returned invalid date %q for %s", row.Date, publicSymbol)
		}
		if ts.Before(spec.From) || !ts.Before(spec.To) {
			continue
		}
		volume := int64(0)
		if row.Volume > 0 && row.Volume <= math.MaxInt64 {
			volume = int64(row.Volume)
		}
		bars = append(bars, market.Bar{Symbol: publicSymbol, Timestamp: ts, Open: market.DecimalFromFloat(row.Open), High: market.DecimalFromFloat(row.High), Low: market.DecimalFromFloat(row.Low), Close: market.DecimalFromFloat(row.Close), Volume: volume, Session: spec.Session, Source: "fmp", Completed: true})
	}
	return bars, nil
}

func parseFMPPriceTime(value string, location *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported FMP time")
}

func fmpErrorMessage(body []byte, apiKey string) string {
	var payload map[string]any
	if json.Unmarshal(body, &payload) == nil {
		for _, key := range []string{"Error Message", "error", "message"} {
			if value := strings.TrimSpace(fmt.Sprint(payload[key])); value != "" && value != "<nil>" {
				return strings.ReplaceAll(value, apiKey, "[redacted]")
			}
		}
	}
	message := strings.TrimSpace(string(body))
	if message == "" || strings.HasPrefix(message, "<") {
		return "upstream error"
	}
	if len(message) > 500 {
		message = message[:500]
	}
	return strings.ReplaceAll(message, apiKey, "[redacted]")
}
