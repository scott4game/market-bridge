package localclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"massive-go/internal/config"
)

type liveSubscriber struct {
	symbols map[string]struct{}
	queue   chan []byte
}

type LiveProxy struct {
	cfg     config.Client
	mu      sync.RWMutex
	subs    map[*liveSubscriber]struct{}
	changed chan struct{}
}

func NewLiveProxy(cfg config.Client) *LiveProxy {
	return &LiveProxy{cfg: cfg, subs: map[*liveSubscriber]struct{}{}, changed: make(chan struct{}, 1)}
}

func (p *LiveProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	_, raw, err := c.Read(r.Context())
	if err != nil {
		return
	}
	var request struct {
		Symbols []string `json:"symbols"`
	}
	if json.Unmarshal(raw, &request) != nil || len(request.Symbols) == 0 {
		_ = c.Close(websocket.StatusPolicyViolation, "symbols required")
		return
	}
	s := &liveSubscriber{symbols: map[string]struct{}{}, queue: make(chan []byte, 128)}
	for _, symbol := range request.Symbols {
		s.symbols[strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(symbol), ".US"))] = struct{}{}
	}
	p.mu.Lock()
	p.subs[s] = struct{}{}
	p.mu.Unlock()
	p.signal()
	defer func() { p.mu.Lock(); delete(p.subs, s); p.mu.Unlock(); p.signal() }()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-s.queue:
			if err := c.Write(r.Context(), websocket.MessageText, msg); err != nil {
				return
			}
		}
	}
}

func (p *LiveProxy) Run(ctx context.Context) {
	for {
		symbols := p.symbols()
		if len(symbols) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-p.changed:
				continue
			}
		}
		u, err := url.Parse(p.cfg.ServerURL)
		if err != nil {
			return
		}
		if u.Scheme == "https" {
			u.Scheme = "wss"
		} else {
			u.Scheme = "ws"
		}
		u.Path = "/v1/live/ws"
		headers := http.Header{}
		if p.cfg.ServerToken != "" {
			headers.Set("Authorization", "Bearer "+p.cfg.ServerToken)
		}
		conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: headers})
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
				continue
			}
		}
		payload, _ := json.Marshal(map[string]any{"action": "subscribe", "symbols": symbols, "events": []string{"bar", "trade", "depth"}})
		if err = conn.Write(ctx, websocket.MessageText, payload); err != nil {
			conn.CloseNow()
			continue
		}
		readCtx, cancel := context.WithCancel(ctx)
		errc := make(chan error, 1)
		go func() {
			for {
				_, msg, e := conn.Read(readCtx)
				if e != nil {
					errc <- e
					return
				}
				p.broadcast(msg)
			}
		}()
		select {
		case <-ctx.Done():
			cancel()
			conn.CloseNow()
			return
		case <-p.changed:
			cancel()
			conn.CloseNow()
		case <-errc:
			cancel()
			conn.CloseNow()
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

func (p *LiveProxy) symbols() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	set := map[string]struct{}{}
	for s := range p.subs {
		for symbol := range s.symbols {
			set[symbol] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for symbol := range set {
		out = append(out, symbol)
	}
	sort.Strings(out)
	return out
}
func (p *LiveProxy) signal() {
	select {
	case p.changed <- struct{}{}:
	default:
	}
}
func (p *LiveProxy) broadcast(msg []byte) {
	var envelope struct {
		Symbol string `json:"symbol"`
	}
	_ = json.Unmarshal(msg, &envelope)
	symbol := strings.ToUpper(strings.TrimSuffix(envelope.Symbol, ".US"))
	p.mu.RLock()
	defer p.mu.RUnlock()
	for s := range p.subs {
		if _, ok := s.symbols[symbol]; !ok {
			continue
		}
		copyMsg := append([]byte(nil), msg...)
		select {
		case s.queue <- copyMsg:
		default:
			select {
			case <-s.queue:
			default:
			}
			gap, _ := json.Marshal(map[string]any{"type": "gap", "symbol": symbol, "reason": "slow_consumer"})
			select {
			case s.queue <- gap:
			default:
			}
		}
	}
}
