package provider

import (
	"context"
	"encoding/json"
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

func TestMassiveBuildsFutuHourBarsFromThirtyMinutes(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	day := time.Date(2026, 8, 24, 0, 0, 0, 0, location)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/range/30/minute/") || r.URL.Query().Get("adjusted") != "true" {
			t.Fatalf("unexpected request %s", r.URL.String())
		}
		results := []map[string]any{}
		for index, minute := range []int{9 * 60, 9*60 + 30, 10 * 60, 10*60 + 30, 11 * 60, 15*60 + 30, 16 * 60} {
			ts := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, location)
			price := float64(index + 1)
			results = append(results, map[string]any{"o": price, "h": price, "l": price, "c": price, "v": 10, "t": ts.UnixMilli()})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": results})
	}))
	defer server.Close()
	spec := market.DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1h", From: day.UTC(), To: day.Add(24 * time.Hour).UTC(), Session: market.RegularSession, Adjustment: market.SplitAdjusted}
	bars, err := (&Massive{APIKey: "test", BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 3 {
		t.Fatalf("bars=%d: %+v", len(bars), bars)
	}
	if got := bars[0].Timestamp.In(location).Format("15:04"); got != "09:30" {
		t.Fatalf("first timestamp=%s", got)
	}
	if bars[0].Open != market.DecimalFromFloat(2) || bars[0].Close != market.DecimalFromFloat(3) || bars[0].Volume != 20 {
		t.Fatalf("first=%+v", bars[0])
	}
}

func TestMassiveForwardAdjustmentUsesDividendFactor(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	today := time.Now().In(location)
	current := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, location)
	previous := current.AddDate(0, 0, -1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/stocks/v1/dividends":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": []map[string]any{{"ex_dividend_date": current.Format("2006-01-02"), "historical_adjustment_factor": 0.5}}})
		case strings.Contains(r.URL.Path, "/range/1/day/"):
			if r.URL.Query().Get("adjusted") != "true" {
				t.Fatalf("forward adjustment must fetch split-adjusted bars")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": []map[string]any{
				{"o": 100, "h": 110, "l": 90, "c": 105, "v": 10, "t": previous.UnixMilli()},
				{"o": 100, "h": 110, "l": 90, "c": 105, "v": 20, "t": current.UnixMilli()},
			}})
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer server.Close()
	spec := market.DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1d", From: previous.UTC(), To: current.AddDate(0, 0, 1).UTC(), Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	bars, err := (&Massive{APIKey: "test", BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 || bars[0].Open != market.DecimalFromFloat(50) || bars[0].Volume != 10 || bars[1].Open != market.DecimalFromFloat(100) {
		t.Fatalf("bars=%+v", bars)
	}
}
