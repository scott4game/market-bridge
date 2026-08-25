package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type Provider interface {
	Name() string
	DataVersion() string
	Bars(context.Context, market.DatasetSpec) ([]market.Bar, error)
}

type Description struct {
	Name        string
	DataVersion string
}

type SpecDescriber interface {
	Describe(market.DatasetSpec) (Description, error)
}

func Describe(p Provider, spec market.DatasetSpec) (Description, error) {
	if describer, ok := p.(SpecDescriber); ok {
		return describer.Describe(spec)
	}
	return Description{Name: p.Name(), DataVersion: p.DataVersion()}, nil
}

type Mock struct{ Version string }

func (m *Mock) Name() string { return "mock" }
func (m *Mock) DataVersion() string {
	if m.Version == "" {
		return "mock-v1"
	}
	return m.Version
}
func (m *Mock) Bars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	spec, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	step := market.IntervalDuration(spec.Interval)
	if step < time.Minute {
		step = time.Minute
	}
	var out []market.Bar
	for _, symbol := range spec.Symbols {
		h := fnv.New32a()
		_, _ = h.Write([]byte(symbol))
		base := float64(50 + h.Sum32()%300)
		index := 0
		for ts := spec.From; ts.Before(spec.To); ts = ts.Add(step) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			if spec.Session == market.RegularSession {
				// The mock uses UTC weekdays and intentionally avoids pretending to be an exchange calendar.
				if ts.Weekday() == time.Saturday || ts.Weekday() == time.Sunday {
					continue
				}
			}
			drift := float64((index%41)-20) / 100
			o := market.DecimalFromFloat(base + drift)
			c := market.DecimalFromFloat(base + drift + float64((index%7)-3)/100)
			high := o
			if c > high {
				high = c
			}
			high += market.DecimalFromFloat(.05)
			low := o
			if c < low {
				low = c
			}
			low -= market.DecimalFromFloat(.05)
			out = append(out, market.Bar{Symbol: symbol, Timestamp: ts.UTC(), Open: o, High: high, Low: low, Close: c, Volume: int64(1000 + index%500), Session: spec.Session, Source: "mock", Completed: true})
			index++
		}
	}
	market.SortBars(out)
	return out, nil
}

type Massive struct {
	APIKey, BaseURL, Version string
	HTTP                     *http.Client
	Usage                    *UsageTracker
}

func (m *Massive) Name() string { return "massive" }
func (m *Massive) DataVersion() string {
	if m.Version == "" {
		return time.Now().UTC().Format("2006-01-02")
	}
	return m.Version
}
func (m *Massive) Bars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	if m.APIKey == "" {
		return nil, fmt.Errorf("MASSIVE_API_KEY is required")
	}
	spec, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	multiplier, span, err := massiveInterval(spec.Interval)
	if err != nil {
		return nil, err
	}
	client := m.HTTP
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	baseURL := strings.TrimRight(m.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.massive.com"
	}
	var bars []market.Bar
	for _, symbol := range spec.Symbols {
		next := fmt.Sprintf("%s/v2/aggs/ticker/%s/range/%d/%s/%d/%d", baseURL, url.PathEscape(symbol), multiplier, span, spec.From.UnixMilli(), spec.To.Add(-time.Millisecond).UnixMilli())
		for next != "" {
			u, err := url.Parse(next)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("apiKey", m.APIKey)
			q.Set("sort", "asc")
			q.Set("limit", "50000")
			q.Set("adjusted", strconv.FormatBool(spec.Adjustment == market.SplitAdjusted))
			u.RawQuery = q.Encode()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
			finish := func(int, error) {}
			if m.Usage != nil {
				finish = m.Usage.Begin("massive", "stocks_aggregates_custom_bars")
			}
			resp, err := client.Do(req)
			if err != nil {
				finish(0, err)
				return nil, err
			}
			var payload struct {
				Status  string `json:"status"`
				Error   string `json:"error"`
				NextURL string `json:"next_url"`
				Results []struct {
					O float64 `json:"o"`
					H float64 `json:"h"`
					L float64 `json:"l"`
					C float64 `json:"c"`
					V float64 `json:"v"`
					T int64   `json:"t"`
				}
			}
			err = json.NewDecoder(resp.Body).Decode(&payload)
			resp.Body.Close()
			if err != nil {
				finish(resp.StatusCode, err)
				return nil, err
			}
			finish(resp.StatusCode, nil)
			if resp.StatusCode/100 != 2 {
				return nil, fmt.Errorf("massive: status %d: %s", resp.StatusCode, payload.Error)
			}
			for _, x := range payload.Results {
				ts := time.UnixMilli(x.T).UTC()
				bars = append(bars, market.Bar{Symbol: symbol, Timestamp: ts, Open: market.DecimalFromFloat(x.O), High: market.DecimalFromFloat(x.H), Low: market.DecimalFromFloat(x.L), Close: market.DecimalFromFloat(x.C), Volume: int64(x.V), Session: spec.Session, Source: "massive", Completed: true})
			}
			next = payload.NextURL
		}
	}
	market.SortBars(bars)
	return bars, nil
}

func massiveInterval(v string) (int, string, error) {
	switch v {
	case "1m":
		return 1, "minute", nil
	case "3m":
		return 3, "minute", nil
	case "5m":
		return 5, "minute", nil
	case "10m":
		return 10, "minute", nil
	case "15m":
		return 15, "minute", nil
	case "30m":
		return 30, "minute", nil
	case "1h":
		return 1, "hour", nil
	case "2h":
		return 2, "hour", nil
	case "3h":
		return 3, "hour", nil
	case "4h":
		return 4, "hour", nil
	case "1d":
		return 1, "day", nil
	case "1w":
		return 1, "week", nil
	case "1mo":
		return 1, "month", nil
	case "1y":
		return 1, "year", nil
	default:
		return 0, "", fmt.Errorf("unsupported Massive interval %q", v)
	}
}
