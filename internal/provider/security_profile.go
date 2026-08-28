package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/scott4game/market-bridge/internal/market"
)

type SecurityProfile struct {
	Symbol          string  `json:"symbol"`
	Name            string  `json:"name,omitempty"`
	CIK             string  `json:"cik,omitempty"`
	Type            string  `json:"type,omitempty"`
	Active          bool    `json:"active"`
	Locale          string  `json:"locale,omitempty"`
	Market          string  `json:"market,omitempty"`
	PrimaryExchange string  `json:"primary_exchange,omitempty"`
	MarketCap       float64 `json:"market_cap,omitempty"`
	SICCode         string  `json:"sic_code,omitempty"`
	SICDescription  string  `json:"sic_description,omitempty"`
	Provider        string  `json:"provider"`
}

type SecurityProfiler interface {
	SecurityProfile(context.Context, string) (SecurityProfile, error)
}

type SecurityProfileUniverseLister interface {
	SecurityProfileUniverse(context.Context) ([]Security, error)
}

func (m *Massive) SecurityProfileUniverse(ctx context.Context) ([]Security, error) {
	return m.Securities(ctx)
}

func (m *Massive) SecurityProfile(ctx context.Context, rawSymbol string) (SecurityProfile, error) {
	if m.APIKey == "" {
		return SecurityProfile{}, fmt.Errorf("MASSIVE_API_KEY is required")
	}
	symbol, venue, err := market.NormalizeSymbol(rawSymbol)
	if err != nil {
		return SecurityProfile{}, err
	}
	if venue != market.VenueUS {
		return SecurityProfile{}, fmt.Errorf("massive security profiles only support US stocks")
	}
	base := strings.TrimRight(m.BaseURL, "/")
	if base == "" {
		base = "https://api.massive.com"
	}
	u, err := url.Parse(base + "/v3/reference/tickers/" + url.PathEscape(symbol))
	if err != nil {
		return SecurityProfile{}, err
	}
	q := u.Query()
	q.Set("apiKey", m.APIKey)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return SecurityProfile{}, err
	}
	client := m.HTTP
	if client == nil {
		client = &http.Client{}
	}
	finish := func(int, error) {}
	if m.Usage != nil {
		finish = m.Usage.Begin("massive", "stocks_ticker_overview")
	}
	resp, err := client.Do(req)
	if err != nil {
		finish(0, err)
		return SecurityProfile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			finish(resp.StatusCode, readErr)
			return SecurityProfile{}, readErr
		}
		finish(resp.StatusCode, nil)
		return SecurityProfile{}, fmt.Errorf("massive ticker overview: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Results struct {
			Ticker          string  `json:"ticker"`
			Name            string  `json:"name"`
			CIK             string  `json:"cik"`
			Type            string  `json:"type"`
			Active          bool    `json:"active"`
			Locale          string  `json:"locale"`
			Market          string  `json:"market"`
			PrimaryExchange string  `json:"primary_exchange"`
			MarketCap       float64 `json:"market_cap"`
			SICCode         string  `json:"sic_code"`
			SICDescription  string  `json:"sic_description"`
		} `json:"results"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		finish(resp.StatusCode, err)
		return SecurityProfile{}, fmt.Errorf("decode massive ticker overview: %w", err)
	}
	finish(resp.StatusCode, nil)
	profile := SecurityProfile{
		Symbol: payload.Results.Ticker, Name: payload.Results.Name, CIK: payload.Results.CIK, Type: payload.Results.Type,
		Active: payload.Results.Active, Locale: payload.Results.Locale, Market: payload.Results.Market, PrimaryExchange: payload.Results.PrimaryExchange,
		MarketCap: payload.Results.MarketCap, SICCode: payload.Results.SICCode, SICDescription: payload.Results.SICDescription,
	}
	profile.Symbol = strings.ToUpper(strings.TrimSpace(profile.Symbol))
	if profile.Symbol == "" {
		return SecurityProfile{}, fmt.Errorf("massive ticker overview returned no symbol for %s", symbol)
	}
	profile.Provider = m.Name()
	return profile, nil
}

func (r *Router) SecurityProfile(ctx context.Context, symbol string) (SecurityProfile, error) {
	_, venue, err := market.NormalizeSymbol(symbol)
	if err != nil {
		return SecurityProfile{}, err
	}
	if venue != market.VenueUS || r.US == nil {
		return SecurityProfile{}, fmt.Errorf("security profiles are unavailable for %s", symbol)
	}
	profiler, ok := r.US.(SecurityProfiler)
	if !ok {
		return SecurityProfile{}, fmt.Errorf("%s provider does not expose security profiles", r.US.Name())
	}
	return profiler.SecurityProfile(ctx, symbol)
}

func (r *Router) SecurityProfileUniverse(ctx context.Context) ([]Security, error) {
	if r.US == nil {
		return nil, fmt.Errorf("US security profile provider is unavailable")
	}
	if lister, ok := r.US.(SecurityProfileUniverseLister); ok {
		return lister.SecurityProfileUniverse(ctx)
	}
	return listSecurities(ctx, r.US)
}
