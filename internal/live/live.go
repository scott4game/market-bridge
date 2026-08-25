package live

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	lbconfig "github.com/longbridge/openapi-go/config"
	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/scott4game/market-bridge/internal/access"
	"github.com/scott4game/market-bridge/internal/market"
)

type Source interface {
	Run(context.Context, []string, func(market.LiveEvent)) error
}

type connectionAwareSource interface {
	SetOnConnected(func())
}
type Sink interface {
	Write(context.Context, market.LiveEvent) error
}
type NopSink struct{}

func (NopSink) Write(context.Context, market.LiveEvent) error { return nil }

type MockSource struct{}

func (MockSource) Run(ctx context.Context, symbols []string, emit func(market.LiveEvent)) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	seq := int64(0)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ts := <-ticker.C:
			for _, symbol := range symbols {
				seq++
				price := market.DecimalFromFloat(100 + float64(seq%20)/10)
				symbol = normalizeSymbol(symbol)
				emit(market.LiveEvent{Type: market.BarEvent, Symbol: symbol, Timestamp: ts.UTC(), Cursor: market.LiveCursor{StreamEpoch: "mock", EventType: market.BarEvent, Symbol: symbol, Sequence: seq}, Bar: &market.Bar{Symbol: symbol, Timestamp: ts.UTC().Truncate(time.Minute), Open: price, High: price, Low: price, Close: price, Volume: seq, Session: market.RegularSession, Source: "mock-live", Completed: false}})
			}
		}
	}
}

type LongbridgeSource struct {
	Quote        *lbquote.QuoteContext
	DepthEnabled bool
	OnConnected  func()
}

func (s *LongbridgeSource) SetOnConnected(fn func()) { s.OnConnected = fn }

func (s *LongbridgeSource) Run(ctx context.Context, symbols []string, emit func(market.LiveEvent)) error {
	q := s.Quote
	owned := false
	if q == nil {
		cfg, err := lbconfig.New()
		if err != nil {
			return err
		}
		q, err = lbquote.NewFromCfg(cfg)
		if err != nil {
			return err
		}
		owned = true
	}
	if owned {
		defer q.Close()
	}
	epoch := fmt.Sprintf("lb-%d", time.Now().UnixMilli())
	var barMu sync.Mutex
	bars := map[string]*market.Bar{}
	q.OnTrade(func(x *lbquote.PushTrade) {
		symbol := normalizeSymbol(x.Symbol)
		raw, _ := json.Marshal(x.Trade)
		ts := time.Now().UTC()
		if len(x.Trade) > 0 {
			ts = time.Unix(x.Trade[len(x.Trade)-1].Timestamp, 0).UTC()
		}
		emit(market.LiveEvent{Type: market.TradeEvent, Symbol: symbol, Timestamp: ts, Cursor: market.LiveCursor{StreamEpoch: epoch, EventType: market.TradeEvent, Symbol: symbol, Sequence: x.Sequence}, Trade: raw})
		barMu.Lock()
		defer barMu.Unlock()
		for _, trade := range x.Trade {
			price, err := market.DecimalFromString(trade.Price)
			if err != nil {
				continue
			}
			tradeTime := time.Unix(trade.Timestamp, 0).UTC()
			minute := tradeTime.Truncate(time.Minute)
			current := bars[symbol]
			if current == nil || !current.Timestamp.Equal(minute) {
				if current != nil {
					completed := *current
					completed.Completed = true
					emit(market.LiveEvent{Type: market.BarEvent, Symbol: symbol, Timestamp: completed.Timestamp, Cursor: market.LiveCursor{StreamEpoch: epoch, EventType: market.BarEvent, Symbol: symbol, Sequence: x.Sequence}, Bar: &completed})
				}
				current = &market.Bar{Symbol: symbol, Timestamp: minute, Open: price, High: price, Low: price, Close: price, Session: market.RegularSession, Source: "longbridge", Completed: false}
				bars[symbol] = current
			}
			if price > current.High {
				current.High = price
			}
			if price < current.Low {
				current.Low = price
			}
			current.Close = price
			current.Volume += trade.Volume
		}
		if current := bars[symbol]; current != nil {
			snapshot := *current
			emit(market.LiveEvent{Type: market.BarEvent, Symbol: symbol, Timestamp: snapshot.Timestamp, Cursor: market.LiveCursor{StreamEpoch: epoch, EventType: market.BarEvent, Symbol: symbol, Sequence: x.Sequence}, Bar: &snapshot})
		}
	})
	q.OnDepth(func(x *lbquote.PushDepth) {
		symbol := normalizeSymbol(x.Symbol)
		raw, _ := json.Marshal(map[string]any{"ask": x.Ask, "bid": x.Bid})
		emit(market.LiveEvent{Type: market.DepthEvent, Symbol: symbol, Timestamp: time.Now().UTC(), Cursor: market.LiveCursor{StreamEpoch: epoch, EventType: market.DepthEvent, Symbol: symbol, Sequence: x.Sequence}, Depth: raw})
	})
	lbSymbols := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		normalized, venue, err := market.NormalizeSymbol(symbol)
		if err != nil || venue == market.VenueBinance {
			continue
		}
		if venue == market.VenueUS {
			normalized += ".US"
		}
		lbSymbols = append(lbSymbols, normalized)
	}
	if len(lbSymbols) == 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	subTypes := []lbquote.SubType{lbquote.SubTypeQuote, lbquote.SubTypeTrade}
	if s.DepthEnabled {
		subTypes = append(subTypes, lbquote.SubTypeDepth)
	}
	if err := q.Subscribe(ctx, lbSymbols, subTypes, true); err != nil {
		return err
	}
	if s.OnConnected != nil {
		s.OnConnected()
	}
	<-ctx.Done()
	return ctx.Err()
}

type subscriber struct {
	symbols map[string]struct{}
	events  map[market.EventType]struct{}
	queue   chan market.LiveEvent
}
type Hub struct {
	source      Source
	sink        Sink
	watchlist   []string
	mu          sync.RWMutex
	subs        map[*subscriber]struct{}
	access      *access.Store
	limiter     *access.Limiter
	statusMu    sync.RWMutex
	state       string
	lastError   string
	connectedAt time.Time
	reconnects  int64
}

func NewHub(source Source, sink Sink, watchlist []string) (*Hub, error) {
	if len(watchlist) == 0 {
		return nil, fmt.Errorf("watchlist is empty")
	}
	if len(watchlist) > 200 {
		return nil, fmt.Errorf("watchlist exceeds 200 symbols")
	}
	set := map[string]struct{}{}
	var normalized []string
	for _, s := range watchlist {
		var err error
		s, _, err = market.NormalizeSymbol(s)
		if err != nil {
			return nil, fmt.Errorf("invalid watchlist symbol %q: %w", s, err)
		}
		if _, ok := set[s]; !ok {
			set[s] = struct{}{}
			normalized = append(normalized, s)
		}
	}
	sort.Strings(normalized)
	if sink == nil {
		sink = NopSink{}
	}
	return &Hub{source: source, sink: sink, watchlist: normalized, subs: map[*subscriber]struct{}{}, state: "connecting"}, nil
}
func (h *Hub) ConfigureAccess(store *access.Store, limiter *access.Limiter) {
	h.access, h.limiter = store, limiter
}
func (h *Hub) ProviderStatus() map[string]any {
	h.statusMu.RLock()
	defer h.statusMu.RUnlock()
	lastError := ""
	if h.lastError != "" {
		lastError = "connection failed; inspect server logs"
	}
	return map[string]any{"longbridge": map[string]any{"state": h.state, "last_error": lastError, "connected_at": h.connectedAt, "reconnects": h.reconnects, "subscribed_symbols": len(h.watchlist)}}
}
func (h *Hub) Run(ctx context.Context) {
	for {
		h.statusMu.Lock()
		h.state = "connecting"
		h.statusMu.Unlock()
		err := h.source.Run(ctx, h.watchlist, h.publish)
		if ctx.Err() != nil {
			return
		}
		h.statusMu.Lock()
		h.state, h.reconnects = "degraded", h.reconnects+1
		if err != nil {
			h.lastError = err.Error()
		}
		h.statusMu.Unlock()
		if err != nil {
			log.Printf("Longbridge live provider disconnected: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
func (h *Hub) publish(event market.LiveEvent) {
	_ = h.sink.Write(context.Background(), event)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs {
		if _, ok := s.symbols[event.Symbol]; !ok {
			continue
		}
		if _, ok := s.events[event.Type]; !ok {
			continue
		}
		select {
		case s.queue <- event:
		default:
			select {
			case <-s.queue:
			default:
			}
			select {
			case s.queue <- market.LiveEvent{Type: market.GapEvent, Symbol: event.Symbol, Timestamp: time.Now().UTC(), Reason: "slow_consumer"}:
			default:
			}
		}
	}
}
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 10)
	_, raw, err := c.Read(r.Context())
	if err != nil {
		return
	}
	var req struct {
		Symbols []string           `json:"symbols"`
		Events  []market.EventType `json:"events"`
	}
	if json.Unmarshal(raw, &req) != nil || len(req.Symbols) == 0 {
		_ = c.Close(websocket.StatusPolicyViolation, "symbols required")
		return
	}
	connectionCtx := c.CloseRead(r.Context())
	if len(req.Events) == 0 {
		req.Events = []market.EventType{market.BarEvent}
	}
	p, secured := access.PrincipalFromContext(r.Context())
	allowed := map[string]struct{}{}
	for _, symbol := range h.watchlist {
		allowed[symbol] = struct{}{}
	}
	normalized := map[string]struct{}{}
	for _, symbol := range req.Symbols {
		var err error
		symbol, _, err = market.NormalizeSymbol(symbol)
		if err != nil {
			_ = c.Close(websocket.StatusPolicyViolation, "invalid symbol")
			return
		}
		if _, ok := allowed[symbol]; !ok {
			_ = c.Close(websocket.StatusPolicyViolation, "symbol outside global watchlist")
			return
		}
		normalized[symbol] = struct{}{}
	}
	if secured && h.limiter != nil && !h.limiter.AcquireLive(p, len(normalized)) {
		_ = c.Close(websocket.StatusPolicyViolation, "live quota exceeded")
		return
	}
	if secured && h.limiter != nil {
		defer h.limiter.ReleaseLive(p.UserID, len(normalized))
	}
	s := &subscriber{symbols: map[string]struct{}{}, events: map[market.EventType]struct{}{}, queue: make(chan market.LiveEvent, 256)}
	for x := range normalized {
		s.symbols[x] = struct{}{}
	}
	for _, x := range req.Events {
		s.events[x] = struct{}{}
	}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.subs, s); h.mu.Unlock() }()
	revalidate := time.NewTicker(time.Minute)
	defer revalidate.Stop()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	for {
		select {
		case <-connectionCtx.Done():
			return
		case event := <-s.queue:
			b, _ := json.Marshal(event)
			writeCtx, cancel := context.WithTimeout(connectionCtx, 10*time.Second)
			err := c.Write(writeCtx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		case <-revalidate.C:
			if secured && h.access != nil {
				if _, err := h.access.Authenticate(connectionCtx, token); err != nil {
					_ = c.Close(websocket.StatusPolicyViolation, "credential expired or revoked")
					return
				}
			}
		}
	}
}
func normalizeSymbol(v string) string {
	normalized, _, err := market.NormalizeSymbol(v)
	if err != nil {
		return strings.ToUpper(strings.TrimSpace(v))
	}
	return normalized
}
