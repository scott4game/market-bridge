package config

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var indexSymbolPattern = regexp.MustCompile(`^I:[A-Z0-9][A-Z0-9._-]*$`)

// ParsedIndexRoutes rejects ambiguous routes instead of silently overwriting them.
func (s Server) ParsedIndexRoutes() (map[string]string, error) {
	routes := map[string]string{}
	if strings.TrimSpace(s.IndexRoutes) == "" {
		return routes, nil
	}
	for _, entry := range strings.Split(s.IndexRoutes, ",") {
		symbol, name, ok := strings.Cut(entry, "=")
		symbol = strings.ToUpper(strings.TrimSpace(symbol))
		name = strings.ToLower(strings.TrimSpace(name))
		if !ok || !indexSymbolPattern.MatchString(symbol) {
			return nil, fmt.Errorf("invalid GO_SERVER_INDEX_ROUTES entry %q", entry)
		}
		switch name {
		case "disabled", "longbridge", "fmp", "massive", "mock":
		default:
			return nil, fmt.Errorf("unsupported index provider %q for %s", name, symbol)
		}
		if _, exists := routes[symbol]; exists {
			return nil, fmt.Errorf("duplicate index route %s", symbol)
		}
		routes[symbol] = name
	}
	return routes, nil
}

// EffectiveIndexProviders includes the default even when explicit routes exist.
// Call Validate before using this configuration to initialize providers.
func (s Server) EffectiveIndexProviders() []string {
	seen := map[string]bool{}
	if s.IndexProvider != "" && s.IndexProvider != "disabled" {
		seen[s.IndexProvider] = true
	}
	routes, _ := s.ParsedIndexRoutes()
	for _, name := range routes {
		if name != "disabled" {
			seen[name] = true
		}
	}
	var names []string
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s Server) UsesIndexProvider(name string) bool {
	return containsString(s.EffectiveIndexProviders(), name)
}

func (s Server) IndexRoutingVersion() string {
	routes, _ := s.ParsedIndexRoutes()
	entries := make([]string, 0, len(routes))
	for symbol, name := range routes {
		entries = append(entries, symbol+"="+name)
	}
	sort.Strings(entries)
	return fmt.Sprintf("index-%x", sha256.Sum256([]byte(s.IndexProvider+";"+strings.Join(entries, ","))))
}
