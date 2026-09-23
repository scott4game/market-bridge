package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/provider"
	decimal "github.com/shopspring/decimal"
)

type serverTailStub struct{ err error }

func (s serverTailStub) HistoryCandlesticksByOffset(_ context.Context, _ string, _ lbquote.Period, _ lbquote.AdjustType, _ bool, cursor *time.Time, _ int32, _ ...lbquote.CandlestickRequestOption) ([]*lbquote.Candlestick, error) {
	v := decimal.NewFromInt(123)
	return []*lbquote.Candlestick{{Timestamp: cursor.Add(-time.Minute).Truncate(time.Minute).Unix(), Open: &v, High: &v, Low: &v, Close: &v, Volume: 100}}, s.err
}
func TestHistoryOverlayDoesNotMutateCachedBase(t *testing.T) {
	now := time.Date(2026, 9, 23, 14, 10, 30, 0, time.UTC)
	h := &HTTP{USTail: &provider.USTail{Quote: serverTailStub{}, Now: func() time.Time { return now }}}
	base := []market.Bar{{Symbol: "AAPL", Timestamp: now.Truncate(time.Minute), Close: market.DecimalFromFloat(90), Volume: 50, Source: "massive"}}
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", Session: market.RegularSession, Adjustment: market.Raw, From: now.Add(-time.Hour), To: now}
	w := httptest.NewRecorder()
	h.writeHistory(w, httptest.NewRequest("POST", "/", nil), spec, map[string]any{"bars": base, "source": "server-redis"})
	var payload struct {
		Bars   []market.Bar
		Tail   provider.TailMetadata
		Source string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Bars) != 1 || payload.Bars[0].Volume != 100 || payload.Bars[0].Completed || base[0].Volume != 50 || payload.Tail.Status != "fresh" {
		t.Fatalf("payload=%+v base=%+v", payload, base)
	}
}
func TestHistoryTailFailureRetainsBaseAndWarns(t *testing.T) {
	now := time.Date(2026, 9, 23, 14, 10, 30, 0, time.UTC)
	h := &HTTP{USTail: &provider.USTail{Quote: serverTailStub{err: errors.New("permission denied")}, Now: func() time.Time { return now }}}
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", Session: market.RegularSession, Adjustment: market.Raw, From: now.Add(-time.Hour), To: now}
	for _, withBase := range []bool{true, false} {
		bars := []market.Bar{}
		if withBase {
			bars = append(bars, market.Bar{Symbol: "AAPL", Timestamp: spec.From})
		}
		w := httptest.NewRecorder()
		h.writeHistory(w, httptest.NewRequest("POST", "/", nil), spec, map[string]any{"bars": bars, "source": "provider"})
		if withBase && w.Code != 200 || !withBase && w.Code != 502 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func TestTailCoverageLeavesDelayedMassiveBucketsRefreshable(t *testing.T) {
	ctx := context.Background()
	catalog, err := OpenHistoryCatalog(t.TempDir() + "/coverage.db")
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	h := &HTTP{HistoryCatalog: catalog, ClickHouse: &redisTestClickHouse{}, USTail: &provider.USTail{}}
	now := time.Now().UTC().Truncate(time.Minute)
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", Session: market.RegularSession, Adjustment: market.Raw, From: now.Add(-time.Hour), To: now}
	bars := []market.Bar{{Symbol: "AAPL", Timestamp: spec.From, Completed: true}, {Symbol: "AAPL", Timestamp: now.Add(-time.Minute), Completed: true}}
	if err := h.persistHistory(ctx, spec, bars, "tail-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.db.Exec("UPDATE history_coverage_v3 SET expires_at=0 WHERE kind='empty'"); err != nil {
		t.Fatal(err)
	}
	missing, err := catalog.Missing(ctx, spec, "tail-test")
	if err != nil || len(missing) != 1 || !missing[0].From.Equal(spec.From.Add(time.Minute)) {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
	// A later authoritative response can fill the gap; provisional bars have
	// never made it into the positive coverage record.
	if err := catalog.RecordCoverage(ctx, spec, "tail-test", bars, time.Second); err != nil {
		t.Fatal(err)
	}
	missing, err = catalog.Missing(ctx, spec, "tail-test")
	if err != nil || len(missing) != 0 {
		t.Fatalf("handoff missing=%+v err=%v", missing, err)
	}
}

func TestTailStartupPreservesOlderCoverage(t *testing.T) {
	ctx := context.Background()
	catalog, err := OpenHistoryCatalog(t.TempDir() + "/history.db")
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	now := time.Now().UTC().Truncate(time.Minute)
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", Session: market.RegularSession, Adjustment: market.Raw, From: now.Add(-72 * time.Hour), To: now}
	if err := catalog.RecordCoverage(ctx, spec, "v1", []market.Bar{{Symbol: "AAPL", Timestamp: now.Add(-time.Minute), Completed: true}}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := catalog.InvalidateRecentUSCoverage(ctx, now, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	missing, err := catalog.Missing(ctx, spec, "v1")
	if err != nil || len(missing) != 1 || !missing[0].From.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
}

type tailRecordingClickHouse struct {
	redisTestClickHouse
	written []market.Bar
}

func (c *tailRecordingClickHouse) WriteBars(_ context.Context, _ string, _ market.AdjustmentMode, bars []market.Bar, _ uint64) error {
	c.written = append(c.written, bars...)
	c.bars = append(c.bars, bars...)
	return nil
}
func TestHistoryTailAcrossProviderAndCanonicalRedisPaths(t *testing.T) {
	for _, canonical := range []bool{false, true} {
		t.Run(fmt.Sprint(canonical), func(t *testing.T) {
			now := time.Date(2026, 9, 23, 14, 10, 30, 0, time.UTC)
			store, err := NewStore(t.TempDir(), &redisCountingProvider{})
			if err != nil {
				t.Fatal(err)
			}
			redis := &memoryBarCache{}
			store.ConfigureBarCache(redis, time.Hour, time.Minute, 1825*24*time.Hour)
			catalog, err := OpenHistoryCatalog(t.TempDir() + "/history.db")
			if err != nil {
				t.Fatal(err)
			}
			defer catalog.Close()
			ch := &tailRecordingClickHouse{}
			h := &HTTP{Store: store, RedisEnabled: true, Redis: redis, ClickHouseEnabled: canonical, ClickHouse: ch, HistoryCatalog: catalog, DataVersion: "test", USTail: &provider.USTail{Quote: serverTailStub{}, Now: func() time.Time { return now }}}
			spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", Session: market.RegularSession, Adjustment: market.Raw, From: now.Add(-time.Hour), To: now}
			body, _ := json.Marshal(map[string]any{"spec": spec})
			for i := 0; i < 2; i++ {
				w := httptest.NewRecorder()
				h.historyBars(w, httptest.NewRequest("POST", "/v1/history/bars", bytes.NewReader(body)))
				var response struct {
					Bars   []market.Bar
					Source string
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if w.Code != 200 || len(response.Bars) != 2 || !strings.Contains(response.Source, "longbridge-tail") {
					t.Fatalf("status=%d response=%s", w.Code, w.Body.String())
				}
				if i == 1 && !strings.Contains(response.Source, "server-redis") {
					t.Fatalf("cache path not exercised: %s", response.Source)
				}
			}
			for _, bar := range ch.written {
				if bar.Source == "longbridge-tail" {
					t.Fatal("provisional data persisted")
				}
			}
			for _, bars := range redis.data {
				for _, bar := range bars {
					if bar.Source == "longbridge-tail" {
						t.Fatal("provisional data in historical Redis")
					}
				}
			}
		})
	}
}
