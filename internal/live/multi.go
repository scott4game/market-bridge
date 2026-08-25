package live

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type SourceRoute struct {
	Name   string
	Source Source
	Accept func(market.Venue) bool
}

type sourceState struct {
	State      string    `json:"state"`
	LastError  string    `json:"last_error,omitempty"`
	Connected  time.Time `json:"connected_at,omitempty"`
	Reconnects int64     `json:"reconnects"`
	Symbols    int       `json:"subscribed_symbols"`
}

type MultiSource struct {
	Routes []SourceRoute
	mu     sync.RWMutex
	states map[string]sourceState
}

func (m *MultiSource) Run(ctx context.Context, symbols []string, emit func(market.LiveEvent)) error {
	m.mu.Lock()
	m.states = map[string]sourceState{}
	for _, route := range m.Routes {
		m.states[route.Name] = sourceState{State: "idle"}
	}
	m.mu.Unlock()
	var started int
	var wg sync.WaitGroup
	for _, route := range m.Routes {
		var selected []string
		for _, symbol := range symbols {
			venue, err := market.VenueOf(symbol)
			if err == nil && route.Accept(venue) {
				selected = append(selected, symbol)
			}
		}
		if len(selected) == 0 || route.Source == nil {
			continue
		}
		started++
		route, selected := route, selected
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.runRoute(ctx, route, selected, emit)
		}()
	}
	if started == 0 {
		return fmt.Errorf("no live source matches the configured watchlist")
	}
	<-ctx.Done()
	wg.Wait()
	return ctx.Err()
}

func (m *MultiSource) runRoute(ctx context.Context, route SourceRoute, symbols []string, emit func(market.LiveEvent)) {
	backoff := time.Second
	for ctx.Err() == nil {
		var connected atomic.Bool
		m.setState(route.Name, func(state *sourceState) {
			state.State = "connecting"
			state.Symbols = len(symbols)
		})
		if aware, ok := route.Source.(connectionAwareSource); ok {
			aware.SetOnConnected(func() {
				connected.Store(true)
				m.setState(route.Name, func(state *sourceState) {
					state.State = "connected"
					state.Connected = time.Now().UTC()
					state.LastError = ""
				})
			})
		}
		err := route.Source.Run(ctx, symbols, emit)
		if ctx.Err() != nil {
			return
		}
		m.setState(route.Name, func(state *sourceState) {
			state.State = "degraded"
			state.Reconnects++
			if err != nil {
				state.LastError = "connection failed; inspect server logs"
			}
		})
		if err != nil {
			log.Printf("%s live provider disconnected: %v", route.Name, err)
		}
		if connected.Load() {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (m *MultiSource) setState(name string, mutate func(*sourceState)) {
	m.mu.Lock()
	state := m.states[name]
	mutate(&state)
	m.states[name] = state
	m.mu.Unlock()
}

func (m *MultiSource) ProviderStatus() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	nameSet := map[string]struct{}{}
	for name := range m.states {
		nameSet[name] = struct{}{}
	}
	for _, route := range m.Routes {
		nameSet[route.Name] = struct{}{}
	}
	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	sort.Strings(names)
	status := map[string]any{}
	for _, name := range names {
		state, ok := m.states[name]
		if !ok {
			state.State = "idle"
		}
		status[name] = map[string]any{"state": state.State, "last_error": state.LastError, "connected_at": state.Connected, "reconnects": state.Reconnects, "subscribed_symbols": state.Symbols}
	}
	return status
}
