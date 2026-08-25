package localclient

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

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

func TestLiveProxyMirrorsWatchlistWithoutLocalSubscribers(t *testing.T) {
	sink := &recordingLiveSink{}
	proxy := NewLiveProxy(config.Client{
		MirrorWatchlist:             []string{" nvda ", "AAPL"},
		ClickHouseCompletedBarsOnly: true,
	}, sink)

	if got := proxy.symbols(); !reflect.DeepEqual(got, []string{"AAPL", "NVDA"}) {
		t.Fatalf("mirror symbols=%v", got)
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
		t.Fatalf("mirrored events=%#v", sink.events)
	}

	incomplete := completed
	incomplete.Bar = &market.Bar{Symbol: "NVDA", Timestamp: time.Now().UTC(), Completed: false}
	raw, _ = json.Marshal(incomplete)
	proxy.handleEvent(context.Background(), raw)
	if len(sink.events) != 1 {
		t.Fatalf("incomplete bar should not be persisted: %#v", sink.events)
	}

	notMirrored := completed
	notMirrored.Symbol = "MSFT"
	raw, _ = json.Marshal(notMirrored)
	proxy.handleEvent(context.Background(), raw)
	if len(sink.events) != 1 {
		t.Fatalf("non-watchlist bar should not be persisted: %#v", sink.events)
	}
}
