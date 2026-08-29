package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

// TushareAShare provides historical bars and the security universe for the
// Shanghai and Shenzhen stock markets through Tushare Pro's HTTP API.
type TushareAShare struct {
	Token, BaseURL, Version string
	RequestsPerMinute       int
	HTTP                    *http.Client

	mu       sync.Mutex
	nextCall time.Time
}

func (p *TushareAShare) Name() string { return "tushare-ashare" }

func (p *TushareAShare) DataVersion() string {
	if p.Version == "" {
		return "tushare-ashare-v1"
	}
	return p.Version
}

func (p *TushareAShare) Supports(spec market.DatasetSpec) bool {
	return spec.Interval == "1d" || spec.Interval == "1w" || spec.Interval == "1mo"
}

type tushareResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Fields []string `json:"fields"`
		Items  [][]any  `json:"items"`
	} `json:"data"`
}

func (p *TushareAShare) query(ctx context.Context, api string, params map[string]any, fields string) (tushareResponse, error) {
	if strings.TrimSpace(p.Token) == "" {
		return tushareResponse{}, fmt.Errorf("TUSHARE_TOKEN is required")
	}
	if err := p.wait(ctx); err != nil {
		return tushareResponse{}, err
	}
	body, err := json.Marshal(map[string]any{"api_name": api, "token": p.Token, "params": params, "fields": fields})
	if err != nil {
		return tushareResponse{}, err
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://api.tushare.pro"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(body))
	if err != nil {
		return tushareResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return tushareResponse{}, err
	}
	defer resp.Body.Close()
	var payload tushareResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return tushareResponse{}, fmt.Errorf("Tushare %s returned invalid JSON: %w", api, err)
	}
	if resp.StatusCode/100 != 2 {
		return tushareResponse{}, fmt.Errorf("Tushare %s returned HTTP %d: %s", api, resp.StatusCode, payload.Msg)
	}
	if payload.Code != 0 {
		return tushareResponse{}, fmt.Errorf("Tushare %s failed (code %d): %s", api, payload.Code, payload.Msg)
	}
	return payload, nil
}

func (p *TushareAShare) wait(ctx context.Context) error {
	if p.RequestsPerMinute <= 0 {
		return nil
	}
	interval := time.Minute / time.Duration(p.RequestsPerMinute)
	p.mu.Lock()
	wait := time.Until(p.nextCall)
	if wait < 0 {
		wait = 0
	}
	p.nextCall = time.Now().Add(wait + interval)
	p.mu.Unlock()
	if wait == 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *TushareAShare) Bars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	normalized, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	for _, symbol := range normalized.Symbols {
		_, venue, symbolErr := market.NormalizeSymbol(symbol)
		if symbolErr != nil || (venue != market.VenueSH && venue != market.VenueSZ) {
			return nil, fmt.Errorf("Tushare A-share provider does not support %s", symbol)
		}
	}
	if !p.Supports(normalized) {
		return nil, fmt.Errorf("Tushare A-share history supports 1d, 1w, and 1mo, got %s", normalized.Interval)
	}
	if normalized.Adjustment != market.Raw && normalized.Adjustment != market.ForwardAdjusted {
		return nil, fmt.Errorf("Tushare A-share history supports raw and forward_adjusted, got %s", normalized.Adjustment)
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	fetchFrom := normalized.From
	if normalized.Interval != "1d" {
		fetchFrom = startOfUSCalendarPeriod(fetchFrom, normalized.Interval, location)
	}
	var bars []market.Bar
	for _, symbol := range normalized.Symbols {
		part, err := p.dailyBars(ctx, symbol, fetchFrom, normalized.To, normalized.Session, normalized.Adjustment, location)
		if err != nil {
			return nil, err
		}
		bars = append(bars, part...)
	}
	if normalized.Interval != "1d" {
		bars, err = aggregateUSCalendar(bars, normalized.Interval, location)
		if err != nil {
			return nil, err
		}
	}
	bars = filterRequestedRange(bars, normalized.From, normalized.To)
	market.SortBars(bars)
	return bars, nil
}

func (p *TushareAShare) dailyBars(ctx context.Context, symbol string, from, to time.Time, session market.Session, adjustment market.AdjustmentMode, location *time.Location) ([]market.Bar, error) {
	params := map[string]any{
		"ts_code":    symbol,
		"start_date": from.In(location).Format("20060102"),
		"end_date":   to.Add(-time.Nanosecond).In(location).Format("20060102"),
	}
	payload, err := p.query(ctx, "daily", params, "ts_code,trade_date,open,high,low,close,vol,amount")
	if err != nil {
		return nil, err
	}
	rows, err := tushareRows(payload)
	if err != nil {
		return nil, err
	}
	latestFactor := 1.0
	factors := map[string]float64{}
	if adjustment == market.ForwardAdjusted {
		factorPayload, factorErr := p.query(ctx, "adj_factor", map[string]any{"ts_code": symbol}, "ts_code,trade_date,adj_factor")
		if factorErr != nil {
			return nil, factorErr
		}
		factorRows, factorErr := tushareRows(factorPayload)
		if factorErr != nil {
			return nil, factorErr
		}
		latestDate := ""
		for _, row := range factorRows {
			date, _ := tushareString(row["trade_date"])
			factor, parseErr := tushareFloat(row["adj_factor"])
			if parseErr != nil || factor <= 0 {
				return nil, fmt.Errorf("Tushare returned an invalid adjustment factor for %s on %s", symbol, date)
			}
			factors[date] = factor
			if date > latestDate {
				latestDate, latestFactor = date, factor
			}
		}
		if latestDate == "" {
			return nil, fmt.Errorf("Tushare returned no adjustment factors for %s", symbol)
		}
	}
	result := make([]market.Bar, 0, len(rows))
	for _, row := range rows {
		date, err := tushareString(row["trade_date"])
		if err != nil {
			return nil, err
		}
		ts, err := time.ParseInLocation("20060102", date, location)
		if err != nil {
			return nil, fmt.Errorf("invalid Tushare trade_date %q: %w", date, err)
		}
		values := make([]market.Decimal, 4)
		for index, name := range []string{"open", "high", "low", "close"} {
			value, parseErr := tushareFloat(row[name])
			if parseErr != nil {
				return nil, fmt.Errorf("invalid Tushare %s for %s on %s: %w", name, symbol, date, parseErr)
			}
			if adjustment == market.ForwardAdjusted {
				factor, ok := factors[date]
				if !ok {
					return nil, fmt.Errorf("missing Tushare adjustment factor for %s on %s", symbol, date)
				}
				value *= factor / latestFactor
			}
			values[index] = market.DecimalFromFloat(value)
		}
		volumeLots, err := tushareFloat(row["vol"])
		if err != nil || volumeLots < 0 || volumeLots > float64(math.MaxInt64)/100 {
			return nil, fmt.Errorf("invalid Tushare volume for %s on %s", symbol, date)
		}
		amountThousands, err := tushareFloat(row["amount"])
		if err != nil {
			return nil, fmt.Errorf("invalid Tushare amount for %s on %s", symbol, date)
		}
		turnover := market.DecimalFromFloat(amountThousands * 1000)
		result = append(result, market.Bar{Symbol: symbol, Timestamp: ts.UTC(), Open: values[0], High: values[1], Low: values[2], Close: values[3], Volume: int64(math.Round(volumeLots * 100)), Turnover: &turnover, Session: session, Source: "tushare", Completed: true})
	}
	market.SortBars(result)
	return result, nil
}

func (p *TushareAShare) Securities(ctx context.Context) ([]Security, error) {
	payload, err := p.query(ctx, "stock_basic", map[string]any{"list_status": "L"}, "ts_code,name")
	if err != nil {
		return nil, err
	}
	rows, err := tushareRows(payload)
	if err != nil {
		return nil, err
	}
	securities := make([]Security, 0, len(rows))
	for _, row := range rows {
		symbol, symbolErr := tushareString(row["ts_code"])
		name, nameErr := tushareString(row["name"])
		if symbolErr != nil || nameErr != nil {
			return nil, fmt.Errorf("invalid Tushare stock_basic row")
		}
		_, venue, normalizeErr := market.NormalizeSymbol(symbol)
		if normalizeErr == nil && (venue == market.VenueSH || venue == market.VenueSZ) {
			securities = append(securities, Security{Symbol: symbol, NameCN: name})
		}
	}
	return mergeSecurities(securities), nil
}

func (p *TushareAShare) Universe(ctx context.Context) ([]string, error) {
	securities, err := p.Securities(ctx)
	if err != nil {
		return nil, err
	}
	return securitySymbols(securities), nil
}

func tushareRows(payload tushareResponse) ([]map[string]any, error) {
	rows := make([]map[string]any, 0, len(payload.Data.Items))
	for _, item := range payload.Data.Items {
		if len(item) != len(payload.Data.Fields) {
			return nil, fmt.Errorf("Tushare returned %d values for %d fields", len(item), len(payload.Data.Fields))
		}
		row := make(map[string]any, len(item))
		for index, field := range payload.Data.Fields {
			row[field] = item[index]
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func tushareString(value any) (string, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("expected a non-empty string, got %v", value)
	}
	return text, nil
}

func tushareFloat(value any) (float64, error) {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, fmt.Errorf("invalid number")
		}
		return typed, nil
	case string:
		return strconv.ParseFloat(typed, 64)
	default:
		return 0, fmt.Errorf("expected a number, got %v", value)
	}
}

var _ Provider = (*TushareAShare)(nil)
var _ SecurityLister = (*TushareAShare)(nil)
