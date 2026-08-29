package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

func TestTushareAShareForwardAdjustedAndCalendarAggregation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			APIName string         `json:"api_name"`
			Token   string         `json:"token"`
			Params  map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Token != "secret" || request.Params["ts_code"] != "600519.SH" {
			t.Fatalf("request=%+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.APIName {
		case "daily":
			_, _ = w.Write([]byte(`{"code":0,"data":{"fields":["trade_date","ts_code","close","open","low","high","amount","vol"],"items":[["20260825","600519.SH",22,20,19,23,2.5,3],["20260824","600519.SH",12,10,9,13,1.5,2]]}}`))
		case "adj_factor":
			_, _ = w.Write([]byte(`{"code":0,"data":{"fields":["ts_code","trade_date","adj_factor"],"items":[["600519.SH","20260826",2],["600519.SH","20260825",2],["600519.SH","20260824",1]]}}`))
		default:
			t.Fatalf("unexpected api %q", request.APIName)
		}
	}))
	defer server.Close()
	location, _ := time.LoadLocation("Asia/Shanghai")
	from := time.Date(2026, 8, 24, 0, 0, 0, 0, location)
	p := &TushareAShare{Token: "secret", BaseURL: server.URL}
	if p.Supports(market.DatasetSpec{Interval: "1y"}) {
		t.Fatal("Tushare yearly history must remain unsupported")
	}
	spec := market.DatasetSpec{Symbols: []string{"600519.SH"}, Interval: "1w", From: from.UTC(), To: from.AddDate(0, 0, 7).UTC(), Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	bars, err := p.Bars(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 {
		t.Fatalf("bars=%+v", bars)
	}
	bar := bars[0]
	if bar.Open != market.DecimalFromFloat(5) || bar.High != market.DecimalFromFloat(23) || bar.Low != market.DecimalFromFloat(4.5) || bar.Close != market.DecimalFromFloat(22) {
		t.Fatalf("unexpected adjusted OHLC: %+v", bar)
	}
	if bar.Volume != 500 || bar.Turnover == nil || *bar.Turnover != market.DecimalFromFloat(4000) || bar.Source != "tushare" {
		t.Fatalf("unexpected units/source: %+v", bar)
	}
}

func TestTushareAShareSecuritiesAndErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			APIName string `json:"api_name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.APIName != "stock_basic" {
			t.Fatalf("api=%q", request.APIName)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"fields":["name","ts_code"],"items":[["贵州茅台","600519.SH"],["平安银行","000001.SZ"],["忽略","00001.HK"]]}}`))
	}))
	defer server.Close()
	p := &TushareAShare{Token: "secret", BaseURL: server.URL}
	securities, err := p.Securities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(securities) != 2 || securities[0].Symbol != "000001.SZ" || securities[1].NameCN != "贵州茅台" {
		t.Fatalf("securities=%+v", securities)
	}
	spec := market.DatasetSpec{Symbols: []string{"600519.SH"}, Interval: "30m", From: time.Now().Add(-time.Hour), To: time.Now(), Session: market.RegularSession, Adjustment: market.ForwardAdjusted}
	if _, err := p.Bars(context.Background(), spec); err == nil {
		t.Fatal("expected unsupported interval error")
	}
}

func TestTushareAShareReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":2002,"msg":"没有权限","data":{"fields":[],"items":[]}}`))
	}))
	defer server.Close()
	p := &TushareAShare{Token: "secret", BaseURL: server.URL}
	if _, err := p.Securities(context.Background()); err == nil {
		t.Fatal("expected permission error")
	}
}
