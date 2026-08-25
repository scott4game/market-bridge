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
		makeBar(16, 0, 99),  // after-hours: must be removed
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

func TestAggregateUSRegularHoursAllFutuAnchors(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	day := time.Date(2026, 8, 24, 0, 0, 0, 0, location)
	one := testPrice(1)
	var input []market.Bar
	for minute := 9*60 + 30; minute < 16*60; minute += 30 {
		input = append(input, market.Bar{Symbol: "SNDK", Timestamp: time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, location), Open: one, High: one, Low: one, Close: one, Volume: 1})
	}
	tests := []struct {
		interval    string
		anchors     []string
		finalVolume int64
	}{
		{"1h", []string{"09:30", "10:30", "11:30", "12:30", "13:30", "14:30", "15:30"}, 1},
		{"2h", []string{"09:30", "11:30", "13:30", "15:30"}, 1},
		{"3h", []string{"09:30", "12:30", "15:30"}, 1},
		{"4h", []string{"09:30", "13:30"}, 5},
	}
	for _, test := range tests {
		t.Run(test.interval, func(t *testing.T) {
			bars, err := aggregateUSRegularHours(append([]market.Bar(nil), input...), test.interval, location)
			if err != nil {
				t.Fatal(err)
			}
			if len(bars) != len(test.anchors) {
				t.Fatalf("bars=%d want=%d", len(bars), len(test.anchors))
			}
			for index, bar := range bars {
				if got := bar.Timestamp.In(location).Format("15:04"); got != test.anchors[index] {
					t.Fatalf("bar %d=%s want=%s", index, got, test.anchors[index])
				}
			}
			if bars[len(bars)-1].Volume != test.finalVolume {
				t.Fatalf("final partial bar=%+v", bars[len(bars)-1])
			}
		})
	}
}

func TestAggregateUSRegularHoursHalfDayMissingBarsAndDayIsolation(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	one := testPrice(1)
	makeBar := func(day, hour, minute int) market.Bar {
		return market.Bar{Symbol: "SNDK", Timestamp: time.Date(2026, 11, day, hour, minute, 0, 0, location), Open: one, High: one, Low: one, Close: one, Volume: 1}
	}
	input := []market.Bar{
		makeBar(27, 9, 30), makeBar(27, 10, 0),
		// The 10:30 bucket is absent and must not produce an empty bar.
		makeBar(27, 11, 30), makeBar(27, 12, 0), makeBar(27, 12, 30),
		makeBar(30, 9, 30),
	}
	bars, err := aggregateUSRegularHours(input, "1h", location)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 4 {
		t.Fatalf("bars=%d: %+v", len(bars), bars)
	}
	want := []string{"27 09:30", "27 11:30", "27 12:30", "30 09:30"}
	for index, bar := range bars {
		if got := bar.Timestamp.In(location).Format("02 15:04"); got != want[index] {
			t.Fatalf("bar %d=%s want=%s", index, got, want[index])
		}
	}
}

func TestAggregateUSCalendarUsesFirstTradingDay(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	one := testPrice(1)
	input := []market.Bar{
		{Symbol: "SNDK", Timestamp: time.Date(2026, 8, 31, 9, 30, 0, 0, location), Open: one, High: one, Low: one, Close: one},
		{Symbol: "SNDK", Timestamp: time.Date(2026, 9, 1, 9, 30, 0, 0, location), Open: one, High: one, Low: one, Close: one},
	}
	weekly, err := aggregateUSCalendar(append([]market.Bar(nil), input...), "1w", location)
	if err != nil || len(weekly) != 1 || weekly[0].Timestamp.In(location).Day() != 31 {
		t.Fatalf("weekly=%+v err=%v", weekly, err)
	}
	monthly, err := aggregateUSCalendar(append([]market.Bar(nil), input...), "1mo", location)
	if err != nil || len(monthly) != 2 {
		t.Fatalf("monthly=%+v err=%v", monthly, err)
	}
}
