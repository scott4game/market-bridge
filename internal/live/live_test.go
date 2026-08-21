package live

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/scott4game/market-bridge/internal/market"
)

func TestMockHubWebSocket(t *testing.T) {
	hub, err := NewHub(MockSource{}, NopSink{}, []string{"AAPL"})
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
