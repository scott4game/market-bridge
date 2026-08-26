package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	openapi "github.com/longbridge/openapi-go"
	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/market"
)

type UniverseLister interface {
	Universe(context.Context) ([]string, error)
}

type Security struct {
	Symbol string `json:"symbol"`
	NameCN string `json:"name_cn,omitempty"`
	NameEN string `json:"name_en,omitempty"`
}

type SecurityLister interface {
	Securities(context.Context) ([]Security, error)
}

func (m *Massive) Securities(ctx context.Context) ([]Security, error) {
	if m.APIKey == "" {
		return nil, fmt.Errorf("MASSIVE_API_KEY is required")
	}
	client := m.HTTP
	if client == nil {
		client = &http.Client{}
	}
	base := strings.TrimRight(m.BaseURL, "/")
	if base == "" {
		base = "https://api.massive.com"
	}
	next := base + "/v3/reference/tickers?market=stocks&active=true&limit=1000"
	var securities []Security
	for next != "" {
		u, err := url.Parse(next)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("apiKey", m.APIKey)
		u.RawQuery = q.Encode()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		var payload struct {
			Results []struct {
				Ticker string `json:"ticker"`
				Type   string `json:"type"`
				Name   string `json:"name"`
			} `json:"results"`
			NextURL string `json:"next_url"`
			Error   string `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("massive universe: status %d: %s", resp.StatusCode, payload.Error)
		}
		for _, item := range payload.Results {
			if item.Type == "CS" && item.Ticker != "" {
				securities = append(securities, Security{Symbol: item.Ticker, NameEN: item.Name})
			}
		}
		next = payload.NextURL
	}
	return mergeSecurities(securities), nil
}

func (m *Massive) Universe(ctx context.Context) ([]string, error) {
	securities, err := m.Securities(ctx)
	if err != nil {
		return nil, err
	}
	return securitySymbols(securities), nil
}

type longbridgeSecurityLister interface {
	SecurityList(context.Context, openapi.Market, lbquote.SecurityListCategory) ([]*lbquote.Security, error)
}

func (p *Longbridge) Securities(ctx context.Context) ([]Security, error) {
	lister, ok := p.Quote.(longbridgeSecurityLister)
	if !ok {
		return nil, fmt.Errorf("Longbridge client does not support security lists")
	}
	var securities []Security
	for _, marketName := range []openapi.Market{openapi.MarketUS, openapi.MarketHK, openapi.MarketCN} {
		items, err := lister.SecurityList(ctx, marketName, "")
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item != nil && item.Symbol != "" {
				securities = append(securities, Security{Symbol: item.Symbol, NameCN: item.NameCN, NameEN: item.NameEN})
			}
		}
	}
	return mergeSecurities(securities), nil
}

func (p *Longbridge) Universe(ctx context.Context) ([]string, error) {
	securities, err := p.Securities(ctx)
	if err != nil {
		return nil, err
	}
	return securitySymbols(securities), nil
}

func (r *Router) Securities(ctx context.Context) ([]Security, error) {
	var securities []Security
	candidates := append([]Provider{r.US, r.Longbridge}, r.UniverseProviders...)
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		part, err := listSecurities(ctx, candidate)
		if err != nil {
			return nil, fmt.Errorf("%s universe: %w", candidate.Name(), err)
		}
		securities = append(securities, part...)
	}
	return mergeSecurities(securities), nil
}

func (r *Router) Universe(ctx context.Context) ([]string, error) {
	securities, err := r.Securities(ctx)
	if err != nil {
		return nil, err
	}
	return securitySymbols(securities), nil
}

func listSecurities(ctx context.Context, candidate Provider) ([]Security, error) {
	if lister, ok := candidate.(SecurityLister); ok {
		return lister.Securities(ctx)
	}
	lister, ok := candidate.(UniverseLister)
	if !ok {
		return nil, nil
	}
	symbols, err := lister.Universe(ctx)
	if err != nil {
		return nil, err
	}
	securities := make([]Security, 0, len(symbols))
	for _, symbol := range symbols {
		securities = append(securities, Security{Symbol: symbol})
	}
	return securities, nil
}

func securitySymbols(securities []Security) []string {
	symbols := make([]string, 0, len(securities))
	for _, security := range securities {
		symbols = append(symbols, security.Symbol)
	}
	return symbols
}

func mergeSecurities(input []Security) []Security {
	bySymbol := make(map[string]Security, len(input))
	for _, security := range input {
		symbol, _, err := market.NormalizeSymbol(security.Symbol)
		if err != nil || symbol == "" {
			continue
		}
		current := bySymbol[symbol]
		current.Symbol = symbol
		if current.NameCN == "" {
			current.NameCN = strings.TrimSpace(security.NameCN)
		}
		if current.NameEN == "" {
			current.NameEN = strings.TrimSpace(security.NameEN)
		}
		bySymbol[symbol] = current
	}
	output := make([]Security, 0, len(bySymbol))
	for _, security := range bySymbol {
		output = append(output, security)
	}
	sort.Slice(output, func(i, j int) bool { return output[i].Symbol < output[j].Symbol })
	return output
}
