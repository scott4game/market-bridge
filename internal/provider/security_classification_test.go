package provider

import "testing"

func TestCommonStockDescriptionConflicts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		excluded bool
	}{
		{"Preferred Bank Common Stock", false}, {"Welltower Inc. Common Stock (REIT)", false},
		{"Unit Corporation Common Stock", false}, {"D-Wave Quantum Inc. Common Stock", false},
		{"Company Series A Preferred Shares", true}, {"Issuer American Depositary Shares", true},
		{"Issuer 9.25% Senior Notes due 2029", true}, {"Issuer ETF", true}, {"Issuer Warrants", true},
		{"Issuer Units each consisting of common stock and a warrant", true},
	} {
		p := SecurityProfile{Type: "CS", Name: tc.name, Active: true, Locale: "us", Market: "stocks", PrimaryExchange: "XNAS"}
		if (CommonStockExclusion(p) != "") != tc.excluded {
			t.Errorf("%s: %s", tc.name, CommonStockExclusion(p))
		}
	}
}
