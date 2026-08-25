package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

func TestClickHouseSchemaAndBarInsert(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		queries = append(queries, string(b))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	sink, err := NewClickHouseSink(ctx, srv.URL, "market", "market", "secret")
	if err != nil {
		t.Fatal(err)
	}
	go sink.Run(ctx)
	price := market.DecimalFromFloat(123.45)
	event := market.LiveEvent{Type: market.BarEvent, Symbol: "AAPL", Timestamp: time.Now().UTC(), Cursor: market.LiveCursor{StreamEpoch: "test", EventType: market.BarEvent, Symbol: "AAPL", Sequence: 1}, Bar: &market.Bar{Symbol: "AAPL", Timestamp: time.Date(2025, 1, 2, 14, 30, 0, 0, time.UTC), Open: price, High: price, Low: price, Close: price, Volume: 10, Source: "test", Completed: true}}
	if err := sink.Write(ctx, event); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	joined := strings.Join(queries, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "TTL timestamp + INTERVAL 1 YEAR") || !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS market.kline_1m") || !strings.Contains(joined, "PARTITION BY (market, toDate(timestamp))") || !strings.Contains(joined, "INSERT INTO market.bars") || !strings.Contains(joined, "123.450000") {
		t.Fatalf("missing schema or insert: %s", joined)
	}
}
