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

	lbconfig "github.com/longbridge/openapi-go/config"
	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/access"
	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/live"
	"github.com/scott4game/market-bridge/internal/market"
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
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var usage *provider.UsageTracker
	var usProvider provider.Provider
	switch cfg.Provider {
	case "massive":
		var err error
		usage, err = provider.NewUsageTracker(filepath.Join(cfg.DataDir, "usage.db"), cfg.MassivePlanName, cfg.MassivePerMinute, cfg.MassivePerMonth, time.Local)
		if err != nil {
			log.Fatal(err)
		}
		defer usage.Close()
		usProvider = &provider.Massive{APIKey: cfg.MassiveAPIKey, BaseURL: cfg.MassiveBaseURL, Version: cfg.DataVersion, Usage: usage}
	default:
		usProvider = &provider.Mock{Version: cfg.DataVersion}
	}
	liveProviders := cfg.EffectiveLiveProviders()
	longbridgeNeeded := cfg.LongbridgeHistoryEnabled || contains(liveProviders, "longbridge")
	var longbridgeQuote *lbquote.QuoteContext
	if longbridgeNeeded {
		longbridgeConfig, err := lbconfig.New()
		if err != nil {
			log.Fatal(err)
		}
		longbridgeQuote, err = lbquote.NewFromCfg(longbridgeConfig)
		if err != nil {
			log.Fatal(err)
		}
		defer longbridgeQuote.Close()
	}
	var longbridgeHistory provider.Provider
	if cfg.LongbridgeHistoryEnabled {
		longbridgeHistory = &provider.Longbridge{Quote: longbridgeQuote, Version: "longbridge-v1-" + cfg.DataVersion}
	}
	var binanceHistory provider.Provider
	if cfg.BinanceEnabled {
		binanceHistory = &provider.Binance{BaseURL: cfg.BinanceRESTURL, Version: "binance-spot-v1-" + cfg.DataVersion}
	}
	var p provider.Provider = usProvider
	if longbridgeHistory != nil || binanceHistory != nil {
		p = &provider.Router{US: usProvider, Longbridge: longbridgeHistory, Binance: binanceHistory}
	}
	if err := os.MkdirAll(filepath.Dir(cfg.AuthDB), 0o755); err != nil {
		log.Fatal(err)
	}
	auth, err := access.Open(cfg.AuthDB, cfg.BearerToken)
	if err != nil {
		log.Fatal(err)
	}
	defer auth.Close()
	go auth.RunCleanup(ctx)
	if usage != nil {
		go usage.RunCleanup(ctx)
	}
	if !auth.HasCredential(context.Background()) {
		log.Fatal("no active API key: set GO_SERVER_TOKEN or create a user key with go-server admin")
	}
	limiter := access.NewLimiter()
	if cfg.BearerToken != "" {
		log.Printf("legacy admin credential enabled via GO_SERVER_TOKEN; migrate clients to personal API keys")
	}
	store, err := marketserver.NewStoreWithBuildOptions(ctx, cfg.DataDir, p, cfg.DatasetWorkers, cfg.DatasetQueueSize, cfg.DatasetBuildTimeout)
	if err != nil {
		log.Fatal(err)
	}
	var source live.Source
	var multiSource *live.MultiSource
	if len(liveProviders) == 1 && liveProviders[0] == "mock" {
		source = live.MockSource{}
	} else {
		multiSource = &live.MultiSource{}
		for _, name := range liveProviders {
			switch name {
			case "longbridge":
				multiSource.Routes = append(multiSource.Routes, live.SourceRoute{Name: "longbridge", Source: &live.LongbridgeSource{Quote: longbridgeQuote, DepthEnabled: cfg.LongbridgeDepthEnabled}, Accept: func(venue market.Venue) bool { return venue != market.VenueBinance }})
			case "binance":
				if !cfg.BinanceEnabled {
					log.Fatal("GO_SERVER_BINANCE_ENABLED=true is required for Binance live data")
				}
				multiSource.Routes = append(multiSource.Routes, live.SourceRoute{Name: "binance", Source: &live.BinanceSource{URL: cfg.BinanceWSURL}, Accept: func(venue market.Venue) bool { return venue == market.VenueBinance }})
			default:
				log.Fatalf("unsupported live provider %q", name)
			}
		}
		source = multiSource
	}
	historyCatalog, err := marketserver.OpenHistoryCatalog(filepath.Join(cfg.DataDir, "market-history.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer historyCatalog.Close()
	go historyCatalog.RunCleanup(ctx, cfg.ClickHouseRetention)
	go store.RunCleanup(ctx, cfg.DatasetTTL)
	var sink live.Sink = live.NopSink{}
	var clickhouse *storage.ClickHouseSink
	if cfg.ClickHouseEnabled {
		ch, err := storage.NewClickHouseSink(ctx, cfg.ClickHouseURL, cfg.ClickHouseDatabase, cfg.ClickHouseUser, cfg.ClickHousePassword)
		if err != nil {
			log.Fatal(err)
		}
		sink = ch
		clickhouse = ch
		go ch.Run(ctx)
		go func() {
			_, _ = ch.CleanupBefore(ctx, time.Now().UTC().Add(-cfg.ClickHouseRetention))
			ticker := time.NewTicker(cfg.ClickHouseCleanupInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					_, _ = ch.CleanupBefore(ctx, time.Now().UTC().Add(-cfg.ClickHouseRetention))
				}
			}
		}()
		if cfg.MarketHistorySyncEnabled {
			go func() {
				for {
					if err := store.SyncRecentUniverse(ctx, ch, historyCatalog, cfg.DataVersion, 2, cfg.EmptyCoverageTTL); err != nil && ctx.Err() == nil {
						log.Printf("market-history sync: %v", err)
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(cfg.MarketHistorySyncInterval):
					}
				}
			}()
		}
	}
	hub, err := live.NewHub(source, sink, cfg.Watchlist)
	if err != nil {
		log.Fatal(err)
	}
	hub.ConfigureAccess(auth, limiter)
	go hub.Run(ctx)
	providerStatus := func() any {
		status := hub.ProviderStatus()
		if multiSource != nil {
			for name, value := range multiSource.ProviderStatus() {
				status[name] = value
			}
		}
		if !contains(liveProviders, "longbridge") {
			state := "disabled"
			if cfg.LongbridgeHistoryEnabled {
				state = "history_only"
			}
			status["longbridge"] = map[string]any{"state": state, "history_enabled": cfg.LongbridgeHistoryEnabled, "depth_enabled": cfg.LongbridgeDepthEnabled, "subscribed_symbols": 0, "reconnects": 0}
		} else if value, ok := status["longbridge"].(map[string]any); ok {
			value["history_enabled"] = cfg.LongbridgeHistoryEnabled
			value["depth_enabled"] = cfg.LongbridgeDepthEnabled
		}
		if !contains(liveProviders, "binance") {
			state := "disabled"
			if cfg.BinanceEnabled {
				state = "history_only"
			}
			status["binance"] = map[string]any{"state": state, "history_enabled": cfg.BinanceEnabled, "subscribed_symbols": 0, "reconnects": 0}
		} else if value, ok := status["binance"].(map[string]any); ok {
			value["history_enabled"] = cfg.BinanceEnabled
		}
		status["massive"] = map[string]any{"state": map[bool]string{true: "enabled", false: "disabled"}[cfg.Provider == "massive"], "plan": cfg.MassivePlanName}
		return status
	}
	var historicalClickHouse marketserver.HistoricalClickHouse
	if clickhouse != nil {
		historicalClickHouse = clickhouse
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           (&marketserver.HTTP{Store: store, Token: cfg.BearerToken, Access: auth, Limiter: limiter, Watchlist: cfg.Watchlist, Live: hub, Usage: usage, ProviderStatus: providerStatus, ClickHouseEnabled: cfg.ClickHouseEnabled, ClickHouse: historicalClickHouse, HistoryCatalog: historyCatalog, DataVersion: cfg.DataVersion, EmptyCoverageTTL: cfg.EmptyCoverageTTL, HistoryRetention: cfg.ClickHouseRetention}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
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
	if cfg.LongbridgeHistoryEnabled {
		log.Printf("Longbridge historical provider enabled: markets=HK,SH,SZ, data_version=%s", cfg.DataVersion)
	}
	if contains(cfg.EffectiveLiveProviders(), "longbridge") {
		log.Printf("Longbridge live provider enabled: watchlist=%s, depth=%t", strings.Join(cfg.Watchlist, ","), cfg.LongbridgeDepthEnabled)
	}
	if cfg.BinanceEnabled {
		log.Printf("Binance Spot historical provider enabled: rest=%s", cfg.BinanceRESTURL)
	}
	if contains(cfg.EffectiveLiveProviders(), "binance") {
		log.Printf("Binance Spot live provider enabled: websocket=%s", cfg.BinanceWSURL)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
