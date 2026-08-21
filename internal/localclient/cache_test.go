package localclient_test

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"massive-go/internal/config"
	"massive-go/internal/localclient"
	"massive-go/internal/market"
	"massive-go/internal/provider"
	marketserver "massive-go/internal/server"
	publicclient "massive-go/pkg/client"
)

type countingProvider struct {
	calls atomic.Int32
	mock  provider.Mock
}

func (p *countingProvider) Name() string        { return p.mock.Name() }
func (p *countingProvider) DataVersion() string { return p.mock.DataVersion() }
func (p *countingProvider) Bars(ctx context.Context, s market.DatasetSpec) ([]market.Bar, error) {
	p.calls.Add(1)
	return p.mock.Bars(ctx, s)
}

func TestCacheRemoteThenParquetAndLocalAPI(t *testing.T) {
	p := &countingProvider{mock: provider.Mock{Version: "test-v1"}}
	store, err := marketserver.NewStore(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer((&marketserver.HTTP{Store: store}).Handler())
	defer upstream.Close()
	cfg := config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL, ParquetTTL: time.Hour, CleanupInterval: time.Hour, RedisEnabled: false}
	cache, err := localclient.NewCache(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: time.Date(2025, 1, 2, 14, 30, 0, 0, time.UTC), To: time.Date(2025, 1, 2, 15, 0, 0, 0, time.UTC), Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	bars, source, err := cache.Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if source != "go-server" || len(bars) != 30 {
		t.Fatalf("source=%s bars=%d", source, len(bars))
	}
	bars, source, err = cache.Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if source != "parquet" || len(bars) != 30 {
		t.Fatalf("source=%s bars=%d", source, len(bars))
	}
	if p.calls.Load() != 1 {
		t.Fatalf("provider calls=%d", p.calls.Load())
	}
	localHTTP := httptest.NewServer((&localclient.HTTP{Cache: cache}).Handler())
	defer localHTTP.Close()
	sdk := publicclient.NewLocalClient(publicclient.Config{BaseURL: localHTTP.URL})
	dataset, err := sdk.EnsureDataset(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := dataset.ScanBars(context.Background(), func(publicclient.Bar) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 30 || dataset.Source != "parquet" {
		t.Fatalf("sdk source=%s bars=%d", dataset.Source, count)
	}
}

func TestConfigurableTTLPrune(t *testing.T) {
	p := &countingProvider{mock: provider.Mock{Version: "test-v1"}}
	store, _ := marketserver.NewStore(t.TempDir(), p)
	upstream := httptest.NewServer((&marketserver.HTTP{Store: store}).Handler())
	defer upstream.Close()
	cache, err := localclient.NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL, ParquetTTL: time.Nanosecond, CleanupInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	spec := market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: time.Date(2025, 1, 2, 14, 30, 0, 0, time.UTC), To: time.Date(2025, 1, 2, 14, 31, 0, 0, time.UTC)}
	if _, _, err := cache.Bars(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	n, err := cache.Prune(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted=%d", n)
	}
	entries, _ := cache.List(context.Background())
	if len(entries) != 0 {
		t.Fatalf("entries=%d", len(entries))
	}
}

func TestSharedLiveProxyFeedsSDK(t *testing.T) {
	p := &countingProvider{mock: provider.Mock{Version: "test-v1"}}
	store, _ := marketserver.NewStore(t.TempDir(), p)
	upstream := httptest.NewServer((&marketserver.HTTP{Store: store}).Handler())
	defer upstream.Close()
	cfg := config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL, RedisEnabled: false}
	cache, err := localclient.NewCache(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	proxy := localclient.NewLiveProxy(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go proxy.Run(ctx)
	localHTTP := httptest.NewServer((&localclient.HTTP{Cache: cache, Live: proxy}).Handler())
	defer localHTTP.Close()
	sdk := publicclient.NewLocalClient(publicclient.Config{BaseURL: localHTTP.URL})
	stream, err := sdk.Subscribe(ctx, publicclient.Subscription{Symbols: []string{"AAPL"}, Events: []market.EventType{market.BarEvent}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case event := <-stream.Events():
		if event.Type != market.BarEvent || event.Symbol != "AAPL" || event.Bar == nil {
			t.Fatalf("unexpected event: %#v", event)
		}
	case err := <-stream.Errors():
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for live event")
	}
}
