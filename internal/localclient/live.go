package localclient

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/market"
)

type liveSubscriber struct {
	symbols map[string]struct{}
	queue   chan []byte
}

type LiveSink interface {
	Write(context.Context, market.LiveEvent) error
}

type LiveProxy struct {
	cfg           config.Client
	sink          LiveSink
	mirrorSymbols map[string]struct{}
	mu            sync.RWMutex
	subs          map[*liveSubscriber]struct{}
	changed       chan struct{}
}

func NewLiveProxy(cfg config.Client, sinks ...LiveSink) *LiveProxy {
	p := &LiveProxy{cfg: cfg, mirrorSymbols: map[string]struct{}{}, subs: map[*liveSubscriber]struct{}{}, changed: make(chan struct{}, 1)}
	if len(sinks) > 0 {
		p.sink = sinks[0]
	}
	for _, symbol := range cfg.MirrorWatchlist {
		if normalized, _, err := market.NormalizeSymbol(symbol); err == nil {
			p.mirrorSymbols[normalized] = struct{}{}
		}
	}
	return p
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
	connectionCtx := c.CloseRead(r.Context())
	s := &liveSubscriber{symbols: map[string]struct{}{}, queue: make(chan []byte, 128)}
	for _, symbol := range request.Symbols {
		normalized, _, err := market.NormalizeSymbol(symbol)
		if err == nil {
			s.symbols[normalized] = struct{}{}
		}
	}
	p.mu.Lock()
	p.subs[s] = struct{}{}
	p.mu.Unlock()
	p.signal()
	defer func() { p.mu.Lock(); delete(p.subs, s); p.mu.Unlock(); p.signal() }()
	for {
		select {
		case <-connectionCtx.Done():
			return
		case msg := <-s.queue:
			writeCtx, cancel := context.WithTimeout(connectionCtx, 10*time.Second)
			err := c.Write(writeCtx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (p *LiveProxy) Run(ctx context.Context) {
	backoff := 500 * time.Millisecond
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
			if !waitLiveReconnect(ctx, backoff) {
				return
			}
			backoff = nextLiveBackoff(backoff)
			continue
		}
		payload, _ := json.Marshal(map[string]any{"action": "subscribe", "symbols": symbols, "events": []string{"bar", "trade", "depth"}})
		if err = conn.Write(ctx, websocket.MessageText, payload); err != nil {
			conn.CloseNow()
			if !waitLiveReconnect(ctx, backoff) {
				return
			}
			backoff = nextLiveBackoff(backoff)
			continue
		}
		readCtx, cancel := context.WithCancel(ctx)
		errc := make(chan error, 1)
		received := make(chan struct{}, 1)
		go func() {
			for {
				_, msg, e := conn.Read(readCtx)
				if e != nil {
					errc <- e
					return
				}
				select {
				case received <- struct{}{}:
				default:
				}
				p.handleEvent(readCtx, msg)
			}
		}()
		reconnect := false
		active := true
		for active {
			select {
			case <-ctx.Done():
				cancel()
				conn.CloseNow()
				return
			case <-p.changed:
				active = false
			case <-received:
				backoff = 500 * time.Millisecond
			case <-errc:
				reconnect = true
				active = false
			}
		}
		cancel()
		conn.CloseNow()
		if reconnect {
			if !waitLiveReconnect(ctx, backoff) {
				return
			}
			backoff = nextLiveBackoff(backoff)
		}
	}
}

func waitLiveReconnect(ctx context.Context, backoff time.Duration) bool {
	jitter := time.Duration(time.Now().UnixNano()%250) * time.Millisecond
	timer := time.NewTimer(backoff + jitter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextLiveBackoff(current time.Duration) time.Duration {
	current *= 2
	if current > 30*time.Second {
		return 30 * time.Second
	}
	return current
}

func (p *LiveProxy) symbols() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	set := map[string]struct{}{}
	for symbol := range p.mirrorSymbols {
		set[symbol] = struct{}{}
	}
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
func (p *LiveProxy) handleEvent(ctx context.Context, msg []byte) {
	var event market.LiveEvent
	if err := json.Unmarshal(msg, &event); err != nil {
		return
	}
	symbol, _, err := market.NormalizeSymbol(event.Symbol)
	if err != nil {
		return
	}
	event.Symbol = symbol
	if p.sink != nil {
		if _, mirrored := p.mirrorSymbols[symbol]; mirrored {
			persist := true
			if p.cfg.ClickHouseCompletedBarsOnly {
				persist = event.Type == market.BarEvent && event.Bar != nil && event.Bar.Completed
			}
			if persist {
				if err := p.sink.Write(ctx, event); err != nil {
					log.Printf("write mirrored event for %s: %v", symbol, err)
				}
			}
		}
	}
	p.broadcastSymbol(symbol, msg)
}
func (p *LiveProxy) signal() {
	select {
	case p.changed <- struct{}{}:
	default:
	}
}
func (p *LiveProxy) broadcastSymbol(symbol string, msg []byte) {
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
