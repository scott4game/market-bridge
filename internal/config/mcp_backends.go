package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
)

type MCPAllowedTool struct {
	Name   string `json:"name"`
	Policy string `json:"policy"` // query or analysis; administrator authority, not an upstream hint
}
type MCPBackend struct {
	ID             string           `json:"id"`
	URL            string           `json:"url"`
	TokenEnv       string           `json:"token_env"`
	TimeoutSeconds int              `json:"timeout_seconds,omitempty"`
	Tools          []MCPAllowedTool `json:"tools"`
	Token          string           `json:"-"`
}

var mcpToolName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)

func (c Client) MCPBackends() ([]MCPBackend, error) {
	if strings.TrimSpace(c.MCPBackendsJSON) == "" {
		return nil, nil
	}
	bad := func() ([]MCPBackend, error) {
		return nil, fmt.Errorf("invalid GO_CLIENT_MCP_BACKENDS configuration or credentials")
	}
	if len(c.MCPBackendsJSON) > 65536 {
		return bad()
	}
	var backends []MCPBackend
	dec := json.NewDecoder(bytes.NewBufferString(c.MCPBackendsJSON))
	dec.DisallowUnknownFields()
	if dec.Decode(&backends) != nil || dec.Decode(new(any)) != io.EOF || len(backends) > 16 {
		return bad()
	}
	ids := map[string]bool{}
	for i := range backends {
		b := &backends[i]
		if !pluginID.MatchString(b.ID) || strings.Contains(b.ID, "__") || ids[b.ID] || !pluginEnvironment.MatchString(b.TokenEnv) {
			return bad()
		}
		ids[b.ID] = true
		u, err := url.Parse(b.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
			return bad()
		}
		for _, part := range strings.Split(u.Path, "/") {
			if part == "." || part == ".." {
				return bad()
			}
		}
		b.Token = os.Getenv(b.TokenEnv)
		if len(b.Token) < 24 || strings.ContainsAny(b.Token, " \r\n\t") || b.Token == c.RemoteToken || b.Token == c.ServerToken {
			return bad()
		}
		if b.TimeoutSeconds == 0 {
			b.TimeoutSeconds = 30
		}
		if b.TimeoutSeconds < 1 || b.TimeoutSeconds > 180 {
			return bad()
		}
		if len(b.Tools) == 0 || len(b.Tools) > 64 {
			return bad()
		}
		names := map[string]bool{}
		for _, tool := range b.Tools {
			if !mcpToolName.MatchString(tool.Name) || len(b.ID)+2+len(tool.Name) > 64 || names[tool.Name] || (tool.Policy != "query" && tool.Policy != "analysis") {
				return bad()
			}
			names[tool.Name] = true
		}
	}
	return backends, nil
}
