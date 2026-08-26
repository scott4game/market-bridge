package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/scott4game/market-bridge/internal/market"
)

type HistoricalProviderDisabledError struct {
	Provider string
	Venue    market.Venue
}

func (e *HistoricalProviderDisabledError) Error() string {
	if e.Provider == "Longbridge" {
		return fmt.Sprintf("Longbridge historical provider is not enabled for %s; set GO_SERVER_LONGBRIDGE_HISTORY_ENABLED=true and restart go-server", e.Venue)
	}
	return fmt.Sprintf("%s historical provider is not enabled for %s", e.Provider, e.Venue)
}

func IsHistoricalProviderDisabled(err error) bool {
	var target *HistoricalProviderDisabledError
	return errors.As(err, &target)
}

type Router struct {
	US                Provider
	Longbridge        Provider
	Binance           Provider
	UniverseProviders []Provider
}

func (r *Router) Name() string        { return "router" }
func (r *Router) DataVersion() string { return "router-v1" }

func (r *Router) providerFor(venue market.Venue) (Provider, error) {
	switch venue {
	case market.VenueUS:
		if r.US != nil {
			return r.US, nil
		}
	case market.VenueHK, market.VenueSH, market.VenueSZ:
		if r.Longbridge != nil {
			return r.Longbridge, nil
		}
		return nil, &HistoricalProviderDisabledError{Provider: "Longbridge", Venue: venue}
	case market.VenueBinance:
		if r.Binance != nil {
			return r.Binance, nil
		}
		return nil, &HistoricalProviderDisabledError{Provider: "Binance", Venue: venue}
	}
	return nil, fmt.Errorf("no historical provider for market %s", venue)
}

type routedSpec struct {
	provider Provider
	spec     market.DatasetSpec
}

func (r *Router) route(spec market.DatasetSpec) ([]routedSpec, error) {
	normalized, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	type group struct {
		provider Provider
		symbols  []string
	}
	groups := map[Provider]*group{}
	for _, symbol := range normalized.Symbols {
		venue, err := market.VenueOf(symbol)
		if err != nil {
			return nil, err
		}
		p, err := r.providerFor(venue)
		if err != nil {
			return nil, err
		}
		if groups[p] == nil {
			groups[p] = &group{provider: p}
		}
		groups[p].symbols = append(groups[p].symbols, symbol)
	}
	result := make([]routedSpec, 0, len(groups))
	for _, item := range groups {
		child := normalized
		child.Symbols = item.symbols
		result = append(result, routedSpec{provider: item.provider, spec: child})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].provider.Name() < result[j].provider.Name() })
	return result, nil
}

func (r *Router) Describe(spec market.DatasetSpec) (Description, error) {
	routes, err := r.route(spec)
	if err != nil {
		return Description{}, err
	}
	names, versions := make([]string, 0, len(routes)), make([]string, 0, len(routes))
	for _, route := range routes {
		description, err := Describe(route.provider, route.spec)
		if err != nil {
			return Description{}, err
		}
		names = append(names, description.Name)
		versions = append(versions, description.DataVersion)
	}
	return Description{Name: strings.Join(names, "+"), DataVersion: strings.Join(versions, "+")}, nil
}

func (r *Router) Bars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	return r.BarsWithForwardFactors(ctx, spec, nil)
}

func (r *Router) BarsWithForwardFactors(ctx context.Context, spec market.DatasetSpec, curves map[string]market.ForwardFactors) ([]market.Bar, error) {
	routes, err := r.route(spec)
	if err != nil {
		return nil, err
	}
	var bars []market.Bar
	for _, route := range routes {
		childCurves := make(map[string]market.ForwardFactors, len(route.spec.Symbols))
		for _, symbol := range route.spec.Symbols {
			if curve, ok := curves[symbol]; ok {
				childCurves[symbol] = curve
			}
		}
		part, err := BarsWithForwardFactors(ctx, route.provider, route.spec, childCurves)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", route.provider.Name(), err)
		}
		bars = append(bars, part...)
	}
	market.SortBars(bars)
	return bars, nil
}

func (r *Router) ForwardAdjustmentFactors(ctx context.Context, symbol string) (market.ForwardFactors, error) {
	_, venue, err := market.NormalizeSymbol(symbol)
	if err != nil {
		return market.ForwardFactors{}, err
	}
	p, err := r.providerFor(venue)
	if err != nil {
		return market.ForwardFactors{}, err
	}
	return ForwardAdjustmentFactors(ctx, p, symbol)
}
