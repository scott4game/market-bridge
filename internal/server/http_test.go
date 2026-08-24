package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/provider"
)

type fakeUsageReader struct{ snapshot provider.UsageSnapshot }

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
