package localclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scott4game/market-bridge/internal/config"
)

// MCPGateway owns only routing and administrator policy, never backend logic.
type MCPGateway struct {
	backends []*mcpBackend
	slots    chan struct{}
}
type mcpBackend struct {
	cfg     config.MCPBackend
	mu      sync.Mutex
	expires time.Time
	tools   []*mcp.Tool
	state   string
}

func NewMCPGateway(configs []config.MCPBackend) *MCPGateway {
	g := &MCPGateway{slots: make(chan struct{}, 8)}
	for _, c := range configs {
		g.backends = append(g.backends, &mcpBackend{cfg: c, state: "not_checked"})
	}
	return g
}

type backendTransport struct{ token string }

func (t backendTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	req := r.Clone(r.Context())
	req.Header = r.Header.Clone()
	req.Header.Del("Cookie")
	req.Header.Del("Proxy-Authorization")
	req.Header.Set("Authorization", "Bearer "+t.token)
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("backend transport unavailable")
	}
	// Bound both JSON and request-scoped SSE; do not silently truncate a result.
	res.Body = &boundedMCPBody{ReadCloser: res.Body, remaining: 16 << 20}
	return res, nil
}

type boundedMCPBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedMCPBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		var probe [1]byte
		n, e := b.ReadCloser.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("MCP response exceeds limit")
		}
		return 0, e
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, e := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, e
}
func (b *mcpBackend) connect(ctx context.Context) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "market-bridge-gateway", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	return client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: b.cfg.URL, HTTPClient: &http.Client{
		Transport: backendTransport{token: b.cfg.Token}, Timeout: time.Duration(b.cfg.TimeoutSeconds) * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
}
func (b *mcpBackend) refresh(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Now().Before(b.expires) {
		return
	}
	b.tools = nil
	b.state = "unavailable"
	b.expires = time.Now().Add(60 * time.Second)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	session, err := b.connect(ctx)
	if err != nil {
		return
	}
	defer session.Close()
	allowed := map[string]string{}
	for _, t := range b.cfg.Tools {
		allowed[t.Name] = t.Policy
	}
	found := map[string]bool{}
	cursors := map[string]bool{}
	cursor := ""
	var tools []*mcp.Tool
	for page := 0; page < 16; page++ {
		result, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return
		}
		for _, tool := range result.Tools {
			policy, ok := allowed[tool.Name]
			if !ok {
				continue
			}
			if found[tool.Name] {
				b.state = "invalid_catalog"
				return
			}
			found[tool.Name] = true
			schema, ok := tool.InputSchema.(map[string]any)
			if !ok || schema["type"] != "object" {
				b.state = "invalid_catalog"
				return
			}
			raw, err := json.Marshal(tool)
			if err != nil || len(raw) > 64<<10 {
				b.state = "invalid_catalog"
				return
			}
			sum := sha256.Sum256(append(raw, []byte(policy)...))
			copy := *tool
			copy.Name = b.cfg.ID + "__" + tool.Name
			// Do not forward upstream authority claims. These are gateway-owned fields.
			copy.Meta = mcp.Meta{"gateway/backend": b.cfg.ID, "gateway/policy": policy, "gateway/fingerprint": hex.EncodeToString(sum[:])}
			copy.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: policy == "query"}
			tools = append(tools, &copy)
		}
		if result.NextCursor == "" {
			b.tools = tools
			b.state = "available"
			if len(found) != len(allowed) {
				b.state = "partial_catalog"
			}
			return
		}
		if cursors[result.NextCursor] {
			b.state = "invalid_catalog"
			return
		}
		cursors[result.NextCursor] = true
		cursor = result.NextCursor
	}
	b.state = "invalid_catalog"
}
func mcpGatewayError(code string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: code}}, StructuredContent: map[string]any{"status": "unavailable", "error": code, "retryable": false}}
}
func (g *MCPGateway) bind(ctx context.Context, s *mcp.Server) {
	if g == nil {
		return
	}
	var wg sync.WaitGroup
	for _, b := range g.backends {
		wg.Add(1)
		go func() { defer wg.Done(); b.refresh(ctx) }()
	}
	wg.Wait()
	for _, b := range g.backends {
		b.mu.Lock()
		tools := append([]*mcp.Tool(nil), b.tools...)
		b.mu.Unlock()
		for _, tool := range tools {
			remoteName := tool.Name[len(b.cfg.ID)+2:]
			fingerprint := tool.Meta["gateway/fingerprint"]
			// SDK validates schema shape when registering. A malformed remote catalogue
			// must never panic the HTTP handler or leave partially advertised tools.
			if !safeAddMCPTool(s, tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				if f, ok := req.Params.Meta["gateway/fingerprint"]; ok && f != fingerprint {
					return mcpGatewayError("tool_catalog_changed"), nil
				}
				if len(req.Params.Arguments) > 128<<10 {
					return mcpGatewayError("tool_arguments_too_large"), nil
				}
				select {
				case g.slots <- struct{}{}:
					defer func() { <-g.slots }()
				default:
					return mcpGatewayError("gateway_busy"), nil
				}
				callCtx, cancel := context.WithTimeout(ctx, time.Duration(b.cfg.TimeoutSeconds)*time.Second)
				defer cancel()
				session, err := b.connect(callCtx)
				if err != nil {
					if ctx.Err() == nil {
						b.unavailable()
					}
					return mcpGatewayError("backend_unavailable"), nil
				}
				defer session.Close()
				// Exactly one invocation, with no caller headers or unrelated client metadata.
				result, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: remoteName, Arguments: json.RawMessage(req.Params.Arguments)})
				if err != nil {
					if ctx.Err() == nil {
						b.unavailable()
					}
					return mcpGatewayError("backend_call_failed_or_outcome_unknown"), nil
				}
				return result, nil
			}) {
				names := []string{}
				for _, t := range tools {
					names = append(names, t.Name)
				}
				s.RemoveTools(names...)
				b.mu.Lock()
				b.tools = nil
				b.state = "invalid_catalog"
				b.mu.Unlock()
				break
			}
		}
	}
	bindMCP(s, "get_mcp_backend_status", "Report configured MCP backend discovery status. Unavailable does not mean no analysis signal.", func(context.Context, struct{}) (map[string]any, error) { return g.status(), nil })
}
func safeAddMCPTool(s *mcp.Server, t *mcp.Tool, h mcp.ToolHandler) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	s.AddTool(t, h)
	return true
}
func (g *MCPGateway) status() map[string]any {
	rows := []map[string]any{}
	for _, b := range g.backends {
		b.mu.Lock()
		rows = append(rows, map[string]any{"id": b.cfg.ID, "status": b.state, "tools": len(b.tools)})
		b.mu.Unlock()
	}
	return map[string]any{"backends": rows, "refresh_seconds": 60}
}

func (b *mcpBackend) unavailable() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tools = nil
	b.state = "unavailable"
	b.expires = time.Now().Add(60 * time.Second)
}
