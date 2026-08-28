package provider

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type securityProfileRoundTripper func(*http.Request) (*http.Response, error)

func (fn securityProfileRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestMassiveSecurityProfile(t *testing.T) {
	client := &http.Client{Transport: securityProfileRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v3/reference/tickers/MRNA" || req.URL.Query().Get("apiKey") != "secret" {
			t.Fatalf("request URL=%s", req.URL.String())
		}
		body := `{"results":{"ticker":"MRNA","name":"Moderna, Inc.","cik":"0001682852","type":"CS","active":true,"locale":"us","market":"stocks","primary_exchange":"XNAS","market_cap":42000000000,"sic_code":"2834","sic_description":"PHARMACEUTICAL PREPARATIONS"}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	profile, err := (&Massive{APIKey: "secret", BaseURL: "https://api.massive.test", HTTP: client}).SecurityProfile(t.Context(), "mrna")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Symbol != "MRNA" || profile.CIK != "0001682852" || profile.MarketCap != 42000000000 || profile.SICCode != "2834" || profile.Provider != "massive" {
		t.Fatalf("profile=%+v", profile)
	}
}

func TestMassiveSecurityProfileRejectsNonUSSymbol(t *testing.T) {
	_, err := (&Massive{APIKey: "secret"}).SecurityProfile(t.Context(), "700.HK")
	if err == nil || !strings.Contains(err.Error(), "only support US") {
		t.Fatalf("err=%v", err)
	}
}
