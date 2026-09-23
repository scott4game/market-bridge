package analytics

import (
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
	"github.com/shopspring/decimal"
)

func flowBar(high, low, closeValue, turnover float64) market.Bar {
	t := market.DecimalFromFloat(turnover)
	return market.Bar{
		Symbol: "NVDA", Timestamp: time.Date(2026, 9, 1, 14, 30, 0, 0, time.UTC),
		High: market.DecimalFromFloat(high), Low: market.DecimalFromFloat(low), Close: market.DecimalFromFloat(closeValue),
		Volume: 100, Turnover: &t, Completed: true,
	}
}

func TestFlowFromBarPreservesTurnover(t *testing.T) {
	for _, test := range []struct {
		name                 string
		bar                  market.Bar
		wantIn, wantOut, net string
	}{
		{name: "close high", bar: flowBar(10, 8, 10, 1000), wantIn: "1000.000000", wantOut: "0.000000", net: "1000.000000"},
		{name: "close low", bar: flowBar(10, 8, 8, 1000), wantIn: "0.000000", wantOut: "1000.000000", net: "-1000.000000"},
		{name: "flat", bar: flowBar(9, 9, 9, 1001), wantIn: "500.500000", wantOut: "500.500000", net: "0.000000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			point, err := FlowFromBar(test.bar)
			if err != nil {
				t.Fatal(err)
			}
			if point.Inflow != test.wantIn || point.Outflow != test.wantOut || point.NetFlow != test.net {
				t.Fatalf("point=%+v", point)
			}
			sum := decimal.RequireFromString(point.Inflow).Add(decimal.RequireFromString(point.Outflow))
			if !sum.Equal(decimal.RequireFromString(point.Turnover)) {
				t.Fatalf("inflow + outflow = %s, turnover=%s", sum, point.Turnover)
			}
		})
	}
}

func TestFlowRequiresCompletedProviderTurnover(t *testing.T) {
	bar := flowBar(10, 8, 9, 1000)
	bar.Turnover = nil
	if _, err := FlowFromBar(bar); err == nil {
		t.Fatal("missing turnover must fail")
	}
	bar.Turnover = new(market.Decimal)
	bar.Completed = false
	if _, err := FlowFromBar(bar); err == nil {
		t.Fatal("incomplete bar must fail")
	}
}

func TestVolumePointsUsesDailyAndSameTimeBucketBaselines(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 1, 13, 30, 0, 0, time.UTC)
	var bars []market.Bar
	for day := 0; day < 21; day++ {
		for slot, volume := range []int64{100, 900} {
			ts := start.AddDate(0, 0, day).Add(time.Duration(slot) * 30 * time.Minute)
			turnover := market.DecimalFromFloat(float64(volume) * 10)
			bars = append(bars, market.Bar{Symbol: "NVDA", Timestamp: ts, Volume: volume, Turnover: &turnover, Completed: true})
		}
	}
	points := VolumePoints(bars, "30m", 20, location)
	lastOpen, lastLate := points[len(points)-2], points[len(points)-1]
	if !lastOpen.BaselineComplete || lastOpen.VolumeRatio == nil || *lastOpen.VolumeRatio != "1.000000" {
		t.Fatalf("open point=%+v", lastOpen)
	}
	if !lastLate.BaselineComplete || lastLate.VolumeRatio == nil || *lastLate.VolumeRatio != "1.000000" {
		t.Fatalf("late point=%+v", lastLate)
	}
	if points[19].BaselineComplete {
		t.Fatal("same-time bucket must require 20 prior sessions")
	}

	var daily []market.Bar
	for day := 0; day < 21; day++ {
		daily = append(daily, market.Bar{Timestamp: start.AddDate(0, 0, day), Volume: 100, Completed: true})
	}
	dailyPoints := VolumePoints(daily, "1d", 20, location)
	if !dailyPoints[20].BaselineComplete || dailyPoints[20].VolumeRatio == nil || *dailyPoints[20].VolumeRatio != "1.000000" {
		t.Fatalf("daily point=%+v", dailyPoints[20])
	}
}
