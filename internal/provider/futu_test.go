package provider

import (
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

func testPrice(v float64) market.Decimal { return market.DecimalFromFloat(v) }

func TestAggregateUSRegularHoursAnchorsAtMarketOpen(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	makeBar := func(hour, minute int, price float64) market.Bar {
		local := time.Date(2026, 8, 24, hour, minute, 0, 0, location)
		value := testPrice(price)
		return market.Bar{Symbol: "SNDK", Timestamp: local.UTC(), Open: value, High: value, Low: value, Close: value, Volume: 10, Completed: true}
	}
	input := []market.Bar{
		makeBar(9, 0, 1), // pre-market: must be removed
		makeBar(9, 30, 10), makeBar(10, 0, 11),
		makeBar(10, 30, 12), makeBar(11, 0, 13),
		makeBar(15, 30, 14), // final 30-minute partial hour
		makeBar(16, 0, 99), // after-hours: must be removed
	}
	bars, err := aggregateUSRegularHours(input, "1h", location)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 3 {
		t.Fatalf("bars=%d: %+v", len(bars), bars)
	}
	want := []string{"09:30", "10:30", "15:30"}
	for index, bar := range bars {
		if got := bar.Timestamp.In(location).Format("15:04"); got != want[index] {
			t.Fatalf("bar %d timestamp=%s want=%s", index, got, want[index])
		}
	}
	if bars[0].Open != testPrice(10) || bars[0].Close != testPrice(11) || bars[0].Volume != 20 {
		t.Fatalf("first=%+v", bars[0])
	}
}

func TestAggregateUSRegularHoursTracksDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	one := testPrice(1)
	input := []market.Bar{
		{Symbol: "SNDK", Timestamp: time.Date(2026, 3, 6, 14, 30, 0, 0, time.UTC), Open: one, High: one, Low: one, Close: one},
		{Symbol: "SNDK", Timestamp: time.Date(2026, 3, 9, 13, 30, 0, 0, time.UTC), Open: one, High: one, Low: one, Close: one},
	}
	bars, err := aggregateUSRegularHours(input, "1h", location)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 || bars[0].Timestamp.Hour() != 14 || bars[1].Timestamp.Hour() != 13 {
		t.Fatalf("bars=%+v", bars)
	}
	for _, bar := range bars {
		if got := bar.Timestamp.In(location).Format("15:04"); got != "09:30" {
			t.Fatalf("local timestamp=%s", got)
		}
	}
}
