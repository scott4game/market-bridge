package live

import (
	"context"
	"sync"
	"testing"
	"time"

	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/market"
	shopdecimal "github.com/shopspring/decimal"
)

type longbridgeLiveStub struct {
	mu          sync.Mutex
	onQuote     func(*lbquote.PushQuote)
	onTrade     func(*lbquote.PushTrade)
	onDepth     func(*lbquote.PushDepth)
	rows        []*lbquote.SecurityQuote
	subTypes    []lbquote.SubType
	subscribed  chan struct{}
	subscribeDo sync.Once
}

func (s *longbridgeLiveStub) OnQuote(fn func(*lbquote.PushQuote)) {
	s.mu.Lock()
	s.onQuote = fn
	s.mu.Unlock()
}
func (s *longbridgeLiveStub) OnTrade(fn func(*lbquote.PushTrade)) {
	s.mu.Lock()
	s.onTrade = fn
	s.mu.Unlock()
}
func (s *longbridgeLiveStub) OnDepth(fn func(*lbquote.PushDepth)) {
	s.mu.Lock()
	s.onDepth = fn
	s.mu.Unlock()
}
func (s *longbridgeLiveStub) Subscribe(_ context.Context, _ []string, subTypes []lbquote.SubType, _ bool) error {
	s.mu.Lock()
	s.subTypes = append([]lbquote.SubType(nil), subTypes...)
	s.mu.Unlock()
	s.subscribeDo.Do(func() { close(s.subscribed) })
	return nil
}
func (s *longbridgeLiveStub) Unsubscribe(context.Context, bool, []string, []lbquote.SubType) error {
	return nil
}
func (s *longbridgeLiveStub) Quote(context.Context, []string) ([]*lbquote.SecurityQuote, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*lbquote.SecurityQuote(nil), s.rows...), nil
}
func (s *longbridgeLiveStub) Close() error { return nil }

func decimalPointer(value string) *shopdecimal.Decimal {
	parsed := shopdecimal.RequireFromString(value)
	return &parsed
}

func waitQuoteEvent(t *testing.T, events <-chan market.LiveEvent) market.LiveEvent {
	t.Helper()
	select {
	case event := <-events:
		if event.Type != market.QuoteEvent || event.Quote == nil {
			t.Fatalf("unexpected event=%#v", event)
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for quote event")
		return market.LiveEvent{}
	}
}

func TestLongbridgeQuoteSnapshotAndSparsePushCalculateChange(t *testing.T) {
	now := time.Now().Unix()
	stub := &longbridgeLiveStub{subscribed: make(chan struct{}), rows: []*lbquote.SecurityQuote{{
		Symbol: "AAPL.US", LastDone: decimalPointer("103"), PrevClose: decimalPointer("100"), Timestamp: now,
	}}}
	source := &LongbridgeSource{Quote: stub, QuoteRefreshInterval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan market.LiveEvent, 4)
	done := make(chan error, 1)
	go func() { done <- source.Run(ctx, []string{"AAPL"}, func(event market.LiveEvent) { events <- event }) }()

	initial := waitQuoteEvent(t, events)
	if initial.Quote.Change.String() != "3.000000" || initial.Quote.ChangePercent.String() != "3.000000" || initial.Quote.TradeSession != market.QuoteSessionRegular {
		t.Fatalf("initial quote=%+v", initial.Quote)
	}
	stub.mu.Lock()
	callback := stub.onQuote
	subTypes := append([]lbquote.SubType(nil), stub.subTypes...)
	stub.mu.Unlock()
	if callback == nil {
		t.Fatal("OnQuote callback was not registered")
	}
	foundQuoteSubscription := false
	for _, subType := range subTypes {
		if subType == lbquote.SubTypeQuote {
			foundQuoteSubscription = true
		}
	}
	if !foundQuoteSubscription {
		t.Fatalf("subscriptions=%v", subTypes)
	}
	callback(&lbquote.PushQuote{Symbol: "AAPL.US", Sequence: 7, LastDone: decimalPointer("97"), Timestamp: now + 1, TradeSession: lbquote.TradeSessionType(lbquote.TradeSessionNormal)})
	updated := waitQuoteEvent(t, events)
	if updated.Quote.Change.String() != "-3.000000" || updated.Quote.ChangePercent.String() != "-3.000000" || updated.Cursor.Sequence != 7 {
		t.Fatalf("updated quote=%+v cursor=%+v", updated.Quote, updated.Cursor)
	}
	callback(&lbquote.PushQuote{Symbol: "AAPL.US", Sequence: 8, Timestamp: now + 2})
	select {
	case event := <-events:
		t.Fatalf("sparse push without last_done emitted event=%#v", event)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not stop")
	}
}

func TestLongbridgeQuoteUsesSessionSpecificPreviousClose(t *testing.T) {
	states := map[string]map[market.QuoteSession]*longbridgeQuoteState{}
	mergeLongbridgeQuoteState(states, "AAPL", market.QuoteSessionPost, decimalPointer("102"), decimalPointer("100"), 10)
	event, ok := longbridgeQuoteEvent("test", "AAPL", market.QuoteSessionPost, 1, states["AAPL"][market.QuoteSessionPost])
	if !ok || event.Quote.ChangePercent == nil || event.Quote.ChangePercent.String() != "2.000000" || event.Quote.TradeSession != market.QuoteSessionPost {
		t.Fatalf("post-market event=%#v ok=%t", event, ok)
	}
	mergeLongbridgeQuoteState(states, "AAPL", market.QuoteSessionPre, decimalPointer("97"), decimalPointer("100"), 11)
	event, ok = longbridgeQuoteEvent("test", "AAPL", market.QuoteSessionPre, 2, states["AAPL"][market.QuoteSessionPre])
	if !ok || event.Quote.ChangePercent == nil || event.Quote.ChangePercent.String() != "-3.000000" || event.Quote.TradeSession != market.QuoteSessionPre {
		t.Fatalf("pre-market event=%#v ok=%t", event, ok)
	}
}

func TestLongbridgeQuoteOmitsChangeWhenPreviousCloseIsZero(t *testing.T) {
	state := &longbridgeQuoteState{last: decimalPointer("10"), prevClose: decimalPointer("0"), timestamp: 1}
	event, ok := longbridgeQuoteEvent("test", "AAPL", market.QuoteSessionRegular, 1, state)
	if !ok || event.Quote.Change != nil || event.Quote.ChangePercent != nil {
		t.Fatalf("event=%#v ok=%t", event, ok)
	}
}
