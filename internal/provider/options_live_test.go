package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	lbquote "github.com/longbridge/openapi-go/quote"
	protocol "github.com/longbridge/openapi-protocol/go"
	"github.com/shopspring/decimal"
)

const callOption = "AAPL261016C200000.US"
const putOption = "AAPL261016P200000.US"

type optionsClientStub struct {
	err        error
	underlying string
	indexes    []lbquote.CalcIndex
}

func (s *optionsClientStub) OptionChainExpiryDateList(_ context.Context, u string) ([]time.Time, error) {
	s.underlying = u
	return []time.Time{time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)}, s.err
}
func (s *optionsClientStub) OptionChainInfoByDate(_ context.Context, u string, _ *time.Time) ([]*lbquote.StrikePriceInfo, error) {
	s.underlying = u
	d := decimal.NewFromInt(200)
	return []*lbquote.StrikePriceInfo{nil, {Price: &d, CallSymbol: callOption, PutSymbol: putOption, Standard: false}}, s.err
}
func (s *optionsClientStub) OptionQuote(context.Context, []string) ([]*lbquote.OptionQuote, error) {
	d := decimal.RequireFromString("1.23000001")
	return []*lbquote.OptionQuote{nil, {Symbol: callOption, LastDone: &d, Timestamp: 1790000000, OptionExtend: &lbquote.OptionExtend{ExpiryDate: "20261016", ImpliedVolatility: "0.251", ContractMultiplier: "10", ContractSize: "50", ContractType: "A", Direction: "C"}}, {Symbol: putOption}}, s.err
}
func (s *optionsClientStub) CalcIndex(_ context.Context, _ []string, indexes []lbquote.CalcIndex) ([]*lbquote.SecurityCalcIndex, error) {
	s.indexes = indexes
	zero := decimal.Zero
	return []*lbquote.SecurityCalcIndex{{Symbol: callOption, Delta: &zero}}, s.err
}
func TestLongbridgeOptionsMapping(t *testing.T) {
	stub := &optionsClientStub{}
	p := &LongbridgeOptions{Quote: stub}
	dates, err := p.Expirations(t.Context(), "aapl")
	if err != nil || len(dates) != 1 || stub.underlying != "AAPL.US" {
		t.Fatalf("dates=%v err=%v", dates, err)
	}
	rows, err := p.Chain(t.Context(), "AAPL", time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
	if err != nil || len(rows) != 2 || rows[0].Type != "call" || rows[0].Standard || *rows[0].StrikePrice != "200" {
		t.Fatalf("chain=%+v err=%v", rows, err)
	}
	quotes, err := p.Quotes(t.Context(), []string{callOption, putOption})
	if err != nil || len(quotes) != 2 {
		t.Fatal(err)
	}
	q := quotes[0]
	if *q.Last != "1.23000001" || *q.ContractMultiplier != "10" || *q.ContractSize != "50" || q.Expiration != "2026-10-16" || q.ExerciseStyle != "american" || q.Timestamp == nil {
		t.Fatalf("quote=%+v", q)
	}
	if quotes[1].Last != nil || quotes[1].OpenInterest != nil || quotes[1].Timestamp != nil {
		t.Fatalf("absent fields must be null: %+v", quotes[1])
	}
	greeks, err := p.Greeks(t.Context(), []string{callOption})
	if err != nil || len(stub.indexes) != 5 || *greeks[0].Delta != "0" || greeks[0].Gamma != nil {
		t.Fatalf("greeks=%+v err=%v", greeks, err)
	}
}
func TestNormalizeLiveOptionSymbols(t *testing.T) {
	for _, raw := range [][]string{nil, {"AAPL.US"}, {"O:AAPL261016C00200000"}, {""}, {"AAPL261016C200000.HK"}, make([]string, 101)} {
		if _, err := NormalizeOptionSymbols(raw); err == nil {
			t.Fatalf("accepted %v", raw)
		}
	}
	out, err := NormalizeOptionSymbols([]string{putOption, strings.ToLower(callOption), callOption})
	if err != nil || len(out) != 2 || out[0] != callOption {
		t.Fatalf("%v %v", out, err)
	}
	for _, s := range []string{"", "700.HK", "I:SPX", "AAPL.SH"} {
		if _, err := NormalizeOptionUnderlying(s); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
func TestLiveOptionErrorRedaction(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   string
		status int
	}{{context.DeadlineExceeded, "upstream_timeout", 504}, {context.Canceled, "request_canceled", 408}, {protocol.NewError(7, 301604, "secret-token"), "quote_permission_denied", 403}, {protocol.NewError(3, 301606, "secret-token"), "upstream_rate_limited", 429}, {errors.New("secret-token"), "upstream_failure", 502}} {
		e := SafeOptionLiveError(tc.err)
		if e.Code != tc.code || e.Status != tc.status || strings.Contains(e.Error(), "secret") {
			t.Fatalf("%+v", e)
		}
	}
}
