package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type ClickHouseSink struct {
	url, database, user, password string
	http                          *http.Client
	queue                         chan market.LiveEvent
}

func NewClickHouseSink(ctx context.Context, url, database, user, password string) (*ClickHouseSink, error) {
	if !validIdentifier(database) {
		return nil, fmt.Errorf("invalid ClickHouse database %q", database)
	}
	s := &ClickHouseSink{url: strings.TrimRight(url, "/"), database: database, user: user, password: password, http: &http.Client{Timeout: 15 * time.Second}, queue: make(chan market.LiveEvent, 8192)}
	for _, q := range schema(database) {
		if err := s.exec(ctx, q); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *ClickHouseSink) Write(ctx context.Context, event market.LiveEvent) error {
	select {
	case s.queue <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return errors.New("clickhouse queue full")
	}
}
func (s *ClickHouseSink) Run(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	batch := make([]market.LiveEvent, 0, 512)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		_ = s.insert(ctx, batch)
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case e := <-s.queue:
			batch = append(batch, e)
			if len(batch) >= 512 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
func (s *ClickHouseSink) insert(ctx context.Context, events []market.LiveEvent) error {
	groups := map[market.EventType][]market.LiveEvent{}
	for _, e := range events {
		groups[e.Type] = append(groups[e.Type], e)
	}
	for typ, items := range groups {
		var table string
		switch typ {
		case market.BarEvent:
			table = s.database + ".bars"
		case market.TradeEvent:
			table = s.database + ".trades"
		case market.DepthEvent:
			table = s.database + ".depth"
		default:
			continue
		}
		var body bytes.Buffer
		fmt.Fprintf(&body, "INSERT INTO %s FORMAT JSONEachRow\n", table)
		for _, e := range items {
			var row any
			if e.Bar != nil {
				row = map[string]any{"symbol": e.Symbol, "timestamp": e.Bar.Timestamp.UTC().Format("2006-01-02 15:04:05.000"), "sequence": e.Cursor.Sequence, "stream_epoch": e.Cursor.StreamEpoch, "open": e.Bar.Open.String(), "high": e.Bar.High.String(), "low": e.Bar.Low.String(), "close": e.Bar.Close.String(), "volume": e.Bar.Volume, "turnover": e.Bar.Turnover, "completed": e.Bar.Completed, "source": e.Bar.Source}
			} else {
				raw := e.Trade
				if typ == market.DepthEvent {
					raw = e.Depth
				}
				row = map[string]any{"symbol": e.Symbol, "timestamp": e.Timestamp.UTC().Format("2006-01-02 15:04:05.000"), "sequence": e.Cursor.Sequence, "stream_epoch": e.Cursor.StreamEpoch, "payload": string(raw)}
			}
			b, _ := json.Marshal(row)
			body.Write(b)
			body.WriteByte('\n')
		}
		if err := s.exec(ctx, body.String()); err != nil {
			return err
		}
	}
	return nil
}
func (s *ClickHouseSink) exec(ctx context.Context, query string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, strings.NewReader(query))
	if err != nil {
		return err
	}
	if s.user != "" {
		req.Header.Set("X-ClickHouse-User", s.user)
	}
	if s.password != "" {
		req.Header.Set("X-ClickHouse-Key", s.password)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("clickhouse returned %d: %s", resp.StatusCode, b)
	}
	return nil
}

func schema(db string) []string {
	return []string{
		`CREATE DATABASE IF NOT EXISTS ` + db,
		`CREATE TABLE IF NOT EXISTS ` + db + `.bars (symbol LowCardinality(String), timestamp DateTime64(3, 'UTC'), sequence Int64, stream_epoch String, open Decimal64(6), high Decimal64(6), low Decimal64(6), close Decimal64(6), volume Int64, turnover Nullable(Decimal64(6)), completed Bool, source LowCardinality(String)) ENGINE = ReplacingMergeTree(sequence) ORDER BY (symbol, timestamp, stream_epoch) TTL timestamp + INTERVAL 1 YEAR DELETE`,
		`CREATE TABLE IF NOT EXISTS ` + db + `.trades (symbol LowCardinality(String), timestamp DateTime64(3, 'UTC'), sequence Int64, stream_epoch String, payload String) ENGINE = MergeTree ORDER BY (symbol, timestamp, stream_epoch, sequence) TTL timestamp + INTERVAL 7 DAY DELETE`,
		`CREATE TABLE IF NOT EXISTS ` + db + `.depth (symbol LowCardinality(String), timestamp DateTime64(3, 'UTC'), sequence Int64, stream_epoch String, payload String) ENGINE = MergeTree ORDER BY (symbol, timestamp, stream_epoch, sequence) TTL timestamp + INTERVAL 7 DAY DELETE`,
	}
}
func validIdentifier(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
