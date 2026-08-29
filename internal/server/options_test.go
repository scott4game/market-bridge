package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/provider"
)

type optionsRoundTripFunc func(*http.Request) (*http.Response, error)

func (f optionsRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type fakeOptionsProvider struct {
	mu            sync.Mutex
	contractCalls int
	barCalls      int
	contractBlock <-chan struct{}
	contractStart chan struct{}
	startOnce     sync.Once
}

func (f *fakeOptionsProvider) Contracts(_ context.Context, q provider.OptionContractQuery) ([]provider.OptionContract, error) {
	if f.contractStart != nil {
		f.startOnce.Do(func() { close(f.contractStart) })
	}
	if f.contractBlock != nil {
		<-f.contractBlock
	}
	f.mu.Lock()
	f.contractCalls++
	f.mu.Unlock()
	return []provider.OptionContract{{Ticker: "O:" + q.Underlying + "261002P00190000", Underlying: q.Underlying, ContractType: "put", ExpirationDate: "2026-10-02", StrikePrice: 190, SharesPerContract: 100, Source: "massive"}}, nil
}

func TestOptionCatalogCoalescesConcurrentRequests(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	fake := &fakeOptionsProvider{contractBlock: block, contractStart: started}
	catalog, err := OpenOptionCatalog(filepath.Join(t.TempDir(), "options.db"), fake)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	query := provider.OptionContractQuery{Underlying: "NVDA", ContractType: "call"}
	errors := make(chan error, 8)
	for range 8 {
		go func() {
			_, _, err := catalog.Contracts(context.Background(), query)
			errors <- err
		}()
	}
	<-started
	close(block)
	for range 8 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if fake.contractCalls != 1 {
		t.Fatalf("provider calls=%d want 1", fake.contractCalls)
	}
}

func (f *fakeOptionsProvider) OptionBars(_ context.Context, contract string, _, _ time.Time) ([]provider.OptionBar, error) {
	f.mu.Lock()
	f.barCalls++
	f.mu.Unlock()
	return []provider.OptionBar{{Contract: contract, Timestamp: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC), Open: market.DecimalFromFloat(1.1), High: market.DecimalFromFloat(1.4), Low: market.DecimalFromFloat(1), Close: market.DecimalFromFloat(1.3), Volume: 6500, Source: "massive", Completed: true}}, nil
}

func TestOptionCatalogCachesContractsAndBars(t *testing.T) {
	fake := &fakeOptionsProvider{}
	catalog, err := OpenOptionCatalog(filepath.Join(t.TempDir(), "options.db"), fake)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	query := provider.OptionContractQuery{Underlying: "NVDA", ContractType: "put"}
	first, source, err := catalog.Contracts(t.Context(), query)
	if err != nil || source != "massive" || len(first) != 1 {
		t.Fatalf("first: %s %#v %v", source, first, err)
	}
	second, source, err := catalog.Contracts(t.Context(), query)
	if err != nil || source != "cache" || len(second) != 1 {
		t.Fatalf("second: %s %#v %v", source, second, err)
	}
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	if _, source, err = catalog.Bars(t.Context(), first[0].Ticker, from, to); err != nil || source != "massive" {
		t.Fatalf("bars first: %s %v", source, err)
	}
	if bars, cachedSource, err := catalog.Bars(t.Context(), first[0].Ticker, from, to); err != nil || cachedSource != "cache" || len(bars) != 1 {
		t.Fatalf("bars second: %s %#v %v", cachedSource, bars, err)
	}
	if fake.contractCalls != 1 || fake.barCalls != 1 {
		t.Fatalf("provider calls contracts=%d bars=%d", fake.contractCalls, fake.barCalls)
	}
}

func TestOptionHTTPValidationAndResponse(t *testing.T) {
	catalog, err := OpenOptionCatalog(filepath.Join(t.TempDir(), "options.db"), &fakeOptionsProvider{})
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	handler := (&HTTP{Token: "secret", Options: catalog}).Handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/options/contracts?underlying=NVDA", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status %d", unauthorized.Code)
	}

	bad := httptest.NewRecorder()
	badRequest := httptest.NewRequest(http.MethodGet, "/v1/options/contracts?underlying=NVDA&strike_gte=nope", nil)
	badRequest.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(bad, badRequest)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad status %d", bad.Code)
	}

	missingUnderlying := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodGet, "/v1/options/contracts", nil)
	missingRequest.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(missingUnderlying, missingRequest)
	if missingUnderlying.Code != http.StatusBadRequest {
		t.Fatalf("missing underlying status %d", missingUnderlying.Code)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/options/contracts?underlying=NVDA&type=put", nil)
	request.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Source string `json:"source"`
		Count  int    `json:"count"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil || payload.Source != "massive" || payload.Count != 1 {
		t.Fatalf("payload=%#v err=%v", payload, err)
	}

	bars := httptest.NewRecorder()
	barRequest := httptest.NewRequest(http.MethodGet, "/v1/options/bars/O:NVDA261002P00190000?from=2026-08-01&to=2026-08-29", nil)
	barRequest.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(bars, barRequest)
	if bars.Code != http.StatusOK {
		t.Fatalf("bars status=%d body=%s", bars.Code, bars.Body.String())
	}
}

func TestMassiveOptionsMockEndToEnd(t *testing.T) {
	client := &http.Client{Transport: optionsRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "api.massive.test" || request.URL.Query().Get("apiKey") != "secret" {
			t.Fatalf("unexpected upstream request %s", request.URL.Redacted())
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"status":"OK","results":[{"ticker":"O:NVDA261002C00200000","underlying_ticker":"NVDA","contract_type":"call","expiration_date":"2026-10-02","strike_price":200,"shares_per_contract":100}]}`))}, nil
	})}
	source := &provider.MassiveOptions{APIKey: "secret", BaseURL: "https://api.massive.test", HTTP: client}
	catalog, err := OpenOptionCatalog(filepath.Join(t.TempDir(), "options.db"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	handler := (&HTTP{Token: "server-token", Options: catalog}).Handler()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/options/contracts?underlying=NVDA&type=call&expiration_from=2026-10-01&expiration_to=2026-10-31", nil)
	request.Header.Set("Authorization", "Bearer server-token")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "O:NVDA261002C00200000") || strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestMassiveOptionsUsageEndpoint(t *testing.T) {
	handler := (&HTTP{Token: "secret", OptionsUsage: fakeUsageReader{snapshot: provider.UsageSnapshot{Provider: "massive_options"}}}).Handler()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/providers/massive-options/usage", nil)
	request.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "massive_options") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
