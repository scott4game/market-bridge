package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/provider"
)

const liveCall = "AAPL261016C200000.US"
const livePut = "AAPL261016P200000.US"

type liveOptionsStub struct {
	calls            atomic.Int32
	err              error
	started, release chan struct{}
}

func (s *liveOptionsStub) Expirations(ctx context.Context, _ string) ([]string, error) {
	s.calls.Add(1)
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []string{"2026-10-16"}, s.err
}
func (s *liveOptionsStub) Chain(context.Context, string, time.Time) ([]provider.LiveOptionContract, error) {
	s.calls.Add(1)
	strike := "200"
	return []provider.LiveOptionContract{{Symbol: liveCall, Type: "call", StrikePrice: &strike}, {Symbol: livePut, Type: "put", StrikePrice: &strike}}, s.err
}
func (s *liveOptionsStub) Quotes(context.Context, []string) ([]provider.LiveOptionQuote, error) {
	s.calls.Add(1)
	return []provider.LiveOptionQuote{{Symbol: liveCall, Volume: 9007199254740993}}, s.err
}
func (s *liveOptionsStub) Greeks(context.Context, []string) ([]provider.LiveOptionGreeks, error) {
	s.calls.Add(1)
	return []provider.LiveOptionGreeks{{Symbol: liveCall}}, s.err
}
func TestLiveOptionHTTP(t *testing.T) {
	stub := &liveOptionsStub{}
	svc := NewOptionLiveService(stub)
	handler := (&HTTP{Token: "secret", OptionsLive: svc}).Handler()
	get := func(path, token string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		handler.ServeHTTP(w, r)
		return w
	}
	if w := get("/v1/options/expirations?underlying=AAPL", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, path := range []string{"chain?underlying=AAPL", "chain?underlying=AAPL&expiration=2026-10-16&strike_gte=NaN", "chain?underlying=AAPL&expiration=2026-10-16&strike_gte=1e999999999", "chain?underlying=AAPL&expiration=2026-10-16&offset=-1", "chain?underlying=AAPL&expiration=2026-10-16&limit=501", "quotes?symbols=AAPL.US"} {
		if w := get("/v1/options/"+path, "secret"); w.Code != 400 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := get("/v1/options/chain?underlying=AAPL&expiration=2026-10-16&type=put&strike_gte=200&limit=1", "secret")
	if w.Code != 200 || !strings.Contains(w.Body.String(), livePut) || strings.Contains(w.Body.String(), liveCall) {
		t.Fatal(w.Body.String())
	}
	w = get("/v1/options/quotes?symbols="+liveCall+","+livePut, "secret")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"missing_symbols":["`+livePut+`"]`) || !strings.Contains(w.Body.String(), "9007199254740993") {
		t.Fatal(w.Body.String())
	}
	w = get("/v1/options/greeks?symbols="+liveCall, "secret")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"delta":null`) {
		t.Fatal(w.Body.String())
	}
	w = get("/v1/options/quotes?symbols="+liveCall+","+livePut, "secret")
	if !strings.Contains(w.Body.String(), `"cache_hit":true`) {
		t.Fatal(w.Body.String())
	}
	if svc.Status()["last_query_success_at"].(time.Time).IsZero() {
		t.Fatal("missing query status")
	}
	disabled := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/options/quotes", nil)
	r.Header.Set("Authorization", "Bearer secret")
	(&HTTP{Token: "secret"}).Handler().ServeHTTP(disabled, r)
	if disabled.Code != 503 {
		t.Fatal(disabled.Code)
	}
}
func TestLiveOptionCacheExpiryFailureAndCapacity(t *testing.T) {
	stub := &liveOptionsStub{}
	svc := NewOptionLiveService(stub)
	now := time.Now()
	svc.now = func() time.Time { return now }
	q, _ := parseOptionLiveRequest("expirations", url.Values{"underlying": {"AAPL"}})
	for range 2 {
		if _, err := svc.query(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	if stub.calls.Load() != 1 {
		t.Fatal(stub.calls.Load())
	}
	now = now.Add(5 * time.Minute)
	if _, err := svc.query(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if stub.calls.Load() != 2 {
		t.Fatal(stub.calls.Load())
	}
	now = now.Add(5 * time.Minute)
	stub.err = errors.New("secret")
	for range 2 {
		if _, err := svc.query(t.Context(), q); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	}
	if stub.calls.Load() != 4 {
		t.Fatal(stub.calls.Load())
	}
	if svc.Status()["last_error"] != "upstream_failure" {
		t.Fatal(svc.Status())
	}
	stub.err = nil
	for i := 0; i < 1001; i++ {
		key := string(rune(i))
		if _, _, err := svc.load(t.Context(), key, time.Minute, func(context.Context) (any, error) { return []string{}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if len(svc.cache) != 1000 {
		t.Fatal(len(svc.cache))
	}
}
func TestLiveOptionCanceledWaiterDoesNotCancelSharedFetch(t *testing.T) {
	stub := &liveOptionsStub{started: make(chan struct{}, 1), release: make(chan struct{})}
	svc := NewOptionLiveService(stub)
	q, _ := parseOptionLiveRequest("expirations", url.Values{"underlying": {"AAPL"}})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := svc.query(ctx, q); done <- err }()
	<-stub.started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected cancellation")
	}
	close(stub.release)
	if _, err := svc.query(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if stub.calls.Load() != 1 {
		t.Fatal(stub.calls.Load())
	}
}
func TestLiveOptionUpstreamErrorHTTP(t *testing.T) {
	for _, status := range []int{403, 429, 504, 502} {
		svc := NewOptionLiveService(&liveOptionsStub{err: &provider.OptionLiveError{Code: "safe", Status: status}})
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/v1/options/expirations?underlying=AAPL", nil)
		(&HTTP{OptionsLive: svc}).optionLive(w, r)
		if w.Code != status {
			t.Fatal(w.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
	}
}
