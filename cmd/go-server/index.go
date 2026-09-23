package main

import (
	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/provider"
)

func buildIndexProviders(cfg config.Server, quote provider.LongbridgeHistoryClient, massive *provider.Massive) map[string]provider.Provider {
	providers := map[string]provider.Provider{}
	for _, name := range cfg.EffectiveIndexProviders() {
		switch name {
		case "longbridge":
			providers[name] = &provider.LongbridgeIndex{Quote: quote, Version: "longbridge-index-v1-" + cfg.DataVersion}
		case "fmp":
			providers[name] = &provider.FMPIndex{APIKey: cfg.FMPAPIKey, BaseURL: cfg.FMPBaseURL, Version: "fmp-index-v1-" + cfg.DataVersion}
		case "massive":
			providers[name] = massive
		case "mock":
			providers[name] = &provider.Mock{Version: "mock-index-v1-" + cfg.DataVersion}
		}
	}
	return providers
}

func indexProviderStatus(cfg config.Server) map[string]any {
	enabled := len(cfg.EffectiveIndexProviders()) > 0
	routes, _ := cfg.ParsedIndexRoutes()
	details := map[string]any{}
	for symbol, name := range routes {
		details[symbol] = map[string]any{"provider": name, "max_years": indexHistoryYears(cfg, name)}
	}
	return map[string]any{
		"state":    map[bool]string{true: "enabled", false: "disabled"}[enabled],
		"provider": cfg.IndexProvider, "history_enabled": enabled,
		"max_years": indexHistoryYears(cfg, cfg.IndexProvider), "routes": details,
	}
}

func indexHistoryYears(cfg config.Server, name string) int {
	if name == "" || name == "disabled" {
		return 0
	}
	return historyYears(cfg, name)
}
