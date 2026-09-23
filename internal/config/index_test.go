package config

import (
	"strings"
	"testing"
)

func TestIndexRoutesParsing(t *testing.T) {
	cfg := Server{IndexProvider: "disabled", IndexRoutes: " i:hsi = Longbridge , I:VIX=fmp,I:SPX=disabled"}
	routes, err := cfg.ParsedIndexRoutes()
	if err != nil || routes["I:HSI"] != "longbridge" || routes["I:SPX"] != "disabled" || !cfg.UsesIndexProvider("fmp") || cfg.UsesIndexProvider("disabled") {
		t.Fatalf("routes=%v err=%v", routes, err)
	}
	for _, raw := range []string{"I:HSI=mock,i:hsi=fmp", "HSI=mock", "I:=mock", "I:H SI=mock", "I:HSI=unknown", "I:HSI=mock,", "I:HSI=mock=extra"} {
		if _, err := (Server{IndexRoutes: raw}).ParsedIndexRoutes(); err == nil {
			t.Errorf("accepted invalid routes %q", raw)
		}
	}
}

func TestIndexRoutingVersion(t *testing.T) {
	a := Server{IndexProvider: "disabled", IndexRoutes: "I:HSI=longbridge,I:VIX=fmp"}
	b := Server{IndexProvider: "disabled", IndexRoutes: " i:vix = FMP ,i:hsi=longbridge"}
	if a.IndexRoutingVersion() != b.IndexRoutingVersion() {
		t.Fatal("order or whitespace changed routing version")
	}
	b.IndexRoutes = "I:HSI=longbridge,I:VIX=massive"
	if a.IndexRoutingVersion() == b.IndexRoutingVersion() {
		t.Fatal("source change did not change routing version")
	}
	b = a
	b.IndexProvider = "mock"
	if a.IndexRoutingVersion() == b.IndexRoutingVersion() {
		t.Fatal("default change did not change routing version")
	}
}

func TestIndexRouteCredentials(t *testing.T) {
	for _, tc := range []struct{ route, key string }{{"I:HSI=longbridge", "LONGBRIDGE_APP_KEY"}, {"I:VIX=fmp", "FMP_API_KEY"}, {"I:VIX=massive", "MASSIVE_API_KEY"}} {
		t.Run(tc.key, func(t *testing.T) {
			t.Setenv("GO_SERVER_INDEX_PROVIDER", "disabled")
			t.Setenv("GO_SERVER_INDEX_ROUTES", tc.route)
			t.Setenv(tc.key, "")
			cfg := ServerFromEnv()
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("expected missing %s, got %v", tc.key, err)
			}
		})
	}
}
