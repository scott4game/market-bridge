package analytics

import (
	"errors"
	"sort"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
	"github.com/shopspring/decimal"
)

const FlowMethod = "massive_close_location_v1"

type FlowPoint struct {
	Symbol    string    `json:"symbol,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Volume    string    `json:"volume"`
	Turnover  string    `json:"turnover"`
	Inflow    string    `json:"inflow"`
	Outflow   string    `json:"outflow"`
	NetFlow   string    `json:"net_flow"`
	FlowRatio string    `json:"flow_ratio"`
	Completed bool      `json:"completed"`
}

type VolumePoint struct {
	Symbol             string    `json:"symbol,omitempty"`
	Timestamp          time.Time `json:"timestamp"`
	Volume             string    `json:"volume"`
	Turnover           *string   `json:"turnover,omitempty"`
	BaselineVolume     *string   `json:"baseline_volume,omitempty"`
	BaselineTurnover   *string   `json:"baseline_turnover,omitempty"`
	VolumeRatio        *string   `json:"volume_ratio,omitempty"`
	TurnoverRatio      *string   `json:"turnover_ratio,omitempty"`
	BaselineComplete   bool      `json:"baseline_complete"`
	BaselineSampleSize int       `json:"baseline_sample_size"`
	Completed          bool      `json:"completed"`
}

type FlowAggregate struct {
	Volume    string `json:"volume"`
	Turnover  string `json:"turnover"`
	Inflow    string `json:"inflow"`
	Outflow   string `json:"outflow"`
	NetFlow   string `json:"net_flow"`
	FlowRatio string `json:"flow_ratio"`
}

func FlowFromBar(bar market.Bar) (FlowPoint, error) {
	if !bar.Completed {
		return FlowPoint{}, errors.New("flow requires a completed bar")
	}
	if bar.Turnover == nil {
		return FlowPoint{}, errors.New("flow requires provider turnover")
	}
	turnover := marketDecimal(*bar.Turnover)
	if turnover.IsNegative() {
		return FlowPoint{}, errors.New("turnover must not be negative")
	}
	high, low, closeValue := marketDecimal(bar.High), marketDecimal(bar.Low), marketDecimal(bar.Close)
	inflow := turnover.Div(decimal.NewFromInt(2))
	if high.GreaterThan(low) {
		position := closeValue.Sub(low).DivRound(high.Sub(low), 18)
		if position.IsNegative() {
			position = decimal.Zero
		} else if position.GreaterThan(decimal.NewFromInt(1)) {
			position = decimal.NewFromInt(1)
		}
		inflow = turnover.Mul(position)
	}
	inflow = inflow.Round(6)
	outflow := turnover.Sub(inflow).Round(6)
	netFlow := inflow.Sub(outflow).Round(6)
	flowRatio := decimal.Zero
	if !turnover.IsZero() {
		flowRatio = netFlow.DivRound(turnover, 12)
	}
	return FlowPoint{
		Symbol: bar.Symbol, Timestamp: bar.Timestamp.UTC(), Volume: decimal.NewFromInt(bar.Volume).String(),
		Turnover: fixed(turnover), Inflow: fixed(inflow), Outflow: fixed(outflow), NetFlow: fixed(netFlow),
		FlowRatio: fixed(flowRatio), Completed: true,
	}, nil
}

func AggregateBars(bars []market.Bar) (FlowAggregate, error) {
	volume, turnover := decimal.Zero, decimal.Zero
	inflow, outflow, netFlow := decimal.Zero, decimal.Zero, decimal.Zero
	for _, bar := range bars {
		point, err := FlowFromBar(bar)
		if err != nil {
			return FlowAggregate{}, err
		}
		volume = volume.Add(decimal.RequireFromString(point.Volume))
		turnover = turnover.Add(decimal.RequireFromString(point.Turnover))
		inflow = inflow.Add(decimal.RequireFromString(point.Inflow))
		outflow = outflow.Add(decimal.RequireFromString(point.Outflow))
		netFlow = netFlow.Add(decimal.RequireFromString(point.NetFlow))
	}
	ratio := decimal.Zero
	if !turnover.IsZero() {
		ratio = netFlow.DivRound(turnover, 12)
	}
	return FlowAggregate{
		Volume: volume.String(), Turnover: fixed(turnover), Inflow: fixed(inflow), Outflow: fixed(outflow),
		NetFlow: fixed(netFlow), FlowRatio: fixed(ratio),
	}, nil
}

func VolumePoints(bars []market.Bar, interval string, lookback int, location *time.Location) []VolumePoint {
	if lookback < 1 {
		lookback = 20
	}
	if location == nil {
		location = time.UTC
	}
	completed := make([]market.Bar, 0, len(bars))
	for _, bar := range bars {
		if bar.Completed {
			completed = append(completed, bar)
		}
	}
	sort.SliceStable(completed, func(i, j int) bool { return completed[i].Timestamp.Before(completed[j].Timestamp) })
	result := make([]VolumePoint, 0, len(completed))
	for index, bar := range completed {
		point := VolumePoint{Symbol: bar.Symbol, Timestamp: bar.Timestamp.UTC(), Volume: decimal.NewFromInt(bar.Volume).String(), Completed: true}
		if bar.Turnover != nil {
			value := fixed(marketDecimal(*bar.Turnover))
			point.Turnover = &value
		}
		baseline := previousComparableBars(completed[:index], bar, interval, lookback, location)
		point.BaselineSampleSize = len(baseline)
		if len(baseline) != lookback {
			result = append(result, point)
			continue
		}
		point.BaselineComplete = true
		volumeTotal := decimal.Zero
		turnoverTotal := decimal.Zero
		turnoverComplete := true
		for _, previous := range baseline {
			volumeTotal = volumeTotal.Add(decimal.NewFromInt(previous.Volume))
			if previous.Turnover == nil {
				turnoverComplete = false
			} else {
				turnoverTotal = turnoverTotal.Add(marketDecimal(*previous.Turnover))
			}
		}
		baselineVolume := volumeTotal.DivRound(decimal.NewFromInt(int64(lookback)), 6)
		baselineVolumeText := fixed(baselineVolume)
		point.BaselineVolume = &baselineVolumeText
		if !baselineVolume.IsZero() {
			ratio := fixed(decimal.NewFromInt(bar.Volume).DivRound(baselineVolume, 12))
			point.VolumeRatio = &ratio
		}
		if turnoverComplete {
			baselineTurnover := turnoverTotal.DivRound(decimal.NewFromInt(int64(lookback)), 6)
			baselineTurnoverText := fixed(baselineTurnover)
			point.BaselineTurnover = &baselineTurnoverText
			if bar.Turnover != nil && !baselineTurnover.IsZero() {
				ratio := fixed(marketDecimal(*bar.Turnover).DivRound(baselineTurnover, 12))
				point.TurnoverRatio = &ratio
			}
		}
		result = append(result, point)
	}
	return result
}

func previousComparableBars(previous []market.Bar, current market.Bar, interval string, count int, location *time.Location) []market.Bar {
	result := make([]market.Bar, 0, count)
	currentLocal := current.Timestamp.In(location)
	currentDate := currentLocal.Format("2006-01-02")
	currentSlot := currentLocal.Format("15:04")
	for index := len(previous) - 1; index >= 0 && len(result) < count; index-- {
		candidate := previous[index]
		candidateLocal := candidate.Timestamp.In(location)
		if candidateLocal.Format("2006-01-02") == currentDate {
			continue
		}
		if interval != "1d" && candidateLocal.Format("15:04") != currentSlot {
			continue
		}
		result = append(result, candidate)
	}
	return result
}

func marketDecimal(value market.Decimal) decimal.Decimal {
	return decimal.NewFromInt(int64(value)).Shift(-6)
}

func fixed(value decimal.Decimal) string {
	return value.Round(6).StringFixed(6)
}
