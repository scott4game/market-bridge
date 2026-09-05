package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/scott4game/market-bridge/internal/analytics"
	"github.com/scott4game/market-bridge/internal/market"
	"github.com/shopspring/decimal"
	_ "modernc.org/sqlite"
)

const analyticsLookback = 20

type AnalyticsCoverage struct {
	Total          int      `json:"total"`
	Evaluated      int      `json:"evaluated"`
	MissingSymbols []string `json:"missing_symbols,omitempty"`
}

type FlowResponse struct {
	Source            string                `json:"source"`
	Method            string                `json:"method"`
	Proxy             bool                  `json:"proxy"`
	AsOf              time.Time             `json:"as_of"`
	Complete          bool                  `json:"complete"`
	Coverage          AnalyticsCoverage     `json:"coverage"`
	Points            []analytics.FlowPoint `json:"points"`
	Errors            []string              `json:"errors,omitempty"`
	RetryAfterSeconds int                   `json:"retry_after_seconds,omitempty"`
}

type VolumeResponse struct {
	Source   string                  `json:"source"`
	Method   string                  `json:"method"`
	Proxy    bool                    `json:"proxy"`
	AsOf     time.Time               `json:"as_of"`
	Complete bool                    `json:"complete"`
	Coverage AnalyticsCoverage       `json:"coverage"`
	Points   []analytics.VolumePoint `json:"points"`
	Errors   []string                `json:"errors,omitempty"`
}

type SectorFlow struct {
	SICCode        string   `json:"sic_code"`
	SICDescription string   `json:"sic_description"`
	Members        int      `json:"members"`
	Evaluated      int      `json:"evaluated"`
	MissingSymbols []string `json:"missing_symbols,omitempty"`
	analytics.FlowAggregate
}

type SectorFlowResponse struct {
	Source   string            `json:"source"`
	Method   string            `json:"method"`
	Proxy    bool              `json:"proxy"`
	Date     string            `json:"date"`
	AsOf     time.Time         `json:"as_of"`
	Complete bool              `json:"complete"`
	Coverage AnalyticsCoverage `json:"coverage"`
	Sectors  []SectorFlow      `json:"sectors"`
	Errors   []string          `json:"errors,omitempty"`
}

type MarketAnalytics struct {
	db          *sql.DB
	store       *Store
	clickhouse  HistoricalClickHouse
	history     *HistoryCatalog
	profiles    *SecurityProfileCatalog
	dataVersion string
	location    *time.Location
}

func OpenMarketAnalytics(path string, store *Store, clickhouse HistoricalClickHouse, history *HistoryCatalog, profiles *SecurityProfileCatalog, dataVersion string) (*MarketAnalytics, error) {
	if store == nil {
		return nil, errors.New("analytics store is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, query := range []string{
		"PRAGMA journal_mode=WAL",
		`CREATE TABLE IF NOT EXISTS grouped_daily_cache (
			date TEXT NOT NULL,
			data_version TEXT NOT NULL,
			bars_json BLOB NOT NULL,
			fetched_at INTEGER NOT NULL,
			PRIMARY KEY(date,data_version)
		)`,
	} {
		if _, err := db.Exec(query); err != nil {
			db.Close()
			return nil, err
		}
	}
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(file, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			db.Close()
			return nil, err
		}
	}
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		db.Close()
		return nil, err
	}
	return &MarketAnalytics{db: db, store: store, clickhouse: clickhouse, history: history, profiles: profiles, dataVersion: dataVersion, location: location}, nil
}

func (a *MarketAnalytics) Close() error { return a.db.Close() }

func (a *MarketAnalytics) Volume(ctx context.Context, spec market.DatasetSpec) (VolumeResponse, error) {
	warm := spec
	warm.From = spec.From.AddDate(0, 0, -45)
	bars, source, err := a.loadBars(ctx, warm, true)
	if err != nil {
		return VolumeResponse{}, err
	}
	points := analytics.VolumePoints(bars, spec.Interval, analyticsLookback, a.location)
	points = filterVolumePoints(points, spec.From, spec.To)
	complete := len(points) > 0
	for _, point := range points {
		complete = complete && point.BaselineComplete && point.Turnover != nil
	}
	return VolumeResponse{
		Source: source, Method: analytics.FlowMethod, Proxy: true, AsOf: time.Now().UTC(), Complete: complete,
		Coverage: AnalyticsCoverage{Total: 1, Evaluated: boolInt(len(points) > 0)}, Points: nonNilVolumePoints(points),
	}, nil
}

func (a *MarketAnalytics) Flow(ctx context.Context, spec market.DatasetSpec) (FlowResponse, error) {
	spec.Adjustment = market.Raw
	bars, source, err := a.loadBars(ctx, spec, true)
	if err != nil {
		return FlowResponse{}, err
	}
	points := make([]analytics.FlowPoint, 0, len(bars))
	for _, bar := range bars {
		if !bar.Timestamp.Before(spec.From) && bar.Timestamp.Before(spec.To) {
			point, pointErr := analytics.FlowFromBar(bar)
			if pointErr != nil {
				return FlowResponse{}, pointErr
			}
			points = append(points, point)
		}
	}
	return FlowResponse{
		Source: source, Method: analytics.FlowMethod, Proxy: true, AsOf: time.Now().UTC(), Complete: len(points) > 0,
		Coverage: AnalyticsCoverage{Total: 1, Evaluated: boolInt(len(points) > 0)}, Points: nonNilFlowPoints(points),
	}, nil
}

func (a *MarketAnalytics) BasketFlow(ctx context.Context, spec market.DatasetSpec) FlowResponse {
	response := FlowResponse{Source: "massive", Method: analytics.FlowMethod, Proxy: true, AsOf: time.Now().UTC(), Coverage: AnalyticsCoverage{Total: len(spec.Symbols)}}
	byTime := map[time.Time][]market.Bar{}
	for _, symbol := range spec.Symbols {
		child := spec
		child.Symbols = []string{symbol}
		child.Adjustment = market.Raw
		bars, _, err := a.loadBars(ctx, child, true)
		if err != nil {
			response.Coverage.MissingSymbols = append(response.Coverage.MissingSymbols, symbol)
			response.Errors = append(response.Errors, symbol+": "+err.Error())
			continue
		}
		response.Coverage.Evaluated++
		for _, bar := range bars {
			if !bar.Timestamp.Before(spec.From) && bar.Timestamp.Before(spec.To) {
				byTime[bar.Timestamp.UTC()] = append(byTime[bar.Timestamp.UTC()], bar)
			}
		}
	}
	for timestamp, bars := range byTime {
		aggregate, err := analytics.AggregateBars(bars)
		if err != nil {
			response.Errors = append(response.Errors, err.Error())
			continue
		}
		response.Points = append(response.Points, analytics.FlowPoint{
			Timestamp: timestamp, Volume: aggregate.Volume, Turnover: aggregate.Turnover, Inflow: aggregate.Inflow,
			Outflow: aggregate.Outflow, NetFlow: aggregate.NetFlow, FlowRatio: aggregate.FlowRatio, Completed: true,
		})
	}
	sort.Slice(response.Points, func(i, j int) bool { return response.Points[i].Timestamp.Before(response.Points[j].Timestamp) })
	sort.Strings(response.Coverage.MissingSymbols)
	response.Complete = len(response.Errors) == 0
	if !response.Complete {
		response.RetryAfterSeconds = 30
	}
	response.Points = nonNilFlowPoints(response.Points)
	return response
}

func (a *MarketAnalytics) SectorFlow(ctx context.Context, date string, limit int) (SectorFlowResponse, error) {
	bars, source, err := a.groupedDaily(ctx, date)
	if err != nil {
		return SectorFlowResponse{}, err
	}
	if a.profiles == nil {
		return SectorFlowResponse{}, errors.New("security profiles are unavailable")
	}
	profiles, err := a.profiles.Ensure(ctx)
	if err != nil {
		return SectorFlowResponse{}, err
	}
	barBySymbol := make(map[string]market.Bar, len(bars))
	errorsOut := make([]string, 0, len(profiles.Errors))
	for _, profileError := range profiles.Errors {
		errorsOut = append(errorsOut, profileError.Symbol+": "+profileError.Error)
	}
	for _, bar := range bars {
		barBySymbol[bar.Symbol] = bar
	}
	type sectorState struct {
		description string
		members     []string
		bars        []market.Bar
		missing     []string
	}
	states := map[string]*sectorState{}
	coverage := AnalyticsCoverage{}
	for _, profile := range profiles.Profiles {
		if !profile.Active || profile.Type != "CS" || len(profile.SICCode) != 4 {
			continue
		}
		state := states[profile.SICCode]
		if state == nil {
			state = &sectorState{description: profile.SICDescription}
			states[profile.SICCode] = state
		}
		state.members = append(state.members, profile.Symbol)
		coverage.Total++
		bar, ok := barBySymbol[profile.Symbol]
		if !ok || bar.Turnover == nil {
			state.missing = append(state.missing, profile.Symbol)
			coverage.MissingSymbols = append(coverage.MissingSymbols, profile.Symbol)
			continue
		}
		state.bars = append(state.bars, bar)
		coverage.Evaluated++
	}
	sectors := make([]SectorFlow, 0, len(states))
	for code, state := range states {
		if len(state.bars) == 0 {
			continue
		}
		aggregate, err := analytics.AggregateBars(state.bars)
		if err != nil {
			return SectorFlowResponse{}, err
		}
		sort.Strings(state.missing)
		sectors = append(sectors, SectorFlow{SICCode: code, SICDescription: state.description, Members: len(state.members), Evaluated: len(state.bars), MissingSymbols: state.missing, FlowAggregate: aggregate})
	}
	sort.Slice(sectors, func(i, j int) bool {
		left, _ := decimal.NewFromString(sectors[i].NetFlow)
		right, _ := decimal.NewFromString(sectors[j].NetFlow)
		if !left.Equal(right) {
			return left.GreaterThan(right)
		}
		return sectors[i].SICCode < sectors[j].SICCode
	})
	if limit > 0 && len(sectors) > limit {
		sectors = sectors[:limit]
	}
	sort.Strings(coverage.MissingSymbols)
	return SectorFlowResponse{
		Source: source, Method: analytics.FlowMethod, Proxy: true, Date: date, AsOf: time.Now().UTC(),
		Complete: len(coverage.MissingSymbols) == 0 && profiles.Complete, Coverage: coverage, Sectors: sectors, Errors: errorsOut,
	}, nil
}

func (a *MarketAnalytics) loadBars(ctx context.Context, spec market.DatasetSpec, requireTurnover bool) ([]market.Bar, string, error) {
	spec, err := spec.Normalize()
	if err != nil {
		return nil, "", err
	}
	if a.clickhouse != nil {
		bars, queryErr := a.clickhouse.QueryBars(ctx, spec)
		if queryErr == nil && len(bars) > 0 && (!requireTurnover || barsHaveTurnover(bars)) {
			return bars, "server-clickhouse", nil
		}
	}
	bars, _, err := a.store.ProviderBarsCached(ctx, spec)
	if err == nil && (!requireTurnover || barsHaveTurnover(bars)) {
		return bars, "massive", nil
	}
	bars, freshErr := a.store.ProviderBarsFresh(ctx, spec)
	if freshErr != nil {
		return nil, "", freshErr
	}
	if requireTurnover && !barsHaveTurnover(bars) {
		return nil, "", errors.New("Massive did not return VWAP turnover for every completed bar")
	}
	if a.clickhouse != nil && len(bars) > 0 {
		if err := a.clickhouse.WriteBars(ctx, spec.Interval, spec.Adjustment, bars, uint64(time.Now().UnixMilli())); err != nil {
			return nil, "", err
		}
		if a.history != nil {
			_, _ = a.history.Bump(ctx)
		}
	}
	return bars, "massive", nil
}

func (a *MarketAnalytics) groupedDaily(ctx context.Context, date string) ([]market.Bar, string, error) {
	var raw []byte
	err := a.db.QueryRowContext(ctx, "SELECT bars_json FROM grouped_daily_cache WHERE date=? AND data_version=?", date, a.dataVersion).Scan(&raw)
	if err == nil {
		var bars []market.Bar
		if json.Unmarshal(raw, &bars) == nil {
			return bars, "analytics-cache", nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, "", err
	}
	bars, err := a.store.GroupedDaily(ctx, date)
	if err != nil {
		return nil, "", err
	}
	raw, err = json.Marshal(bars)
	if err != nil {
		return nil, "", err
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO grouped_daily_cache(date,data_version,bars_json,fetched_at) VALUES(?,?,?,?) ON CONFLICT(date,data_version) DO UPDATE SET bars_json=excluded.bars_json,fetched_at=excluded.fetched_at`, date, a.dataVersion, raw, time.Now().Unix()); err != nil {
		return nil, "", err
	}
	if a.clickhouse != nil && len(bars) > 0 {
		if err := a.clickhouse.WriteBars(ctx, "1d", market.Raw, bars, uint64(time.Now().UnixMilli())); err != nil {
			return nil, "", err
		}
		if a.history != nil {
			_, _ = a.history.Bump(ctx)
		}
	}
	return bars, "massive-grouped-daily", nil
}

func barsHaveTurnover(bars []market.Bar) bool {
	for _, bar := range bars {
		if bar.Completed && bar.Turnover == nil {
			return false
		}
	}
	return true
}

func filterVolumePoints(points []analytics.VolumePoint, from, to time.Time) []analytics.VolumePoint {
	result := points[:0]
	for _, point := range points {
		if !point.Timestamp.Before(from) && point.Timestamp.Before(to) {
			result = append(result, point)
		}
	}
	return result
}

func nonNilFlowPoints(points []analytics.FlowPoint) []analytics.FlowPoint {
	if points == nil {
		return []analytics.FlowPoint{}
	}
	return points
}

func nonNilVolumePoints(points []analytics.VolumePoint) []analytics.VolumePoint {
	if points == nil {
		return []analytics.VolumePoint{}
	}
	return points
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func normalizeAnalyticsSymbols(symbols []string) ([]string, error) {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(symbols))
	for _, raw := range symbols {
		symbol, venue, err := market.NormalizeSymbol(raw)
		if err != nil {
			return nil, err
		}
		if venue != market.VenueUS {
			return nil, fmt.Errorf("market analytics currently support US stocks only")
		}
		if _, ok := seen[symbol]; !ok {
			seen[symbol] = struct{}{}
			result = append(result, symbol)
		}
	}
	sort.Strings(result)
	return result, nil
}

func (h *HTTP) analyticsVolume(w http.ResponseWriter, r *http.Request) {
	if h.Analytics == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "market analytics are unavailable"})
		return
	}
	spec, err := parseAnalyticsQuery(r, r.PathValue("symbol"), false)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	response, err := h.Analytics.Volume(r.Context(), spec)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *HTTP) analyticsFlow(w http.ResponseWriter, r *http.Request) {
	if h.Analytics == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "market analytics are unavailable"})
		return
	}
	spec, err := parseAnalyticsQuery(r, r.PathValue("symbol"), true)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	response, err := h.Analytics.Flow(r.Context(), spec)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *HTTP) analyticsBasketFlow(w http.ResponseWriter, r *http.Request) {
	if h.Analytics == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "market analytics are unavailable"})
		return
	}
	var body struct {
		Symbols  []string       `json:"symbols"`
		Interval string         `json:"interval"`
		From     time.Time      `json:"from"`
		To       time.Time      `json:"to"`
		Session  market.Session `json:"session"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	symbols, err := normalizeAnalyticsSymbols(body.Symbols)
	if err != nil || len(symbols) == 0 || len(symbols) > 200 {
		if err == nil {
			err = errors.New("symbols must contain 1 to 200 US stocks")
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	spec, err := (market.DatasetSpec{Symbols: symbols, Interval: body.Interval, From: body.From, To: body.To, Session: body.Session, Adjustment: market.Raw}).Normalize()
	if err != nil || market.IntervalDuration(spec.Interval) > 24*time.Hour {
		if err == nil {
			err = errors.New("basket flow supports intervals from 1m through 1d")
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, h.Analytics.BasketFlow(r.Context(), spec))
}

func (h *HTTP) analyticsSectorFlow(w http.ResponseWriter, r *http.Request) {
	if h.Analytics == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "market analytics are unavailable"})
		return
	}
	if taxonomy := r.URL.Query().Get("taxonomy"); taxonomy != "" && taxonomy != "sic" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "taxonomy must be sic"})
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and 500"})
			return
		}
		limit = parsed
	}
	date := r.URL.Query().Get("date")
	if date != "" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date must be YYYY-MM-DD"})
			return
		}
		response, err := h.Analytics.SectorFlow(r.Context(), date, limit)
		if err != nil {
			writeProviderError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	candidate := latestCompletedUSDate(time.Now(), h.Analytics.location)
	for attempts := 0; attempts < 7; attempts++ {
		response, err := h.Analytics.SectorFlow(r.Context(), candidate, limit)
		if err != nil {
			writeProviderError(w, err)
			return
		}
		if len(response.Sectors) > 0 {
			writeJSON(w, http.StatusOK, response)
			return
		}
		parsed, _ := time.Parse("2006-01-02", candidate)
		candidate = previousWeekday(parsed.AddDate(0, 0, -1)).Format("2006-01-02")
	}
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "no completed US grouped daily data found"})
}

func parseAnalyticsQuery(r *http.Request, rawSymbol string, forceRaw bool) (market.DatasetSpec, error) {
	q := r.URL.Query()
	from, err := time.Parse(time.RFC3339, q.Get("from"))
	if err != nil {
		return market.DatasetSpec{}, errors.New("from must be RFC3339")
	}
	to, err := time.Parse(time.RFC3339, q.Get("to"))
	if err != nil {
		return market.DatasetSpec{}, errors.New("to must be RFC3339")
	}
	adjustment := market.AdjustmentMode(q.Get("adjustment"))
	if forceRaw {
		adjustment = market.Raw
	} else if adjustment == "" {
		return market.DatasetSpec{}, errors.New("adjustment is required")
	}
	spec, err := (market.DatasetSpec{
		Symbols: []string{rawSymbol}, Interval: q.Get("interval"), From: from, To: to,
		Session: market.Session(q.Get("session")), Adjustment: adjustment,
	}).Normalize()
	if err != nil {
		return market.DatasetSpec{}, err
	}
	if spec.Session == market.ContinuousSession || market.IntervalDuration(spec.Interval) > 24*time.Hour {
		return market.DatasetSpec{}, errors.New("market analytics support US stock intervals from 1m through 1d")
	}
	return spec, nil
}

func latestCompletedUSDate(now time.Time, location *time.Location) string {
	local := now.In(location)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	closeAt := time.Date(local.Year(), local.Month(), local.Day(), 16, 0, 0, 0, location)
	if local.Before(closeAt) {
		date = date.AddDate(0, 0, -1)
	}
	return previousWeekday(date).Format("2006-01-02")
}

func previousWeekday(value time.Time) time.Time {
	for value.Weekday() == time.Saturday || value.Weekday() == time.Sunday {
		value = value.AddDate(0, 0, -1)
	}
	return value
}
