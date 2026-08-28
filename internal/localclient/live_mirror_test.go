package localclient

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/market"
)

type recordingLiveSink struct {
	events []market.LiveEvent
}

func (s *recordingLiveSink) Write(_ context.Context, event market.LiveEvent) error {
	s.events = append(s.events, event)
	return nil
}

func TestLiveProxyPersistsOnlyActiveOnDemandSymbols(t *testing.T) {
	sink := &recordingLiveSink{}
	proxy := NewLiveProxy(config.Client{ClickHouseCompletedBarsOnly: true}, sink)
	subscriber := &liveSubscriber{symbols: map[string]struct{}{"NVDA": {}}, events: map[market.EventType]struct{}{market.BarEvent: {}}, queue: make(chan []byte, 1)}
	proxy.subs[subscriber] = struct{}{}

	if got := proxy.symbols(); !reflect.DeepEqual(got, []string{"NVDA"}) {
		t.Fatalf("on-demand symbols=%v", got)
	}

	completed := market.LiveEvent{
		Type: market.BarEvent, Symbol: "NVDA", Timestamp: time.Now().UTC(),
		Bar: &market.Bar{Symbol: "NVDA", Timestamp: time.Now().UTC(), Completed: true},
	}
	raw, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	proxy.handleEvent(context.Background(), raw)
	if len(sink.events) != 1 || sink.events[0].Symbol != "NVDA" {
		t.Fatalf("on-demand events=%#v", sink.events)
	}

	incomplete := completed
	incomplete.Bar = &market.Bar{Symbol: "NVDA", Timestamp: time.Now().UTC(), Completed: false}
	raw, _ = json.Marshal(incomplete)
	proxy.handleEvent(context.Background(), raw)
	if len(sink.events) != 1 {
		t.Fatalf("incomplete bar should not be persisted: %#v", sink.events)
	}

	delete(proxy.subs, subscriber)
	raw, _ = json.Marshal(completed)
	proxy.handleEvent(context.Background(), raw)
	if len(sink.events) != 1 {
		t.Fatalf("inactive symbol should not be persisted: %#v", sink.events)
	}
}

func TestLiveProxyFiltersQuoteEventsAndDoesNotPersistThem(t *testing.T) {
	sink := &recordingLiveSink{}
	proxy := NewLiveProxy(config.Client{}, sink)
	barSubscriber := &liveSubscriber{symbols: map[string]struct{}{"AAPL": {}}, events: map[market.EventType]struct{}{market.BarEvent: {}}, queue: make(chan []byte, 1)}
	quoteSubscriber := &liveSubscriber{symbols: map[string]struct{}{"AAPL": {}}, events: map[market.EventType]struct{}{market.QuoteEvent: {}}, queue: make(chan []byte, 1)}
	proxy.subs[barSubscriber] = struct{}{}
	proxy.subs[quoteSubscriber] = struct{}{}
	price := market.DecimalFromFloat(103)
	event := market.LiveEvent{Type: market.QuoteEvent, Symbol: "AAPL", Timestamp: time.Now().UTC(), Quote: &market.Quote{LastDone: price, TradeSession: market.QuoteSessionRegular, Source: "longbridge"}}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	proxy.handleEvent(context.Background(), raw)
	if len(sink.events) != 0 {
		t.Fatalf("quote event was persisted: %#v", sink.events)
	}
	select {
	case <-quoteSubscriber.queue:
	default:
		t.Fatal("quote subscriber did not receive quote")
	}
	select {
	case message := <-barSubscriber.queue:
		t.Fatalf("bar-only subscriber received quote: %s", message)
	default:
	}
	if got := proxy.events(); !reflect.DeepEqual(got, []market.EventType{market.BarEvent, market.QuoteEvent}) {
		t.Fatalf("events=%v", got)
	}
}

func TestLiveProxyStatusFramesAreOptIn(t *testing.T) {
	proxy := NewLiveProxy(config.Client{})
	srv := httptest.NewServer(proxy)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"symbols":["SNDK"],"status":true}`)); err != nil {
		t.Fatal(err)
	}
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Type  string `json:"type"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &status); err != nil || status.Type != "status" || status.State != "connecting" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}
