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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type OptionContractQuery struct {
	Underlying     string
	ContractType   string
	ExpirationFrom string
	ExpirationTo   string
	StrikeGTE      float64
	StrikeLTE      float64
	AsOf           string
}

func (q OptionContractQuery) Normalize() (OptionContractQuery, error) {
	underlying, venue, err := market.NormalizeSymbol(q.Underlying)
	if err != nil || venue != market.VenueUS {
		return q, errors.New("underlying must be a US stock symbol")
	}
	q.Underlying = underlying
	q.ContractType = strings.ToLower(strings.TrimSpace(q.ContractType))
	if q.ContractType != "" && q.ContractType != "call" && q.ContractType != "put" {
		return q, errors.New("type must be call or put")
	}
	for name, value := range map[string]string{"expiration_from": q.ExpirationFrom, "expiration_to": q.ExpirationTo, "as_of": q.AsOf} {
		if value == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", value); err != nil {
			return q, fmt.Errorf("%s must be YYYY-MM-DD", name)
		}
	}
	if q.ExpirationFrom != "" && q.ExpirationTo != "" && q.ExpirationFrom > q.ExpirationTo {
		return q, errors.New("expiration_from must not be after expiration_to")
	}
	if math.IsNaN(q.StrikeGTE) || math.IsNaN(q.StrikeLTE) || math.IsInf(q.StrikeGTE, 0) || math.IsInf(q.StrikeLTE, 0) || q.StrikeGTE < 0 || q.StrikeLTE < 0 || (q.StrikeGTE > 0 && q.StrikeLTE > 0 && q.StrikeGTE > q.StrikeLTE) {
		return q, errors.New("invalid strike range")
	}
	return q, nil
}

type OptionContract struct {
	Ticker            string  `json:"ticker"`
	Underlying        string  `json:"underlying"`
	ContractType      string  `json:"contract_type"`
	ExpirationDate    string  `json:"expiration_date"`
	StrikePrice       float64 `json:"strike_price"`
	SharesPerContract float64 `json:"shares_per_contract"`
	ExerciseStyle     string  `json:"exercise_style,omitempty"`
	PrimaryExchange   string  `json:"primary_exchange,omitempty"`
	Source            string  `json:"source"`
}

type OptionBar struct {
	Contract  string         `json:"contract"`
	Timestamp time.Time      `json:"timestamp"`
	Open      market.Decimal `json:"open"`
	High      market.Decimal `json:"high"`
	Low       market.Decimal `json:"low"`
	Close     market.Decimal `json:"close"`
	Volume    int64          `json:"volume"`
	Source    string         `json:"source"`
	Completed bool           `json:"completed"`
}

type OptionsProvider interface {
	Contracts(context.Context, OptionContractQuery) ([]OptionContract, error)
	OptionBars(context.Context, string, time.Time, time.Time) ([]OptionBar, error)
}

type MassiveOptions struct {
	APIKey, BaseURL   string
	HTTP              *http.Client
	Usage             *UsageTracker
	RequestsPerMinute int

	gateOnce sync.Once
	gate     *minuteGate
}

func (m *MassiveOptions) requestGate() *minuteGate {
	m.gateOnce.Do(func() { m.gate = &minuteGate{limit: m.RequestsPerMinute, now: time.Now} })
	return m.gate
}

func (m *MassiveOptions) Contracts(ctx context.Context, query OptionContractQuery) ([]OptionContract, error) {
	query, err := query.Normalize()
	if err != nil {
		return nil, err
	}
	base, baseURL, err := m.base()
	if err != nil {
		return nil, err
	}
	values := url.Values{"underlying_ticker": {query.Underlying}, "expired": {"true"}, "limit": {"1000"}, "sort": {"expiration_date"}, "order": {"asc"}}
	if query.ContractType != "" {
		values.Set("contract_type", query.ContractType)
	}
	if query.ExpirationFrom != "" {
		values.Set("expiration_date.gte", query.ExpirationFrom)
	}
	if query.ExpirationTo != "" {
		values.Set("expiration_date.lte", query.ExpirationTo)
	}
	if query.StrikeGTE > 0 {
		values.Set("strike_price.gte", strconv.FormatFloat(query.StrikeGTE, 'f', -1, 64))
	}
	if query.StrikeLTE > 0 {
		values.Set("strike_price.lte", strconv.FormatFloat(query.StrikeLTE, 'f', -1, 64))
	}
	if query.AsOf != "" {
		values.Set("as_of", query.AsOf)
	}
	next := baseURL + "/v3/reference/options/contracts?" + values.Encode()
	visited := map[string]struct{}{}
	var contracts []OptionContract
	for pages := 0; next != ""; pages++ {
		if pages >= 1000 {
			return nil, errors.New("massive options contracts pagination exceeded 1000 pages")
		}
		u, err := validatedProviderURL(next, base)
		if err != nil {
			return nil, fmt.Errorf("massive options: %w", err)
		}
		canonical := u.String()
		if _, ok := visited[canonical]; ok {
			return nil, errors.New("massive options contracts pagination cycle")
		}
		visited[canonical] = struct{}{}
		var payload struct {
			Status  string `json:"status"`
			Error   string `json:"error"`
			NextURL string `json:"next_url"`
			Results []struct {
				Ticker            string  `json:"ticker"`
				Underlying        string  `json:"underlying_ticker"`
				ContractType      string  `json:"contract_type"`
				ExpirationDate    string  `json:"expiration_date"`
				StrikePrice       float64 `json:"strike_price"`
				SharesPerContract float64 `json:"shares_per_contract"`
				ExerciseStyle     string  `json:"exercise_style"`
				PrimaryExchange   string  `json:"primary_exchange"`
			} `json:"results"`
		}
		if err := m.getJSON(ctx, u, "options_contracts", &payload); err != nil {
			return nil, err
		}
		if payload.Error != "" {
			return nil, redactOptionProviderError(errors.New(payload.Error), m.APIKey)
		}
		for _, item := range payload.Results {
			contracts = append(contracts, OptionContract{Ticker: item.Ticker, Underlying: item.Underlying, ContractType: item.ContractType, ExpirationDate: item.ExpirationDate, StrikePrice: item.StrikePrice, SharesPerContract: item.SharesPerContract, ExerciseStyle: item.ExerciseStyle, PrimaryExchange: item.PrimaryExchange, Source: "massive"})
		}
		next = payload.NextURL
	}
	sort.Slice(contracts, func(i, j int) bool {
		if contracts[i].ExpirationDate != contracts[j].ExpirationDate {
			return contracts[i].ExpirationDate < contracts[j].ExpirationDate
		}
		if contracts[i].StrikePrice != contracts[j].StrikePrice {
			return contracts[i].StrikePrice < contracts[j].StrikePrice
		}
		return contracts[i].Ticker < contracts[j].Ticker
	})
	return contracts, nil
}

func (m *MassiveOptions) OptionBars(ctx context.Context, contract string, from, to time.Time) ([]OptionBar, error) {
	contract = strings.ToUpper(strings.TrimSpace(contract))
	if !strings.HasPrefix(contract, "O:") || len(contract) < 5 {
		return nil, errors.New("contract must be an OCC ticker prefixed with O:")
	}
	if !from.Before(to) {
		return nil, errors.New("from must be before to")
	}
	base, baseURL, err := m.base()
	if err != nil {
		return nil, err
	}
	lastDate := to.UTC().Add(-time.Nanosecond).Format("2006-01-02")
	next := fmt.Sprintf("%s/v2/aggs/ticker/%s/range/1/day/%s/%s?adjusted=false&limit=50000&sort=asc", baseURL, url.PathEscape(contract), from.UTC().Format("2006-01-02"), lastDate)
	visited := map[string]struct{}{}
	var bars []OptionBar
	nowNY, _ := time.LoadLocation("America/New_York")
	today := time.Now().In(nowNY).Format("2006-01-02")
	for pages := 0; next != ""; pages++ {
		if pages >= 1000 {
			return nil, errors.New("massive options bars pagination exceeded 1000 pages")
		}
		u, err := validatedProviderURL(next, base)
		if err != nil {
			return nil, fmt.Errorf("massive options: %w", err)
		}
		canonical := u.String()
		if _, ok := visited[canonical]; ok {
			return nil, errors.New("massive options bars pagination cycle")
		}
		visited[canonical] = struct{}{}
		var payload struct {
			Status  string `json:"status"`
			Error   string `json:"error"`
			NextURL string `json:"next_url"`
			Results []struct {
				O, H, L, C float64
				V          float64
				T          int64
			} `json:"results"`
		}
		if err := m.getJSON(ctx, u, "options_aggregates_custom_bars", &payload); err != nil {
			return nil, err
		}
		if payload.Error != "" {
			return nil, redactOptionProviderError(errors.New(payload.Error), m.APIKey)
		}
		for _, item := range payload.Results {
			ts := time.UnixMilli(item.T).UTC()
			bars = append(bars, OptionBar{Contract: contract, Timestamp: ts, Open: market.DecimalFromFloat(item.O), High: market.DecimalFromFloat(item.H), Low: market.DecimalFromFloat(item.L), Close: market.DecimalFromFloat(item.C), Volume: int64(item.V), Source: "massive", Completed: ts.In(nowNY).Format("2006-01-02") < today})
		}
		next = payload.NextURL
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Timestamp.Before(bars[j].Timestamp) })
	return bars, nil
}

func (m *MassiveOptions) base() (*url.URL, string, error) {
	if strings.TrimSpace(m.APIKey) == "" {
		return nil, "", errors.New("MASSIVE_API_KEY is required for options")
	}
	baseURL := strings.TrimRight(m.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.massive.com"
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, "", errors.New("invalid Massive base URL")
	}
	return base, baseURL, nil
}

func validatedProviderURL(raw string, base *url.URL) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) {
		return nil, errors.New("rejected cross-origin next_url")
	}
	return u, nil
}

func (m *MassiveOptions) getJSON(ctx context.Context, u *url.URL, endpoint string, target any) error {
	client := m.HTTP
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := m.requestGate().Wait(ctx); err != nil {
			return err
		}
		copyURL := *u
		q := copyURL.Query()
		q.Set("apiKey", m.APIKey)
		copyURL.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, copyURL.String(), nil)
		if err != nil {
			return err
		}
		finish := func(int, error) {}
		if m.Usage != nil {
			finish = m.Usage.Begin("massive_options", endpoint)
		}
		resp, err := client.Do(req)
		if err != nil {
			safeErr := redactOptionProviderError(err, m.APIKey)
			finish(0, safeErr)
			return safeErr
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 2 {
			bodyErr := resp.Body.Close()
			finish(resp.StatusCode, bodyErr)
			wait := time.Duration(1<<attempt) * time.Second
			if seconds, parseErr := strconv.Atoi(resp.Header.Get("Retry-After")); parseErr == nil && seconds >= 0 {
				wait = time.Duration(seconds) * time.Second
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if resp.StatusCode/100 != 2 {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if readErr != nil {
				finish(resp.StatusCode, readErr)
				return readErr
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
			return redactOptionProviderError(fmt.Errorf("massive options: status %d: %s", resp.StatusCode, message), m.APIKey)
		}
		err = json.NewDecoder(resp.Body).Decode(target)
		resp.Body.Close()
		finish(resp.StatusCode, err)
		return err
	}
	return errors.New("massive options: retry limit exceeded")
}

func redactOptionProviderError(err error, secret string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	return errors.New(message)
}

type minuteGate struct {
	mu    sync.Mutex
	limit int
	calls []time.Time
	now   func() time.Time
}

func (g *minuteGate) Wait(ctx context.Context) error {
	if g == nil || g.limit <= 0 {
		return nil
	}
	for {
		g.mu.Lock()
		now := g.now()
		cutoff := now.Add(-time.Minute)
		keep := g.calls[:0]
		for _, call := range g.calls {
			if call.After(cutoff) {
				keep = append(keep, call)
			}
		}
		g.calls = keep
		if len(g.calls) < g.limit {
			g.calls = append(g.calls, now)
			g.mu.Unlock()
			return nil
		}
		wait := g.calls[0].Add(time.Minute).Sub(now)
		g.mu.Unlock()
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
