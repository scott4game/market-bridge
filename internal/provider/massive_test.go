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

func TestMassiveDividendFactorsPaginateAndAccumulate(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	now := time.Now().In(location)
	firstDate := now.AddDate(0, 0, -20).Format("2006-01-02")
	secondDate := now.AddDate(0, 0, -10).Format("2006-01-02")
	requests := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("ticker") != "SNDK" || r.URL.Query().Get("apiKey") != "test" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		if requests == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"next_url": server.URL + "/page/2", "results": []map[string]any{{"ex_dividend_date": firstDate, "historical_adjustment_factor": 0.9}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"ex_dividend_date": secondDate, "historical_adjustment_factor": 0.8}}})
	}))
	defer server.Close()
	curve, err := (&Massive{APIKey: "test", BaseURL: server.URL, HTTP: server.Client()}).ForwardAdjustmentFactors(context.Background(), "sndk.us")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || curve.Symbol != "SNDK" || curve.Version == "" || len(curve.Factors) != 2 {
		t.Fatalf("requests=%d curve=%+v", requests, curve)
	}
	if curve.Factors[0].Factor != market.DecimalFromFloat(0.72) || curve.Factors[1].Factor != market.DecimalFromFloat(0.8) {
		t.Fatalf("factors=%+v", curve.Factors)
	}
}

func TestMassiveDividendFactorsRejectCrossOriginAndReportHTTPStatus(t *testing.T) {
	t.Run("cross_origin", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"next_url":"https://example.com/page/2","results":[]}`)
		}))
		defer server.Close()
		_, err := (&Massive{APIKey: "test", BaseURL: server.URL, HTTP: server.Client()}).ForwardAdjustmentFactors(context.Background(), "SNDK")
		if err == nil || !strings.Contains(err.Error(), "rejected cross-origin") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("status_before_decode", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, "rate limited")
		}))
		defer server.Close()
		_, err := (&Massive{APIKey: "test", BaseURL: server.URL, HTTP: server.Client()}).ForwardAdjustmentFactors(context.Background(), "SNDK")
		if err == nil || !strings.Contains(err.Error(), "status 429") || strings.Contains(err.Error(), "invalid character") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestMassiveWeeklyForwardAdjustmentAppliesBeforeAggregation(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	monday := time.Date(2026, 8, 17, 9, 30, 0, 0, location)
	wednesday := monday.AddDate(0, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/stocks/v1/dividends":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"ex_dividend_date": wednesday.Format("2006-01-02"), "historical_adjustment_factor": 0.5}}})
		case strings.Contains(r.URL.Path, "/range/1/day/"):
			var results []map[string]any
			for day := 0; day < 5; day++ {
				results = append(results, map[string]any{"o": 100, "h": 100, "l": 100, "c": 100, "v": 10, "t": monday.AddDate(0, 0, day).UnixMilli()})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": results})
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer server.Close()
	spec := market.DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1w", From: monday.Add(-time.Hour).UTC(), To: monday.AddDate(0, 0, 7).UTC(), Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	bars, err := (&Massive{APIKey: "test", BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].Open != market.DecimalFromFloat(50) || bars[0].High != market.DecimalFromFloat(100) || bars[0].Close != market.DecimalFromFloat(100) || bars[0].Volume != 50 || bars[0].Timestamp.In(location).Day() != monday.Day() {
		t.Fatalf("bars=%+v", bars)
	}
}

func TestMassiveStocksBasicForwardAdjustmentLimitExplainsAlternatives(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	from := time.Now().In(location).AddDate(-2, 0, -1)
	spec := market.DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1d", From: from.UTC(), To: from.AddDate(0, 0, 1).UTC(), Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	_, err := (&Massive{APIKey: "test", PlanName: "stocks_basic"}).Bars(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "upgrade the Massive plan") || !strings.Contains(err.Error(), "split_adjusted") {
		t.Fatalf("err=%v", err)
	}
}
