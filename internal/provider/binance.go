package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type Binance struct {
	BaseURL string
	Version string
	HTTP    *http.Client
}

func (p *Binance) Name() string { return "binance" }
func (p *Binance) DataVersion() string {
	if p.Version == "" {
		return "binance-spot-v1"
	}
	return p.Version
}

type binancePeriod struct {
	interval string
	factor   int
}

func mapBinancePeriod(interval string) (binancePeriod, error) {
	switch interval {
	case "1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "1d", "1w":
		return binancePeriod{interval: interval, factor: 1}, nil
	case "10m":
		return binancePeriod{interval: "5m", factor: 2}, nil
	case "3h":
		return binancePeriod{interval: "1h", factor: 3}, nil
	case "1mo":
		return binancePeriod{interval: "1M", factor: 1}, nil
	case "1y":
		return binancePeriod{interval: "1M", factor: 12}, nil
	default:
		return binancePeriod{}, fmt.Errorf("unsupported Binance interval %q", interval)
	}
}

func (p *Binance) Bars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	normalized, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	period, err := mapBinancePeriod(normalized.Interval)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimRight(p.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://data-api.binance.vision"
	}
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	var bars []market.Bar
	for _, canonical := range normalized.Symbols {
		venue, _ := market.VenueOf(canonical)
		if venue != market.VenueBinance {
			return nil, fmt.Errorf("Binance provider does not route %s", canonical)
		}
		symbol := strings.TrimSuffix(canonical, ".BINANCE")
		cursor := normalized.From.UnixMilli()
		for cursor < normalized.To.UnixMilli() {
			u, _ := url.Parse(baseURL + "/api/v3/klines")
			query := u.Query()
			query.Set("symbol", symbol)
			query.Set("interval", period.interval)
			query.Set("startTime", strconv.FormatInt(cursor, 10))
			query.Set("endTime", strconv.FormatInt(normalized.To.Add(-time.Millisecond).UnixMilli(), 10))
			query.Set("limit", "1000")
			u.RawQuery = query.Encode()
			rows, retryAfter, err := requestBinanceKlines(ctx, client, u.String())
			if retryAfter > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(retryAfter):
				}
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("klines %s: %w", canonical, err)
			}
			if len(rows) == 0 {
				break
			}
			lastOpen := int64(0)
			for _, row := range rows {
				bar, parseErr := parseBinanceKline(canonical, row)
				if parseErr != nil {
					return nil, parseErr
				}
				lastOpen = bar.Timestamp.UnixMilli()
				if !bar.Timestamp.Before(normalized.From) && bar.Timestamp.Before(normalized.To) {
					bars = append(bars, bar)
				}
			}
			next := lastOpen + 1
			if next <= cursor || len(rows) < 1000 {
				break
			}
			cursor = next
		}
	}
	bars = deduplicateBars(bars)
	if period.factor > 1 {
		bars, err = aggregateBars(bars, normalized.Interval, period.factor, time.UTC)
		if err != nil {
			return nil, err
		}
	}
	market.SortBars(bars)
	return bars, nil
}

func requestBinanceKlines(ctx context.Context, client *http.Client, endpoint string) ([][]json.RawMessage, time.Duration, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusTeapot {
		seconds, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		if seconds < 1 {
			seconds = 1
		}
		return nil, time.Duration(seconds) * time.Second, nil
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, 0, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var rows [][]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, 0, err
	}
	return rows, 0, nil
}

func parseBinanceKline(symbol string, row []json.RawMessage) (market.Bar, error) {
	if len(row) < 8 {
		return market.Bar{}, fmt.Errorf("Binance returned a malformed kline")
	}
	var openTime int64
	var open, high, low, closeValue, volume, quoteVolume string
	if json.Unmarshal(row[0], &openTime) != nil || json.Unmarshal(row[1], &open) != nil || json.Unmarshal(row[2], &high) != nil || json.Unmarshal(row[3], &low) != nil || json.Unmarshal(row[4], &closeValue) != nil || json.Unmarshal(row[5], &volume) != nil || json.Unmarshal(row[7], &quoteVolume) != nil {
		return market.Bar{}, fmt.Errorf("Binance returned an invalid kline")
	}
	o, e1 := market.DecimalFromString(open)
	h, e2 := market.DecimalFromString(high)
	l, e3 := market.DecimalFromString(low)
	c, e4 := market.DecimalFromString(closeValue)
	turnover, e5 := market.DecimalFromString(quoteVolume)
	volumeFloat, e6 := strconv.ParseFloat(volume, 64)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil {
		return market.Bar{}, fmt.Errorf("Binance returned an invalid kline decimal")
	}
	return market.Bar{Symbol: symbol, Timestamp: time.UnixMilli(openTime).UTC(), Open: o, High: h, Low: l, Close: c, Volume: int64(volumeFloat), VolumeDecimal: volume, Turnover: &turnover, Session: market.ContinuousSession, Source: "binance", Completed: true}, nil
}
