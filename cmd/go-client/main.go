package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/localclient"
	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/news"
	"github.com/scott4game/market-bridge/internal/storage"
)

func main() {
	cfg := config.ClientFromEnv()
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var clickhouse *storage.ClickHouseSink
	if cfg.ClickHouseEnabled && (cmd == "serve" || cmd == "market-history") {
		var sinkErr error
		clickhouse, sinkErr = storage.NewClickHouseSink(ctx, cfg.ClickHouseURL, cfg.ClickHouseDatabase, cfg.ClickHouseUser, cfg.ClickHousePassword)
		if sinkErr != nil {
			log.Fatalf("initialize client ClickHouse: %v", sinkErr)
		}
		go clickhouse.Run(ctx)
	}
	var historicalClickHouse localclient.HistoricalClickHouse
	if clickhouse != nil {
		historicalClickHouse = clickhouse
	}
	cache, err := localclient.NewCacheWithClickHouse(cfg, historicalClickHouse)
	if err != nil {
		log.Fatal(err)
	}
	defer cache.Close()
	switch cmd {
	case "serve":
		go cache.RunCleanup(ctx)
		go cache.RunMarketHistorySync(ctx)
		if clickhouse != nil {
			log.Printf("client ClickHouse available for on-demand live data; server capability decides whether it is active")
		}
		live := localclient.NewLiveProxy(cfg, cache)
		go live.Run(ctx)
		newsStore, newsErr := news.OpenStore(filepath.Join(cfg.CacheDir, "news.db"))
		if newsErr != nil {
			log.Fatal(newsErr)
		}
		defer newsStore.Close()
		newsProxy := localclient.NewNewsProxy(cfg, newsStore)
		go newsProxy.Run(ctx)
		srv := &http.Server{Addr: cfg.Listen, Handler: (&localclient.HTTP{Cache: cache, Live: live, News: newsProxy}).Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20}
		go func() {
			<-ctx.Done()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
		log.Printf("go-client and KLineChart listening on http://%s", cfg.Listen)
		err = srv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	case "cache":
		cacheCommand(ctx, cache)
	case "fetch":
		fetchCommand(ctx, cache)
	case "market-history":
		marketHistoryCommand(ctx, cache)
	default:
		log.Fatalf("unknown command %q", cmd)
	}
}

func marketHistoryCommand(ctx context.Context, cache *localclient.Cache) {
	set := flag.NewFlagSet("market-history", flag.ExitOnError)
	days := set.Int("days", 730, "rolling number of days to backfill")
	workers := set.Int("workers", 2, "concurrent symbols")
	_ = set.Parse(os.Args[2:])
	if *days < 1 || *days > 730 || *workers < 1 || *workers > 16 {
		log.Fatal("days must be 1..730 and workers must be 1..16")
	}
	status := cache.StorageStatus(ctx)
	if status["mode"] == "provider_only" || status["mode"] == "unknown" {
		log.Fatalf("ClickHouse storage is unavailable: %#v", status)
	}
	symbols, err := cache.Universe(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("backfilling %d symbols into %v", len(symbols), status["mode"])
	jobs := make(chan string)
	errc := make(chan error, *workers)
	var completed atomic.Int64
	from, to := time.Now().UTC().AddDate(0, 0, -*days), time.Now().UTC()
	var wg sync.WaitGroup
	for range *workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for symbol := range jobs {
				_, venue, normalizeErr := market.NormalizeSymbol(symbol)
				if normalizeErr != nil {
					errc <- normalizeErr
					return
				}
				adjustment := market.ForwardAdjusted
				if venue == market.VenueUS {
					adjustment = market.SplitAdjusted
				}
				spec := market.DatasetSpec{Symbols: []string{symbol}, Interval: "1m", From: from, To: to, Session: market.RegularSession, Adjustment: adjustment}
				if _, _, barsErr := cache.Bars(ctx, spec); barsErr != nil {
					errc <- fmt.Errorf("%s: %w", symbol, barsErr)
					return
				}
				n := completed.Add(1)
				if n%100 == 0 {
					log.Printf("market-history progress %d/%d", n, len(symbols))
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, symbol := range symbols {
			select {
			case jobs <- symbol:
			case <-ctx.Done():
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case err := <-errc:
		log.Fatal(err)
	case <-done:
		log.Printf("market-history backfill complete: %d symbols", completed.Load())
	case <-ctx.Done():
		log.Fatal(ctx.Err())
	}
}
func cacheCommand(ctx context.Context, c *localclient.Cache) {
	sub := "list"
	if len(os.Args) > 2 {
		sub = os.Args[2]
	}
	switch sub {
	case "list":
		v, err := c.List(ctx)
		if err != nil {
			log.Fatal(err)
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(b))
	case "prune":
		expired := len(os.Args) > 3 && os.Args[3] == "--expired"
		n, err := c.Prune(ctx, expired)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("deleted %d dataset(s)\n", n)
	case "refresh":
		if len(os.Args) < 4 {
			log.Fatal("usage: go-client cache refresh <dataset-id>")
		}
		if err := c.Delete(ctx, os.Args[3]); err != nil {
			log.Fatal(err)
		}
		fmt.Println("dataset removed; the next request will download it again")
	default:
		log.Fatalf("unknown cache command %q", sub)
	}
}

func fetchCommand(ctx context.Context, c *localclient.Cache) {
	set := flag.NewFlagSet("fetch", flag.ExitOnError)
	symbols := set.String("symbols", "", "comma-separated symbols")
	interval := set.String("interval", "1m", "bar interval")
	from := set.String("from", "", "RFC3339 start")
	to := set.String("to", "", "RFC3339 end")
	session := set.String("session", "", "regular, extended or continuous; inferred when omitted")
	adjustment := set.String("adjustment", "auto", "auto, raw, split_adjusted or forward_adjusted")
	_ = set.Parse(os.Args[2:])
	start, e1 := time.Parse(time.RFC3339, *from)
	end, e2 := time.Parse(time.RFC3339, *to)
	if *symbols == "" || e1 != nil || e2 != nil {
		log.Fatal("symbols, from and to (RFC3339) are required")
	}
	bars, source, err := c.Bars(ctx, market.DatasetSpec{Symbols: strings.Split(*symbols, ","), Interval: *interval, From: start, To: end, Session: market.Session(*session), Adjustment: market.AdjustmentMode(*adjustment)})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("loaded %d bars from %s\n", len(bars), source)
}
