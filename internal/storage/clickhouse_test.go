package storage

import (
	"context"
	"encoding/json"
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
	if _, err := sink.QueryBars(ctx, market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: event.Bar.Timestamp, To: event.Bar.Timestamp.Add(time.Minute), Session: market.RegularSession, Adjustment: market.Raw}); err != nil {
		t.Fatal(err)
	}
	daily := *event.Bar
	daily.Symbol = "600519.SH"
	daily.Source = "tushare"
	if err := sink.WriteBars(ctx, "1d", market.ForwardAdjusted, []market.Bar{daily}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.QueryBars(ctx, market.DatasetSpec{Symbols: []string{"600519.SH"}, Interval: "1d", From: daily.Timestamp, To: daily.Timestamp.Add(24 * time.Hour), Session: market.RegularSession, Adjustment: market.ForwardAdjusted}); err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	joined := strings.Join(queries, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "TTL timestamp + INTERVAL 1 YEAR") || !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS market.kline_1m") || !strings.Contains(joined, "PARTITION BY (market, toYYYYMM(timestamp))") || !strings.Contains(joined, "INSERT INTO market.bars") || !strings.Contains(joined, "123.450000") || !strings.Contains(joined, `"adjustment":"raw"`) || !strings.Contains(joined, `"interval":"1d"`) || !strings.Contains(joined, "interval='1d'") {
		t.Fatalf("missing schema or insert: %s", joined)
	}
}

func TestClickHouseWriteBarsLimitsPartitionsPerInsert(t *testing.T) {
	var mu sync.Mutex
	var inserts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.HasPrefix(string(body), "INSERT INTO market.kline_1m FORMAT JSONEachRow\n") {
			mu.Lock()
			inserts = append(inserts, string(body))
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink, err := NewClickHouseSink(context.Background(), srv.URL, "market", "market", "secret")
	if err != nil {
		t.Fatal(err)
	}
	price := market.DecimalFromFloat(10)
	bars := make([]market.Bar, 0, 120)
	for month := 0; month < 60; month++ {
		for _, symbol := range []string{"AAPL", "600519.SH"} {
			bars = append(bars, market.Bar{
				Symbol: symbol, Timestamp: time.Date(2021, 1, 1, 14, 30, 0, 0, time.UTC).AddDate(0, month, 0),
				Open: price, High: price, Low: price, Close: price, Volume: 1,
				Session: market.RegularSession, Source: "test", Completed: true,
			})
		}
	}
	if err := sink.WriteBars(context.Background(), "1d", market.Raw, bars, 1); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(inserts) != 2 {
		t.Fatalf("insert count=%d, want 2", len(inserts))
	}
	for index, insert := range inserts {
		partitions := map[string]struct{}{}
		for _, line := range strings.Split(strings.TrimSpace(insert), "\n")[1:] {
			var row struct {
				Market    string `json:"market"`
				Timestamp string `json:"timestamp"`
			}
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatalf("decode insert %d: %v", index, err)
			}
			partitions[row.Market+":"+row.Timestamp[:7]] = struct{}{}
		}
		if len(partitions) > 90 {
			t.Fatalf("insert %d spans %d partitions", index, len(partitions))
		}
	}
}
