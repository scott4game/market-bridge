package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/scott4game/market-bridge/internal/market"
)

type dynamicSourceStub struct {
	runs chan []string
}

func (s *dynamicSourceStub) Run(ctx context.Context, symbols []string, _ func(market.LiveEvent)) error {
	s.runs <- append([]string(nil), symbols...)
	<-ctx.Done()
	return ctx.Err()
}

func waitDynamicSymbols(t *testing.T, runs <-chan []string, want []string) {
	t.Helper()
	select {
	case got := <-runs:
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("dynamic symbols=%v, want %v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for dynamic symbols %v", want)
	}
}

func dialLiveSubscriber(t *testing.T, ctx context.Context, wsURL string, symbols ...string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(map[string]any{"symbols": symbols, "events": []string{"bar"}})
	if err := conn.Write(ctx, websocket.MessageText, request); err != nil {
		conn.CloseNow()
		t.Fatal(err)
	}
	return conn
}

func TestMockHubWebSocket(t *testing.T) {
	hub, err := NewHub(MockSource{}, NopSink{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)
	srv := httptest.NewServer(hub)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	dialCtx, dialCancel := context.WithTimeout(ctx, 3*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	request, _ := json.Marshal(map[string]any{"symbols": []string{"AAPL"}, "events": []string{"bar"}})
	if err := conn.Write(dialCtx, websocket.MessageText, request); err != nil {
		t.Fatal(err)
	}
	_, raw, err := conn.Read(dialCtx)
	if err != nil {
		t.Fatal(err)
	}
	var event market.LiveEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != market.BarEvent || event.Symbol != "AAPL" || event.Bar == nil {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestHubRejectsCrossOriginWebSocket(t *testing.T) {
	hub, err := NewHub(MockSource{}, NopSink{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(hub)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}}})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("err=%v response=%v", err, response)
	}
}

func TestHubSubscriptionsFollowActiveConnections(t *testing.T) {
	source := &dynamicSourceStub{runs: make(chan []string, 4)}
	hub, err := NewHub(source, NopSink{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)
	srv := httptest.NewServer(hub)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	sndk := dialLiveSubscriber(t, ctx, wsURL, "SNDK")
	waitDynamicSymbols(t, source.runs, []string{"SNDK"})
	aapl := dialLiveSubscriber(t, ctx, wsURL, "AAPL")
	waitDynamicSymbols(t, source.runs, []string{"AAPL", "SNDK"})

	_ = sndk.Close(websocket.StatusNormalClosure, "done")
	waitDynamicSymbols(t, source.runs, []string{"AAPL"})
	_ = aapl.Close(websocket.StatusNormalClosure, "done")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := hub.ProviderStatus()["longbridge"].(map[string]any)
		if status["state"] == "idle" && status["subscribed_symbols"] == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("hub did not become idle after the last subscriber disconnected")
}

func TestHubRejectsProviderCapacityOverflow(t *testing.T) {
	hub, err := NewHub(MockSource{}, NopSink{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(hub)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	symbols := make([]string, maxProviderLiveSymbols+1)
	for i := range symbols {
		symbols[i] = fmt.Sprintf("S%03d", i)
	}
	request, _ := json.Marshal(map[string]any{"symbols": symbols})
	if err := conn.Write(ctx, websocket.MessageText, request); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("capacity close status=%d err=%v", websocket.CloseStatus(err), err)
	}
}
