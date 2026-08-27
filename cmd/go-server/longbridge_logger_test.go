package main

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/scott4game/market-bridge/internal/config"
)

func TestLongbridgeLoggerExplainsDisconnectImpactAndRecovery(t *testing.T) {
	var output bytes.Buffer
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
	})

	logger := newLongbridgeLogger("recent_trades,live_quotes,market_depth")
	logger.Errorf("close conn, err: %v", errors.New("websocket: close 1006: unexpected EOF"))
	logger.Info("start reconnecting.")
	logger.Info("reconnect success")
	logger.Errorf("faield to do sub, err: %v", errors.New("timeout"))

	got := output.String()
	for _, want := range []string{
		"Longbridge quote WebSocket disconnected",
		"affected=recent_trades,live_quotes,market_depth",
		"recovery=automatic reconnect",
		"Longbridge quote WebSocket reconnecting",
		"status=restoring subscriptions",
		"subscription restore failed",
		"status=live streams degraded",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log %q does not contain %q", got, want)
		}
	}
}

func TestLongbridgeAffectedFeaturesReflectConfiguration(t *testing.T) {
	cfg := config.Server{
		LongbridgeHistoryEnabled: true,
		LongbridgeDepthEnabled:   true,
		IndexProvider:            "longbridge",
	}
	got := longbridgeAffectedFeatures(cfg, []string{"longbridge"})
	for _, want := range []string{
		"recent_trades",
		"security_directory",
		"HK_CN_history_klines",
		"index_history_klines",
		"live_quotes",
		"live_trades",
		"market_depth",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("features %q do not contain %q", got, want)
		}
	}
}
