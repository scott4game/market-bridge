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

type PluginRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}
type ClientPlugin struct {
	ID             string        `json:"id"`
	URL            string        `json:"url"`
	TokenEnv       string        `json:"token_env"`
	TimeoutSeconds int           `json:"timeout_seconds,omitempty"`
	Routes         []PluginRoute `json:"routes"`
	Token          string        `json:"-"`
}

var pluginID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var pluginEnvironment = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
var pluginSegment = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)
var pluginParameter = regexp.MustCompile(`^\{[a-z][a-z0-9_]*\}$`)

// Runtime implementation and secrets stay in the separately deployed service.
func (c Client) Plugins() ([]ClientPlugin, error) {
	if strings.TrimSpace(c.PluginsJSON) == "" {
		return nil, nil
	}
	if len(c.PluginsJSON) > 65536 {
		return nil, fmt.Errorf("GO_CLIENT_PLUGINS exceeds 64 KiB")
	}
	var plugins []ClientPlugin
	dec := json.NewDecoder(bytes.NewBufferString(c.PluginsJSON))
	dec.DisallowUnknownFields()
	if dec.Decode(&plugins) != nil {
		return nil, fmt.Errorf("GO_CLIENT_PLUGINS must be a JSON array of plugin definitions")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("GO_CLIENT_PLUGINS has trailing data")
	}
	if len(plugins) > 16 {
		return nil, fmt.Errorf("GO_CLIENT_PLUGINS supports at most 16 plugins")
	}
	seen := map[string]bool{}
	for i := range plugins {
		p := &plugins[i]
		if !pluginID.MatchString(p.ID) || seen[p.ID] {
			return nil, fmt.Errorf("invalid or duplicate plugin id")
		}
		seen[p.ID] = true
		u, err := url.Parse(p.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
			return nil, fmt.Errorf("invalid plugin URL for %s", p.ID)
		}
		for _, part := range strings.Split(strings.Trim(u.Path, "/"), "/") {
			if part == "." || part == ".." {
				return nil, fmt.Errorf("invalid plugin URL path for %s", p.ID)
			}
		}
		if !pluginEnvironment.MatchString(p.TokenEnv) {
			return nil, fmt.Errorf("plugin %s requires token_env", p.ID)
		}
		p.Token = os.Getenv(p.TokenEnv)
		if p.Token == "" || strings.ContainsAny(p.Token, " \t\r\n") {
			return nil, fmt.Errorf("plugin %s credential environment is missing or invalid", p.ID)
		}
		if p.Token == c.RemoteToken || p.Token == c.ServerToken {
			return nil, fmt.Errorf("plugin %s requires an independent upstream credential", p.ID)
		}
		if p.TimeoutSeconds == 0 {
			p.TimeoutSeconds = 30
		}
		if p.TimeoutSeconds < 1 || p.TimeoutSeconds > 180 {
			return nil, fmt.Errorf("plugin %s timeout must be 1..180 seconds", p.ID)
		}
		if len(p.Routes) == 0 || len(p.Routes) > 32 {
			return nil, fmt.Errorf("plugin %s requires 1..32 explicit routes", p.ID)
		}
		paths := map[string]bool{}
		for _, r := range p.Routes {
			if r.Method != "GET" && r.Method != "POST" {
				return nil, fmt.Errorf("plugin %s only permits GET/POST routes", p.ID)
			}
			if !strings.HasPrefix(r.Path, "/") || strings.HasSuffix(r.Path, "/") {
				return nil, fmt.Errorf("invalid plugin route for %s", p.ID)
			}
			for _, s := range strings.Split(strings.TrimPrefix(r.Path, "/"), "/") {
				if !pluginSegment.MatchString(s) && !pluginParameter.MatchString(s) {
					return nil, fmt.Errorf("invalid plugin route segment for %s", p.ID)
				}
			}
			k := r.Method + " " + r.Path
			if paths[k] {
				return nil, fmt.Errorf("duplicate plugin route for %s", p.ID)
			}
			paths[k] = true
		}
	}
	return plugins, nil
}
