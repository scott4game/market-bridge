package market

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	shopdecimal "github.com/shopspring/decimal"
)

// ForwardFactor is a cumulative multiplicative price factor effective for bars
// strictly before EffectiveDate. Provider adapters convert per-event upstream
// factors into this cumulative representation.
type ForwardFactor struct {
	EffectiveDate string              `json:"effective_date"`
	Factor        shopdecimal.Decimal `json:"factor"`
}

type ForwardFactors struct {
	Symbol  string          `json:"symbol"`
	Mode    AdjustmentMode  `json:"mode"`
	AsOf    string          `json:"as_of"`
	Version string          `json:"version"`
	Factors []ForwardFactor `json:"factors"`
}

// ApplyForwardFactors returns a copy whose OHLC values are adjusted. Volume is
// already on Massive's split-adjusted share basis and turnover is actual traded
// value, so neither is changed for cash dividends.
func ApplyForwardFactors(input []Bar, curves map[string]ForwardFactors, location *time.Location) ([]Bar, error) {
	if location == nil {
		location = time.UTC
	}
	out := append([]Bar(nil), input...)
	for index := range out {
		bar := &out[index]
		curve, ok := curves[bar.Symbol]
		if !ok {
			return nil, fmt.Errorf("missing forward-adjustment factors for %s", bar.Symbol)
		}
		date := bar.Timestamp.In(location).Format("2006-01-02")
		factor := shopdecimal.NewFromInt(1)
		for _, item := range curve.Factors {
			if item.EffectiveDate > date {
				factor = item.Factor
				break
			}
		}
		var err error
		if bar.Open, err = multiplyDecimal(bar.Open, factor); err != nil {
			return nil, err
		}
		if bar.High, err = multiplyDecimal(bar.High, factor); err != nil {
			return nil, err
		}
		if bar.Low, err = multiplyDecimal(bar.Low, factor); err != nil {
			return nil, err
		}
		if bar.Close, err = multiplyDecimal(bar.Close, factor); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func NormalizeForwardFactors(curve ForwardFactors) (ForwardFactors, error) {
	for _, prefix := range []string{"massive-qfq-v1:", "massive-qfq-v2:", "massive-qfq-v3:"} {
		if strings.HasPrefix(curve.Version, prefix) {
			return curve, fmt.Errorf("obsolete forward-adjustment semantics %q; upgrade server and rebuild adjusted caches", prefix)
		}
	}
	curve.Factors = append([]ForwardFactor(nil), curve.Factors...)
	for _, item := range curve.Factors {
		if _, err := time.Parse("2006-01-02", item.EffectiveDate); err != nil {
			return curve, fmt.Errorf("invalid adjustment effective date %q", item.EffectiveDate)
		}
		if !item.Factor.IsPositive() {
			return curve, fmt.Errorf("invalid adjustment factor %s", item.Factor.String())
		}
	}
	sort.Slice(curve.Factors, func(i, j int) bool { return curve.Factors[i].EffectiveDate < curve.Factors[j].EffectiveDate })
	unique := curve.Factors[:0]
	for _, item := range curve.Factors {
		if len(unique) > 0 && unique[len(unique)-1].EffectiveDate == item.EffectiveDate {
			if !unique[len(unique)-1].Factor.Equal(item.Factor) {
				return curve, fmt.Errorf("conflicting adjustment factors on %s", item.EffectiveDate)
			}
			continue
		}
		unique = append(unique, item)
	}
	curve.Factors = unique
	return curve, nil
}

// FactorFromFloat is intended for constants; upstream factors are parsed as decimal strings.
func FactorFromFloat(value float64) shopdecimal.Decimal { return shopdecimal.NewFromFloat(value) }
func ParseAdjustmentFactor(value string) (shopdecimal.Decimal, error) {
	return shopdecimal.NewFromString(value)
}

// AccumulateForwardFactors converts per-event factors into the cumulative
// curve consumed by ApplyForwardFactors. Each returned factor includes that
// event and every later event in the response.
func AccumulateForwardFactors(curve ForwardFactors) (ForwardFactors, error) {
	curve, err := NormalizeForwardFactors(curve)
	if err != nil {
		return curve, err
	}
	cumulative := shopdecimal.NewFromInt(1)
	for index := len(curve.Factors) - 1; index >= 0; index-- {
		cumulative = cumulative.Mul(curve.Factors[index].Factor)
		curve.Factors[index].Factor = cumulative
	}
	return curve, nil
}

func multiplyDecimal(value Decimal, factor shopdecimal.Decimal) (Decimal, error) {
	result := shopdecimal.NewFromInt(int64(value)).Mul(factor).Round(0)
	max := shopdecimal.NewFromInt(math.MaxInt64)
	min := shopdecimal.NewFromInt(math.MinInt64)
	if result.GreaterThan(max) || result.LessThan(min) {
		return 0, fmt.Errorf("decimal overflow while applying forward adjustment")
	}
	return Decimal(result.IntPart()), nil
}
