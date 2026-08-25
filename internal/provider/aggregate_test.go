package provider

import (
	"math"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

func TestAggregateBarsRejectsTurnoverOverflow(t *testing.T) {
	a := market.Decimal(math.MaxInt64)
	b := market.Decimal(1)
	_, err := aggregateBars([]market.Bar{
		{Symbol: "BTCUSDT.BINANCE", Timestamp: time.Unix(0, 0), Turnover: &a},
		{Symbol: "BTCUSDT.BINANCE", Timestamp: time.Unix(60, 0), Turnover: &b},
	}, "3m", 3, time.UTC)
	if err == nil {
		t.Fatal("expected turnover overflow")
	}
}
