package localclient_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"net/http/httptest"

	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/localclient"
	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/provider"
	marketserver "github.com/scott4game/market-bridge/internal/server"
)

type fakeHistoricalCH struct {
	mu     sync.Mutex
	bars   []market.Bar
	writes int
}

func (f *fakeHistoricalCH) Healthy(context.Context) error { return nil }
func (f *fakeHistoricalCH) QueryBars(_ context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []market.Bar
	for _, bar := range f.bars {
		if !bar.Timestamp.Before(spec.From) && bar.Timestamp.Before(spec.To) {
			out = append(out, bar)
		}
	}
	return out, nil
}
func (f *fakeHistoricalCH) WriteBars(_ context.Context, _ market.AdjustmentMode, bars []market.Bar, _ uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	f.bars = append(f.bars, bars...)
	return nil
}
func (f *fakeHistoricalCH) Write(context.Context, market.LiveEvent) error {
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return nil
}
func (f *fakeHistoricalCH) CleanupBefore(context.Context, time.Time) (int, error) { return 0, nil }

func weekdayRange(daysAgo int) (time.Time, time.Time) {
	day := time.Now().UTC().AddDate(0, 0, -daysAgo).Truncate(24 * time.Hour)
	for day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		day = day.AddDate(0, 0, -1)
	}
	return day.Add(14 * time.Hour), day.Add(14*time.Hour + 30*time.Minute)
}

func TestLocalClickHouseUsedOnlyWhenServerClickHouseDisabled(t *testing.T) {
	p := &countingProvider{mock: provider.Mock{Version: "mode-v1"}}
	store, _ := marketserver.NewStore(t.TempDir(), p)
	upstream := httptest.NewServer((&marketserver.HTTP{Store: store, DataVersion: "mode-v1"}).Handler())
	defer upstream.Close()
	localCH := &fakeHistoricalCH{}
	cache, err := localclient.NewCacheWithClickHouse(config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL, RedisEnabled: false, RedisTTL: time.Hour}, localCH)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	from, to := weekdayRange(1)
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: from, To: to, Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	if _, source, err := cache.Bars(context.Background(), spec); err != nil || source != "provider+local-clickhouse" {
		t.Fatalf("source=%s err=%v", source, err)
	}
	if _, source, err := cache.Bars(context.Background(), spec); err != nil || source != "local-clickhouse" {
		t.Fatalf("source=%s err=%v", source, err)
	}
	localCH.mu.Lock()
	writes := localCH.writes
	localCH.mu.Unlock()
	if writes != 1 {
		t.Fatalf("local writes=%d", writes)
	}
}

func TestArchiveBypassesBothClickHouses(t *testing.T) {
	p := &countingProvider{mock: provider.Mock{Version: "archive-v1"}}
	store, _ := marketserver.NewStore(t.TempDir(), p)
	serverCH := &fakeHistoricalCH{}
	catalog, _ := marketserver.OpenHistoryCatalog(t.TempDir() + "/history.db")
	defer catalog.Close()
	upstream := httptest.NewServer((&marketserver.HTTP{Store: store, DataVersion: "archive-v1", ClickHouseEnabled: true, ClickHouse: serverCH, HistoryCatalog: catalog}).Handler())
	defer upstream.Close()
	localCH := &fakeHistoricalCH{}
	cache, err := localclient.NewCacheWithClickHouse(config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL, RedisEnabled: false}, localCH)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	from := time.Now().UTC().AddDate(-3, 0, 0).Truncate(24 * time.Hour)
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "3m", From: from, To: from.Add(time.Hour), Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	if _, source, err := cache.Bars(context.Background(), spec); err != nil || source != "provider" {
		t.Fatalf("source=%s err=%v", source, err)
	}
	serverCH.mu.Lock()
	serverWrites := serverCH.writes
	serverCH.mu.Unlock()
	localCH.mu.Lock()
	localWrites := localCH.writes
	localCH.mu.Unlock()
	if serverWrites != 0 || localWrites != 0 {
		t.Fatalf("archive wrote server=%d local=%d", serverWrites, localWrites)
	}
}

func TestServerHistoryRoutingUsesConfiguredClickHouseRetention(t *testing.T) {
	p := &countingProvider{mock: provider.Mock{Version: "retention-v1"}}
	store, _ := marketserver.NewStore(t.TempDir(), p)
	serverCH := &fakeHistoricalCH{}
	catalog, _ := marketserver.OpenHistoryCatalog(t.TempDir() + "/history.db")
	defer catalog.Close()
	from := time.Now().UTC().AddDate(0, 0, -60).Truncate(24 * time.Hour)
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: from, To: from.Add(time.Minute), Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	body, _ := json.Marshal(map[string]any{"spec": spec})
	recorder := httptest.NewRecorder()
	handler := (&marketserver.HTTP{
		Store: store, DataVersion: "retention-v1", ClickHouseEnabled: true,
		ClickHouse: serverCH, HistoryCatalog: catalog, HistoryRetention: 30 * 24 * time.Hour,
	}).Handler()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/history/bars", bytes.NewReader(body)))
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"source":"provider"`)) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	serverCH.mu.Lock()
	writes := serverCH.writes
	serverCH.mu.Unlock()
	if writes != 0 {
		t.Fatalf("expired range wrote to ClickHouse: %d", writes)
	}
}

func TestRemoteClickHouseLogicallyDisablesLocalWrites(t *testing.T) {
	p := &countingProvider{mock: provider.Mock{Version: "remote-v1"}}
	store, _ := marketserver.NewStore(t.TempDir(), p)
	from, to := weekdayRange(1)
	price := market.DecimalFromFloat(100)
	serverCH := &fakeHistoricalCH{bars: []market.Bar{{Symbol: "AAPL", Timestamp: from, Open: price, High: price, Low: price, Close: price, Volume: 1, Session: market.RegularSession, Source: "server-ch", Completed: true}}}
	catalog, _ := marketserver.OpenHistoryCatalog(t.TempDir() + "/history.db")
	defer catalog.Close()
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: from, To: to, Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	if err := catalog.RecordCoverage(context.Background(), spec, "remote-v1", serverCH.bars, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer((&marketserver.HTTP{Store: store, DataVersion: "remote-v1", ClickHouseEnabled: true, ClickHouse: serverCH, HistoryCatalog: catalog}).Handler())
	defer upstream.Close()
	localCH := &fakeHistoricalCH{}
	cache, err := localclient.NewCacheWithClickHouse(config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL, RedisEnabled: false}, localCH)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	if _, source, err := cache.Bars(context.Background(), spec); err != nil || source != "server-clickhouse" {
		t.Fatalf("source=%s err=%v", source, err)
	}
	if err := cache.Write(context.Background(), market.LiveEvent{Type: market.BarEvent, Symbol: "AAPL"}); err != nil {
		t.Fatal(err)
	}
	localCH.mu.Lock()
	writes := localCH.writes
	localCH.mu.Unlock()
	if writes != 0 {
		t.Fatalf("local ClickHouse writes=%d", writes)
	}
	status := cache.StorageStatus(context.Background())
	if status["mode"] != "remote_clickhouse" || status["local_clickhouse_enabled"] != false {
		t.Fatalf("status=%#v", status)
	}
}

func TestRemoteRedisRoutesHistoryAndDisablesLocalRedis(t *testing.T) {
	var historyCalls, datasetCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/storage/capabilities":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"clickhouse":   map[string]any{"enabled": false, "healthy": false, "error": ""},
				"redis":        map[string]any{"enabled": true, "healthy": true, "error": ""},
				"data_version": "remote-redis-v1",
			})
		case "/v1/history/bars":
			historyCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"source": "server-redis", "bars": []market.Bar{}})
		default:
			datasetCalls++
			http.Error(w, "unexpected dataset request", http.StatusInternalServerError)
		}
	}))
	defer upstream.Close()
	cache, err := localclient.NewCache(config.Client{
		CacheDir: t.TempDir(), ServerURL: upstream.URL,
		RedisEnabled: true, RedisAddress: "127.0.0.1:0", RedisTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	from := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1h", From: from, To: from.Add(time.Hour), Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	if _, source, err := cache.Bars(context.Background(), spec); err != nil || source != "server-redis" {
		t.Fatalf("source=%s err=%v", source, err)
	}
	if historyCalls != 1 || datasetCalls != 0 {
		t.Fatalf("history calls=%d dataset calls=%d", historyCalls, datasetCalls)
	}
	status := cache.StorageStatus(context.Background())
	if status["redis_mode"] != "remote_redis" || status["local_redis_enabled"] != true || status["local_redis_active"] != false {
		t.Fatalf("status=%#v", status)
	}
}
