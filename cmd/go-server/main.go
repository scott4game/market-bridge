package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	lbconfig "github.com/longbridge/openapi-go/config"
	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/access"
	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/live"
	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/news"
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
	var optionsUsage *provider.UsageTracker
	var optionsCatalog *marketserver.OptionCatalog
	var massiveProvider *provider.Massive
	if cfg.Provider == "massive" || cfg.IndexProvider == "massive" {
		var err error
		usage, err = provider.NewUsageTracker(filepath.Join(cfg.DataDir, "usage.db"), cfg.MassivePlanName, cfg.MassivePerMinute, cfg.MassivePerMonth, time.Local)
		if err != nil {
			log.Fatal(err)
		}
		defer usage.Close()
		massiveProvider = &provider.Massive{APIKey: cfg.MassiveAPIKey, BaseURL: cfg.MassiveBaseURL, Version: cfg.DataVersion, PlanName: cfg.MassivePlanName, Usage: usage}
	}
	if cfg.OptionsProvider == "massive" {
		var err error
		optionsUsage, err = provider.NewUsageTracker(filepath.Join(cfg.DataDir, "options-usage.db"), cfg.MassiveOptionsPlanName, cfg.MassiveOptionsPerMinute, cfg.MassiveOptionsPerMonth, time.Local)
		if err != nil {
			log.Fatal(err)
		}
		defer optionsUsage.Close()
		optionsSource := &provider.MassiveOptions{APIKey: cfg.MassiveAPIKey, BaseURL: cfg.MassiveBaseURL, Usage: optionsUsage, RequestsPerMinute: cfg.MassiveOptionsPerMinute}
		optionsCatalog, err = marketserver.OpenOptionCatalog(filepath.Join(cfg.DataDir, "options-cache.db"), optionsSource)
		if err != nil {
			log.Fatal(err)
		}
		defer optionsCatalog.Close()
	}
	var usProvider provider.Provider
	switch cfg.Provider {
	case "massive":
		usProvider = massiveProvider
	default:
		usProvider = &provider.Mock{Version: cfg.DataVersion}
	}
	liveProviders := cfg.EffectiveLiveProviders()
	longbridgeNeeded := cfg.AShareProvider == "longbridge" || cfg.HKProvider == "longbridge" || cfg.IndexProvider == "longbridge" || contains(liveProviders, "longbridge")
	var longbridgeQuote *lbquote.QuoteContext
	if longbridgeNeeded {
		longbridgeConfig, err := lbconfig.New()
		if err != nil {
			log.Fatal(err)
		}
		longbridgeConfig.SetLogger(newLongbridgeLogger(longbridgeAffectedFeatures(cfg, liveProviders)))
		longbridgeQuote, err = lbquote.NewFromCfg(longbridgeConfig)
		if err != nil {
			log.Fatal(err)
		}
		defer longbridgeQuote.Close()
	}
	var longbridgeHistory provider.Provider
	if cfg.AShareProvider == "longbridge" || cfg.HKProvider == "longbridge" {
		longbridgeHistory = &provider.Longbridge{Quote: longbridgeQuote, Version: "longbridge-v1-" + cfg.DataVersion}
	}
	var aShareHistory provider.Provider
	switch cfg.AShareProvider {
	case "tushare":
		aShareHistory = &provider.TushareAShare{Token: cfg.TushareToken, BaseURL: cfg.TushareBaseURL, Version: "tushare-ashare-v1-" + cfg.DataVersion, RequestsPerMinute: cfg.TusharePerMinute}
	case "longbridge":
		aShareHistory = longbridgeHistory
	}
	var hkHistory provider.Provider
	if cfg.HKProvider == "longbridge" {
		hkHistory = longbridgeHistory
	}
	var universeProviders []provider.Provider
	if contains(liveProviders, "longbridge") && longbridgeHistory == nil {
		universeProviders = append(universeProviders, &provider.Longbridge{Quote: longbridgeQuote, Version: "longbridge-universe-v1-" + cfg.DataVersion})
	}
	var binanceHistory provider.Provider
	if cfg.BinanceEnabled {
		binanceHistory = &provider.Binance{BaseURL: cfg.BinanceRESTURL, Version: "binance-spot-v1-" + cfg.DataVersion}
	}
	var indexProvider provider.Provider
	switch cfg.IndexProvider {
	case "longbridge":
		indexProvider = &provider.LongbridgeIndex{Quote: longbridgeQuote, Version: "longbridge-index-v1-" + cfg.DataVersion}
	case "fmp":
		indexProvider = &provider.FMPIndex{APIKey: cfg.FMPAPIKey, BaseURL: cfg.FMPBaseURL, Version: "fmp-index-v1-" + cfg.DataVersion}
	case "massive":
		indexProvider = massiveProvider
	case "mock":
		indexProvider = &provider.Mock{Version: "mock-index-v1-" + cfg.DataVersion}
	}
	var p provider.Provider = &provider.Router{US: usProvider, Index: indexProvider, AShare: aShareHistory, HK: hkHistory, Binance: binanceHistory, UniverseProviders: universeProviders}
	historyDataVersion := fmt.Sprintf("%s:ashare=%s:hk=%s", cfg.DataVersion, cfg.AShareProvider, cfg.HKProvider)
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
	if optionsUsage != nil {
		go optionsUsage.RunCleanup(ctx)
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
	securityProfiles, err := marketserver.OpenSecurityProfileCatalog(filepath.Join(cfg.DataDir, "security-profiles.db"), store, cfg.SecurityProfileTTL, cfg.SecurityProfileMaxStale, cfg.SecurityProfileWorkers)
	if err != nil {
		log.Fatal(err)
	}
	defer securityProfiles.Close()
	var redisCache *storage.RedisBarCache
	if cfg.RedisEnabled {
		redisCache = storage.NewRedisBarCache(cfg.RedisAddress, cfg.RedisUsername, cfg.RedisPassword, cfg.RedisDB)
		defer redisCache.Close()
		store.ConfigureBarCache(redisCache, cfg.RedisTTL, cfg.EmptyCoverageTTL, cfg.ClickHouseRetention)
		redisCtx, redisCancel := context.WithTimeout(ctx, time.Second)
		redisErr := redisCache.Healthy(redisCtx)
		redisCancel()
		if redisErr != nil {
			log.Printf("go-server Redis connection failed; bypassing hot cache: address=%s db=%d user=%s: %v", cfg.RedisAddress, cfg.RedisDB, displayUser(cfg.RedisUsername), redisErr)
		} else {
			log.Printf("go-server Redis connection succeeded: address=%s db=%d user=%s", cfg.RedisAddress, cfg.RedisDB, displayUser(cfg.RedisUsername))
		}
	} else {
		log.Printf("go-server Redis disabled")
	}
	var newsService *news.Service
	if cfg.NewsProvider == "fmp" {
		newsStore, newsErr := news.OpenStore(filepath.Join(cfg.DataDir, "news.db"))
		if newsErr != nil {
			log.Fatal(newsErr)
		}
		defer newsStore.Close()
		newsService = news.NewService(newsStore, &news.FMP{APIKey: cfg.FMPAPIKey, BaseURL: cfg.FMPBaseURL}, cfg.FMPNewsPollInterval, cfg.NewsRetention)
		go newsService.Run(ctx)
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
			log.Fatalf("go-server ClickHouse connection failed: database=%s user=%s: %v", cfg.ClickHouseDatabase, displayUser(cfg.ClickHouseUser), err)
		}
		log.Printf("go-server ClickHouse connection succeeded: database=%s user=%s", cfg.ClickHouseDatabase, displayUser(cfg.ClickHouseUser))
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
					if err := store.SyncRecentUniverse(ctx, ch, historyCatalog, historyDataVersion, 2, cfg.EmptyCoverageTTL); err != nil && ctx.Err() == nil {
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
	hub, err := live.NewHub(source, sink)
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
			if cfg.AShareProvider == "longbridge" || cfg.HKProvider == "longbridge" {
				state = "history_only"
			}
			status["longbridge"] = map[string]any{"state": state, "history_enabled": cfg.AShareProvider == "longbridge" || cfg.HKProvider == "longbridge", "depth_enabled": cfg.LongbridgeDepthEnabled, "subscribed_symbols": 0, "reconnects": 0}
		} else if value, ok := status["longbridge"].(map[string]any); ok {
			value["history_enabled"] = cfg.AShareProvider == "longbridge" || cfg.HKProvider == "longbridge"
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
		status["index"] = map[string]any{"state": map[bool]string{true: "enabled", false: "disabled"}[cfg.IndexProvider != "disabled"], "provider": cfg.IndexProvider, "history_enabled": cfg.IndexProvider != "disabled"}
		aShareEnabled := cfg.AShareProvider != "" && cfg.AShareProvider != "disabled"
		hkEnabled := cfg.HKProvider != "" && cfg.HKProvider != "disabled"
		status["ashare"] = map[string]any{"state": map[bool]string{true: "enabled", false: "disabled"}[aShareEnabled], "provider": cfg.AShareProvider, "history_enabled": aShareEnabled}
		status["hk"] = map[string]any{"state": map[bool]string{true: "enabled", false: "disabled"}[hkEnabled], "provider": cfg.HKProvider, "history_enabled": hkEnabled}
		status["massive"] = map[string]any{"state": map[bool]string{true: "enabled", false: "disabled"}[cfg.Provider == "massive" || cfg.IndexProvider == "massive"], "plan": cfg.MassivePlanName}
		status["options"] = map[string]any{"state": map[bool]string{true: "enabled", false: "disabled"}[cfg.OptionsProvider == "massive"], "provider": cfg.OptionsProvider, "plan": cfg.MassiveOptionsPlanName, "history_enabled": cfg.OptionsProvider == "massive"}
		if newsService != nil {
			status["fmp_news"] = newsService.Status()
		} else {
			status["fmp_news"] = map[string]any{"state": "disabled"}
		}
		return status
	}
	var historicalClickHouse marketserver.HistoricalClickHouse
	if clickhouse != nil {
		historicalClickHouse = clickhouse
	}
	var recentTrades marketserver.RecentTradesReader
	if longbridgeQuote != nil {
		recentTrades = longbridgeQuote
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           (&marketserver.HTTP{Store: store, Token: cfg.BearerToken, Access: auth, Limiter: limiter, Live: hub, Usage: usage, OptionsUsage: optionsUsage, Options: optionsCatalog, ProviderStatus: providerStatus, ClickHouseEnabled: cfg.ClickHouseEnabled, ClickHouse: historicalClickHouse, RedisEnabled: cfg.RedisEnabled, Redis: redisCache, HistoryCatalog: historyCatalog, DataVersion: historyDataVersion, EmptyCoverageTTL: cfg.EmptyCoverageTTL, HistoryRetention: cfg.ClickHouseRetention, RecentTrades: recentTrades, News: newsService, SecurityProfiles: securityProfiles}).Handler(),
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
	if cfg.OptionsProvider == "massive" {
		log.Printf("Massive options provider enabled: plan=%s, requests_per_minute=%d", cfg.MassiveOptionsPlanName, cfg.MassiveOptionsPerMinute)
	}
	if cfg.NewsProvider == "fmp" {
		log.Printf("FMP news provider enabled: poll_interval=%s, retention=%s", cfg.FMPNewsPollInterval, cfg.NewsRetention)
	}
	if cfg.IndexProvider != "" && cfg.IndexProvider != "disabled" && cfg.IndexProvider != "mock" {
		log.Printf("Index historical provider enabled: provider=%s, data_version=%s", cfg.IndexProvider, cfg.DataVersion)
	}
	if cfg.AShareProvider != "" && cfg.AShareProvider != "disabled" {
		log.Printf("A-share historical provider enabled: provider=%s, markets=SH,SZ, data_version=%s", cfg.AShareProvider, cfg.DataVersion)
	}
	if cfg.HKProvider != "" && cfg.HKProvider != "disabled" {
		log.Printf("HK historical provider enabled: provider=%s, data_version=%s", cfg.HKProvider, cfg.DataVersion)
	}
	if contains(cfg.EffectiveLiveProviders(), "longbridge") {
		log.Printf("Longbridge live provider enabled: subscription_mode=on_demand, depth=%t", cfg.LongbridgeDepthEnabled)
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

func displayUser(user string) string {
	if user == "" {
		return "default"
	}
	return user
}
