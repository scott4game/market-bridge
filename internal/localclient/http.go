package localclient

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

//go:embed ui/*
var ui embed.FS

type HTTP struct {
	Cache *Cache
	Live  *LiveProxy
}

func (h *HTTP) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { jsonResponse(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /v1/datasets/ensure", h.ensure)
	mux.HandleFunc("GET /v1/datasets/{id}", h.dataset)
	mux.HandleFunc("POST /v1/datasets/{id}/refresh", h.refresh)
	mux.HandleFunc("GET /v1/bars/{symbol}", h.bars)
	mux.HandleFunc("GET /v1/cache", h.cacheList)
	mux.HandleFunc("POST /v1/cache/prune", h.prune)
	mux.HandleFunc("GET /v1/providers/massive/usage", h.providerUsage)
	mux.HandleFunc("GET /v1/providers/status", h.proxyServerJSON)
	mux.HandleFunc("GET /v1/live/trades/{symbol}", h.proxyServerJSON)
	mux.HandleFunc("GET /v1/market-history/universe", h.proxyServerJSON)
	mux.HandleFunc("GET /v1/market-history/adjustments/{symbol}", h.proxyServerJSON)
	mux.HandleFunc("GET /v1/storage/status", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, h.Cache.StorageStatus(r.Context())) })
	mux.HandleFunc("GET /v1/me", h.proxyServerJSON)
	mux.HandleFunc("GET /v1/me/usage", h.proxyServerJSON)
	mux.HandleFunc("GET /v1/me/watchlist", h.proxyServerJSON)
	mux.HandleFunc("PUT /v1/me/watchlist", h.proxyServerJSON)
	mux.HandleFunc("GET /v1/me/indicators", h.getLocalIndicators)
	mux.HandleFunc("POST /v1/me/indicators", h.createLocalIndicator)
	mux.HandleFunc("POST /v1/me/indicators/reset-display", h.resetLocalIndicatorDisplay)
	mux.HandleFunc("PUT /v1/me/indicators/{id}", h.updateLocalIndicator)
	mux.HandleFunc("DELETE /v1/me/indicators/{id}", h.deleteLocalIndicator)
	mux.HandleFunc("POST /v1/me/indicators/{id}/copy", h.copyLocalIndicator)
	if h.Live != nil {
		mux.Handle("/v1/live/ws", h.Live)
	}
	assets, _ := fs.Sub(ui, "ui")
	mux.Handle("/", http.FileServer(http.FS(assets)))
	return security(mux)
}

func (h *HTTP) getLocalIndicators(w http.ResponseWriter, r *http.Request) {
	indicators, err := h.Cache.LocalIndicators(r.Context())
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, 200, map[string]any{"indicators": indicators, "storage": "local"})
}

func (h *HTTP) resetLocalIndicatorDisplay(w http.ResponseWriter, r *http.Request) {
	indicators, err := h.Cache.ResetLocalIndicatorDisplay(r.Context())
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, 200, map[string]any{"indicators": indicators, "storage": "local"})
}

func decodeLocalIndicator(w http.ResponseWriter, r *http.Request) (localIndicatorMutation, bool) {
	var mutation localIndicatorMutation
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&mutation); err != nil {
		jsonResponse(w, 400, map[string]string{"error": err.Error()})
		return mutation, false
	}
	return mutation, true
}

func localIndicatorError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch err {
	case errLocalIndicatorNotFound:
		status = http.StatusNotFound
	case errLocalIndicatorConflict, errLocalIndicatorName:
		status = http.StatusConflict
	case errLocalIndicatorLimit, errLocalIndicatorEnabled:
		status = http.StatusTooManyRequests
	case errLocalIndicatorTemplate:
		status = http.StatusForbidden
	}
	jsonResponse(w, status, map[string]string{"error": err.Error()})
}

func (h *HTTP) createLocalIndicator(w http.ResponseWriter, r *http.Request) {
	mutation, ok := decodeLocalIndicator(w, r)
	if !ok {
		return
	}
	indicator, err := h.Cache.CreateLocalIndicator(r.Context(), mutation)
	if err != nil {
		localIndicatorError(w, err)
		return
	}
	jsonResponse(w, http.StatusCreated, indicator)
}

func (h *HTTP) updateLocalIndicator(w http.ResponseWriter, r *http.Request) {
	mutation, ok := decodeLocalIndicator(w, r)
	if !ok {
		return
	}
	indicator, err := h.Cache.UpdateLocalIndicator(r.Context(), r.PathValue("id"), mutation)
	if err != nil {
		localIndicatorError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, indicator)
}

func (h *HTTP) deleteLocalIndicator(w http.ResponseWriter, r *http.Request) {
	revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if err != nil || revision < 1 {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "revision is required"})
		return
	}
	if err := h.Cache.DeleteLocalIndicator(r.Context(), r.PathValue("id"), revision); err != nil {
		localIndicatorError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTP) copyLocalIndicator(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil && err != io.EOF {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	indicator, err := h.Cache.CopyLocalIndicator(r.Context(), r.PathValue("id"), body.Name)
	if err != nil {
		localIndicatorError(w, err)
		return
	}
	jsonResponse(w, http.StatusCreated, indicator)
}

func (h *HTTP) proxyServerJSON(w http.ResponseWriter, r *http.Request) {
	var body io.Reader
	if r.Body != nil && r.Body != http.NoBody {
		body = http.MaxBytesReader(w, r.Body, 64<<10)
	}
	path := r.URL.Path
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	raw, status, err := h.Cache.ServerJSON(r.Context(), r.Method, path, body)
	if err != nil {
		jsonResponse(w, 502, map[string]string{"error": err.Error()})
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
func (h *HTTP) providerUsage(w http.ResponseWriter, r *http.Request) {
	raw, err := h.Cache.ProviderUsage(r.Context())
	if err != nil {
		jsonResponse(w, 502, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, 200, raw)
}

func (h *HTTP) dataset(w http.ResponseWriter, r *http.Request) {
	m, err := h.Cache.Manifest(r.Context(), r.PathValue("id"))
	if err != nil {
		jsonResponse(w, 404, map[string]string{"error": "not found"})
		return
	}
	jsonResponse(w, 200, m)
}
func (h *HTTP) refresh(w http.ResponseWriter, r *http.Request) {
	if err := h.Cache.Delete(r.Context(), r.PathValue("id")); err != nil {
		jsonResponse(w, 409, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, 200, map[string]string{"state": "removed"})
}
func security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; worker-src 'self'; connect-src 'self' ws: wss:")
		if origin := r.Header.Get("Origin"); origin != "" && !allowedOrigin(origin, r.Host) {
			jsonResponse(w, 403, map[string]string{"error": "cross-origin request denied"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedOrigin(origin, requestHost string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return u.Host == requestHost || u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"
}
func (h *HTTP) ensure(w http.ResponseWriter, r *http.Request) {
	var spec market.DatasetSpec
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&spec); err != nil {
		jsonResponse(w, 400, map[string]string{"error": err.Error()})
		return
	}
	bars, source, err := h.Cache.Bars(r.Context(), spec)
	if err != nil {
		jsonResponse(w, 502, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("X-Cache-Source", source)
	jsonResponse(w, 200, map[string]any{"source": source, "count": len(bars), "bars": bars})
}
func (h *HTTP) bars(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err := time.Parse(time.RFC3339, q.Get("from"))
	if err != nil {
		jsonResponse(w, 400, map[string]string{"error": "from must be RFC3339"})
		return
	}
	to, err := time.Parse(time.RFC3339, q.Get("to"))
	if err != nil {
		jsonResponse(w, 400, map[string]string{"error": "to must be RFC3339"})
		return
	}
	spec := market.DatasetSpec{Symbols: []string{r.PathValue("symbol")}, Interval: q.Get("interval"), From: from, To: to, Session: market.Session(q.Get("session")), Adjustment: market.AdjustmentMode(q.Get("adjustment"))}
	bars, source, err := h.Cache.Bars(r.Context(), spec)
	if err != nil {
		jsonResponse(w, 502, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("X-Cache-Source", source)
	jsonResponse(w, 200, map[string]any{"source": source, "bars": bars})
}
func (h *HTTP) cacheList(w http.ResponseWriter, r *http.Request) {
	v, err := h.Cache.List(r.Context())
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, 200, v)
}
func (h *HTTP) prune(w http.ResponseWriter, r *http.Request) {
	expired, _ := strconv.ParseBool(r.URL.Query().Get("expired"))
	n, err := h.Cache.Prune(r.Context(), expired)
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, 200, map[string]int{"deleted": n})
}
func jsonResponse(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
