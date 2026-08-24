package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/scott4game/market-bridge/internal/access"
	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/live"
	"github.com/scott4game/market-bridge/internal/provider"
	marketserver "github.com/scott4game/market-bridge/internal/server"
	"github.com/scott4game/market-bridge/internal/storage"
)

func main() {
	cfg := config.ServerFromEnv()
	if cfg.AuthDB == "" {
		cfg.AuthDB = filepath.Join(cfg.DataDir, "auth.db")
	}
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		adminCommand(cfg)
		return
	}
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
	if err := os.MkdirAll(filepath.Dir(cfg.AuthDB), 0o755); err != nil {
		log.Fatal(err)
	}
	auth, err := access.Open(cfg.AuthDB, cfg.BearerToken)
	if err != nil {
		log.Fatal(err)
	}
	defer auth.Close()
	if !auth.HasCredential(context.Background()) {
		log.Fatal("no active API key: set GO_SERVER_TOKEN or create a user key with go-server admin")
	}
	limiter := access.NewLimiter()
	if cfg.BearerToken != "" {
		log.Printf("legacy admin credential enabled via GO_SERVER_TOKEN; migrate clients to personal API keys")
	}
	store, err := marketserver.NewStoreWithOptions(cfg.DataDir, p, cfg.DatasetWorkers, cfg.DatasetQueueSize)
	if err != nil {
		log.Fatal(err)
	}
	var source live.Source = live.MockSource{}
	var longbridgeSource *live.LongbridgeSource
	if cfg.LiveProvider == "longbridge" {
		longbridgeSource = &live.LongbridgeSource{}
		source = longbridgeSource
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
	hub.ConfigureAccess(auth, limiter)
	if longbridgeSource != nil {
		longbridgeSource.OnConnected = hub.MarkConnected
	} else {
		hub.MarkConnected()
	}
	go hub.Run(ctx)
	providerStatus := func() any {
		status := hub.ProviderStatus()
		if cfg.LiveProvider != "longbridge" {
			status["longbridge"] = map[string]any{"state": "disabled", "subscribed_symbols": 0, "reconnects": 0}
		}
		status["massive"] = map[string]any{"state": map[bool]string{true: "enabled", false: "disabled"}[cfg.Provider == "massive"], "plan": cfg.MassivePlanName}
		return status
	}
	srv := &http.Server{Addr: cfg.Listen, Handler: (&marketserver.HTTP{Store: store, Token: cfg.BearerToken, Access: auth, Limiter: limiter, Watchlist: cfg.Watchlist, Live: hub, Usage: usage, ProviderStatus: providerStatus}).Handler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	logEnabledProviders(cfg)
	log.Printf("go-server listening on %s with %s provider", cfg.Listen, p.Name())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func logEnabledProviders(cfg config.Server) {
	if cfg.Provider == "massive" {
		log.Printf("Massive historical provider enabled: plan=%s, data_version=%s", cfg.MassivePlanName, cfg.DataVersion)
	}
	if cfg.LiveProvider == "longbridge" {
		log.Printf("Longbridge live provider enabled: watchlist=%s", strings.Join(cfg.Watchlist, ","))
	}
}
