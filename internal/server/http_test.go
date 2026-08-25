package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/provider"
)

type fakeUsageReader struct{ snapshot provider.UsageSnapshot }

type factorProvider struct{ version string }

type pinnedDatasetProvider struct {
	version string
	used    chan string
}

func (p *pinnedDatasetProvider) Name() string        { return "pinned-test" }
func (p *pinnedDatasetProvider) DataVersion() string { return "bars-v1" }
func (p *pinnedDatasetProvider) Bars(context.Context, market.DatasetSpec) ([]market.Bar, error) {
	p.used <- "unpinned"
	return nil, nil
}
func (p *pinnedDatasetProvider) ForwardAdjustmentFactors(_ context.Context, symbol string) (market.ForwardFactors, error) {
	return market.ForwardFactors{Symbol: symbol, Mode: market.ForwardAdjusted, AsOf: "2026-08-25", Version: p.version}, nil
}
func (p *pinnedDatasetProvider) BarsWithForwardFactors(_ context.Context, _ market.DatasetSpec, curves map[string]market.ForwardFactors) ([]market.Bar, error) {
	p.used <- curves["SNDK"].Version
	return nil, nil
}

func (p *factorProvider) Name() string        { return "factor-test" }
func (p *factorProvider) DataVersion() string { return "bars-v1" }
func (p *factorProvider) Bars(context.Context, market.DatasetSpec) ([]market.Bar, error) {
	return nil, nil
}
func (p *factorProvider) ForwardAdjustmentFactors(context.Context, string) (market.ForwardFactors, error) {
	return market.ForwardFactors{
		Symbol: "SNDK", Mode: market.ForwardAdjusted, AsOf: "2026-08-25", Version: p.version,
		Factors: []market.ForwardFactor{{EffectiveDate: "2026-08-20", Factor: market.DecimalFromFloat(0.9)}},
	}, nil
}

func (f fakeUsageReader) Snapshot(context.Context, string) (provider.UsageSnapshot, error) {
	return f.snapshot, nil
}

func TestMassiveUsageEndpointRequiresToken(t *testing.T) {
	h := (&HTTP{Token: "secret", Usage: fakeUsageReader{snapshot: provider.UsageSnapshot{
		Provider: "massive", UpdatedAt: time.Now().UTC(),
	}}}).Handler()

	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/providers/massive/usage", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/providers/massive/usage", nil)
	req.Header.Set("Authorization", "Bearer secret")
	ok := httptest.NewRecorder()
	h.ServeHTTP(ok, req)
	if ok.Code != http.StatusOK {
		t.Fatalf("authorized status=%d body=%s", ok.Code, ok.Body.String())
	}
	var got provider.UsageSnapshot
	if err := json.NewDecoder(ok.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Provider != "massive" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestMassiveUsageEndpointDisabled(t *testing.T) {
	rec := httptest.NewRecorder()
	(&HTTP{}).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/providers/massive/usage", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestForwardAdjustmentEndpointIsProtectedAndVersioned(t *testing.T) {
	p := &factorProvider{version: "factor-v1"}
	store, err := NewStore(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&HTTP{Store: store, Token: "secret"}).Handler()
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/market-history/adjustments/SNDK", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d", unauthorized.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/market-history/adjustments/SNDK", nil)
	req.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var curve market.ForwardFactors
	if err := json.NewDecoder(recorder.Body).Decode(&curve); err != nil {
		t.Fatal(err)
	}
	if curve.Symbol != "SNDK" || curve.AsOf != "2026-08-25" || curve.Version != "factor-v1" || len(curve.Factors) != 1 {
		t.Fatalf("curve=%+v", curve)
	}

	now := time.Now()
	spec := market.DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1d", From: now.Add(-time.Hour), To: now, Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	first, err := store.describe(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	p.version = "factor-v2"
	second, err := store.describe(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.DataVersion == second.DataVersion || !strings.Contains(first.DataVersion, "factor-v1") || !strings.Contains(second.DataVersion, "factor-v2") {
		t.Fatalf("first=%q second=%q", first.DataVersion, second.DataVersion)
	}
}

func TestDatasetBuildUsesFactorsCapturedDuringAdmission(t *testing.T) {
	p := &pinnedDatasetProvider{version: "factor-v1", used: make(chan string, 1)}
	store, err := NewStore(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	spec := market.DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1d", From: now.Add(-24 * time.Hour), To: now, Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	status, err := store.Ensure(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	p.version = "factor-v2"
	select {
	case used := <-p.used:
		if used != "factor-v1" {
			t.Fatalf("build used %q", used)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("build did not start")
	}
	waitForState(t, store, status.DatasetID, "ready")
	manifest, err := store.Manifest(status.DatasetID)
	if err != nil || !strings.Contains(manifest.DataVersion, "factor-v1") {
		t.Fatalf("manifest=%+v err=%v", manifest, err)
	}
}
