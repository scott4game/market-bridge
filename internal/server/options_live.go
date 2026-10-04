package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scott4game/market-bridge/internal/provider"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/singleflight"
)

type optionLiveCacheEntry struct {
	raw                    []byte
	fetched, expires, used time.Time
}
type OptionLiveService struct {
	source      provider.OptionsLiveProvider
	mu          sync.Mutex
	cache       map[string]optionLiveCacheEntry
	group       singleflight.Group
	now         func() time.Time
	lastSuccess time.Time
	lastError   string
}

func NewOptionLiveService(source provider.OptionsLiveProvider) *OptionLiveService {
	return &OptionLiveService{source: source, cache: map[string]optionLiveCacheEntry{}, now: time.Now}
}
func (s *OptionLiveService) Status() map[string]any {
	if s == nil {
		return map[string]any{"state": "disabled", "provider": "disabled", "configured": false}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{"state": "configured", "provider": "longbridge", "configured": true, "last_query_success_at": s.lastSuccess, "last_error": s.lastError}
}
func (s *OptionLiveService) cached(key string) (optionLiveCacheEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[key]
	now := s.now()
	if !ok {
		return e, false
	}
	if !now.Before(e.expires) {
		delete(s.cache, key)
		return e, false
	}
	e.used = now
	s.cache[key] = e
	return e, true
}
func (s *OptionLiveService) load(ctx context.Context, key string, ttl time.Duration, fetch func(context.Context) (any, error)) (optionLiveCacheEntry, bool, error) {
	if e, ok := s.cached(key); ok {
		return e, true, nil
	}
	// The shared fetch has a fixed lifetime independent of an individual waiter.
	// Canceling one HTTP request must not cancel another request sharing its work.
	ch := s.group.DoChan(key, func() (any, error) {
		if e, ok := s.cached(key); ok {
			return e, nil
		}
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		data, err := fetch(callCtx)
		if err != nil {
			safe := provider.SafeOptionLiveError(err)
			s.mu.Lock()
			s.lastError = safe.Code
			s.mu.Unlock()
			return nil, safe
		}
		raw, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		now := s.now()
		e := optionLiveCacheEntry{raw: raw, fetched: now, expires: now.Add(ttl), used: now}
		if len(s.cache) >= 1000 {
			oldest := ""
			var used time.Time
			for k, v := range s.cache {
				if oldest == "" || v.used.Before(used) {
					oldest = k
					used = v.used
				}
			}
			delete(s.cache, oldest)
		}
		s.cache[key] = e
		s.lastSuccess = now
		s.lastError = ""
		return e, nil
	})
	select {
	case <-ctx.Done():
		return optionLiveCacheEntry{}, false, provider.SafeOptionLiveError(ctx.Err())
	case res := <-ch:
		if res.Err != nil {
			return optionLiveCacheEntry{}, false, res.Err
		}
		return res.Val.(optionLiveCacheEntry), false, nil
	}
}

var optionStrikePattern = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,8})?$`)

type optionLiveRequest struct {
	operation, underlying, expiration, direction string
	symbols                                      []string
	strikeGTE, strikeLTE                         *decimal.Decimal
	offset, limit                                int
}

func parseOptionLiveRequest(operation string, q url.Values) (optionLiveRequest, error) {
	out := optionLiveRequest{operation: operation, limit: 100}
	var err error
	switch operation {
	case "quotes", "greeks":
		out.symbols, err = provider.NormalizeOptionSymbols(strings.Split(q.Get("symbols"), ","))
		return out, err
	case "expirations", "chain":
		out.underlying, err = provider.NormalizeOptionUnderlying(q.Get("underlying"))
		if err != nil {
			return out, err
		}
	default:
		return out, errors.New("unknown options operation")
	}
	if operation == "expirations" {
		return out, nil
	}
	out.expiration = q.Get("expiration")
	if _, err = time.Parse("2006-01-02", out.expiration); err != nil {
		return out, errors.New("expiration must be YYYY-MM-DD")
	}
	out.direction = strings.ToLower(strings.TrimSpace(q.Get("type")))
	if out.direction != "" && out.direction != "call" && out.direction != "put" {
		return out, errors.New("type must be call or put")
	}
	for _, f := range []struct {
		name   string
		target **decimal.Decimal
	}{{"strike_gte", &out.strikeGTE}, {"strike_lte", &out.strikeLTE}} {
		if raw := q.Get(f.name); raw != "" {
			if !optionStrikePattern.MatchString(raw) {
				return out, errors.New("invalid strike range")
			}
			d, e := decimal.NewFromString(raw)
			if e != nil || d.IsNegative() {
				return out, errors.New("invalid strike range")
			}
			*f.target = &d
		}
	}
	if out.strikeGTE != nil && out.strikeLTE != nil && out.strikeGTE.GreaterThan(*out.strikeLTE) {
		return out, errors.New("invalid strike range")
	}
	if raw := q.Get("offset"); raw != "" {
		out.offset, err = strconv.Atoi(raw)
		if err != nil || out.offset < 0 {
			return out, errors.New("offset must be nonnegative")
		}
	}
	if raw := q.Get("limit"); raw != "" {
		out.limit, err = strconv.Atoi(raw)
		if err != nil || out.limit < 1 || out.limit > 500 {
			return out, errors.New("limit must be 1..500")
		}
	}
	return out, nil
}
func (s *OptionLiveService) query(ctx context.Context, q optionLiveRequest) (map[string]any, error) {
	ttl := 2 * time.Second
	key := "longbridge:" + q.operation + ":" + strings.Join(q.symbols, ",")
	if q.operation == "chain" || q.operation == "expirations" {
		key = "longbridge:" + q.operation + ":" + q.underlying + ":" + q.expiration
		ttl = 5 * time.Minute
	}
	e, hit, err := s.load(ctx, key, ttl, func(ctx context.Context) (any, error) {
		switch q.operation {
		case "expirations":
			return s.source.Expirations(ctx, q.underlying)
		case "chain":
			d, _ := time.Parse("2006-01-02", q.expiration)
			return s.source.Chain(ctx, q.underlying, d)
		case "quotes":
			return s.source.Quotes(ctx, q.symbols)
		default:
			return s.source.Greeks(ctx, q.symbols)
		}
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"provider": "longbridge", "cache_hit": hit, "fetched_at": e.fetched.UTC()}
	switch q.operation {
	case "chain":
		var all []provider.LiveOptionContract
		if err = json.Unmarshal(e.raw, &all); err != nil {
			return nil, err
		}
		rows := []provider.LiveOptionContract{}
		for _, r := range all {
			if q.direction != "" && r.Type != q.direction {
				continue
			}
			if q.strikeGTE != nil || q.strikeLTE != nil {
				if r.StrikePrice == nil {
					continue
				}
				d, err := decimal.NewFromString(*r.StrikePrice)
				if err != nil {
					continue
				}
				if q.strikeGTE != nil && d.LessThan(*q.strikeGTE) {
					continue
				}
				if q.strikeLTE != nil && d.GreaterThan(*q.strikeLTE) {
					continue
				}
			}
			rows = append(rows, r)
		}
		start := min(q.offset, len(rows))
		end := start + min(q.limit, len(rows)-start)
		out["underlying"] = q.underlying
		out["expiration"] = q.expiration
		out["total"] = len(rows)
		out["offset"] = q.offset
		out["limit"] = q.limit
		out["count"] = end - start
		out["contracts"] = rows[start:end]
	case "expirations":
		var dates []string
		if err = json.Unmarshal(e.raw, &dates); err != nil {
			return nil, err
		}
		if dates == nil {
			dates = []string{}
		}
		out["underlying"] = q.underlying
		out["expirations"] = dates
		out["count"] = len(dates)
	default:
		var rows []map[string]any
		if err = json.Unmarshal(e.raw, &rows); err != nil {
			return nil, err
		}
		// Keep integer and decimal representations intact when serializing the cache.
		var rawRows []json.RawMessage
		if err = json.Unmarshal(e.raw, &rawRows); err != nil {
			return nil, err
		}
		wanted := map[string]bool{}
		for _, sym := range q.symbols {
			wanted[sym] = true
		}
		found := map[string]bool{}
		filtered := []json.RawMessage{}
		for i, r := range rows {
			sym, _ := r["symbol"].(string)
			if wanted[sym] && !found[sym] {
				filtered = append(filtered, rawRows[i])
				found[sym] = true
			}
		}
		missing := []string{}
		for _, sym := range q.symbols {
			if !found[sym] {
				missing = append(missing, sym)
			}
		}
		sort.Strings(missing)
		out[q.operation] = filtered
		out["count"] = len(filtered)
		out["missing_symbols"] = missing
	}
	return out, nil
}
func (h *HTTP) optionLive(w http.ResponseWriter, r *http.Request) {
	if h.OptionsLive == nil {
		writeJSON(w, 503, map[string]string{"error": "options live provider is not enabled", "code": "provider_disabled"})
		return
	}
	operation := strings.TrimPrefix(r.URL.Path, "/v1/options/")
	q, err := parseOptionLiveRequest(operation, r.URL.Query())
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error(), "code": "invalid_request"})
		return
	}
	out, err := h.OptionsLive.query(r.Context(), q)
	if err != nil {
		safe := provider.SafeOptionLiveError(err)
		writeJSON(w, safe.Status, map[string]string{"error": safe.Error(), "code": safe.Code})
		return
	}
	writeJSON(w, 200, out)
}
