package provider

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openapi "github.com/longbridge/openapi-go"
	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/market"
	decimal "github.com/shopspring/decimal"
)

type tailStub struct {
	calls atomic.Int32
	rows  []*lbquote.Candlestick
	err   error
}

func (s *tailStub) HistoryCandlesticksByOffset(_ context.Context, symbol string, period lbquote.Period, adjust lbquote.AdjustType, forward bool, cursor *time.Time, count int32, opts ...lbquote.CandlestickRequestOption) ([]*lbquote.Candlestick, error) {
	s.calls.Add(1)
	if symbol != "AAPL.US" || period != lbquote.PeriodOneMinute || adjust != lbquote.AdjustTypeNo || forward || count > 1000 || cursor.Location().String() != "America/New_York" || len(opts) != 1 {
		return nil, errors.New("wrong upstream request")
	}
	return s.rows, s.err
}
func tailTime(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }
func tailCandle(ts time.Time, price int64) *lbquote.Candlestick {
	v := decimal.NewFromInt(price)
	return &lbquote.Candlestick{Timestamp: ts.Unix(), Open: &v, High: &v, Low: &v, Close: &v, Volume: 10, Turnover: &v}
}
func tailSpec(now time.Time, interval string) market.DatasetSpec {
	return market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: interval, Session: market.RegularSession, Adjustment: market.Raw, From: now.Add(-8 * time.Hour), To: now}
}
func TestUSTailFullHourBucketAndSharedMinuteCache(t *testing.T) {
	now := tailTime("2026-09-23T17:10:30Z") // 13:10 New York
	stub := &tailStub{}
	for i := 0; i <= 220; i++ {
		stub.rows = append(stub.rows, tailCandle(tailTime("2026-09-23T13:30:00Z").Add(time.Duration(i)*time.Minute), int64(i+1)))
	}
	p := &USTail{Quote: stub, Now: func() time.Time { return now }}
	bars, meta, err := p.Bars(context.Background(), tailSpec(now, "4h"))
	if err != nil || len(bars) != 1 {
		t.Fatalf("bars=%v err=%v", bars, err)
	}
	b := bars[0]
	if b.Timestamp != tailTime("2026-09-23T13:30:00Z") || b.Volume != 2210 || b.Open != market.DecimalFromFloat(1) || b.Close != market.DecimalFromFloat(221) || b.Completed || meta.Status != "fresh" {
		t.Fatalf("bar=%+v meta=%+v", b, meta)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, e := p.Bars(context.Background(), tailSpec(now, "1m"))
			if e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if stub.calls.Load() != 1 {
		t.Fatalf("calls=%d", stub.calls.Load())
	}
}
func TestUSTailSparseMinutesUseTimeBuckets(t *testing.T) {
	now := tailTime("2026-09-23T14:08:30Z")
	stub := &tailStub{rows: []*lbquote.Candlestick{tailCandle(now.Truncate(time.Hour), 1), tailCandle(now.Truncate(time.Hour).Add(4*time.Minute), 2), tailCandle(now.Truncate(time.Hour).Add(8*time.Minute), 3)}}
	p := &USTail{Quote: stub, Now: func() time.Time { return now }}
	bars, _, err := p.Bars(context.Background(), tailSpec(now, "3m"))
	if err != nil || len(bars) != 3 || bars[0].Timestamp.Minute() != 0 || bars[1].Timestamp.Minute() != 3 || bars[2].Timestamp.Minute() != 6 || bars[2].Completed {
		t.Fatalf("bars=%+v err=%v", bars, err)
	}
}
func TestUSTailExtendedFiltersOvernight(t *testing.T) {
	now := tailTime("2026-09-23T08:10:30Z")
	stub := &tailStub{rows: []*lbquote.Candlestick{tailCandle(tailTime("2026-09-23T07:59:00Z"), 1), tailCandle(tailTime("2026-09-23T08:00:00Z"), 2)}}
	p := &USTail{Quote: stub, Now: func() time.Time { return now }}
	spec := tailSpec(now, "1m")
	spec.Session = market.ExtendedSession
	bars, _, err := p.Bars(context.Background(), spec)
	if err != nil || len(bars) != 1 || bars[0].Timestamp.Hour() != 8 {
		t.Fatalf("bars=%v err=%v", bars, err)
	}
}
func TestUSTailFailureBackoffAndRecovery(t *testing.T) {
	now := tailTime("2026-09-23T14:08:30Z")
	stub := &tailStub{err: errors.New("no permission")}
	p := &USTail{Quote: stub, Now: func() time.Time { return now }}
	for i := 0; i < 2; i++ {
		_, meta, err := p.Bars(context.Background(), tailSpec(now, "1m"))
		if err == nil || meta.Status != "degraded" {
			t.Fatal("missing degradation")
		}
	}
	if stub.calls.Load() != 1 {
		t.Fatal("failure not cached")
	}
	now = now.Add(6 * time.Second)
	stub.err = nil
	stub.rows = []*lbquote.Candlestick{tailCandle(now.Truncate(time.Minute), 1)}
	bars, meta, err := p.Bars(context.Background(), tailSpec(now, "1m"))
	if err != nil || len(bars) != 1 || meta.Status != "fresh" || stub.calls.Load() != 2 {
		t.Fatalf("recovery %v %v", bars, err)
	}
}
func TestUSTailBucketDSTAndOldQueries(t *testing.T) {
	for _, ts := range []string{"2026-01-05T16:00:00Z", "2026-07-06T15:00:00Z"} {
		got := TailBucket(tailTime(ts), "4h", market.RegularSession)
		loc, _ := time.LoadLocation("America/New_York")
		if got.In(loc).Hour() != 9 || got.In(loc).Minute() != 30 {
			t.Fatal(got)
		}
	}
	now := tailTime("2026-09-23T14:08:30Z")
	spec := tailSpec(now, "1m")
	spec.To = now.Add(-time.Hour)
	p := &USTail{Now: func() time.Time { return now }}
	bars, meta, err := p.Bars(context.Background(), spec)
	if bars != nil || meta != nil || err != nil {
		t.Fatal("old query touched tail")
	}
}

type halfDayTailStub struct{ tailStub }

func (s *halfDayTailStub) TradingDays(_ context.Context, _ openapi.Market, from, to *time.Time) (*lbquote.MarketTradingDay, error) {
	return &lbquote.MarketTradingDay{HalfTradeDay: []time.Time{*from}}, nil
}
func TestUSTailHalfDayCompletesAtEarlyClose(t *testing.T) {
	now := tailTime("2026-11-27T18:05:00Z")
	stub := &halfDayTailStub{tailStub: tailStub{rows: []*lbquote.Candlestick{tailCandle(tailTime("2026-11-27T14:30:00Z"), 1), tailCandle(tailTime("2026-11-27T17:59:00Z"), 2)}}}
	p := &USTail{Quote: stub, Now: func() time.Time { return now }}
	bars, _, err := p.Bars(context.Background(), tailSpec(now, "4h"))
	if err != nil || len(bars) != 1 || !bars[0].Completed || bars[0].Volume != 20 {
		t.Fatalf("bars=%+v err=%v", bars, err)
	}
}
func TestUSTailRequestToInsideBucketDoesNotTruncateOHLC(t *testing.T) {
	now := tailTime("2026-09-23T14:29:30Z")
	stub := &tailStub{rows: []*lbquote.Candlestick{tailCandle(tailTime("2026-09-23T14:00:00Z"), 1), tailCandle(tailTime("2026-09-23T14:28:00Z"), 2)}}
	p := &USTail{Quote: stub, Now: func() time.Time { return now }}
	spec := tailSpec(now, "30m")
	spec.To = now.Add(-10 * time.Minute)
	bars, _, err := p.Bars(context.Background(), spec)
	if err != nil || len(bars) != 1 || bars[0].Volume != 20 || bars[0].Completed {
		t.Fatalf("bars=%+v err=%v", bars, err)
	}
}

func TestUSTailConcurrentColdRequestsCoalesce(t *testing.T) {
	now := tailTime("2026-09-23T14:08:30Z")
	stub := &tailStub{rows: []*lbquote.Candlestick{tailCandle(now.Truncate(time.Minute), 1)}}
	p := &USTail{Quote: stub, Now: func() time.Time { return now }}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			bars, _, err := p.Bars(context.Background(), tailSpec(now, "1m"))
			if err != nil || len(bars) != 1 {
				t.Errorf("bars=%v err=%v", bars, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if stub.calls.Load() != 1 {
		t.Fatalf("upstream requests=%d", stub.calls.Load())
	}
}
