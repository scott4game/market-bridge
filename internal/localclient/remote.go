package localclient

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// RemoteHandler authenticates ALL requests, including loopback, streams and MCP.
// It never interprets proxy headers as permission and never exposes maintenance/UI.
func (h *HTTP) RemoteHandler(token string) http.Handler {
	local := h.Handler()
	var mcp http.Handler = http.NotFoundHandler()
	if h.Cache != nil && h.Cache.cfg.MCPEnabled {
		mcp = h.mcpAccessHandler(true)
	}

	dispatch := security(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			mcp.ServeHTTP(w, r)
			return
		}
		if !remoteRouteAllowed(r.Method, r.URL.Path) {
			jsonResponse(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		local.ServeHTTP(w, r)
	}))
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		auth := r.Header.Get("Authorization")
		actual := sha256.Sum256([]byte(strings.TrimPrefix(auth, "Bearer ")))
		if len(token) < 32 || len(r.Header.Values("Authorization")) != 1 || !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="market-bridge-client"`)
			jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "error_details": map[string]any{"code": "unauthorized", "retryable": false}})
			return
		}
		request := r.Clone(r.Context())
		request.Header = r.Header.Clone()
		request.Header.Del("Authorization")
		dispatch.ServeHTTP(w, request)
	})
}
func remoteRouteAllowed(method, path string) bool {
	if (method == http.MethodGet || method == http.MethodPost) && strings.HasPrefix(path, "/v1/plugins/") {
		return true
	}
	if method == http.MethodGet && path == "/v1/plugins" {
		return true
	}

	if method == http.MethodPost {
		return path == "/v1/datasets/ensure" || path == "/v1/market-analytics/basket-flow"
	}
	if method == http.MethodPut {
		return path == "/v1/me/watchlist"
	}
	if method != http.MethodGet {
		return false
	}
	switch path {
	case "/healthz", "/readyz", "/v1/providers/status", "/v1/storage/status", "/v1/me", "/v1/me/usage", "/v1/me/watchlist",
		"/v1/market-history/universe", "/v1/market-history/security-profiles", "/v1/options/expirations", "/v1/options/chain", "/v1/options/quotes", "/v1/options/greeks", "/v1/options/contracts",
		"/v1/live/ws", "/v1/news", "/v1/news/ws", "/v1/news/stream", "/v1/market-analytics/sector-flow":
		return true
	}
	for _, prefix := range []string{"/v1/bars/", "/v1/datasets/", "/v1/market-history/adjustments/", "/v1/options/bars/", "/v1/live/trades/", "/v1/market-analytics/volume/", "/v1/market-analytics/flow/"} {
		if strings.HasPrefix(path, prefix) {
			tail := strings.TrimPrefix(path, prefix)
			return tail != "" && !strings.ContainsAny(tail, "/\\") && tail != "." && tail != ".."
		}
	}
	return false
}
