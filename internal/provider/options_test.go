package provider

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type optionRoundTripFunc func(*http.Request) (*http.Response, error)

func (f optionRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func optionResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestMassiveOptionsContractsAndAPIKey(t *testing.T) {
	var paths []string
	client := &http.Client{Transport: optionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.String())
		if r.URL.Query().Get("apiKey") != "secret" || r.Header.Get("Authorization") != "" {
			t.Fatalf("missing API key or unexpected authorization header")
		}
		return optionResponse(200, `{"status":"OK","results":[{"ticker":"O:NVDA261002P00190000","underlying_ticker":"NVDA","contract_type":"put","expiration_date":"2026-10-02","strike_price":190,"shares_per_contract":100,"exercise_style":"american","primary_exchange":"BATO"}]}`), nil
	})}
	p := &MassiveOptions{APIKey: "secret", BaseURL: "https://api.massive.test", HTTP: client}
	contracts, err := p.Contracts(context.Background(), OptionContractQuery{Underlying: "nvda", ContractType: "put", ExpirationFrom: "2026-10-01", ExpirationTo: "2026-10-10", StrikeGTE: 180, StrikeLTE: 200, AsOf: "2026-08-28"})
	if err != nil {
		t.Fatal(err)
	}
	if len(contracts) != 1 || contracts[0].Ticker != "O:NVDA261002P00190000" || contracts[0].StrikePrice != 190 {
		t.Fatalf("unexpected contracts: %#v", contracts)
	}
	if len(paths) != 1 || !strings.Contains(paths[0], "expiration_date.gte=2026-10-01") || !strings.Contains(paths[0], "expired=true") {
		t.Fatalf("unexpected request %v", paths)
	}
}

func TestMassiveOptionsRejectsCrossOriginPagination(t *testing.T) {
	client := &http.Client{Transport: optionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return optionResponse(200, `{"status":"OK","next_url":"https://evil.example/contracts"}`), nil
	})}
	_, err := (&MassiveOptions{APIKey: "secret", BaseURL: "https://api.massive.test", HTTP: client}).Contracts(context.Background(), OptionContractQuery{Underlying: "NVDA"})
	if err == nil || !strings.Contains(err.Error(), "cross-origin") {
		t.Fatalf("expected cross-origin error, got %v", err)
	}
}

func TestMassiveOptionsBarsRetries429AndParses(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	client := &http.Client{Transport: optionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if requests == 1 {
			response := optionResponse(429, `{"error":"rate limited"}`)
			response.Header.Set("Retry-After", "0")
			return response, nil
		}
		return optionResponse(200, `{"status":"OK","results":[{"o":1.1,"h":1.4,"l":1.0,"c":1.3,"v":6500,"t":1787932800000}]}`), nil
	})}
	bars, err := (&MassiveOptions{APIKey: "secret", BaseURL: "https://api.massive.test", HTTP: client}).OptionBars(context.Background(), "O:NVDA261002P00190000", time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(bars) != 1 || bars[0].Volume != 6500 || bars[0].Close.String() != "1.300000" {
		t.Fatalf("unexpected result requests=%d bars=%#v", requests, bars)
	}
}

func TestOptionContractQueryValidation(t *testing.T) {
	for _, query := range []OptionContractQuery{
		{},
		{Underlying: "700.HK"},
		{Underlying: "NVDA", ContractType: "straddle"},
		{Underlying: "NVDA", ExpirationFrom: "bad"},
		{Underlying: "NVDA", StrikeGTE: 200, StrikeLTE: 100},
		{Underlying: "NVDA", StrikeGTE: math.NaN()},
		{Underlying: "NVDA", StrikeLTE: math.Inf(1)},
	} {
		if _, err := query.Normalize(); err == nil {
			t.Fatalf("expected validation error for %#v", query)
		}
	}
}

func TestMassiveOptionsRedactsAPIKeyFromTransportErrors(t *testing.T) {
	client := &http.Client{Transport: optionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("request failed for " + r.URL.String())
	})}
	_, err := (&MassiveOptions{APIKey: "top-secret", BaseURL: "https://api.massive.test", HTTP: client}).Contracts(context.Background(), OptionContractQuery{Underlying: "NVDA"})
	if err == nil || strings.Contains(err.Error(), "top-secret") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("secret leaked or redaction missing: %v", err)
	}
}

func TestMassiveOptionsRedactsAPIKeyFromProviderErrors(t *testing.T) {
	client := &http.Client{Transport: optionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return optionResponse(http.StatusBadRequest, `{"error":"bad apiKey top-secret"}`), nil
	})}
	_, err := (&MassiveOptions{APIKey: "top-secret", BaseURL: "https://api.massive.test", HTTP: client}).Contracts(context.Background(), OptionContractQuery{Underlying: "NVDA"})
	if err == nil || strings.Contains(err.Error(), "top-secret") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("secret leaked or redaction missing: %v", err)
	}
}
