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

	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/market"
	shopdecimal "github.com/shopspring/decimal"
)

type indexLongbridgeStub struct {
	symbol string
	period lbquote.Period
	adjust lbquote.AdjustType
	bar    *lbquote.Candlestick
}

func (s *indexLongbridgeStub) HistoryCandlesticksByOffset(_ context.Context, symbol string, period lbquote.Period, adjust lbquote.AdjustType, _ bool, _ *time.Time, _ int32, _ ...lbquote.CandlestickRequestOption) ([]*lbquote.Candlestick, error) {
	s.symbol, s.period, s.adjust = symbol, period, adjust
	if s.bar == nil {
		return nil, nil
	}
	bar := s.bar
	s.bar = nil
	return []*lbquote.Candlestick{bar}, nil
}

func TestLongbridgeIndexMapsAndRestoresSymbol(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	from := time.Date(2026, 8, 24, 9, 30, 0, 0, location)
	one := shopdecimal.NewFromInt(1)
	stub := &indexLongbridgeStub{bar: &lbquote.Candlestick{Open: &one, High: &one, Low: &one, Close: &one, Volume: 7, Timestamp: from.Unix()}}
	spec := market.DatasetSpec{Symbols: []string{"I:IXIC"}, Interval: "1m", From: from.UTC(), To: from.Add(time.Minute).UTC(), Adjustment: market.Raw}
	bars, err := (&LongbridgeIndex{Quote: stub}).Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if stub.symbol != ".IXIC.US" || stub.adjust != lbquote.AdjustTypeNo || len(bars) != 1 || bars[0].Symbol != "I:IXIC" || bars[0].Source != "longbridge" {
		t.Fatalf("request=%s adjust=%v bars=%+v", stub.symbol, stub.adjust, bars)
	}
}

func TestLongbridgeIndexRejectsUnsupportedSymbol(t *testing.T) {
	from := time.Now().Add(-time.Hour)
	spec := market.DatasetSpec{Symbols: []string{"I:VIX"}, Interval: "1m", From: from, To: from.Add(time.Minute), Adjustment: market.Raw}
	_, err := (&LongbridgeIndex{Quote: &indexLongbridgeStub{}}).Bars(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "I:IXIC") || !strings.Contains(err.Error(), "does not support I:VIX") {
		t.Fatalf("err=%v", err)
	}
}

func TestFMPIndexIntradayMappingAndAggregation(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	from := time.Date(2026, 8, 24, 9, 30, 0, 0, location)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stable/historical-chart/1min" || r.URL.Query().Get("symbol") != "^VIX" || r.URL.Query().Get("apikey") != "secret" {
			t.Fatalf("request=%s", r.URL.String())
		}
		rows := []map[string]any{}
		for index := 0; index < 3; index++ {
			rows = append(rows, map[string]any{"date": from.Add(time.Duration(index) * time.Minute).Format("2006-01-02 15:04:05"), "open": 10 + index, "high": 11 + index, "low": 9 + index, "close": 10.5 + float64(index), "volume": 2})
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer server.Close()
	spec := market.DatasetSpec{Symbols: []string{"I:VIX"}, Interval: "3m", From: from.UTC(), To: from.Add(3 * time.Minute).UTC(), Adjustment: market.Raw}
	bars, err := (&FMPIndex{APIKey: "secret", BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].Symbol != "I:VIX" || bars[0].Open != market.DecimalFromFloat(10) || bars[0].Close != market.DecimalFromFloat(12.5) || bars[0].Volume != 6 {
		t.Fatalf("bars=%+v", bars)
	}
}

func TestFMPIndexDailyCalendarAggregation(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, location)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stable/historical-price-eod/full" || r.URL.Query().Get("symbol") != "^NDX" {
			t.Fatalf("request=%s", r.URL.String())
		}
		var rows []map[string]any
		for day := 0; day < 5; day++ {
			rows = append(rows, map[string]any{"date": monday.AddDate(0, 0, day).Format("2006-01-02"), "open": 100, "high": 102, "low": 99, "close": 101 + day, "volume": 1})
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer server.Close()
	spec := market.DatasetSpec{Symbols: []string{"I:NDX"}, Interval: "1w", From: monday.UTC(), To: monday.AddDate(0, 0, 7).UTC(), Adjustment: market.Raw}
	bars, err := (&FMPIndex{APIKey: "secret", BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].Close != market.DecimalFromFloat(105) || bars[0].Volume != 5 {
		t.Fatalf("bars=%+v", bars)
	}
}

func TestFMPIndexErrorsAreClearAndRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"Error Message":"key secret is not entitled"}`)
	}))
	defer server.Close()
	from := time.Now().Add(-time.Hour)
	spec := market.DatasetSpec{Symbols: []string{"I:VIX"}, Interval: "1m", From: from, To: from.Add(time.Minute), Adjustment: market.Raw}
	_, err := (&FMPIndex{APIKey: "secret", BaseURL: server.URL, HTTP: server.Client()}).Bars(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "status 403") || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("err=%v", err)
	}
}
