package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
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
		if err := s.insert(ctx, batch); err != nil && ctx.Err() == nil {
			log.Printf("write ClickHouse batch: %v", err)
		}
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
		if typ == market.BarEvent {
			bars := make([]market.Bar, 0, len(items))
			for _, event := range items {
				if event.Bar != nil && event.Bar.Completed {
					bars = append(bars, *event.Bar)
				}
			}
			if len(bars) > 0 {
				if err := s.WriteBars(ctx, market.Raw, bars, uint64(time.Now().UnixMilli())); err != nil {
					return err
				}
			}
		}
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
				row = map[string]any{"symbol": e.Symbol, "timestamp": e.Bar.Timestamp.UTC().Format("2006-01-02 15:04:05.000"), "sequence": e.Cursor.Sequence, "stream_epoch": e.Cursor.StreamEpoch, "open": e.Bar.Open.String(), "high": e.Bar.High.String(), "low": e.Bar.Low.String(), "close": e.Bar.Close.String(), "volume": e.Bar.Volume, "volume_decimal": e.Bar.VolumeDecimal, "turnover": e.Bar.Turnover, "completed": e.Bar.Completed, "source": e.Bar.Source}
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

// WriteBars writes completed one-minute bars in batches. The supplied version
// makes corrected rows replace older rows with the same logical key.
func (s *ClickHouseSink) WriteBars(ctx context.Context, adjustment market.AdjustmentMode, bars []market.Bar, version uint64) error {
	if len(bars) == 0 {
		return nil
	}
	if adjustment == "" || adjustment == market.AutoAdjusted {
		adjustment = market.Raw
	}
	const batchSize = 5000
	for start := 0; start < len(bars); start += batchSize {
		end := start + batchSize
		if end > len(bars) {
			end = len(bars)
		}
		var body bytes.Buffer
		fmt.Fprintf(&body, "INSERT INTO %s.kline_1m FORMAT JSONEachRow\n", s.database)
		for _, bar := range bars[start:end] {
			if !bar.Completed {
				continue
			}
			_, venue, err := market.NormalizeSymbol(bar.Symbol)
			if err != nil {
				return err
			}
			row := map[string]any{
				"market": string(venue), "symbol": bar.Symbol, "interval": "1m",
				"adjustment": string(adjustment), "session": string(bar.Session),
				"timestamp": bar.Timestamp.UTC().Format("2006-01-02 15:04:05.000"),
				"open":      bar.Open.String(), "high": bar.High.String(), "low": bar.Low.String(), "close": bar.Close.String(),
				"volume": bar.Volume, "volume_decimal": bar.VolumeDecimal, "turnover": bar.Turnover,
				"completed": true, "source": bar.Source, "version": version,
			}
			encoded, _ := json.Marshal(row)
			body.Write(encoded)
			body.WriteByte('\n')
		}
		if err := s.exec(ctx, body.String()); err != nil {
			return err
		}
	}
	return nil
}

func (s *ClickHouseSink) QueryBars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	normalized, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	if normalized.Interval != "1m" {
		return nil, fmt.Errorf("ClickHouse canonical store only accepts interval 1m, got %s", normalized.Interval)
	}
	symbols := make([]string, 0, len(normalized.Symbols))
	for _, symbol := range normalized.Symbols {
		symbols = append(symbols, sqlString(symbol))
	}
	query := fmt.Sprintf(`SELECT symbol, timestamp, open, high, low, close, volume, volume_decimal, turnover, session, source, completed
FROM %s.kline_1m FINAL
WHERE symbol IN (%s) AND interval='1m' AND adjustment=%s AND session=%s
  AND timestamp >= fromUnixTimestamp64Milli(%d) AND timestamp < fromUnixTimestamp64Milli(%d)
ORDER BY timestamp, symbol FORMAT JSONEachRow`, s.database, strings.Join(symbols, ","), sqlString(string(normalized.Adjustment)), sqlString(string(normalized.Session)), normalized.From.UnixMilli(), normalized.To.UnixMilli())
	resp, err := s.query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer resp.Close()
	decoder := json.NewDecoder(resp)
	var bars []market.Bar
	for decoder.More() {
		var row struct {
			Symbol        string          `json:"symbol"`
			Timestamp     string          `json:"timestamp"`
			Open          market.Decimal  `json:"open"`
			High          market.Decimal  `json:"high"`
			Low           market.Decimal  `json:"low"`
			Close         market.Decimal  `json:"close"`
			Volume        int64           `json:"volume"`
			VolumeDecimal string          `json:"volume_decimal"`
			Turnover      *market.Decimal `json:"turnover"`
			Session       market.Session  `json:"session"`
			Source        string          `json:"source"`
			Completed     bool            `json:"completed"`
		}
		if err := decoder.Decode(&row); err != nil {
			return nil, err
		}
		ts, err := time.Parse("2006-01-02 15:04:05.000", row.Timestamp)
		if err != nil {
			return nil, err
		}
		bars = append(bars, market.Bar{Symbol: row.Symbol, Timestamp: ts.UTC(), Open: row.Open, High: row.High, Low: row.Low, Close: row.Close, Volume: row.Volume, VolumeDecimal: row.VolumeDecimal, Turnover: row.Turnover, Session: row.Session, Source: row.Source, Completed: row.Completed})
	}
	market.SortBars(bars)
	return bars, nil
}

func (s *ClickHouseSink) Healthy(ctx context.Context) error { return s.exec(ctx, "SELECT 1") }

func (s *ClickHouseSink) CleanupBefore(ctx context.Context, cutoff time.Time) (int, error) {
	query := fmt.Sprintf(`SELECT partition_id, max(max_time) AS max_time FROM system.parts
WHERE active AND database=%s AND table='kline_1m' GROUP BY partition_id FORMAT JSONEachRow`, sqlString(s.database))
	resp, err := s.query(ctx, query)
	if err != nil {
		return 0, err
	}
	defer resp.Close()
	type partition struct{ ID, MaxTime string }
	var expired []string
	decoder := json.NewDecoder(resp)
	for decoder.More() {
		var raw map[string]any
		if err := decoder.Decode(&raw); err != nil {
			return 0, err
		}
		id, _ := raw["partition_id"].(string)
		maxValue, _ := raw["max_time"].(string)
		maxTime, parseErr := time.Parse("2006-01-02", maxValue)
		if parseErr != nil {
			maxTime, parseErr = time.Parse("2006-01-02 15:04:05", maxValue)
		}
		if parseErr == nil && maxTime.UTC().Before(cutoff.UTC()) && safePartitionID(id) {
			expired = append(expired, id)
		}
	}
	sort.Strings(expired)
	for _, id := range expired {
		if err := s.exec(ctx, fmt.Sprintf("ALTER TABLE %s.kline_1m DROP PARTITION ID %s", s.database, sqlString(id))); err != nil {
			return 0, err
		}
	}
	return len(expired), nil
}

func (s *ClickHouseSink) query(ctx context.Context, query string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, strings.NewReader(query))
	if err != nil {
		return nil, err
	}
	if s.user != "" {
		req.Header.Set("X-ClickHouse-User", s.user)
	}
	if s.password != "" {
		req.Header.Set("X-ClickHouse-Key", s.password)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("clickhouse returned %d: %s", resp.StatusCode, b)
	}
	return resp.Body, nil
}

func sqlString(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
func safePartitionID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r == ',' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
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
		`CREATE TABLE IF NOT EXISTS ` + db + `.bars (symbol LowCardinality(String), timestamp DateTime64(3, 'UTC'), sequence Int64, stream_epoch String, open Decimal64(6), high Decimal64(6), low Decimal64(6), close Decimal64(6), volume Int64, volume_decimal String DEFAULT '', turnover Nullable(Decimal64(6)), completed Bool, source LowCardinality(String)) ENGINE = ReplacingMergeTree(sequence) ORDER BY (symbol, timestamp, stream_epoch) TTL timestamp + INTERVAL 1 YEAR DELETE`,
		`ALTER TABLE ` + db + `.bars ADD COLUMN IF NOT EXISTS volume_decimal String DEFAULT '' AFTER volume`,
		`CREATE TABLE IF NOT EXISTS ` + db + `.kline_1m (market LowCardinality(String), symbol LowCardinality(String), interval LowCardinality(String), adjustment LowCardinality(String), session LowCardinality(String), timestamp DateTime64(3, 'UTC'), open Decimal64(6), high Decimal64(6), low Decimal64(6), close Decimal64(6), volume Int64, volume_decimal String DEFAULT '', turnover Nullable(Decimal64(6)), completed Bool, source LowCardinality(String), version UInt64) ENGINE = ReplacingMergeTree(version) PARTITION BY (market, toDate(timestamp)) ORDER BY (market, interval, adjustment, session, symbol, timestamp)`,
		`CREATE TABLE IF NOT EXISTS ` + db + `.schema_migrations (version UInt32, applied_at DateTime('UTC')) ENGINE = TinyLog`,
		`INSERT INTO ` + db + `.kline_1m SELECT multiIf(endsWith(symbol,'.HK'),'HK',endsWith(symbol,'.SH'),'SH',endsWith(symbol,'.SZ'),'SZ',endsWith(symbol,'.BINANCE'),'BINANCE','US'), symbol, '1m', 'raw', 'regular', timestamp, open, high, low, close, volume, volume_decimal, turnover, completed, source, toUInt64(greatest(sequence,0)) FROM ` + db + `.bars WHERE completed AND timestamp >= now() - INTERVAL 365 DAY AND NOT EXISTS (SELECT 1 FROM ` + db + `.schema_migrations WHERE version=2)`,
		`INSERT INTO ` + db + `.schema_migrations SELECT 2, now() WHERE NOT EXISTS (SELECT 1 FROM ` + db + `.schema_migrations WHERE version=2)`,
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
