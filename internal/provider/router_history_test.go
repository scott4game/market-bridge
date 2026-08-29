package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type historyPolicyStub struct {
	calls  []market.DatasetSpec
	failAt int
}

func (s *historyPolicyStub) Name() string        { return "massive" }
func (s *historyPolicyStub) DataVersion() string { return "test-v1" }
func (s *historyPolicyStub) Bars(_ context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	s.calls = append(s.calls, spec)
	if s.failAt > 0 && len(s.calls) == s.failAt {
		return nil, errors.New("permission denied")
	}
	price := market.DecimalFromFloat(float64(len(s.calls)))
	return []market.Bar{{Symbol: spec.Symbols[0], Timestamp: spec.From, Open: price, High: price, Low: price, Close: price, Session: spec.Session, Source: "massive", Completed: true}}, nil
}

func TestRouterChunksHistoryAndCoolsDownAfterFailure(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	stub := &historyPolicyStub{failAt: 3}
	router := &Router{US: stub, HistoryMaxYears: map[string]int{"massive": 5}, HistoryCooldown: 10 * time.Minute, Now: func() time.Time { return now }}
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1d", From: now.AddDate(-8, 0, 0), To: now, Session: market.RegularSession, Adjustment: market.SplitAdjusted}

	bars, err := router.Bars(context.Background(), spec)
	if err == nil || len(bars) != 2 || len(stub.calls) != 3 {
		t.Fatalf("first fetch bars=%d calls=%d err=%v", len(bars), len(stub.calls), err)
	}
	if got := stub.calls[0].To.Sub(stub.calls[0].From); got < 365*24*time.Hour || got > 366*24*time.Hour {
		t.Fatalf("first chunk span=%v", got)
	}
	if !stub.calls[len(stub.calls)-1].From.Equal(now.AddDate(-3, 0, 0)) {
		t.Fatalf("failed chunk starts at %v", stub.calls[len(stub.calls)-1].From)
	}

	bars, err = router.Bars(context.Background(), spec)
	if err == nil || len(bars) != 2 || len(stub.calls) != 3 {
		t.Fatalf("cooldown bars=%d calls=%d err=%v", len(bars), len(stub.calls), err)
	}

	now = now.Add(10*time.Minute + time.Second)
	stub.failAt = 0
	bars, err = router.Bars(context.Background(), spec)
	if err != nil || len(bars) != 5 || len(stub.calls) != 8 {
		t.Fatalf("retry bars=%d calls=%d err=%v", len(bars), len(stub.calls), err)
	}
}

func TestRouterHistoryCooldownIsolatedByInterval(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	stub := &historyPolicyStub{failAt: 1}
	router := &Router{US: stub, HistoryMaxYears: map[string]int{"massive": 2}, Now: func() time.Time { return now }}
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1d", From: now.AddDate(-2, 0, 0), To: now, Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	_, _ = router.Bars(context.Background(), spec)
	stub.failAt = 0
	spec.Interval = "1w"
	if bars, err := router.Bars(context.Background(), spec); err != nil || len(bars) != 2 || len(stub.calls) != 3 {
		t.Fatalf("isolated fetch bars=%d calls=%d err=%v", len(bars), len(stub.calls), err)
	}
}

func TestRouterLeavesSubHourHistoryUnchanged(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	stub := &historyPolicyStub{}
	router := &Router{US: stub, HistoryMaxYears: map[string]int{"massive": 5}, Now: func() time.Time { return now }}
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "30m", From: now.AddDate(-5, 0, 0), To: now, Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	if _, err := router.Bars(context.Background(), spec); err != nil || len(stub.calls) != 1 {
		t.Fatalf("calls=%d err=%v", len(stub.calls), err)
	}
}
