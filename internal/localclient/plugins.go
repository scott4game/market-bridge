package localclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scott4game/market-bridge/internal/config"
)

const pluginBodyLimit = 128 << 10
const pluginResponseLimit = 16 << 20

type pluginTarget struct {
	definition config.ClientPlugin
	client     *http.Client
}
type PluginGateway struct {
	targets map[string]pluginTarget
	order   []string
}

func NewPluginGateway(definitions []config.ClientPlugin) *PluginGateway {
	g := &PluginGateway{targets: map[string]pluginTarget{}}
	for _, p := range definitions {
		g.order = append(g.order, p.ID)
		g.targets[p.ID] = pluginTarget{p, &http.Client{Timeout: time.Duration(p.TimeoutSeconds) * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	}
	return g
}
func (g *PluginGateway) Catalog() []map[string]any {
	out := []map[string]any{}
	if g == nil {
		return out
	}
	for _, id := range g.order {
		p := g.targets[id].definition
		out = append(out, map[string]any{"id": id, "base_path": "/v1/plugins/" + id, "routes": p.Routes})
	}
	return out
}
func allowedPluginPath(routes []config.PluginRoute, method, path string) bool {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "%\\?#") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	for _, r := range routes {
		if r.Method != method {
			continue
		}
		expected := strings.Split(strings.TrimPrefix(r.Path, "/"), "/")
		if len(expected) != len(parts) {
			continue
		}
		match := true
		for i, want := range expected {
			if strings.HasPrefix(want, "{") && strings.HasSuffix(want, "}") {
				for _, ch := range parts[i] {
					if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '.') {
						match = false
						break
					}
				}
			} else if want != parts[i] {
				match = false
			}
		}
		if match {
			return true
		}
	}
	return false
}

type pluginReply struct {
	status     int
	value      json.RawMessage
	retryAfter string
}

func pluginFailure(status int, code string, retry bool) pluginReply {
	value, _ := json.Marshal(map[string]any{"error": code, "error_details": map[string]any{"code": code, "retryable": retry}})
	return pluginReply{status: status, value: value}
}
func (g *PluginGateway) call(ctx context.Context, id, method, path, query string, body []byte) pluginReply {
	if g == nil {
		return pluginFailure(404, "plugin_not_found", false)
	}
	target, ok := g.targets[id]
	if !ok {
		return pluginFailure(404, "plugin_not_found", false)
	}
	if !allowedPluginPath(target.definition.Routes, method, path) {
		return pluginFailure(404, "plugin_route_not_allowed", false)
	}
	if len(query) > 16384 || len(body) > pluginBodyLimit {
		return pluginFailure(413, "plugin_request_too_large", false)
	}
	if method == "POST" && (!json.Valid(body) || !bytes.HasPrefix(bytes.TrimSpace(body), []byte("{"))) {
		return pluginFailure(400, "plugin_invalid_json", false)
	}
	if method == "GET" && len(body) > 0 {
		return pluginFailure(400, "plugin_get_body_not_allowed", false)
	}
	base, _ := url.Parse(target.definition.URL)
	base.Path = strings.TrimSuffix(base.Path, "/") + path
	base.RawQuery = query
	req, err := http.NewRequestWithContext(ctx, method, base.String(), bytes.NewReader(body))
	if err != nil {
		return pluginFailure(400, "plugin_invalid_request", false)
	}
	req.Header.Set("Authorization", "Bearer "+target.definition.Token)
	req.Header.Set("Accept", "application/json")
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := target.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return pluginFailure(499, "plugin_request_cancelled", false)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return pluginFailure(504, "plugin_timeout", true)
		}
		return pluginFailure(502, "plugin_unavailable", true)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, pluginResponseLimit+1))
	if err != nil {
		return pluginFailure(502, "plugin_response_read_failed", true)
	}
	if len(data) > pluginResponseLimit {
		return pluginFailure(502, "plugin_response_too_large", false)
	}
	// Redact any echoed backend token, including JSON escaped representations.
	escaped, _ := json.Marshal(target.definition.Token)
	data = bytes.ReplaceAll(data, escaped[1:len(escaped)-1], []byte("[REDACTED]"))
	data = bytes.ReplaceAll(data, []byte(target.definition.Token), []byte("[REDACTED]"))
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return pluginFailure(502, "plugin_redirect_refused", false)
	}
	if !json.Valid(data) {
		status := response.StatusCode
		if status >= 200 && status < 300 {
			status = 502
		}
		reply := pluginFailure(status, "plugin_non_json_response", status == 429 || status >= 500)
		reply.retryAfter = response.Header.Get("Retry-After")
		return reply
	}
	return pluginReply{response.StatusCode, data, response.Header.Get("Retry-After")}
}
func (g *PluginGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.EscapedPath() != r.URL.Path {
		jsonResponse(w, 400, map[string]string{"error": "encoded plugin paths not supported"})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/plugins/")
	id, path, found := strings.Cut(rest, "/")
	if !found {
		jsonResponse(w, 404, map[string]string{"error": "plugin route not found"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pluginBodyLimit))
	if err != nil {
		jsonResponse(w, 413, map[string]string{"error": "plugin request too large"})
		return
	}
	reply := g.call(r.Context(), id, r.Method, "/"+path, r.URL.RawQuery, body)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if reply.retryAfter != "" {
		w.Header().Set("Retry-After", reply.retryAfter)
	}
	w.WriteHeader(reply.status)
	_, _ = w.Write(reply.value)
}

type pluginQueryInput struct {
	Plugin string `json:"plugin"`
	Path   string `json:"path"`
}
type pluginSubmitInput struct {
	Plugin string         `json:"plugin"`
	Path   string         `json:"path"`
	Body   map[string]any `json:"body"`
}

func (h *HTTP) bindPluginMCP(s *mcp.Server) {
	if h.Plugins == nil || len(h.Plugins.order) == 0 {
		return
	}
	bindMCP(s, "list_plugins", "List configured HTTP plugins and their allowed routes, without backend URLs or credentials.", func(context.Context, struct{}) (map[string]any, error) {
		return map[string]any{"plugins": h.Plugins.Catalog()}, nil
	})
	bindMCP(s, "query_plugin", "Read an explicitly allowlisted plugin GET route. Plugin identifiers and paths come from list_plugins.", func(ctx context.Context, input pluginQueryInput) (map[string]any, error) {
		return pluginMCPReply(h.Plugins.call(ctx, input.Plugin, "GET", input.Path, "", nil))
	})
	mcp.AddTool(s, &mcp.Tool{Name: "submit_plugin", Description: "Submit JSON to an explicitly allowlisted plugin POST route; may create a background job. Never retries automatically.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}},
		func(ctx context.Context, _ *mcp.CallToolRequest, input pluginSubmitInput) (*mcp.CallToolResult, map[string]any, error) {
			body, err := json.Marshal(input.Body)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid plugin body")
			}
			value, err := pluginMCPReply(h.Plugins.call(ctx, input.Plugin, "POST", input.Path, "", body))
			return nil, value, err
		})
}
func pluginMCPReply(reply pluginReply) (map[string]any, error) {
	var value any
	if err := json.Unmarshal(reply.value, &value); err != nil {
		return nil, fmt.Errorf("plugin response invalid")
	}
	return map[string]any{"http_status": reply.status, "ok": reply.status >= 200 && reply.status < 300, "result": value, "retry_after": reply.retryAfter}, nil
}
