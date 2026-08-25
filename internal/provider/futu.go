package provider

import (
	"fmt"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

func isUSIntraday(interval string) bool {
	switch interval {
	case "1m", "3m", "5m", "10m", "15m", "30m", "1h", "2h", "3h", "4h":
		return true
	default:
		return false
	}
}

func isUSHour(interval string) bool {
	return interval == "1h" || interval == "2h" || interval == "3h" || interval == "4h"
}

func filterUSRegularBars(input []market.Bar, location *time.Location) []market.Bar {
	output := make([]market.Bar, 0, len(input))
	for _, bar := range input {
		local := bar.Timestamp.In(location)
		minute := local.Hour()*60 + local.Minute()
		if local.Weekday() == time.Saturday || local.Weekday() == time.Sunday || minute < 9*60+30 || minute >= 16*60 {
			continue
		}
		output = append(output, bar)
	}
	return output
}

func aggregateUSRegularHours(input []market.Bar, target string, location *time.Location) ([]market.Bar, error) {
	hours := map[string]int{"1h": 1, "2h": 2, "3h": 3, "4h": 4}[target]
	if hours == 0 {
		return nil, fmt.Errorf("unsupported US hour interval %q", target)
	}
	input = filterUSRegularBars(input, location)
	market.SortBars(input)
	var output, bucket []market.Bar
	lastKey := ""
	flush := func(anchor time.Time) error {
		if len(bucket) == 0 {
			return nil
		}
		bar, err := mergeBarBucket(bucket, anchor)
		if err != nil {
			return err
		}
		output = append(output, bar)
		bucket = nil
		return nil
	}
	var anchor time.Time
	for _, bar := range input {
		local := bar.Timestamp.In(location)
		open := time.Date(local.Year(), local.Month(), local.Day(), 9, 30, 0, 0, location)
		index := int(local.Sub(open) / (time.Duration(hours) * time.Hour))
		currentAnchor := open.Add(time.Duration(index*hours) * time.Hour)
		key := fmt.Sprintf("%s:%s", bar.Symbol, currentAnchor.Format(time.RFC3339))
		if key != lastKey {
			if err := flush(anchor); err != nil {
				return nil, err
			}
			anchor, lastKey = currentAnchor, key
		}
		bucket = append(bucket, bar)
	}
	if err := flush(anchor); err != nil {
		return nil, err
	}
	return output, nil
}

func aggregateUSCalendar(input []market.Bar, target string, location *time.Location) ([]market.Bar, error) {
	market.SortBars(input)
	var output, bucket []market.Bar
	lastKey := ""
	flush := func() error {
		if len(bucket) == 0 {
			return nil
		}
		bar, err := mergeBarBucket(bucket, bucket[0].Timestamp)
		if err != nil {
			return err
		}
		output = append(output, bar)
		bucket = nil
		return nil
	}
	for _, bar := range input {
		local := bar.Timestamp.In(location)
		var period string
		switch target {
		case "1w":
			year, week := local.ISOWeek()
			period = fmt.Sprintf("%04d-W%02d", year, week)
		case "1mo":
			period = local.Format("2006-01")
		case "1y":
			period = local.Format("2006")
		default:
			return nil, fmt.Errorf("unsupported US calendar interval %q", target)
		}
		key := bar.Symbol + ":" + period
		if key != lastKey {
			if err := flush(); err != nil {
				return nil, err
			}
			lastKey = key
		}
		bucket = append(bucket, bar)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return output, nil
}

func startOfUSCalendarPeriod(value time.Time, target string, location *time.Location) time.Time {
	local := value.In(location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	switch target {
	case "1w":
		daysSinceMonday := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -daysSinceMonday).UTC()
	case "1mo":
		return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, location).UTC()
	case "1y":
		return time.Date(local.Year(), time.January, 1, 0, 0, 0, 0, location).UTC()
	default:
		return value.UTC()
	}
}

func filterRequestedRange(input []market.Bar, from, to time.Time) []market.Bar {
	output := make([]market.Bar, 0, len(input))
	for _, bar := range input {
		if !bar.Timestamp.Before(from) && bar.Timestamp.Before(to) {
			output = append(output, bar)
		}
	}
	return output
}
