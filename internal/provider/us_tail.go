package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	openapi "github.com/longbridge/openapi-go"
	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/market"
)

// USTail holds provisional minute candles independently of historical coverage.
// The shared QuoteContext retains responsibility for account-wide SDK throttling.
type USTail struct {
	Quote         LongbridgeHistoryClient
	Window        time.Duration
	Now           func() time.Time
	mu            sync.Mutex
	cache         map[string]tailEntry
	flights       map[string]chan struct{}
	gate          chan struct{}
	next          time.Time
	calendarDate  string
	calendarClose time.Time
	requests      atomic.Uint64
	failures      atomic.Uint64
	cacheHits     atomic.Uint64
}
type tailCalendar interface {
	TradingDays(context.Context, openapi.Market, *time.Time, *time.Time) (*lbquote.MarketTradingDay, error)
}

type tailEntry struct {
	close            time.Time
	observed         time.Time
	bars             []market.Bar
	fetched, expires time.Time
	err              error
}
type TailMetadata struct {
	LatestBarAt time.Time `json:"latest_bar_at,omitempty"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	FetchedAt   time.Time `json:"fetched_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Status      string    `json:"status"`
}

func (p *USTail) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}
func (p *USTail) TailWindow() time.Duration {
	if p.Window > 0 {
		return p.Window
	}
	return 30 * time.Minute
}
func USTailEligible(spec market.DatasetSpec, now time.Time, window time.Duration) bool {
	if !isUSIntraday(spec.Interval) || !spec.To.After(now.Add(-window)) || !spec.From.Before(now) {
		return false
	}
	for _, symbol := range spec.Symbols {
		if v, _ := market.VenueOf(symbol); v == market.VenueUS {
			return true
		}
	}
	return false
}
func (p *USTail) Eligible(spec market.DatasetSpec) bool {
	return USTailEligible(spec, p.now(), p.TailWindow())
}

// TailBucket preserves Massive's wall-clock minute buckets and the existing
// regular-session hour buckets anchored at 09:30 New York time.
func TailBucket(t time.Time, interval string, session market.Session) time.Time {
	loc, _ := time.LoadLocation("America/New_York")
	local := t.In(loc)
	anchor := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	if session == market.RegularSession && isUSHour(interval) {
		anchor = anchor.Add(9*time.Hour + 30*time.Minute)
	}
	step := market.IntervalDuration(interval)
	delta := local.Sub(anchor)
	index := delta / step
	if delta < 0 && delta%step != 0 {
		index--
	}
	return anchor.Add(index * step).UTC()
}
func (p *USTail) Bars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, *TailMetadata, error) {
	now := p.now()
	if !p.Eligible(spec) {
		return nil, nil, nil
	}
	start := TailBucket(now.Add(-p.TailWindow()), spec.Interval, spec.Session)
	if spec.From.After(start) {
		start = TailBucket(spec.From, spec.Interval, spec.Session)
	}
	end := now
	if spec.To.Before(end) {
		end = spec.To
	}
	meta := &TailMetadata{From: start, To: end, Status: "fresh"}
	var out []market.Bar
	var failures error
	for _, symbol := range spec.Symbols {
		if v, _ := market.VenueOf(symbol); v != market.VenueUS {
			continue
		}
		entry := p.minutes(ctx, symbol, spec.Session)
		if meta.FetchedAt.IsZero() || entry.fetched.Before(meta.FetchedAt) {
			meta.FetchedAt = entry.fetched
		}
		if meta.ExpiresAt.IsZero() || entry.expires.Before(meta.ExpiresAt) {
			meta.ExpiresAt = entry.expires
		}
		if entry.err != nil {
			failures = errors.Join(failures, fmt.Errorf("Longbridge tail %s: %w", symbol, entry.err))
			continue
		}
		buckets := map[time.Time][]market.Bar{}
		for _, bar := range entry.bars {
			if bar.Timestamp.Before(start) || !bar.Timestamp.Before(now) {
				continue
			}
			key := TailBucket(bar.Timestamp, spec.Interval, spec.Session)
			buckets[key] = append(buckets[key], bar)
			if bar.Timestamp.After(meta.LatestBarAt) {
				meta.LatestBarAt = bar.Timestamp
			}
		}
		for key, rows := range buckets {
			if key.Before(spec.From) || !key.Before(spec.To) {
				continue
			}
			bar, err := mergeBarBucket(rows, key)
			if err != nil {
				return nil, meta, err
			}
			bucketEnd := key.Add(market.IntervalDuration(spec.Interval))
			if !entry.close.IsZero() && entry.close.Before(bucketEnd) {
				bucketEnd = entry.close
			}
			bar.Completed = !bucketEnd.After(entry.observed)
			out = append(out, bar)
		}
	}
	if failures != nil {
		meta.Status = "degraded"
	}
	market.SortBars(out)
	return out, meta, failures
}
func (p *USTail) minutes(ctx context.Context, symbol string, session market.Session) tailEntry {
	key := symbol + ":" + string(session)
	for {
		p.mu.Lock()
		if p.cache == nil {
			p.cache = map[string]tailEntry{}
			p.flights = map[string]chan struct{}{}
			p.gate = make(chan struct{}, 1)
		}
		now := p.now()
		if entry, ok := p.cache[key]; ok && now.Before(entry.expires) {
			p.cacheHits.Add(1)
			p.mu.Unlock()
			return entry
		}
		if done, ok := p.flights[key]; ok {
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return tailEntry{err: ctx.Err()}
			case <-done:
				continue
			}
		}
		done := make(chan struct{})
		p.flights[key] = done
		for k, e := range p.cache {
			if now.Sub(e.expires) > 5*time.Minute {
				delete(p.cache, k)
			}
		}
		p.mu.Unlock()
		entry := p.fetch(ctx, symbol, session)
		p.mu.Lock()
		p.cache[key] = entry
		delete(p.flights, key)
		close(done)
		p.mu.Unlock()
		return entry
	}
}
func (p *USTail) fetch(ctx context.Context, symbol string, session market.Session) (entry tailEntry) {
	defer func() {
		entry.fetched = p.now()
		ttl := 15 * time.Second
		if entry.err != nil {
			p.failures.Add(1)
			ttl = 5 * time.Second
		}
		entry.expires = entry.fetched.Add(ttl)
		if entry.err == nil {
			boundary := entry.observed.Truncate(time.Minute).Add(time.Minute)
			if boundary.Before(entry.expires) {
				entry.expires = boundary
			}
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case p.gate <- struct{}{}:
	case <-ctx.Done():
		entry.err = ctx.Err()
		return
	}
	defer func() { <-p.gate }()
	if delay := time.Until(p.next); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			entry.err = ctx.Err()
			return
		}
	}
	p.next = time.Now().Add(600 * time.Millisecond)
	if p.Quote == nil {
		entry.err = errors.New("quote context unavailable")
		return
	}
	loc, _ := time.LoadLocation("America/New_York")
	cursor := p.now().Add(time.Minute).In(loc)
	date := cursor.Format("2006-01-02")
	closeHour := 16
	if calendar, ok := p.Quote.(tailCalendar); ok && session == market.RegularSession {
		if p.calendarDate != date {
			days, e := calendar.TradingDays(ctx, openapi.MarketUS, &cursor, &cursor)
			if e != nil {
				entry.err = fmt.Errorf("trading calendar: %w", e)
				return
			}
			if days == nil {
				entry.err = errors.New("missing trading calendar")
				return
			}
			for _, day := range days.HalfTradeDay {
				if day.Format("2006-01-02") == date {
					closeHour = 13
				}
			}
			p.calendarClose = time.Date(cursor.Year(), cursor.Month(), cursor.Day(), closeHour, 0, 0, 0, loc)
			p.calendarDate = date
			timer := time.NewTimer(600 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				entry.err = ctx.Err()
				return
			}
			p.next = time.Now().Add(600 * time.Millisecond)
		}
		entry.close = p.calendarClose
	} else if session == market.RegularSession {
		entry.close = time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 16, 0, 0, 0, loc)
	}

	tradeSession := lbquote.CandlestickTradeSessionAll
	if session == market.RegularSession {
		tradeSession = lbquote.CandlestickTradeSessionNormal
	}
	entry.observed = p.now()
	p.requests.Add(1)
	sticks, err := p.Quote.HistoryCandlesticksByOffset(ctx, symbol+".US", lbquote.PeriodOneMinute, lbquote.AdjustTypeNo, false, &cursor, 1000, lbquote.CandlestickRequestTradeSession(tradeSession))
	if err != nil {
		entry.err = err
		return
	}
	for _, s := range sticks {
		if s == nil || s.Open == nil || s.High == nil || s.Low == nil || s.Close == nil {
			entry.err = errors.New("invalid candle")
			return
		}
		ts := time.Unix(s.Timestamp, 0).UTC()
		local := ts.In(loc)
		minute := local.Hour()*60 + local.Minute()
		if local.Format("2006-01-02") != date {
			continue
		}
		first, last := 240, 1200
		if session == market.RegularSession {
			first, last = 570, 960
			if !entry.close.IsZero() {
				last = entry.close.In(loc).Hour() * 60
			}
		}
		if minute < first || minute >= last || local.Weekday() == time.Saturday || local.Weekday() == time.Sunday {
			continue
		}
		o, e1 := market.DecimalFromString(s.Open.String())
		h, e2 := market.DecimalFromString(s.High.String())
		l, e3 := market.DecimalFromString(s.Low.String())
		c, e4 := market.DecimalFromString(s.Close.String())
		if errors.Join(e1, e2, e3, e4) != nil {
			entry.err = errors.New("invalid candle decimal")
			return
		}
		bar := market.Bar{Symbol: symbol, Timestamp: ts, Open: o, High: h, Low: l, Close: c, Volume: s.Volume, Session: session, Source: "longbridge-tail", Completed: !ts.Add(time.Minute).After(p.now())}
		if s.Turnover != nil {
			v, e := market.DecimalFromString(s.Turnover.String())
			if e != nil {
				entry.err = e
				return
			}
			bar.Turnover = &v
		}
		entry.bars = append(entry.bars, bar)
	}
	entry.bars = deduplicateBars(entry.bars)
	return
}

// Stats reports cumulative tail activity without exposing account credentials.
func (p *USTail) Stats() map[string]uint64 {
	return map[string]uint64{"requests": p.requests.Load(), "failures": p.failures.Load(), "cache_hits": p.cacheHits.Load()}
}
