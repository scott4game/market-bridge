package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/live"
	"github.com/scott4game/market-bridge/internal/provider"
	marketserver "github.com/scott4game/market-bridge/internal/server"
	"github.com/scott4game/market-bridge/internal/storage"
)

func main() {
	cfg := config.ServerFromEnv()
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	var usage *provider.UsageTracker
	var p provider.Provider
	switch cfg.Provider {
	case "massive":
		var err error
		usage, err = provider.NewUsageTracker(filepath.Join(cfg.DataDir, "usage.db"), cfg.MassivePlanName, cfg.MassivePerMinute, cfg.MassivePerMonth, time.Local)
		if err != nil {
			log.Fatal(err)
		}
		defer usage.Close()
		p = &provider.Massive{APIKey: cfg.MassiveAPIKey, BaseURL: cfg.MassiveBaseURL, Version: cfg.DataVersion, Usage: usage}
	default:
		p = &provider.Mock{Version: cfg.DataVersion}
	}
	store, err := marketserver.NewStore(cfg.DataDir, p)
	if err != nil {
		log.Fatal(err)
	}
	var source live.Source = live.MockSource{}
	if cfg.LiveProvider == "longbridge" {
		source = live.LongbridgeSource{}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go store.RunCleanup(ctx, cfg.DatasetTTL)
	var sink live.Sink = live.NopSink{}
	if cfg.ClickHouseEnabled {
		ch, err := storage.NewClickHouseSink(ctx, cfg.ClickHouseURL, cfg.ClickHouseDatabase, cfg.ClickHouseUser, cfg.ClickHousePassword)
		if err != nil {
			log.Fatal(err)
		}
		sink = ch
		go ch.Run(ctx)
	}
	hub, err := live.NewHub(source, sink, cfg.Watchlist)
	if err != nil {
		log.Fatal(err)
	}
	go hub.Run(ctx)
	srv := &http.Server{Addr: cfg.Listen, Handler: (&marketserver.HTTP{Store: store, Token: cfg.BearerToken, Live: hub, Usage: usage}).Handler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Printf("go-server listening on %s with %s provider", cfg.Listen, p.Name())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
