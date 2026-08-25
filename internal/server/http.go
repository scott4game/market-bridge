package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/scott4game/market-bridge/internal/access"
	"github.com/scott4game/market-bridge/internal/coverage"
	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/provider"
)

type UsageReader interface {
	Snapshot(context.Context, string) (provider.UsageSnapshot, error)
}

type HistoricalClickHouse interface {
	Healthy(context.Context) error
	QueryBars(context.Context, market.DatasetSpec) ([]market.Bar, error)
	WriteBars(context.Context, market.AdjustmentMode, []market.Bar, uint64) error
}

type HTTP struct {
	Store             *Store
	Token             string
	Access            *access.Store
	Limiter           *access.Limiter
	Live              http.Handler
	Usage             UsageReader
	ProviderStatus    func() any
	ClickHouseEnabled bool
	ClickHouse        HistoricalClickHouse
	HistoryCatalog    *HistoryCatalog
	DataVersion       string
	EmptyCoverageTTL  time.Duration
	HistoryRetention  time.Duration
}

func (h *HTTP) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /v1/datasets", h.auth("history:read", h.create))
	mux.HandleFunc("GET /v1/datasets/{id}", h.auth("history:read", h.status))
	mux.HandleFunc("GET /v1/datasets/{id}/manifest", h.auth("history:read", h.manifest))
	mux.HandleFunc("GET /v1/datasets/{id}/files/{name...}", h.auth("history:read", h.file))
	mux.HandleFunc("GET /v1/live/ws", h.auth("live:read", h.live))
	mux.HandleFunc("GET /v1/providers/massive/usage", h.auth("provider:usage", h.usage))
	mux.HandleFunc("GET /v1/providers/status", h.auth("profile:read", h.providerStatus))
	mux.HandleFunc("GET /v1/storage/capabilities", h.auth("profile:read", h.storageCapabilities))
	mux.HandleFunc("POST /v1/history/bars", h.auth("history:read", h.historyBars))
	mux.HandleFunc("GET /v1/market-history/universe", h.auth("history:read", h.historyUniverse))
	mux.HandleFunc("GET /v1/me", h.auth("profile:read", h.me))
	mux.HandleFunc("GET /v1/me/usage", h.auth("profile:read", h.myUsage))
	mux.HandleFunc("GET /v1/me/watchlist", h.auth("profile:read", h.getWatchlist))
	mux.HandleFunc("PUT /v1/me/watchlist", h.auth("profile:read", h.putWatchlist))
	mux.HandleFunc("GET /v1/me/indicators", h.auth("indicators:read", h.getIndicators))
	mux.HandleFunc("POST /v1/me/indicators", h.auth("indicators:write", h.createIndicator))
	mux.HandleFunc("PUT /v1/me/indicators/{id}", h.auth("indicators:write", h.updateIndicator))
	mux.HandleFunc("DELETE /v1/me/indicators/{id}", h.auth("indicators:write", h.deleteIndicator))
	mux.HandleFunc("POST /v1/me/indicators/{id}/copy", h.auth("indicators:write", h.copyIndicator))
	return mux
}

func (h *HTTP) historyUniverse(w http.ResponseWriter, r *http.Request) {
	symbols, err := h.Store.Universe(r.Context())
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"symbols": symbols, "updated_at": time.Now().UTC(), "data_version": h.DataVersion})
}

func (h *HTTP) storageCapabilities(w http.ResponseWriter, r *http.Request) {
	revision := uint64(0)
	updated := time.Unix(0, 0).UTC()
	if h.HistoryCatalog != nil {
		revision, updated, _ = h.HistoryCatalog.Current(r.Context())
	}
	healthy := false
	var healthError string
	if h.ClickHouseEnabled && h.ClickHouse != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.ClickHouse.Healthy(ctx); err == nil {
			healthy = true
		} else {
			healthError = err.Error()
		}
	}
	writeJSON(w, 200, map[string]any{
		"clickhouse":       map[string]any{"enabled": h.ClickHouseEnabled, "healthy": healthy, "error": healthError},
		"history_revision": revision, "data_version": h.DataVersion, "updated_at": updated,
	})
}

func (h *HTTP) historyBars(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Spec         market.DatasetSpec `json:"spec"`
		ProviderOnly bool               `json:"provider_only"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	spec, err := body.Spec.Normalize()
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	retention := h.HistoryRetention
	if retention <= 0 {
		retention = 730 * 24 * time.Hour
	}
	recent := !spec.From.Before(time.Now().UTC().Add(-retention))
	canonical := recent && spec.Interval == "1m" && h.ClickHouseEnabled && h.ClickHouse != nil
	if !canonical || body.ProviderOnly || h.HistoryCatalog == nil {
		bars, err := h.Store.ProviderBars(r.Context(), spec)
		if err != nil {
			writeJSON(w, 502, map[string]string{"error": err.Error()})
			return
		}
		if canonical {
			if err := h.persistHistory(r.Context(), spec, bars); err != nil {
				writeJSON(w, 503, map[string]string{"error": err.Error()})
				return
			}
		}
		source := "provider"
		if canonical {
			source = "provider+server-clickhouse"
		}
		writeJSON(w, 200, map[string]any{"source": source, "bars": nonNilBars(bars)})
		return
	}

	missing, err := h.HistoryCatalog.Missing(r.Context(), spec, h.DataVersion)
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": err.Error()})
		return
	}
	fetched := false
	for _, gap := range coverage.GroupMissing(missing) {
		bars, err := h.Store.ProviderBars(r.Context(), gap)
		if err != nil {
			writeJSON(w, 502, map[string]string{"error": err.Error()})
			return
		}
		if err := h.persistHistory(r.Context(), gap, bars); err != nil {
			writeJSON(w, 503, map[string]string{"error": err.Error()})
			return
		}
		fetched = true
	}
	bars, err := h.ClickHouse.QueryBars(r.Context(), spec)
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": err.Error()})
		return
	}
	source := "server-clickhouse"
	if fetched {
		source = "provider+server-clickhouse"
	}
	writeJSON(w, 200, map[string]any{"source": source, "bars": nonNilBars(bars)})
}

func (h *HTTP) persistHistory(ctx context.Context, spec market.DatasetSpec, bars []market.Bar) error {
	if len(bars) > 0 {
		if err := h.ClickHouse.WriteBars(ctx, spec.Adjustment, bars, uint64(time.Now().UnixMilli())); err != nil {
			return err
		}
	}
	if h.HistoryCatalog != nil {
		ttl := h.EmptyCoverageTTL
		if ttl <= 0 {
			ttl = 15 * time.Minute
		}
		if err := h.HistoryCatalog.RecordCoverage(ctx, spec, h.DataVersion, bars, ttl); err != nil {
			return err
		}
		if len(bars) > 0 {
			if _, err := h.HistoryCatalog.Bump(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func nonNilBars(bars []market.Bar) []market.Bar {
	if bars == nil {
		return []market.Bar{}
	}
	return bars
}
func (h *HTTP) usage(w http.ResponseWriter, r *http.Request) {
	if h.Usage == nil {
		writeJSON(w, 404, map[string]string{"error": "Massive usage tracking is not enabled"})
		return
	}
	snapshot, err := h.Usage.Snapshot(r.Context(), "massive")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, snapshot)
}

func (h *HTTP) auth(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if h.Access == nil {
			if h.Token != "" && token != h.Token {
				writeJSON(w, 401, map[string]string{"error": "unauthorized"})
				return
			}
			next(w, r)
			return
		}
		principal, err := h.Access.Authenticate(r.Context(), token)
		if err != nil {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		if !principal.HasScope(scope) {
			writeJSON(w, 403, map[string]string{"error": "forbidden"})
			return
		}
		if h.Limiter != nil && !h.Limiter.AllowRequest(principal) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, 429, map[string]string{"error": "request quota exceeded"})
			return
		}
		r = r.WithContext(access.WithPrincipal(r.Context(), principal))
		recorder := &statusRecorder{ResponseWriter: w, status: 200}
		started := time.Now()
		next(recorder, r)
		h.Access.RecordRequest(context.Background(), principal, requestID(), r.Method, r.Pattern, recorder.status, time.Since(started), r.Method == http.MethodPost && r.URL.Path == "/v1/datasets")
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func requestID() string { var b [8]byte; _, _ = rand.Read(b[:]); return fmt.Sprintf("%x", b[:]) }
func (h *HTTP) create(w http.ResponseWriter, r *http.Request) {
	var spec market.DatasetSpec
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&spec); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	var st market.DatasetStatus
	var err error
	if p, ok := access.PrincipalFromContext(r.Context()); ok {
		admit := func() bool { return h.Limiter == nil || h.Limiter.AllowDataset(p) }
		st, err = h.Store.EnsureForAdmission(r.Context(), spec, p.UserID, p.Quotas.ConcurrentBuilds, admit)
	} else {
		st, err = h.Store.Ensure(r.Context(), spec)
	}
	if err != nil {
		if errors.Is(err, ErrDatasetRateQuota) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, 429, map[string]string{"error": err.Error()})
			return
		}
		if errors.Is(err, ErrBuildQuota) {
			writeJSON(w, 429, map[string]string{"error": err.Error()})
			return
		}
		if errors.Is(err, ErrBuildQueueFull) {
			writeJSON(w, 503, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	code := 202
	if st.State == "ready" {
		code = 200
	}
	w.Header().Set("Location", "/v1/datasets/"+st.DatasetID)
	writeJSON(w, code, st)
}

func (h *HTTP) me(w http.ResponseWriter, r *http.Request) {
	p, _ := access.PrincipalFromContext(r.Context())
	writeJSON(w, 200, p)
}

func (h *HTTP) myUsage(w http.ResponseWriter, r *http.Request) {
	p, _ := access.PrincipalFromContext(r.Context())
	daily, err := h.Access.Usage(r.Context(), p.UserID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	requests, datasets, connections, symbols := 0, 0, 0, 0
	if h.Limiter != nil {
		requests, datasets, connections, symbols = h.Limiter.Snapshot(p.UserID)
	}
	writeJSON(w, 200, map[string]any{"daily": daily, "current_minute": map[string]int{"requests": requests, "datasets": datasets}, "active_builds": h.Store.ActiveBuilds(p.UserID), "live": map[string]int{"connections": connections, "symbols": symbols}, "quotas": p.Quotas})
}

func (h *HTTP) providerStatus(w http.ResponseWriter, _ *http.Request) {
	if h.ProviderStatus == nil {
		writeJSON(w, 200, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, 200, h.ProviderStatus())
}

func (h *HTTP) getWatchlist(w http.ResponseWriter, r *http.Request) {
	p, _ := access.PrincipalFromContext(r.Context())
	symbols, err := h.Access.Watchlist(r.Context(), p.UserID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"symbols": symbols, "max_symbols": p.Quotas.LiveSymbols, "subscription_mode": "on_demand"})
}

func (h *HTTP) putWatchlist(w http.ResponseWriter, r *http.Request) {
	p, _ := access.PrincipalFromContext(r.Context())
	var body struct {
		Symbols []string `json:"symbols"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	set := map[string]struct{}{}
	symbols := make([]string, 0, len(body.Symbols))
	for _, symbol := range body.Symbols {
		normalized, _, err := market.NormalizeSymbol(symbol)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if _, ok := set[normalized]; !ok {
			set[normalized] = struct{}{}
			symbols = append(symbols, normalized)
		}
	}
	if len(symbols) > p.Quotas.LiveSymbols {
		writeJSON(w, 429, map[string]string{"error": "watchlist quota exceeded"})
		return
	}
	if err := h.Access.SetWatchlist(r.Context(), p.UserID, symbols); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"symbols": symbols, "max_symbols": p.Quotas.LiveSymbols, "subscription_mode": "on_demand"})
}

func (h *HTTP) getIndicators(w http.ResponseWriter, r *http.Request) {
	p, _ := access.PrincipalFromContext(r.Context())
	indicators, err := h.Access.Indicators(r.Context(), p.UserID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"indicators": indicators})
}

func decodeIndicatorMutation(w http.ResponseWriter, r *http.Request) (access.IndicatorMutation, error) {
	var body access.IndicatorMutation
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 96<<10)).Decode(&body)
	return body, err
}

func indicatorError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, access.ErrIndicatorNotFound):
		writeJSON(w, 404, map[string]string{"error": err.Error()})
	case errors.Is(err, access.ErrIndicatorConflict):
		writeJSON(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, access.ErrIndicatorName):
		writeJSON(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, access.ErrIndicatorLimit), errors.Is(err, access.ErrIndicatorEnabled):
		writeJSON(w, 429, map[string]string{"error": err.Error()})
	case errors.Is(err, access.ErrIndicatorTemplate):
		writeJSON(w, 403, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, 400, map[string]string{"error": err.Error()})
	}
}

func (h *HTTP) createIndicator(w http.ResponseWriter, r *http.Request) {
	p, _ := access.PrincipalFromContext(r.Context())
	body, err := decodeIndicatorMutation(w, r)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	indicator, err := h.Access.CreateIndicator(r.Context(), p.UserID, body)
	if err != nil {
		indicatorError(w, err)
		return
	}
	writeJSON(w, 201, indicator)
}

func (h *HTTP) updateIndicator(w http.ResponseWriter, r *http.Request) {
	p, _ := access.PrincipalFromContext(r.Context())
	body, err := decodeIndicatorMutation(w, r)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	indicator, err := h.Access.UpdateIndicator(r.Context(), p.UserID, r.PathValue("id"), body)
	if err != nil {
		indicatorError(w, err)
		return
	}
	writeJSON(w, 200, indicator)
}

func (h *HTTP) deleteIndicator(w http.ResponseWriter, r *http.Request) {
	p, _ := access.PrincipalFromContext(r.Context())
	revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if err != nil || revision < 1 {
		writeJSON(w, 400, map[string]string{"error": "revision is required"})
		return
	}
	if err := h.Access.DeleteIndicator(r.Context(), p.UserID, r.PathValue("id"), revision); err != nil {
		indicatorError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTP) copyIndicator(w http.ResponseWriter, r *http.Request) {
	p, _ := access.PrincipalFromContext(r.Context())
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	indicator, err := h.Access.CopyIndicator(r.Context(), p.UserID, r.PathValue("id"), body.Name)
	if err != nil {
		indicatorError(w, err)
		return
	}
	writeJSON(w, 201, indicator)
}

func normalizeSymbol(symbol string) string {
	normalized, _, err := market.NormalizeSymbol(symbol)
	if err != nil {
		return strings.ToUpper(strings.TrimSpace(symbol))
	}
	return normalized
}
func (h *HTTP) status(w http.ResponseWriter, r *http.Request) {
	st := h.Store.Status(r.PathValue("id"))
	code := 200
	if st.State == "not_found" {
		code = 404
	}
	writeJSON(w, code, st)
}
func (h *HTTP) manifest(w http.ResponseWriter, r *http.Request) {
	m, err := h.Store.Manifest(r.PathValue("id"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, 200, m)
}
func (h *HTTP) file(w http.ResponseWriter, r *http.Request) {
	path, err := h.Store.PartitionPath(r.PathValue("id"), r.PathValue("name"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeJSON(w, 410, map[string]string{"error": "partition expired"})
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	w.Header().Set("ETag", fmt.Sprintf("\"%x-%x\"", info.Size(), info.ModTime().UnixNano()))
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
}
func (h *HTTP) live(w http.ResponseWriter, r *http.Request) {
	if h.Live != nil {
		h.Live.ServeHTTP(w, r)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	var sub struct {
		Symbols []string `json:"symbols"`
	}
	if err := wsRead(r, c, &sub); err != nil {
		return
	}
	connectionCtx := c.CloseRead(r.Context())
	if len(sub.Symbols) == 0 {
		sub.Symbols = []string{"AAPL"}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	seq := int64(0)
	for {
		select {
		case <-connectionCtx.Done():
			return
		case ts := <-ticker.C:
			for _, symbol := range sub.Symbols {
				seq++
				v := market.DecimalFromFloat(100 + float64(seq%20)/10)
				event := map[string]any{"type": "bar", "symbol": strings.ToUpper(symbol), "timestamp": ts.UTC(), "cursor": map[string]any{"stream_epoch": "mock", "event_type": "bar", "symbol": strings.ToUpper(symbol), "sequence": seq}, "bar": market.Bar{Symbol: strings.ToUpper(symbol), Timestamp: ts.UTC().Truncate(time.Minute), Open: v, High: v, Low: v, Close: v, Volume: seq, Session: market.RegularSession, Source: "mock-live", Completed: false}}
				writeCtx, cancel := context.WithTimeout(connectionCtx, 10*time.Second)
				err := wsWriteContext(writeCtx, c, event)
				cancel()
				if err != nil {
					return
				}
			}
		}
	}
}

func wsRead(r *http.Request, c *websocket.Conn, v any) error {
	_, b, err := c.Read(r.Context())
	if err == nil {
		err = json.Unmarshal(b, v)
	}
	return err
}

func wsWriteContext(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, b)
}
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
