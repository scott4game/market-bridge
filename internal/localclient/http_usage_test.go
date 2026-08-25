package localclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scott4game/market-bridge/internal/config"
)

func TestEmbeddedKLineChartAssets(t *testing.T) {
	handler := (&HTTP{}).Handler()
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/", want: "/klinecharts.min.js"},
		{path: "/", want: "MX MACD 背离副图"},
		{path: "/app.js", want: "MX_INDICATOR_NAME = 'MX_MACD'"},
		{path: "/klinecharts.min.js", want: "KLineChart v10.0.2"},
		{path: "/klinecharts.LICENSE.txt", want: "Apache License"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		body, err := io.ReadAll(recorder.Result().Body)
		if err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK || !strings.Contains(string(body), test.want) {
			t.Fatalf("path=%s status=%d missing=%q", test.path, recorder.Code, test.want)
		}
	}
}

func TestProviderUsageProxyForwardsServerToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider":"massive","totals":{"requests":7}}`))
	}))
	defer upstream.Close()

	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), ServerURL: upstream.URL, ServerToken: "secret", RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	rec := httptest.NewRecorder()
	(&HTTP{Cache: cache}).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/providers/massive/usage", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"provider\":\"massive\",\"totals\":{\"requests\":7}}\n" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
