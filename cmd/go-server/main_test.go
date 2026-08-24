package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/scott4game/market-bridge/internal/config"
)

func TestLogEnabledProviders(t *testing.T) {
	var output bytes.Buffer
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
	})

	cfg := config.Server{
		Provider:        "massive",
		DataVersion:     "massive-v1",
		MassivePlanName: "stocks_basic",
		MassiveAPIKey:   "must-not-be-logged",
		LiveProvider:    "longbridge",
		Watchlist:       []string{"AAPL", "NVDA"},
	}
	logEnabledProviders(cfg)

	got := output.String()
	for _, want := range []string{
		"Massive historical provider enabled: plan=stocks_basic, data_version=massive-v1",
		"Longbridge live provider enabled: watchlist=AAPL,NVDA",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("startup log %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, "must-not-be-logged") {
		t.Fatalf("startup log contains a credential: %q", got)
	}
}

func TestLogEnabledProvidersDoesNotLogMockProviders(t *testing.T) {
	var output bytes.Buffer
	originalWriter := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(originalWriter) })

	logEnabledProviders(config.Server{Provider: "mock", LiveProvider: "mock"})
	if output.Len() != 0 {
		t.Fatalf("unexpected startup log for mock providers: %q", output.String())
	}
}
