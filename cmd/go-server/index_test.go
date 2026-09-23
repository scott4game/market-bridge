package main

import (
	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/provider"
	"testing"
)

func TestIndexRouteInitializationAndStatus(t *testing.T) {
	cfg := config.Server{IndexProvider: "disabled", IndexRoutes: "I:HSI=longbridge,I:VIX=fmp,I:SPX=massive,I:DJI=disabled", LongbridgeHistoryMaxYears: 5, FMPHistoryMaxYears: 3, MassiveHistoryMaxYears: 2}
	massive := &provider.Massive{}
	providers := buildIndexProviders(cfg, nil, massive)
	if len(providers) != 3 || providers["massive"] != massive || providers["longbridge"] == nil || providers["fmp"] == nil {
		t.Fatalf("providers=%v", providers)
	}
	status := indexProviderStatus(cfg)
	if status["state"] != "enabled" || status["provider"] != "disabled" || status["history_enabled"] != true {
		t.Fatalf("status=%v", status)
	}
	routes := status["routes"].(map[string]any)
	if routes["I:VIX"].(map[string]any)["max_years"] != 3 {
		t.Fatalf("routes=%v", routes)
	}
	if indexProviderStatus(config.Server{IndexProvider: "disabled"})["state"] != "disabled" {
		t.Fatal("empty configuration enabled")
	}
}
