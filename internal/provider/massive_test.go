package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

func massiveTestSpec() market.DatasetSpec {
	from := time.Date(2026, 8, 24, 14, 0, 0, 0, time.UTC)
	return market.DatasetSpec{Symbols: []string{"AAPL"}, Interval: "1m", From: from, To: from.Add(time.Minute), Session: market.RegularSession, Adjustment: market.SplitAdjusted}
}

func TestMockSupportsUSForwardAdjustment(t *testing.T) {
	mock := &Mock{}
	curve, err := mock.ForwardAdjustmentFactors(context.Background(), "sndk.us")
	if err != nil || curve.Symbol != "SNDK" || curve.Version == "" || len(curve.Factors) != 0 {
		t.Fatalf("curve=%+v err=%v", curve, err)
	}
	spec := massiveTestSpec()
	spec.Adjustment = market.ForwardAdjusted
	if _, err := mock.Bars(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
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

func TestMassiveReadsIndexWithNativeTicker(t *testing.T) {
	from := time.Date(2026, 8, 24, 14, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/v2/aggs/ticker/I:VIX/range/1/minute/") {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.URL.Query().Get("adjusted") != "false" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": []map[string]any{{"o": 14.1, "h": 14.3, "l": 14.0, "c": 14.2, "t": from.UnixMilli()}}})
	}))
	defer server.Close()
	spec := market.DatasetSpec{Symbols: []string{"I:VIX"}, Interval: "1m", From: from, To: from.Add(time.Minute), Session: market.RegularSession, Adjustment: market.Raw}
	bars, err := (&Massive{APIKey: "test", BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].Symbol != "I:VIX" || bars[0].Close != market.DecimalFromFloat(14.2) || bars[0].Volume != 0 {
		t.Fatalf("bars=%+v", bars)
	}
}

func TestMassiveReadsFuturesFromFuturesEndpoint(t *testing.T) {
	from := time.Date(2026, 8, 24, 0, 0, 0, 123, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/futures/v1/aggs/MNQZ6" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("resolution") != "3min" || query.Get("window_start.gte") != fmt.Sprint(from.UnixNano()) || query.Get("window_start.lt") != fmt.Sprint(from.Add(3*time.Minute).UnixNano()) || query.Get("adjusted") != "" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": []map[string]any{{"open": 23000.25, "high": 23010.5, "low": 22990.0, "close": 23005.75, "volume": 42, "window_start": from.UnixNano()}}})
	}))
	defer server.Close()
	spec := market.DatasetSpec{Symbols: []string{"F:MNQZ6"}, Interval: "3m", From: from, To: from.Add(3 * time.Minute), Session: market.ContinuousSession, Adjustment: market.Raw}
	bars, err := (&Massive{APIKey: "test", BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].Symbol != "F:MNQZ6" || !bars[0].Timestamp.Equal(from) || bars[0].Close != market.DecimalFromFloat(23005.75) || bars[0].Volume != 42 {
		t.Fatalf("bars=%+v", bars)
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
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "next_url": server.URL + "/page/2", "results": []map[string]any{{"ex_dividend_date": firstDate, "historical_adjustment_factor": 0.9}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": []map[string]any{{"ex_dividend_date": secondDate, "historical_adjustment_factor": 0.8}}})
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
			fmt.Fprint(w, `{"status":"OK","next_url":"https://example.com/page/2","results":[]}`)
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

func TestMassiveDividendFactorsRequireExplicitLogicalSuccess(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "logical_error", body: `{"status":"ERROR","error":"not entitled","results":[]}`, want: "not entitled"},
		{name: "missing_status", body: `{"results":[]}`, want: "unexpected status"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			_, err := (&Massive{APIKey: "test", BaseURL: server.URL, HTTP: server.Client()}).ForwardAdjustmentFactors(context.Background(), "SNDK")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestMassiveForwardAdjustmentCanUsePinnedFactors(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	day := time.Date(2026, 8, 24, 9, 30, 0, 0, location)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stocks/v1/dividends" {
			t.Fatal("pinned build fetched a new factor curve")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": []map[string]any{{"o": 100, "h": 100, "l": 100, "c": 100, "v": 10, "t": day.UnixMilli()}}})
	}))
	defer server.Close()
	spec := market.DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1d", From: day.Add(-time.Hour).UTC(), To: day.AddDate(0, 0, 1).UTC(), Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	curve := market.ForwardFactors{Symbol: "SNDK", Mode: market.ForwardAdjusted, AsOf: "2026-08-25", Version: "pinned-v1"}
	bars, err := (&Massive{APIKey: "test", BaseURL: server.URL, HTTP: server.Client()}).BarsWithForwardFactors(context.Background(), spec, map[string]market.ForwardFactors{"SNDK": curve})
	if err != nil || len(bars) != 1 {
		t.Fatalf("bars=%+v err=%v", bars, err)
	}
}

func TestMassiveWeeklyForwardAdjustmentAppliesBeforeAggregation(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	monday := time.Date(2026, 8, 17, 9, 30, 0, 0, location)
	wednesday := monday.AddDate(0, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/stocks/v1/dividends":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": []map[string]any{{"ex_dividend_date": wednesday.Format("2006-01-02"), "historical_adjustment_factor": 0.5}}})
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

func TestMassiveWeeklyForwardAdjustmentDoesNotReturnPartialFirstWeek(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	monday := time.Date(2026, 8, 17, 9, 30, 0, 0, location)
	from := monday.AddDate(0, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/stocks/v1/dividends":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": []any{}})
		case strings.Contains(r.URL.Path, "/range/1/day/"):
			parts := strings.Split(r.URL.Path, "/")
			requestedFrom, err := time.ParseDuration(parts[len(parts)-2] + "ms")
			if err != nil || requestedFrom.Milliseconds() > monday.UnixMilli() {
				t.Fatalf("daily fetch did not expand to period start: %s", r.URL.Path)
			}
			var results []map[string]any
			for week := 0; week < 2; week++ {
				for day := 0; day < 5; day++ {
					ts := monday.AddDate(0, 0, week*7+day)
					results = append(results, map[string]any{"o": 100 + week, "h": 100 + week, "l": 100 + week, "c": 100 + week, "v": 10, "t": ts.UnixMilli()})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": results})
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer server.Close()
	spec := market.DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1w", From: from.UTC(), To: monday.AddDate(0, 0, 14).UTC(), Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	bars, err := (&Massive{APIKey: "test", BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].Timestamp.In(location).Day() != monday.AddDate(0, 0, 7).Day() || bars[0].Open != market.DecimalFromFloat(101) {
		t.Fatalf("bars=%+v", bars)
	}
}

func TestStartOfUSCalendarPeriod(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	value := time.Date(2026, 8, 26, 12, 0, 0, 0, location)
	for interval, want := range map[string]string{"1w": "2026-08-24", "1mo": "2026-08-01", "1y": "2026-01-01"} {
		if got := startOfUSCalendarPeriod(value, interval, location).In(location).Format("2006-01-02"); got != want {
			t.Fatalf("interval=%s got=%s want=%s", interval, got, want)
		}
	}
}

func TestMassiveStockPlanHistoryWindows(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, location)
	for plan, want := range map[string]string{
		"basic": "2024-08-30", "stocks-basic": "2024-08-30",
		"Starter": "2021-08-30", "stocks_starter": "2021-08-30",
		"developer": "2016-08-30", "stocks_advanced": "2006-08-30",
	} {
		got, ok := massiveStockHistoryStart(plan, now, time.UTC)
		if !ok || got.In(location).Format("2006-01-02") != want {
			t.Fatalf("plan=%s got=%s ok=%v want=%s", plan, got, ok, want)
		}
	}
	if _, ok := massiveStockHistoryStart("custom", now, time.UTC); ok {
		t.Fatal("custom plans must not receive an inferred history limit")
	}
}

func TestMassiveStocksStarterClampsHistoryToFiveYears(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	want, _ := massiveStockHistoryStart("stocks_starter", time.Now(), time.UTC)
	var ranges [][2]time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		fromMillis, fromErr := strconv.ParseInt(parts[len(parts)-2], 10, 64)
		toMillis, toErr := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		if fromErr != nil || toErr != nil {
			t.Fatalf("path=%s from_err=%v to_err=%v", r.URL.Path, fromErr, toErr)
		}
		ranges = append(ranges, [2]time.Time{time.UnixMilli(fromMillis), time.UnixMilli(toMillis + 1)})
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": []map[string]any{}})
	}))
	defer server.Close()
	spec := market.DatasetSpec{
		Symbols: []string{"SNDK"}, Interval: "1d",
		From: time.Now().In(location).AddDate(-10, 0, 0).UTC(), To: time.Now().Add(time.Hour),
		Session: market.RegularSession, Adjustment: market.SplitAdjusted,
	}
	if _, err := (&Massive{APIKey: "test", PlanName: "stocks_starter", BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 5 || !ranges[len(ranges)-1][0].Equal(want) {
		t.Fatalf("ranges=%v want_start=%v", ranges, want)
	}
	for index := 1; index < len(ranges); index++ {
		if !ranges[index][0].Before(ranges[index-1][0]) {
			t.Fatalf("Massive ranges must be fetched newest first: %v", ranges)
		}
	}
	for _, item := range ranges {
		if item[1].After(item[0].AddDate(1, 0, 0)) {
			t.Fatalf("Massive request exceeds one year: %v", item)
		}
	}
}

func TestMassiveReturnsRecentBarsWhenOlderRangeIsForbidden(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"status":"NOT_AUTHORIZED","message":"timeframe not included"}`))
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		fromMillis, err := strconv.ParseInt(parts[len(parts)-2], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "OK",
			"results": []map[string]any{{"o": 100, "h": 101, "l": 99, "c": 100, "v": 10, "t": fromMillis}},
		})
	}))
	defer server.Close()
	now := time.Now().UTC().Truncate(time.Hour)
	spec := market.DatasetSpec{
		Symbols: []string{"AAPL"}, Interval: "1d", From: now.AddDate(-2, 0, 0), To: now,
		Session: market.RegularSession, Adjustment: market.SplitAdjusted,
	}
	bars, err := (&Massive{APIKey: "test", PlanName: "custom", BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "status 403") {
		t.Fatalf("err=%v", err)
	}
	if calls != 2 || len(bars) != 1 || bars[0].Timestamp.Before(now.AddDate(-1, 0, -1)) {
		t.Fatalf("calls=%d bars=%v", calls, bars)
	}
}

func TestMassiveDividendFactorsUsePlanHistoryFloor(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	want, _ := massiveStockHistoryStart("stocks_starter", time.Now(), location)
	var ranges [][2]time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, fromErr := time.ParseInLocation("2006-01-02", r.URL.Query().Get("ex_dividend_date.gte"), location)
		to, toErr := time.ParseInLocation("2006-01-02", r.URL.Query().Get("ex_dividend_date.lte"), location)
		if fromErr != nil || toErr != nil {
			t.Fatalf("query=%s from_err=%v to_err=%v", r.URL.RawQuery, fromErr, toErr)
		}
		ranges = append(ranges, [2]time.Time{from, to})
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": []map[string]any{}})
	}))
	defer server.Close()
	if _, err := (&Massive{APIKey: "test", PlanName: "stocks_starter", BaseURL: server.URL, HTTP: server.Client()}).ForwardAdjustmentFactors(context.Background(), "NVDA"); err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 5 || ranges[0][0].Format("2006-01-02") != want.In(location).Format("2006-01-02") {
		t.Fatalf("ranges=%v want_start=%v", ranges, want)
	}
	for _, item := range ranges {
		if item[1].After(item[0].AddDate(1, 0, -1)) {
			t.Fatalf("Massive dividend request exceeds one year: %v", item)
		}
	}
}

func TestMassiveStocksStarterSkipsRangesOlderThanFiveYears(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK"})
	}))
	defer server.Close()
	from := time.Now().AddDate(-10, 0, 0)
	spec := market.DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1d", From: from, To: from.AddDate(0, 1, 0), Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	bars, err := (&Massive{APIKey: "test", PlanName: "stocks_starter", BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec)
	if err != nil || len(bars) != 0 || requests != 0 {
		t.Fatalf("bars=%v requests=%d err=%v", bars, requests, err)
	}
}

func TestMassiveStockPlanDoesNotLimitIndices(t *testing.T) {
	from := time.Date(2010, 1, 4, 14, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, fmt.Sprintf("/%d/", from.UnixMilli())) {
			t.Fatalf("index history was clamped: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "results": []map[string]any{}})
	}))
	defer server.Close()
	spec := market.DatasetSpec{Symbols: []string{"I:VIX"}, Interval: "1d", From: from, To: from.AddDate(0, 0, 1), Session: market.RegularSession, Adjustment: market.Raw}
	if _, err := (&Massive{APIKey: "test", PlanName: "stocks_starter", BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
}
