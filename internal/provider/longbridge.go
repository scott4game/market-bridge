package provider

import (
	"context"
	"fmt"
	"sort"
	"time"

	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/market"
)

type LongbridgeHistoryClient interface {
	HistoryCandlesticksByOffset(context.Context, string, lbquote.Period, lbquote.AdjustType, bool, *time.Time, int32, ...lbquote.CandlestickRequestOption) ([]*lbquote.Candlestick, error)
}

type Longbridge struct {
	Quote   LongbridgeHistoryClient
	Version string
}

func (p *Longbridge) Name() string { return "longbridge" }
func (p *Longbridge) DataVersion() string {
	if p.Version == "" {
		return "longbridge-v1"
	}
	return p.Version
}

type longbridgePeriod struct {
	period lbquote.Period
	factor int
}

func mapLongbridgePeriod(interval string) (longbridgePeriod, error) {
	switch interval {
	case "1m":
		return longbridgePeriod{lbquote.PeriodOneMinute, 1}, nil
	case "3m":
		return longbridgePeriod{lbquote.PeriodOneMinute, 3}, nil
	case "5m":
		return longbridgePeriod{lbquote.PeriodFiveMinute, 1}, nil
	case "10m":
		return longbridgePeriod{lbquote.PeriodFiveMinute, 2}, nil
	case "15m":
		return longbridgePeriod{lbquote.PeriodFifteenMinute, 1}, nil
	case "30m":
		return longbridgePeriod{lbquote.PeriodThirtyMinute, 1}, nil
	case "1h":
		return longbridgePeriod{lbquote.PeriodSixtyMinute, 1}, nil
	case "2h":
		return longbridgePeriod{lbquote.PeriodSixtyMinute, 2}, nil
	case "3h":
		return longbridgePeriod{lbquote.PeriodSixtyMinute, 3}, nil
	case "4h":
		return longbridgePeriod{lbquote.PeriodSixtyMinute, 4}, nil
	case "1d":
		return longbridgePeriod{lbquote.PeriodDay, 1}, nil
	case "1w":
		return longbridgePeriod{lbquote.PeriodWeek, 1}, nil
	case "1mo":
		return longbridgePeriod{lbquote.PeriodMonth, 1}, nil
	case "1y":
		return longbridgePeriod{lbquote.PeriodYear, 1}, nil
	default:
		return longbridgePeriod{}, fmt.Errorf("unsupported Longbridge interval %q", interval)
	}
}

func (p *Longbridge) Bars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	if p.Quote == nil {
		return nil, fmt.Errorf("Longbridge quote context is not configured")
	}
	normalized, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	period, err := mapLongbridgePeriod(normalized.Interval)
	if err != nil {
		return nil, err
	}
	adjust := lbquote.AdjustTypeNo
	if normalized.Adjustment == market.ForwardAdjusted {
		adjust = lbquote.AdjustTypeForward
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	var bars []market.Bar
	for _, symbol := range normalized.Symbols {
		venue, _ := market.VenueOf(symbol)
		if venue != market.VenueHK && venue != market.VenueSH && venue != market.VenueSZ {
			return nil, fmt.Errorf("Longbridge historical provider does not route %s", symbol)
		}
		part, fetchErr := fetchLongbridgeHistory(ctx, p.Quote, normalized, period, adjust, symbol, symbol, location)
		if fetchErr != nil {
			return nil, fetchErr
		}
		bars = append(bars, part...)
	}
	bars = deduplicateBars(bars)
	market.SortBars(bars)
	return bars, nil
}

func fetchLongbridgeHistory(ctx context.Context, quote LongbridgeHistoryClient, spec market.DatasetSpec, period longbridgePeriod, adjust lbquote.AdjustType, publicSymbol, upstreamSymbol string, location *time.Location) ([]market.Bar, error) {
	var bars []market.Bar
	cursor := spec.From.In(location)
	for cursor.Before(spec.To.In(location)) {
		sticks, err := quote.HistoryCandlesticksByOffset(ctx, upstreamSymbol, period.period, adjust, true, &cursor, 1000, lbquote.CandlestickRequestTradeSession(lbquote.CandlestickTradeSessionNormal))
		if err != nil {
			return nil, fmt.Errorf("history %s from %s: %w", publicSymbol, cursor.Format(time.RFC3339), err)
		}
		if len(sticks) == 0 {
			break
		}
		sort.Slice(sticks, func(i, j int) bool { return sticks[i].Timestamp < sticks[j].Timestamp })
		last := time.Unix(sticks[len(sticks)-1].Timestamp, 0).UTC()
		for _, stick := range sticks {
			ts := time.Unix(stick.Timestamp, 0).UTC()
			if ts.Before(spec.From) || !ts.Before(spec.To) || stick.Open == nil || stick.High == nil || stick.Low == nil || stick.Close == nil {
				continue
			}
			open, e1 := market.DecimalFromString(stick.Open.String())
			high, e2 := market.DecimalFromString(stick.High.String())
			low, e3 := market.DecimalFromString(stick.Low.String())
			closeValue, e4 := market.DecimalFromString(stick.Close.String())
			if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
				return nil, fmt.Errorf("Longbridge returned an invalid decimal for %s", publicSymbol)
			}
			bar := market.Bar{Symbol: publicSymbol, Timestamp: ts, Open: open, High: high, Low: low, Close: closeValue, Volume: stick.Volume, Session: spec.Session, Source: "longbridge", Completed: true}
			if stick.Turnover != nil {
				turnover, parseErr := market.DecimalFromString(stick.Turnover.String())
				if parseErr == nil {
					bar.Turnover = &turnover
				}
			}
			bars = append(bars, bar)
		}
		next := last.Add(time.Second).In(location)
		if !next.After(cursor) || last.After(spec.To) || len(sticks) < 1000 {
			break
		}
		cursor = next
	}
	bars = deduplicateBars(bars)
	if period.factor > 1 {
		var err error
		bars, err = aggregateBars(bars, spec.Interval, period.factor, location)
		if err != nil {
			return nil, err
		}
	}
	return bars, nil
}

func deduplicateBars(input []market.Bar) []market.Bar {
	market.SortBars(input)
	output := make([]market.Bar, 0, len(input))
	for _, bar := range input {
		if len(output) > 0 && output[len(output)-1].Symbol == bar.Symbol && output[len(output)-1].Timestamp.Equal(bar.Timestamp) {
			output[len(output)-1] = bar
			continue
		}
		output = append(output, bar)
	}
	return output
}
