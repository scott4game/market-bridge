package localclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/market"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGatewayPreservesHTTPFailureAndSanitizesBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "77")
		w.WriteHeader(429)
		fmt.Fprint(w, "plain error token=DO_NOT_LEAK")
	}))
	defer server.Close()
	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	now := time.Now()
	spec := market.DatasetSpec{Symbols: []string{"A"}, Interval: "1d", From: now.Add(-time.Hour), To: now, Session: market.RegularSession, Adjustment: market.Raw}
	_, _, err = cache.remoteHistoryBars(context.Background(), spec, true)
	detail := market.ErrorDetails(err)
	if detail.UpstreamStatus != 429 || detail.RetryAfterSeconds != 77 || strings.Contains(err.Error(), "DO_NOT_LEAK") {
		t.Fatal(err)
	}
	raw, status, err := cache.ServerJSON(t.Context(), http.MethodGet, "/v1/market-history/adjustments/A", nil)
	if err != nil || status != 429 || strings.Contains(string(raw), "DO_NOT_LEAK") {
		t.Fatalf("status=%d raw=%s err=%v", status, raw, err)
	}
	var payload struct {
		Details *market.DataError `json:"error_details"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Details == nil || payload.Details.RetryAfterSeconds != 77 {
		t.Fatalf("%s", raw)
	}
}
func TestClientRejectsObsoleteFactorEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"symbol":"A","mode":"forward_adjusted","as_of":"2026-10-05","version":"massive-qfq-v3:A:old","factors":[]}`)
	}))
	defer server.Close()
	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	if _, err = cache.forwardFactors(t.Context(), "A"); err == nil || !strings.Contains(err.Error(), "obsolete") {
		t.Fatal(err)
	}
}

func TestRedactedErrorPreservesCancellation(t *testing.T) {
	cache := &Cache{cfg: config.Client{ServerToken: "test-secret"}}
	err := cache.redactError(fmt.Errorf("upstream test-secret: %w", context.Canceled))
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("%v", err)
	}
}
