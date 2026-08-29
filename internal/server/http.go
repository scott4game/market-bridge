package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/access"
	"github.com/scott4game/market-bridge/internal/coverage"
	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/news"
	"github.com/scott4game/market-bridge/internal/provider"
)

type UsageReader interface {
	Snapshot(context.Context, string) (provider.UsageSnapshot, error)
}

type HistoricalClickHouse interface {
	Healthy(context.Context) error
	QueryBars(context.Context, market.DatasetSpec) ([]market.Bar, error)
	WriteBars(context.Context, string, market.AdjustmentMode, []market.Bar, uint64) error
}

type RemoteRedis interface {
	BarCache
	Healthy(context.Context) error
}

type RecentTradesReader interface {
	Trades(context.Context, string, int32) ([]*lbquote.Trade, error)
}

type HTTP struct {
	Store             *Store
	Token             string
	Access            *access.Store
	Limiter           *access.Limiter
	Live              http.Handler
	Usage             UsageReader
	OptionsUsage      UsageReader
	Options           *OptionCatalog
	ProviderStatus    func() any
	ClickHouseEnabled bool
	ClickHouse        HistoricalClickHouse
	RedisEnabled      bool
	Redis             RemoteRedis
	HistoryCatalog    *HistoryCatalog
	DataVersion       string
	EmptyCoverageTTL  time.Duration
	HistoryRetention  time.Duration
	RecentTrades      RecentTradesReader
	News              *news.Service
	SecurityProfiles  *SecurityProfileCatalog
}

func (h *HTTP) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /v1/datasets", h.auth("history:read", h.create))
	mux.HandleFunc("GET /v1/datasets/{id}", h.auth("history:read", h.status))
	mux.HandleFunc("GET /v1/datasets/{id}/manifest", h.auth("history:read", h.manifest))
	mux.HandleFunc("GET /v1/datasets/{id}/files/{name...}", h.auth("history:read", h.file))
	mux.HandleFunc("GET /v1/live/ws", h.auth("live:read", h.live))
	mux.HandleFunc("GET /v1/live/trades/{symbol}", h.auth("live:read", h.recentTrades))
	mux.HandleFunc("GET /v1/news", h.auth("news:read", h.newsList))
	mux.HandleFunc("GET /v1/news/ws", h.auth("news:read", h.newsWS))
	mux.HandleFunc("GET /v1/news/stream", h.auth("news:read", h.newsSSE))
	mux.HandleFunc("GET /v1/providers/massive/usage", h.auth("provider:usage", h.usage))
	mux.HandleFunc("GET /v1/providers/massive-options/usage", h.auth("provider:usage", h.optionsUsage))
	mux.HandleFunc("GET /v1/providers/status", h.auth("profile:read", h.providerStatus))
	mux.HandleFunc("GET /v1/storage/capabilities", h.auth("profile:read", h.storageCapabilities))
	mux.HandleFunc("POST /v1/history/bars", h.auth("history:read", h.historyBars))
	mux.HandleFunc("GET /v1/market-history/universe", h.auth("history:read", h.historyUniverse))
	mux.HandleFunc("GET /v1/market-history/security-profiles", h.auth("history:read", h.historySecurityProfiles))
	mux.HandleFunc("GET /v1/market-history/adjustments/{symbol}", h.auth("history:read", h.historyAdjustments))
	mux.HandleFunc("GET /v1/options/contracts", h.auth("history:read", h.optionContracts))
	mux.HandleFunc("GET /v1/options/bars/{contract}", h.auth("history:read", h.optionBars))
	mux.HandleFunc("GET /v1/me", h.auth("profile:read", h.me))
	mux.HandleFunc("GET /v1/me/usage", h.auth("profile:read", h.myUsage))
	mux.HandleFunc("GET /v1/me/watchlist", h.auth("profile:read", h.getWatchlist))
	mux.HandleFunc("PUT /v1/me/watchlist", h.auth("profile:read", h.putWatchlist))
	return mux
}

func (h *HTTP) historySecurityProfiles(w http.ResponseWriter, r *http.Request) {
	if h.SecurityProfiles == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "security profiles are unavailable"})
		return
	}
	response, err := h.SecurityProfiles.Ensure(r.Context())
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *HTTP) newsList(w http.ResponseWriter, r *http.Request) {
	if h.News == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "news provider is not enabled"})
		return
	}
	h.News.ListHTTP(w, r)
}

func (h *HTTP) newsWS(w http.ResponseWriter, r *http.Request) {
	if h.News == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "news provider is not enabled"})
		return
	}
	release, ok := h.acquireNewsStream(w, r)
	if !ok {
		return
	}
	defer release()
	h.News.ServeWS(w, r)
}

func (h *HTTP) newsSSE(w http.ResponseWriter, r *http.Request) {
	if h.News == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "news provider is not enabled"})
		return
	}
	release, ok := h.acquireNewsStream(w, r)
	if !ok {
		return
	}
	defer release()
	h.News.ServeSSE(w, r)
}

func (h *HTTP) acquireNewsStream(w http.ResponseWriter, r *http.Request) (func(), bool) {
	p, secured := access.PrincipalFromContext(r.Context())
	if !secured || h.Limiter == nil {
		return func() {}, true
	}
	if !h.Limiter.AcquireLive(p, 0) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "live connection quota exceeded"})
		return nil, false
	}
	return func() { h.Limiter.ReleaseLive(p.UserID, 0) }, true
}

type recentTrade struct {
	Price        string               `json:"price"`
	Volume       int64                `json:"volume"`
	Timestamp    int64                `json:"timestamp"`
	TradeType    string               `json:"trade_type"`
	Direction    int32                `json:"direction"`
	TradeSession lbquote.TradeSession `json:"trade_session"`
}

func (h *HTTP) recentTrades(w http.ResponseWriter, r *http.Request) {
	if h.RecentTrades == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Longbridge live provider is not enabled"})
		return
	}
	symbol, venue, err := market.NormalizeSymbol(r.PathValue("symbol"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if venue == market.VenueBinance {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "recent trades are only available for Longbridge securities"})
		return
	}
	limit := int64(100)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.ParseInt(raw, 10, 32)
		if err != nil || limit < 1 || limit > 1000 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and 1000"})
			return
		}
	}
	upstreamSymbol := symbol
	if venue == market.VenueUS {
		upstreamSymbol += ".US"
	}
	rows, err := h.RecentTrades.Trades(r.Context(), upstreamSymbol, int32(limit))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Longbridge recent trades are unavailable"})
		return
	}
	trades := make([]recentTrade, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		trades = append(trades, recentTrade{Price: row.Price, Volume: row.Volume, Timestamp: row.Timestamp, TradeType: row.TradeType, Direction: row.Direction, TradeSession: row.TradeSession})
	}
	writeJSON(w, http.StatusOK, map[string]any{"symbol": symbol, "trades": trades})
}

func (h *HTTP) historyAdjustments(w http.ResponseWriter, r *http.Request) {
	curve, err := h.Store.ForwardAdjustmentFactors(r.Context(), r.PathValue("symbol"))
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, 200, curve)
}

func (h *HTTP) historyUniverse(w http.ResponseWriter, r *http.Request) {
	securities, err := h.Store.Securities(r.Context())
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": err.Error()})
		return
	}
	symbols := make([]string, 0, len(securities))
	for _, security := range securities {
		symbols = append(symbols, security.Symbol)
	}
	writeJSON(w, 200, map[string]any{"symbols": symbols, "securities": securities, "updated_at": time.Now().UTC(), "data_version": h.DataVersion})
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
	redisHealthy := false
	var redisHealthError string
	if h.RedisEnabled && h.Redis != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.Redis.Healthy(ctx); err == nil {
			redisHealthy = true
		} else {
			redisHealthError = err.Error()
		}
	}
	writeJSON(w, 200, map[string]any{
		"clickhouse":       map[string]any{"enabled": h.ClickHouseEnabled, "healthy": healthy, "error": healthError},
		"redis":            map[string]any{"enabled": h.RedisEnabled, "healthy": redisHealthy, "error": redisHealthError},
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
		retention = 1825 * 24 * time.Hour
	}
	recent := !spec.From.Before(time.Now().UTC().Add(-retention))
	canonical := recent && h.ClickHouseEnabled && h.ClickHouse != nil
	if body.ProviderOnly || !canonical || h.HistoryCatalog == nil {
		bars, cached, err := h.Store.ProviderBarsCached(r.Context(), spec)
		if err != nil {
			if len(bars) > 0 {
				writeJSON(w, 200, map[string]any{"source": "provider-partial", "bars": nonNilBars(bars), "warning": err.Error()})
				return
			}
			writeProviderError(w, err)
			return
		}
		source := "provider"
		if cached {
			source = "server-redis"
		}
		writeJSON(w, 200, map[string]any{"source": source, "bars": nonNilBars(bars)})
		return
	}
	storageSpec := spec
	applyForward := market.IsUSForwardAdjusted(spec)
	if applyForward {
		storageSpec.Adjustment = market.SplitAdjusted
	}
	revision, _, _ := h.HistoryCatalog.Current(r.Context())
	clickHouseDataVersion := h.DataVersion + ":" + market.KlineStorageVersion
	coverageVersion, _, err := h.Store.SemanticDataVersion(r.Context(), spec, clickHouseDataVersion)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	cacheVersion, curves, err := h.Store.SemanticDataVersion(r.Context(), spec, fmt.Sprintf("clickhouse:%s:%d", clickHouseDataVersion, revision))
	if err != nil {
		writeProviderError(w, err)
		return
	}
	cacheKey, err := spec.Hash(market.SchemaVersion, cacheVersion)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if h.RedisEnabled && h.Redis != nil {
		if bars, ok, cacheErr := h.Redis.Get(r.Context(), "clickhouse:"+cacheKey); cacheErr == nil && ok {
			writeJSON(w, 200, map[string]any{"source": "server-redis", "bars": nonNilBars(bars)})
			return
		}
	}

	missing, err := h.HistoryCatalog.Missing(r.Context(), storageSpec, coverageVersion)
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": err.Error()})
		return
	}
	fetched := false
	var providerWarning error
	for _, gap := range coverage.GroupMissing(missing) {
		bars, err := h.Store.ProviderBars(r.Context(), gap)
		if err != nil {
			providerWarning = errors.Join(providerWarning, err)
			if len(bars) > 0 {
				if persistErr := h.persistHistory(r.Context(), gap, bars, coverageVersion); persistErr != nil {
					writeJSON(w, 503, map[string]string{"error": persistErr.Error()})
					return
				}
				fetched = true
			}
			continue
		}
		if err := h.persistHistory(r.Context(), gap, bars, coverageVersion); err != nil {
			writeJSON(w, 503, map[string]string{"error": err.Error()})
			return
		}
		fetched = true
	}
	bars, err := h.ClickHouse.QueryBars(r.Context(), storageSpec)
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": err.Error()})
		return
	}
	source := "server-clickhouse"
	if fetched {
		source = "provider+server-clickhouse"
	}
	if applyForward {
		bars, err = applyForwardFactorCurves(spec, bars, curves)
		if err != nil {
			writeProviderError(w, err)
			return
		}
	}
	if h.RedisEnabled && h.Redis != nil {
		currentRevision, _, _ := h.HistoryCatalog.Current(r.Context())
		currentVersion, _, versionErr := h.Store.SemanticDataVersion(r.Context(), spec, fmt.Sprintf("clickhouse:%s:%d", clickHouseDataVersion, currentRevision))
		if versionErr == nil {
			if currentKey, keyErr := spec.Hash(market.SchemaVersion, currentVersion); keyErr == nil {
				_ = h.Redis.Set(r.Context(), "clickhouse:"+currentKey, bars, h.Store.barCacheTTL(spec, bars))
			}
		}
	}
	payload := map[string]any{"source": source, "bars": nonNilBars(bars)}
	if providerWarning != nil {
		if len(bars) == 0 {
			writeProviderError(w, providerWarning)
			return
		}
		payload["warning"] = providerWarning.Error()
	}
	writeJSON(w, 200, payload)
}

func writeProviderError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if provider.IsHistoricalProviderDisabled(err) {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (h *HTTP) forwardAdjustBars(ctx context.Context, spec market.DatasetSpec, bars []market.Bar) ([]market.Bar, error) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, err
	}
	curves := make(map[string]market.ForwardFactors, len(spec.Symbols))
	for _, symbol := range spec.Symbols {
		curve, factorErr := h.Store.ForwardAdjustmentFactors(ctx, symbol)
		if factorErr != nil {
			return nil, factorErr
		}
		curves[symbol] = curve
	}
	return market.ApplyForwardFactors(bars, curves, location)
}

func applyForwardFactorCurves(spec market.DatasetSpec, bars []market.Bar, curves map[string]market.ForwardFactors) ([]market.Bar, error) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, err
	}
	if len(curves) != len(spec.Symbols) {
		return nil, errors.New("incomplete forward-adjustment factor set")
	}
	return market.ApplyForwardFactors(bars, curves, location)
}

func (h *HTTP) persistHistory(ctx context.Context, spec market.DatasetSpec, bars []market.Bar, coverageVersion string) error {
	if len(bars) > 0 {
		if err := h.ClickHouse.WriteBars(ctx, spec.Interval, spec.Adjustment, bars, uint64(time.Now().UnixMilli())); err != nil {
			return err
		}
	}
	if h.HistoryCatalog != nil {
		ttl := h.EmptyCoverageTTL
		if ttl <= 0 {
			ttl = 15 * time.Minute
		}
		if err := h.HistoryCatalog.RecordCoverage(ctx, spec, coverageVersion, bars, ttl); err != nil {
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

func (h *HTTP) optionsUsage(w http.ResponseWriter, r *http.Request) {
	if h.OptionsUsage == nil {
		writeJSON(w, 404, map[string]string{"error": "Massive options usage tracking is not enabled"})
		return
	}
	snapshot, err := h.OptionsUsage.Snapshot(r.Context(), "massive_options")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, snapshot)
}

func (h *HTTP) optionContracts(w http.ResponseWriter, r *http.Request) {
	if h.Options == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "options provider is not enabled"})
		return
	}
	q := r.URL.Query()
	query := provider.OptionContractQuery{
		Underlying: q.Get("underlying"), ContractType: q.Get("type"), ExpirationFrom: q.Get("expiration_from"),
		ExpirationTo: q.Get("expiration_to"), AsOf: q.Get("as_of"),
	}
	var err error
	if value := q.Get("strike_gte"); value != "" {
		query.StrikeGTE, err = strconv.ParseFloat(value, 64)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "strike_gte must be numeric"})
			return
		}
	}
	if value := q.Get("strike_lte"); value != "" {
		query.StrikeLTE, err = strconv.ParseFloat(value, 64)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "strike_lte must be numeric"})
			return
		}
	}
	query, err = query.Normalize()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	contracts, source, err := h.Options.Contracts(r.Context(), query)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"source": source, "count": len(contracts), "contracts": nonNilOptionContracts(contracts)})
}

func (h *HTTP) optionBars(w http.ResponseWriter, r *http.Request) {
	if h.Options == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "options provider is not enabled"})
		return
	}
	from, err := parseOptionDate(r.URL.Query().Get("from"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "from must be YYYY-MM-DD or RFC3339"})
		return
	}
	to, err := parseOptionDate(r.URL.Query().Get("to"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "to must be YYYY-MM-DD or RFC3339"})
		return
	}
	if !from.Before(to) {
		writeJSON(w, 400, map[string]string{"error": "from must be before to"})
		return
	}
	contract := strings.ToUpper(strings.TrimSpace(r.PathValue("contract")))
	if !strings.HasPrefix(contract, "O:") || len(contract) < 5 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "contract must be an OCC ticker prefixed with O:"})
		return
	}
	bars, source, err := h.Options.Bars(r.Context(), contract, from, to)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"source": source, "contract": contract, "count": len(bars), "bars": nonNilOptionBars(bars)})
}

func parseOptionDate(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, errors.New("date is required")
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), nil
	}
	return time.Parse("2006-01-02", value)
}

func nonNilOptionContracts(values []provider.OptionContract) []provider.OptionContract {
	if values == nil {
		return []provider.OptionContract{}
	}
	return values
}

func nonNilOptionBars(values []provider.OptionBar) []provider.OptionBar {
	if values == nil {
		return []provider.OptionBar{}
	}
	return values
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
func (w *statusRecorder) Flush()                      { _ = http.NewResponseController(w.ResponseWriter).Flush() }

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
		if provider.IsHistoricalProviderDisabled(err) {
			writeProviderError(w, err)
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
