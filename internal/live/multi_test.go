package live

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type routedLiveStub struct {
	connected func()
	symbols   chan []string
}

func (s *routedLiveStub) SetOnConnected(fn func()) { s.connected = fn }
func (s *routedLiveStub) Run(ctx context.Context, symbols []string, _ func(market.LiveEvent)) error {
	if s.connected != nil {
		s.connected()
	}
	s.symbols <- append([]string(nil), symbols...)
	<-ctx.Done()
	return ctx.Err()
}

func TestMultiSourceRoutesAndTracksProvidersIndependently(t *testing.T) {
	securities := &routedLiveStub{symbols: make(chan []string, 1)}
	crypto := &routedLiveStub{symbols: make(chan []string, 1)}
	multi := &MultiSource{Routes: []SourceRoute{
		{Name: "longbridge", Source: securities, Accept: func(venue market.Venue) bool { return venue != market.VenueBinance }},
		{Name: "binance", Source: crypto, Accept: func(venue market.Venue) bool { return venue == market.VenueBinance }},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go multi.Run(ctx, []string{"AAPL", "700.HK", "BTCUSDT.BINANCE"}, func(market.LiveEvent) {})
	select {
	case got := <-securities.symbols:
		if !reflect.DeepEqual(got, []string{"AAPL", "700.HK"}) {
			t.Fatalf("securities=%v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("securities source did not start")
	}
	select {
	case got := <-crypto.symbols:
		if !reflect.DeepEqual(got, []string{"BTCUSDT.BINANCE"}) {
			t.Fatalf("crypto=%v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("crypto source did not start")
	}
	status := multi.ProviderStatus()
	for _, name := range []string{"longbridge", "binance"} {
		value, ok := status[name].(map[string]any)
		if !ok || value["state"] != "connected" {
			t.Fatalf("%s status=%v", name, status[name])
		}
	}
}

func TestHubRejectsInvalidMarketSuffix(t *testing.T) {
	if _, err := NewHub(MockSource{}, NopSink{}, []string{"AAPL.UNKNOWN"}); err == nil {
		t.Fatal("invalid watchlist suffix should fail")
	}
}
