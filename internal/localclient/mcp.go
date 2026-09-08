package localclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/news"
	"github.com/scott4game/market-bridge/internal/provider"
)

type mcpBarsInput struct {
	Symbol     string `json:"symbol" jsonschema:"Symbol using existing market suffixes, e.g. NVDA or 700.HK"`
	Interval   string `json:"interval" jsonschema:"Existing interval: 1m,3m,5m,10m,15m,30m,1h,2h,3h,4h,1d,1w,1mo,1y"`
	From       string `json:"from" jsonschema:"Range start in RFC3339 with timezone"`
	To         string `json:"to" jsonschema:"Range end in RFC3339 with timezone; must follow from"`
	Session    string `json:"session,omitempty" jsonschema:"regular, extended or continuous; defaults to the market session"`
	Adjustment string `json:"adjustment,omitempty" jsonschema:"auto, raw, split_adjusted or forward_adjusted; defaults follow the existing market API"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Maximum returned rows, default 100, range 1..1000; most recent rows in ascending time order"`
}

type mcpTradesInput struct {
	Symbol string `json:"symbol" jsonschema:"Longbridge security symbol; Binance is unsupported"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Default 100, range 1..1000"`
}

type mcpNewsInput struct {
	Symbols        []string    `json:"symbols,omitempty" jsonschema:"US stock symbols; omitted means all symbols"`
	Kinds          []news.Kind `json:"kinds,omitempty" jsonschema:"stock_news or press_release; omitted means both"`
	AfterSequence  int64       `json:"after_sequence,omitempty" jsonschema:"Exclusive forward cursor; cannot combine with before_sequence"`
	BeforeSequence int64       `json:"before_sequence,omitempty" jsonschema:"Exclusive backward cursor from next_before_sequence"`
	Limit          int         `json:"limit,omitempty" jsonschema:"Default 50, range 1..500"`
}

type mcpContractsInput struct {
	Underlying     string  `json:"underlying" jsonschema:"US stock symbol"`
	Type           string  `json:"type,omitempty" jsonschema:"call or put"`
	ExpirationFrom string  `json:"expiration_from,omitempty" jsonschema:"YYYY-MM-DD"`
	ExpirationTo   string  `json:"expiration_to,omitempty" jsonschema:"YYYY-MM-DD"`
	StrikeGTE      float64 `json:"strike_gte,omitempty" jsonschema:"Minimum strike price"`
	StrikeLTE      float64 `json:"strike_lte,omitempty" jsonschema:"Maximum strike price"`
	AsOf           string  `json:"as_of,omitempty" jsonschema:"YYYY-MM-DD"`
	Limit          int     `json:"limit,omitempty" jsonschema:"Default 100, range 1..1000"`
	Offset         int     `json:"offset,omitempty" jsonschema:"Nonnegative offset into contracts sorted by ticker; default 0"`
}

type mcpOptionBarsInput struct {
	Contract string `json:"contract" jsonschema:"OCC ticker prefixed with O:"`
	From     string `json:"from" jsonschema:"YYYY-MM-DD or RFC3339 range start"`
	To       string `json:"to" jsonschema:"YYYY-MM-DD or RFC3339 range end"`
	Limit    int    `json:"limit,omitempty" jsonschema:"Default 100, range 1..1000; most recent rows in ascending time order"`
}

func bindMCP[I any](s *mcp.Server, name, description string, call func(context.Context, I) (map[string]any, error)) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcp.CallToolRequest, input I) (*mcp.CallToolResult, map[string]any, error) {
			result, err := call(ctx, input)
			return nil, result, err
		})
}

func (h *HTTP) mcpHandler() http.Handler {
	s := mcp.NewServer(&mcp.Implementation{Name: "market-bridge", Version: "1.0.0"}, nil)
	bindMCP(s, "get_bars", "Query historical bars. Cache misses may fetch and cache upstream data. Returns the latest limited rows within the explicit time range, not a complete history when truncated.", h.mcpBars)
	bindMCP(s, "get_recent_trades", "Query recent Longbridge trades; requires the upstream live provider.", h.mcpTrades)
	bindMCP(s, "get_news", "Query locally stored news using sequence pagination.", h.mcpNews)
	bindMCP(s, "get_option_contracts", "Query option contracts sorted by ticker with offset pagination; requires the options provider.", h.mcpContracts)
	bindMCP(s, "get_option_bars", "Query option bars at the existing server's native granularity; no interval conversion. Cache misses may fetch upstream data.", h.mcpOptionBars)
	bindMCP(s, "get_provider_status", "Query upstream market provider availability.", func(ctx context.Context, _ struct{}) (map[string]any, error) {
		return h.mcpServerJSON(ctx, "/v1/providers/status", nil)
	})
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: true, MaxRequestBodyBytes: 1 << 20})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, _, err := net.SplitHostPort(r.RemoteAddr)
		host := r.Host
		if hostname, _, e := net.SplitHostPort(host); e == nil {
			host = hostname
		}
		if err != nil || !isLoopback(peer) || !(strings.EqualFold(host, "localhost") || isLoopback(host)) {
			http.Error(w, "MCP is restricted to localhost", http.StatusForbidden)
			return
		}
		transport.ServeHTTP(w, r)
	})
}

func isLoopback(host string) bool { ip := net.ParseIP(host); return ip != nil && ip.IsLoopback() }

func mcpLimit(n int) (int, error) {
	if n == 0 {
		return 100, nil
	}
	if n < 1 || n > 1000 {
		return 0, errors.New("limit must be between 1 and 1000")
	}
	return n, nil
}

func (h *HTTP) mcpBars(ctx context.Context, in mcpBarsInput) (map[string]any, error) {
	limit, err := mcpLimit(in.Limit)
	if err != nil {
		return nil, err
	}
	from, err := time.Parse(time.RFC3339, in.From)
	if err != nil {
		return nil, errors.New("from must be RFC3339")
	}
	to, err := time.Parse(time.RFC3339, in.To)
	if err != nil {
		return nil, errors.New("to must be RFC3339")
	}
	if in.Interval == "" {
		return nil, errors.New("interval is required")
	}
	spec, err := (market.DatasetSpec{Symbols: []string{in.Symbol}, Interval: in.Interval, From: from, To: to, Session: market.Session(in.Session), Adjustment: market.AdjustmentMode(in.Adjustment)}).Normalize()
	if err != nil {
		return nil, err
	}
	bars, source, fetchErr := h.Cache.Bars(ctx, spec)
	if fetchErr != nil && len(bars) == 0 {
		return nil, errors.New("historical bars unavailable; check client and provider status")
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Timestamp.Before(bars[j].Timestamp) })
	total := len(bars)
	if total > limit {
		bars = bars[total-limit:]
	}
	if bars == nil {
		bars = []market.Bar{}
	}
	out := map[string]any{"bars": bars, "source": source, "count": len(bars), "total_count": total, "truncated": total > limit, "spec": spec}
	if fetchErr != nil {
		out["warning"] = "Partial data returned: some requested history is unavailable"
	}
	return out, nil
}

func (h *HTTP) mcpServerJSON(ctx context.Context, path string, q url.Values) (map[string]any, error) {
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	raw, status, err := h.Cache.ServerJSON(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, errors.New("upstream request failed; check client and provider status")
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("upstream request failed (HTTP %d); check provider availability and access permissions", status)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return nil, errors.New("upstream returned an invalid response")
	}
	return out, nil
}

func (h *HTTP) mcpTrades(ctx context.Context, in mcpTradesInput) (map[string]any, error) {
	limit, err := mcpLimit(in.Limit)
	if err != nil {
		return nil, err
	}
	symbol, venue, err := market.NormalizeSymbol(in.Symbol)
	if err != nil {
		return nil, err
	}
	if venue == market.VenueBinance {
		return nil, errors.New("recent trades are only available for Longbridge securities")
	}
	return h.mcpServerJSON(ctx, "/v1/live/trades/"+url.PathEscape(symbol), url.Values{"limit": {strconv.Itoa(limit)}})
}

func (h *HTTP) mcpNews(ctx context.Context, in mcpNewsInput) (map[string]any, error) {
	if h.News == nil || h.News.Service == nil {
		return nil, errors.New("local news service is unavailable")
	}
	response, err := h.News.Service.List(ctx, news.Query{Symbols: in.Symbols, Kinds: in.Kinds, AfterSequence: in.AfterSequence, BeforeSequence: in.BeforeSequence, Limit: in.Limit})
	if err != nil {
		return nil, errors.New("news query failed; verify filters, cursor and limit (1..500), and local news availability")
	}
	out := map[string]any{"news": response.News, "latest_sequence": response.LatestSequence}
	if response.NextBeforeSequence != 0 {
		out["next_before_sequence"] = response.NextBeforeSequence
	}
	return out, nil
}

func (h *HTTP) mcpContracts(ctx context.Context, in mcpContractsInput) (map[string]any, error) {
	limit, err := mcpLimit(in.Limit)
	if err != nil {
		return nil, err
	}
	if in.Offset < 0 {
		return nil, errors.New("offset must be nonnegative")
	}
	query, err := (provider.OptionContractQuery{Underlying: in.Underlying, ContractType: in.Type, ExpirationFrom: in.ExpirationFrom, ExpirationTo: in.ExpirationTo, StrikeGTE: in.StrikeGTE, StrikeLTE: in.StrikeLTE, AsOf: in.AsOf}).Normalize()
	if err != nil {
		return nil, err
	}
	q := url.Values{"underlying": {query.Underlying}, "type": {query.ContractType}, "expiration_from": {query.ExpirationFrom}, "expiration_to": {query.ExpirationTo}, "as_of": {query.AsOf}, "strike_gte": {strconv.FormatFloat(query.StrikeGTE, 'f', -1, 64)}, "strike_lte": {strconv.FormatFloat(query.StrikeLTE, 'f', -1, 64)}}
	out, err := h.mcpServerJSON(ctx, "/v1/options/contracts", q)
	if err != nil {
		return nil, err
	}
	return mcpPage(out, "contracts", "ticker", in.Offset, limit, false)
}

func (h *HTTP) mcpOptionBars(ctx context.Context, in mcpOptionBarsInput) (map[string]any, error) {
	limit, err := mcpLimit(in.Limit)
	if err != nil {
		return nil, err
	}
	parse := func(s string) (time.Time, error) {
		if t, e := time.Parse(time.RFC3339, s); e == nil {
			return t, nil
		}
		return time.Parse("2006-01-02", s)
	}
	from, err := parse(in.From)
	if err != nil {
		return nil, errors.New("from must be YYYY-MM-DD or RFC3339")
	}
	to, err := parse(in.To)
	if err != nil {
		return nil, errors.New("to must be YYYY-MM-DD or RFC3339")
	}
	if !from.Before(to) {
		return nil, errors.New("from must be before to")
	}
	contract := strings.ToUpper(strings.TrimSpace(in.Contract))
	if !strings.HasPrefix(contract, "O:") || len(contract) < 5 {
		return nil, errors.New("contract must be an OCC ticker prefixed with O:")
	}
	out, err := h.mcpServerJSON(ctx, "/v1/options/bars/"+url.PathEscape(contract), url.Values{"from": {from.UTC().Format(time.RFC3339)}, "to": {to.UTC().Format(time.RFC3339)}})
	if err != nil {
		return nil, err
	}
	return mcpPage(out, "bars", "timestamp", 0, limit, true)
}

// mcpPage bounds existing upstream JSON arrays while preserving their metadata.
func mcpPage(out map[string]any, key, sortKey string, offset, limit int, recent bool) (map[string]any, error) {
	rows, ok := out[key].([]any)
	if !ok && out[key] != nil {
		return nil, errors.New("upstream returned an invalid list")
	}
	if rows == nil {
		rows = []any{}
	}
	// Validate before sorting so malformed rows cannot panic the server.
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			return nil, errors.New("upstream returned an invalid row")
		}
		if _, ok := m[sortKey].(string); !ok {
			return nil, errors.New("upstream returned an invalid sort key")
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].(map[string]any)[sortKey].(string), rows[j].(map[string]any)[sortKey].(string)
		if recent {
			ta, ea := time.Parse(time.RFC3339Nano, a)
			tb, eb := time.Parse(time.RFC3339Nano, b)
			if ea == nil && eb == nil {
				return ta.Before(tb)
			}
		}
		return a < b
	})
	total := len(rows)
	if recent && total > limit {
		offset = total - limit
	}
	if offset > total {
		offset = total
	}
	end := offset + min(limit, total-offset)
	out[key] = rows[offset:end]
	out["count"] = end - offset
	out["total_count"] = total
	out["truncated"] = offset > 0 || end < total
	if !recent && end < total {
		out["next_offset"] = end
	}
	return out, nil
}
