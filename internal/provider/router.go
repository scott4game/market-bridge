package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type HistoricalProviderDisabledError struct {
	Provider string
	Venue    market.Venue
}

func (e *HistoricalProviderDisabledError) Error() string {
	if e.Provider == "Index" {
		return "index historical provider is disabled; set GO_SERVER_INDEX_PROVIDER to longbridge, fmp, massive, or mock and restart go-server"
	}
	if e.Provider == "Longbridge" {
		return fmt.Sprintf("Longbridge historical provider is not enabled for %s; set GO_SERVER_LONGBRIDGE_HISTORY_ENABLED=true and restart go-server", e.Venue)
	}
	if e.Provider == "A-share" {
		return fmt.Sprintf("A-share historical provider is not enabled for %s; set GO_SERVER_A_SHARE_PROVIDER=tushare or longbridge and restart go-server", e.Venue)
	}
	if e.Provider == "HK" {
		return "HK historical provider is not enabled; set GO_SERVER_HK_PROVIDER=longbridge and restart go-server"
	}
	return fmt.Sprintf("%s historical provider is not enabled for %s", e.Provider, e.Venue)
}

func IsHistoricalProviderDisabled(err error) bool {
	var target *HistoricalProviderDisabledError
	return errors.As(err, &target)
}

type Router struct {
	US                Provider
	Index             Provider
	AShare            Provider
	HK                Provider
	Binance           Provider
	UniverseProviders []Provider
	HistoryMaxYears   map[string]int
	HistoryCooldown   time.Duration
	Now               func() time.Time

	historyMu       sync.Mutex
	historyFailures map[string]historyFailure
}

type historyFailure struct {
	bars      []market.Bar
	err       error
	expiresAt time.Time
}

func (r *Router) Name() string        { return "router" }
func (r *Router) DataVersion() string { return "router-v1" }

func (r *Router) providerFor(venue market.Venue) (Provider, error) {
	switch venue {
	case market.VenueUS, market.VenueFutures:
		if r.US != nil {
			return r.US, nil
		}
	case market.VenueIndex:
		if r.Index != nil {
			return r.Index, nil
		}
		return nil, &HistoricalProviderDisabledError{Provider: "Index", Venue: venue}
	case market.VenueSH, market.VenueSZ:
		if r.AShare != nil {
			return r.AShare, nil
		}
		return nil, &HistoricalProviderDisabledError{Provider: "A-share", Venue: venue}
	case market.VenueHK:
		if r.HK != nil {
			return r.HK, nil
		}
		return nil, &HistoricalProviderDisabledError{Provider: "HK", Venue: venue}
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

func (r *Router) Supports(spec market.DatasetSpec) bool {
	routes, err := r.route(spec)
	if err != nil {
		return false
	}
	for _, route := range routes {
		if !Supports(route.provider, route.spec) {
			return false
		}
	}
	return true
}

func (r *Router) BarsWithForwardFactors(ctx context.Context, spec market.DatasetSpec, curves map[string]market.ForwardFactors) ([]market.Bar, error) {
	routes, err := r.route(spec)
	if err != nil {
		return nil, err
	}
	var bars []market.Bar
	var warnings error
	for _, route := range routes {
		childCurves := make(map[string]market.ForwardFactors, len(route.spec.Symbols))
		for _, symbol := range route.spec.Symbols {
			if curve, ok := curves[symbol]; ok {
				childCurves[symbol] = curve
			}
		}
		part, err := r.routeHistoryBars(ctx, route.provider, route.spec, childCurves)
		bars = append(bars, part...)
		if err != nil {
			warnings = errors.Join(warnings, fmt.Errorf("%s: %w", route.provider.Name(), err))
		}
	}
	market.SortBars(bars)
	return bars, warnings
}

func (r *Router) routeHistoryBars(ctx context.Context, p Provider, spec market.DatasetSpec, curves map[string]market.ForwardFactors) ([]market.Bar, error) {
	normalized, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	if !Supports(p, normalized) {
		return nil, fmt.Errorf("provider %s does not support interval %s", p.Name(), normalized.Interval)
	}
	if !historyPolicyInterval(normalized.Interval) {
		return BarsWithForwardFactors(ctx, p, normalized, curves)
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	anchor := normalized.To
	if anchor.After(now) {
		anchor = now
	}
	floor := anchor.AddDate(-r.maxHistoryYears(p), 0, 0)
	if !normalized.To.After(floor) {
		return []market.Bar{}, nil
	}
	if normalized.From.Before(floor) {
		normalized.From = floor
	}
	key := historyFailureKey(p, normalized)
	if bars, failureErr, ok := r.cachedHistoryFailure(key, normalized, now); ok {
		return bars, failureErr
	}

	var bars []market.Bar
	for end := normalized.To; end.After(normalized.From); {
		start := end.AddDate(-1, 0, 0)
		if start.Before(normalized.From) {
			start = normalized.From
		}
		chunk := normalized
		chunk.From, chunk.To = start, end
		part, fetchErr := BarsWithForwardFactors(ctx, p, chunk, curves)
		bars = append(bars, part...)
		if fetchErr != nil {
			market.SortBars(bars)
			bars = deduplicateBars(bars)
			wrapped := fmt.Errorf("history %s to %s: %w", start.Format("2006-01-02"), end.Format("2006-01-02"), fetchErr)
			r.storeHistoryFailure(key, bars, wrapped, now)
			return bars, wrapped
		}
		end = start
	}
	market.SortBars(bars)
	r.clearHistoryFailure(key)
	return deduplicateBars(bars), nil
}

func historyPolicyInterval(interval string) bool {
	switch interval {
	case "1h", "2h", "3h", "4h", "1d", "1w", "1mo", "1y":
		return true
	default:
		return false
	}
}

func canonicalProviderName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if base, _, ok := strings.Cut(name, "-"); ok {
		return base
	}
	return name
}

func (r *Router) maxHistoryYears(p Provider) int {
	if years := r.HistoryMaxYears[canonicalProviderName(p.Name())]; years > 0 {
		return years
	}
	return 5
}

func historyFailureKey(p Provider, spec market.DatasetSpec) string {
	symbols := append([]string(nil), spec.Symbols...)
	sort.Strings(symbols)
	return strings.Join([]string{canonicalProviderName(p.Name()), strings.Join(symbols, ","), spec.Interval, string(spec.Session), string(spec.Adjustment)}, "|")
}

func (r *Router) cachedHistoryFailure(key string, spec market.DatasetSpec, now time.Time) ([]market.Bar, error, bool) {
	r.historyMu.Lock()
	defer r.historyMu.Unlock()
	failure, ok := r.historyFailures[key]
	if !ok {
		return nil, nil, false
	}
	if !now.Before(failure.expiresAt) {
		delete(r.historyFailures, key)
		return nil, nil, false
	}
	bars := filterRequestedRange(append([]market.Bar(nil), failure.bars...), spec.From, spec.To)
	return bars, fmt.Errorf("history fetch cooling down until %s: %w", failure.expiresAt.UTC().Format(time.RFC3339), failure.err), true
}

func (r *Router) storeHistoryFailure(key string, bars []market.Bar, err error, now time.Time) {
	ttl := r.HistoryCooldown
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	r.historyMu.Lock()
	defer r.historyMu.Unlock()
	if r.historyFailures == nil {
		r.historyFailures = map[string]historyFailure{}
	}
	r.historyFailures[key] = historyFailure{bars: append([]market.Bar(nil), bars...), err: err, expiresAt: now.Add(ttl)}
}

func (r *Router) clearHistoryFailure(key string) {
	r.historyMu.Lock()
	defer r.historyMu.Unlock()
	delete(r.historyFailures, key)
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
