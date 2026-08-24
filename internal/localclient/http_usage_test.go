package localclient

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/scott4game/market-bridge/internal/config"
)

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
