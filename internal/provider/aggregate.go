package provider

import (
	"fmt"
	"math"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
	shopdecimal "github.com/shopspring/decimal"
)

func aggregateBars(input []market.Bar, target string, factor int, location *time.Location) ([]market.Bar, error) {
	if factor <= 1 {
		return input, nil
	}
	if location == nil {
		location = time.UTC
	}
	market.SortBars(input)
	var output []market.Bar
	var bucket []market.Bar
	lastSymbol, lastKey := "", ""
	flush := func() error {
		if len(bucket) == 0 {
			return nil
		}
		bar := bucket[0]
		bar.Close = bucket[len(bucket)-1].Close
		bar.Completed = true
		bar.Volume = 0
		var volumeDecimal shopdecimal.Decimal
		hasVolumeDecimal := false
		var turnover market.Decimal
		hasTurnover := false
		for _, item := range bucket {
			if item.High > bar.High {
				bar.High = item.High
			}
			if item.Low < bar.Low {
				bar.Low = item.Low
			}
			bar.Volume += item.Volume
			if item.VolumeDecimal != "" {
				if value, err := shopdecimal.NewFromString(item.VolumeDecimal); err == nil {
					volumeDecimal = volumeDecimal.Add(value)
					hasVolumeDecimal = true
				}
			}
			if item.Turnover != nil {
				if (*item.Turnover > 0 && turnover > market.Decimal(math.MaxInt64)-*item.Turnover) || (*item.Turnover < 0 && turnover < market.Decimal(math.MinInt64)-*item.Turnover) {
					return fmt.Errorf("turnover overflow while aggregating %s", item.Symbol)
				}
				turnover += *item.Turnover
				hasTurnover = true
			}
		}
		if hasTurnover {
			bar.Turnover = &turnover
		} else {
			bar.Turnover = nil
		}
		if hasVolumeDecimal {
			bar.VolumeDecimal = volumeDecimal.String()
		} else {
			bar.VolumeDecimal = ""
		}
		output = append(output, bar)
		bucket = nil
		return nil
	}
	for _, bar := range input {
		local := bar.Timestamp.In(location)
		key := fmt.Sprintf("%04d-%03d", local.Year(), local.YearDay())
		if target == "1y" {
			key = fmt.Sprintf("%04d", local.Year())
		}
		if bar.Symbol != lastSymbol || key != lastKey || len(bucket) == factor {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		lastSymbol, lastKey = bar.Symbol, key
		bucket = append(bucket, bar)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return output, nil
}
