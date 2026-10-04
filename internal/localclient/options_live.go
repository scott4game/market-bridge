package localclient

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpOptionExpirationsInput struct {
	Underlying string `json:"underlying" jsonschema:"US stock symbol, e.g. AAPL or AAPL.US"`
}
type mcpOptionChainInput struct {
	Underlying string `json:"underlying" jsonschema:"US stock symbol"`
	Expiration string `json:"expiration" jsonschema:"Required expiration date YYYY-MM-DD; use get_option_expirations first"`
	Type       string `json:"type,omitempty" jsonschema:"call or put; omitted returns both"`
	StrikeGTE  string `json:"strike_gte,omitempty" jsonschema:"Minimum strike as a decimal string"`
	StrikeLTE  string `json:"strike_lte,omitempty" jsonschema:"Maximum strike as a decimal string"`
	Offset     int    `json:"offset,omitempty" jsonschema:"Nonnegative offset, default 0"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Default 100, maximum 500"`
}
type mcpOptionSymbolsInput struct {
	Symbols []string `json:"symbols" jsonschema:"1..100 native Longbridge option codes returned by get_option_chain; not Massive O: codes"`
}

func (h *HTTP) bindOptionsLiveMCP(s *mcp.Server) {
	bindMCP(s, "get_option_expirations", "Query current US option expiration dates through Longbridge; requires live:read.", func(ctx context.Context, in mcpOptionExpirationsInput) (map[string]any, error) {
		return h.mcpServerJSON(ctx, "/v1/options/expirations", url.Values{"underlying": {in.Underlying}})
	})
	bindMCP(s, "get_option_chain", "Query current Longbridge option contracts for one expiry, sorted by strike, type and symbol; not historical as_of data.", func(ctx context.Context, in mcpOptionChainInput) (map[string]any, error) {
		limit := in.Limit
		if limit == 0 {
			limit = 100
		}
		return h.mcpServerJSON(ctx, "/v1/options/chain", url.Values{"underlying": {in.Underlying}, "expiration": {in.Expiration}, "type": {in.Type}, "strike_gte": {in.StrikeGTE}, "strike_lte": {in.StrikeLTE}, "offset": {strconv.Itoa(in.Offset)}, "limit": {strconv.Itoa(limit)}})
	})
	bindMCP(s, "get_option_quotes", "Query Longbridge option quotes, IV and open interest. Inspect timestamps and missing_symbols; no real-time freshness guarantee outside trading hours.", func(ctx context.Context, in mcpOptionSymbolsInput) (map[string]any, error) {
		return h.mcpServerJSON(ctx, "/v1/options/quotes", url.Values{"symbols": {strings.Join(in.Symbols, ",")}})
	})
	bindMCP(s, "get_option_greeks", "Query Longbridge Delta, Gamma, Theta, Vega and Rho. Missing values remain null; fetched_at is retrieval time, not calculation time.", func(ctx context.Context, in mcpOptionSymbolsInput) (map[string]any, error) {
		return h.mcpServerJSON(ctx, "/v1/options/greeks", url.Values{"symbols": {strings.Join(in.Symbols, ",")}})
	})
}
