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
	"strings"
	"syscall"
	"time"

	"massive-go/internal/config"
	"massive-go/internal/localclient"
	"massive-go/internal/market"
)

func main() {
	cfg := config.ClientFromEnv()
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	cache, err := localclient.NewCache(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer cache.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch cmd {
	case "serve":
		go cache.RunCleanup(ctx)
		live := localclient.NewLiveProxy(cfg)
		go live.Run(ctx)
		srv := &http.Server{Addr: cfg.Listen, Handler: (&localclient.HTTP{Cache: cache, Live: live}).Handler()}
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
	default:
		log.Fatalf("unknown command %q", cmd)
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
	session := set.String("session", "regular", "regular or extended")
	adjustment := set.String("adjustment", "split_adjusted", "raw or split_adjusted")
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
