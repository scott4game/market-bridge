package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

func massiveTestSpec() market.DatasetSpec {
	from := time.Date(2026, 8, 24, 14, 0, 0, 0, time.UTC)
	return market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: from, To: from.Add(time.Minute), Session: market.RegularSession, Adjustment: market.SplitAdjusted}
}

func TestMassiveReportsHTTPStatusBeforeJSONDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "<html>upstream unavailable</html>")
	}))
	defer server.Close()
	_, err := (&Massive{APIKey: "test", BaseURL: server.URL}).Bars(context.Background(), massiveTestSpec())
	if err == nil || !strings.Contains(err.Error(), "status 502") || strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("error=%v", err)
	}
}

func TestMassiveRejectsPaginationCycle(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"status":"OK","next_url":%q,"results":[]}`, server.URL+r.URL.Path)
	}))
	defer server.Close()
	_, err := (&Massive{APIKey: "test", BaseURL: server.URL}).Bars(context.Background(), massiveTestSpec())
	if err == nil || !strings.Contains(err.Error(), "pagination cycle") {
		t.Fatalf("error=%v", err)
	}
}
