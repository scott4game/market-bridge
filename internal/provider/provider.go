package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
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

type ForwardAdjustmentProvider interface {
	ForwardAdjustmentFactors(context.Context, string) (market.ForwardFactors, error)
}

func ForwardAdjustmentFactors(ctx context.Context, p Provider, symbol string) (market.ForwardFactors, error) {
	adjuster, ok := p.(ForwardAdjustmentProvider)
	if !ok {
		return market.ForwardFactors{}, fmt.Errorf("provider %s does not support forward adjustment", p.Name())
	}
	return adjuster.ForwardAdjustmentFactors(ctx, symbol)
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
	PlanName                 string
	HTTP                     *http.Client
	Usage                    *UsageTracker
	factorMu                 sync.Mutex
	factorCache              map[string]massiveFactorCache
}

type massiveFactorCache struct {
	curve     market.ForwardFactors
	expiresAt time.Time
}

func (m *Massive) Name() string { return "massive" }
func (m *Massive) DataVersion() string {
	if m.Version == "" {
		return "massive-v1"
	}
	return m.Version
}
func (m *Massive) Describe(spec market.DatasetSpec) (Description, error) {
	if _, err := spec.Normalize(); err != nil {
		return Description{}, err
	}
	return Description{Name: m.Name(), DataVersion: m.DataVersion()}, nil
}

func (m *Massive) Bars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	if m.APIKey == "" {
		return nil, fmt.Errorf("MASSIVE_API_KEY is required")
	}
	spec, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, fmt.Errorf("load New York timezone: %w", err)
	}
	if market.IsUSForwardAdjusted(spec) && strings.EqualFold(m.PlanName, "stocks_basic") {
		now := time.Now().In(location)
		oldest := time.Date(now.Year()-2, now.Month(), now.Day(), 0, 0, 0, 0, location)
		if spec.From.In(location).Before(oldest) {
			return nil, fmt.Errorf("Massive Stocks Basic forward_adjusted history is limited to the most recent two years; upgrade the Massive plan or use split_adjusted")
		}
	}
	target := spec.Interval
	fetchSpec := spec
	if market.IsUSForwardAdjusted(spec) {
		fetchSpec.Adjustment = market.SplitAdjusted
	}
	if spec.Session == market.RegularSession && isUSHour(target) {
		fetchSpec.Interval = "30m"
	}
	if market.IsUSForwardAdjusted(spec) && (target == "1w" || target == "1mo" || target == "1y") {
		fetchSpec.Interval = "1d"
	}
	bars, err := m.fetchBars(ctx, fetchSpec)
	if err != nil {
		return nil, err
	}
	if market.IsUSForwardAdjusted(spec) {
		curves := make(map[string]market.ForwardFactors, len(spec.Symbols))
		for _, symbol := range spec.Symbols {
			curve, factorErr := m.ForwardAdjustmentFactors(ctx, symbol)
			if factorErr != nil {
				return nil, factorErr
			}
			curves[symbol] = curve
		}
		bars, err = market.ApplyForwardFactors(bars, curves, location)
		if err != nil {
			return nil, err
		}
	}
	if spec.Session == market.RegularSession && isUSIntraday(target) {
		if isUSHour(target) {
			bars, err = aggregateUSRegularHours(bars, target, location)
		} else {
			bars = filterUSRegularBars(bars, location)
		}
		if err != nil {
			return nil, err
		}
	}
	if market.IsUSForwardAdjusted(spec) && (target == "1w" || target == "1mo" || target == "1y") {
		bars, err = aggregateUSCalendar(bars, target, location)
		if err != nil {
			return nil, err
		}
	}
	bars = filterRequestedRange(bars, spec.From, spec.To)
	market.SortBars(bars)
	return bars, nil
}

func (m *Massive) fetchBars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
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
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Massive base URL: %w", err)
	}
	var bars []market.Bar
	for _, symbol := range spec.Symbols {
		next := fmt.Sprintf("%s/v2/aggs/ticker/%s/range/%d/%s/%d/%d", baseURL, url.PathEscape(symbol), multiplier, span, spec.From.UnixMilli(), spec.To.Add(-time.Millisecond).UnixMilli())
		visited := map[string]struct{}{}
		pages := 0
		for next != "" {
			u, err := url.Parse(next)
			if err != nil {
				return nil, err
			}
			if u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) {
				return nil, fmt.Errorf("massive: rejected cross-origin next_url %q", next)
			}
			canonical := u.String()
			if _, ok := visited[canonical]; ok {
				return nil, fmt.Errorf("massive: pagination cycle at %q", canonical)
			}
			visited[canonical] = struct{}{}
			pages++
			if pages > 10_000 {
				return nil, fmt.Errorf("massive: pagination exceeded 10000 pages")
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
			if resp.StatusCode/100 != 2 {
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				if readErr != nil {
					finish(resp.StatusCode, readErr)
					return nil, readErr
				}
				finish(resp.StatusCode, nil)
				var failure struct {
					Error string `json:"error"`
				}
				_ = json.Unmarshal(body, &failure)
				message := strings.TrimSpace(string(body))
				if failure.Error != "" {
					message = failure.Error
				}
				return nil, fmt.Errorf("massive: status %d: %s", resp.StatusCode, message)
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

func (m *Massive) ForwardAdjustmentFactors(ctx context.Context, symbol string) (market.ForwardFactors, error) {
	normalized, venue, err := market.NormalizeSymbol(symbol)
	if err != nil || venue != market.VenueUS {
		return market.ForwardFactors{}, fmt.Errorf("forward adjustment requires a US symbol")
	}
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return market.ForwardFactors{}, err
	}
	now := time.Now().In(location)
	m.factorMu.Lock()
	if entry, ok := m.factorCache[normalized]; ok && now.Before(entry.expiresAt) {
		m.factorMu.Unlock()
		return entry.curve, nil
	}
	m.factorMu.Unlock()

	client := m.HTTP
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	baseURL := strings.TrimRight(m.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.massive.com"
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return market.ForwardFactors{}, fmt.Errorf("invalid Massive base URL: %w", err)
	}
	next := baseURL + "/stocks/v1/dividends"
	visited := map[string]struct{}{}
	var factors []market.ForwardFactor
	pages := 0
	for next != "" {
		u, parseErr := url.Parse(next)
		if parseErr != nil {
			return market.ForwardFactors{}, parseErr
		}
		if u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) {
			return market.ForwardFactors{}, fmt.Errorf("massive dividends: rejected cross-origin next_url %q", next)
		}
		if _, ok := visited[u.String()]; ok {
			return market.ForwardFactors{}, fmt.Errorf("massive dividends: pagination cycle")
		}
		visited[u.String()] = struct{}{}
		pages++
		if pages > 10_000 {
			return market.ForwardFactors{}, fmt.Errorf("massive dividends: pagination exceeded 10000 pages")
		}
		q := u.Query()
		q.Set("apiKey", m.APIKey)
		q.Set("ticker", normalized)
		q.Set("limit", "5000")
		q.Set("sort", "ex_dividend_date.asc")
		q.Set("ex_dividend_date.lte", now.Format("2006-01-02"))
		u.RawQuery = q.Encode()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		finish := func(int, error) {}
		if m.Usage != nil {
			finish = m.Usage.Begin("massive", "stocks_dividends")
		}
		resp, requestErr := client.Do(req)
		if requestErr != nil {
			finish(0, requestErr)
			return market.ForwardFactors{}, requestErr
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			finish(resp.StatusCode, readErr)
			return market.ForwardFactors{}, readErr
		}
		if resp.StatusCode/100 != 2 {
			finish(resp.StatusCode, nil)
			var failure struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(body, &failure)
			message := strings.TrimSpace(string(body))
			if failure.Error != "" {
				message = failure.Error
			}
			return market.ForwardFactors{}, fmt.Errorf("massive dividends: status %d: %s", resp.StatusCode, message)
		}
		var payload struct {
			NextURL string `json:"next_url"`
			Results []struct {
				Date   string       `json:"ex_dividend_date"`
				Factor *json.Number `json:"historical_adjustment_factor"`
			} `json:"results"`
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		decodeErr := decoder.Decode(&payload)
		finish(resp.StatusCode, decodeErr)
		if decodeErr != nil {
			return market.ForwardFactors{}, decodeErr
		}
		for _, item := range payload.Results {
			if item.Date == "" || item.Factor == nil {
				return market.ForwardFactors{}, fmt.Errorf("massive dividends returned an incomplete adjustment factor for %s", normalized)
			}
			factor, factorErr := market.DecimalFromString(item.Factor.String())
			if factorErr != nil {
				return market.ForwardFactors{}, fmt.Errorf("massive dividends factor for %s: %w", normalized, factorErr)
			}
			factors = append(factors, market.ForwardFactor{EffectiveDate: item.Date, Factor: factor})
		}
		next = payload.NextURL
	}
	curve := market.ForwardFactors{Symbol: normalized, Mode: market.ForwardAdjusted, AsOf: now.Format("2006-01-02"), Factors: factors}
	curve, err = market.AccumulateForwardFactors(curve)
	if err != nil {
		return market.ForwardFactors{}, err
	}
	raw, _ := json.Marshal(curve.Factors)
	digest := sha256.Sum256(raw)
	curve.Version = fmt.Sprintf("massive-qfq-v2:%s:%x", curve.AsOf, digest[:8])
	expiresAt := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, location)
	m.factorMu.Lock()
	if m.factorCache == nil {
		m.factorCache = map[string]massiveFactorCache{}
	}
	m.factorCache[normalized] = massiveFactorCache{curve: curve, expiresAt: expiresAt}
	m.factorMu.Unlock()
	return curve, nil
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
