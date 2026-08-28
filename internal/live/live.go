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
	shopdecimal "github.com/shopspring/decimal"
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
	Quote                LongbridgeLiveClient
	DepthEnabled         bool
	QuoteRefreshInterval time.Duration
	OnConnected          func()
}

type LongbridgeLiveClient interface {
	OnQuote(func(*lbquote.PushQuote))
	OnTrade(func(*lbquote.PushTrade))
	OnDepth(func(*lbquote.PushDepth))
	Subscribe(context.Context, []string, []lbquote.SubType, bool) error
	Unsubscribe(context.Context, bool, []string, []lbquote.SubType) error
	Quote(context.Context, []string) ([]*lbquote.SecurityQuote, error)
	Close() error
}

type longbridgeQuoteState struct {
	last      *shopdecimal.Decimal
	prevClose *shopdecimal.Decimal
	timestamp int64
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
	var quoteMu sync.Mutex
	quotes := map[string]map[market.QuoteSession]*longbridgeQuoteState{}
	emitQuote := func(symbol string, session market.QuoteSession, sequence int64) {
		quoteMu.Lock()
		state := quotes[symbol][session]
		if state == nil || state.last == nil {
			quoteMu.Unlock()
			return
		}
		event, ok := longbridgeQuoteEvent(epoch, symbol, session, sequence, state)
		quoteMu.Unlock()
		if ok {
			emit(event)
		}
	}
	mergeQuoteSnapshot := func(rows []*lbquote.SecurityQuote) []struct {
		symbol  string
		session market.QuoteSession
	} {
		quoteMu.Lock()
		defer quoteMu.Unlock()
		latest := make([]struct {
			symbol  string
			session market.QuoteSession
		}, 0, len(rows))
		for _, row := range rows {
			if row == nil {
				continue
			}
			symbol := normalizeSymbol(row.Symbol)
			mergeLongbridgeQuoteState(quotes, symbol, market.QuoteSessionRegular, row.LastDone, row.PrevClose, row.Timestamp)
			if row.PreMarketQuote != nil {
				mergeLongbridgeQuoteState(quotes, symbol, market.QuoteSessionPre, row.PreMarketQuote.LastDone, row.PreMarketQuote.PrevClose, row.PreMarketQuote.Timestamp)
			}
			if row.PostMarketQuote != nil {
				mergeLongbridgeQuoteState(quotes, symbol, market.QuoteSessionPost, row.PostMarketQuote.LastDone, row.PostMarketQuote.PrevClose, row.PostMarketQuote.Timestamp)
			}
			if row.OverNightQuote != nil {
				mergeLongbridgeQuoteState(quotes, symbol, market.QuoteSessionOvernight, row.OverNightQuote.LastDone, row.OverNightQuote.PrevClose, row.OverNightQuote.Timestamp)
			}
			session, ok := latestLongbridgeQuoteSession(quotes[symbol])
			if ok {
				latest = append(latest, struct {
					symbol  string
					session market.QuoteSession
				}{symbol: symbol, session: session})
			}
		}
		return latest
	}
	refreshQuotes := func() {
		rows, err := q.Quote(ctx, lbSymbolsFor(symbols))
		if err != nil {
			log.Printf("Longbridge quote snapshot refresh failed: affected=live_change_percent; detail=%v", err)
			return
		}
		for _, item := range mergeQuoteSnapshot(rows) {
			emitQuote(item.symbol, item.session, 0)
		}
	}
	q.OnQuote(func(x *lbquote.PushQuote) {
		if x == nil || x.LastDone == nil {
			return
		}
		symbol := normalizeSymbol(x.Symbol)
		session := longbridgeQuoteSession(x.TradeSession)
		quoteMu.Lock()
		mergeLongbridgeQuoteState(quotes, symbol, session, x.LastDone, nil, x.Timestamp)
		quoteMu.Unlock()
		emitQuote(symbol, session, x.Sequence)
	})
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
	lbSymbols := lbSymbolsFor(symbols)
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
	defer func() {
		unsubscribeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := q.Unsubscribe(unsubscribeCtx, false, lbSymbols, subTypes); err != nil {
			log.Printf("Longbridge unsubscribe failed: %v", err)
		}
	}()
	if s.OnConnected != nil {
		s.OnConnected()
	}
	refreshQuotes()
	refreshInterval := s.QuoteRefreshInterval
	if refreshInterval <= 0 {
		refreshInterval = time.Minute
	}
	refreshTicker := time.NewTicker(refreshInterval)
	defer refreshTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-refreshTicker.C:
			refreshQuotes()
		}
	}
}

func lbSymbolsFor(symbols []string) []string {
	result := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		normalized, venue, err := market.NormalizeSymbol(symbol)
		if err != nil || venue == market.VenueBinance {
			continue
		}
		if venue == market.VenueUS {
			normalized += ".US"
		}
		result = append(result, normalized)
	}
	return result
}

func longbridgeQuoteSession(session lbquote.TradeSessionType) market.QuoteSession {
	switch int32(session) {
	case int32(lbquote.TradeSessionPreTrade):
		return market.QuoteSessionPre
	case int32(lbquote.TradeSessionPostTrade):
		return market.QuoteSessionPost
	case int32(lbquote.TradeSessionOvernight):
		return market.QuoteSessionOvernight
	default:
		return market.QuoteSessionRegular
	}
}

func mergeLongbridgeQuoteState(states map[string]map[market.QuoteSession]*longbridgeQuoteState, symbol string, session market.QuoteSession, last, prevClose *shopdecimal.Decimal, timestamp int64) {
	bySession := states[symbol]
	if bySession == nil {
		bySession = map[market.QuoteSession]*longbridgeQuoteState{}
		states[symbol] = bySession
	}
	state := bySession[session]
	if state == nil {
		state = &longbridgeQuoteState{}
		bySession[session] = state
	}
	if prevClose != nil {
		value := *prevClose
		state.prevClose = &value
	}
	if last != nil && (state.last == nil || timestamp >= state.timestamp) {
		value := *last
		state.last = &value
		state.timestamp = timestamp
	}
}

func latestLongbridgeQuoteSession(states map[market.QuoteSession]*longbridgeQuoteState) (market.QuoteSession, bool) {
	var latest market.QuoteSession
	var timestamp int64
	found := false
	for session, state := range states {
		if state != nil && state.last != nil && (!found || state.timestamp > timestamp) {
			latest, timestamp, found = session, state.timestamp, true
		}
	}
	return latest, found
}

func longbridgeQuoteEvent(epoch, symbol string, session market.QuoteSession, sequence int64, state *longbridgeQuoteState) (market.LiveEvent, bool) {
	last, err := market.DecimalFromString(state.last.String())
	if err != nil {
		return market.LiveEvent{}, false
	}
	quote := &market.Quote{LastDone: last, TradeSession: session, Source: "longbridge"}
	if state.prevClose != nil {
		prev, prevErr := market.DecimalFromString(state.prevClose.String())
		if prevErr == nil {
			quote.PrevClose = &prev
			if !state.prevClose.IsZero() {
				changeValue := state.last.Sub(*state.prevClose)
				percentValue := changeValue.Div(*state.prevClose).Mul(shopdecimal.NewFromInt(100))
				change, changeErr := market.DecimalFromString(changeValue.String())
				percent, percentErr := market.DecimalFromString(percentValue.String())
				if changeErr == nil && percentErr == nil {
					quote.Change, quote.ChangePercent = &change, &percent
				}
			}
		}
	}
	timestamp := time.Now().UTC()
	if state.timestamp > 0 {
		timestamp = time.Unix(state.timestamp, 0).UTC()
	}
	return market.LiveEvent{Type: market.QuoteEvent, Symbol: symbol, Timestamp: timestamp, Cursor: market.LiveCursor{StreamEpoch: epoch, EventType: market.QuoteEvent, Symbol: symbol, Sequence: sequence}, Quote: quote}, true
}

type subscriber struct {
	symbols map[string]struct{}
	events  map[market.EventType]struct{}
	queue   chan market.LiveEvent
}
type Hub struct {
	source            Source
	sink              Sink
	mu                sync.RWMutex
	subs              map[*subscriber]struct{}
	changed           chan struct{}
	access            *access.Store
	limiter           *access.Limiter
	statusMu          sync.RWMutex
	state             string
	lastError         string
	connectedAt       time.Time
	reconnects        int64
	subscribedSymbols int
}

const maxProviderLiveSymbols = 500

func NewHub(source Source, sink Sink) (*Hub, error) {
	if source == nil {
		return nil, fmt.Errorf("live source is required")
	}
	if sink == nil {
		sink = NopSink{}
	}
	return &Hub{source: source, sink: sink, subs: map[*subscriber]struct{}{}, changed: make(chan struct{}, 1), state: "idle"}, nil
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
	return map[string]any{"longbridge": map[string]any{"state": h.state, "last_error": lastError, "connected_at": h.connectedAt, "reconnects": h.reconnects, "subscribed_symbols": h.subscribedSymbols}}
}
func (h *Hub) Run(ctx context.Context) {
	backoff := time.Second
	for {
		symbols := h.symbols()
		if len(symbols) == 0 {
			h.setState("idle", "", 0)
			select {
			case <-ctx.Done():
				return
			case <-h.changed:
				continue
			}
		}

		h.setState("connecting", "", len(symbols))
		runCtx, cancel := context.WithCancel(ctx)
		if aware, ok := h.source.(connectionAwareSource); ok {
			aware.SetOnConnected(func() {
				h.statusMu.Lock()
				h.state = "connected"
				h.connectedAt = time.Now().UTC()
				h.lastError = ""
				h.statusMu.Unlock()
			})
		}
		errC := make(chan error, 1)
		go func() { errC <- h.source.Run(runCtx, symbols, h.publish) }()

		var err error
		changed := false
		select {
		case <-ctx.Done():
			cancel()
			<-errC
			return
		case <-h.changed:
			changed = true
			cancel()
			err = <-errC
		case err = <-errC:
			cancel()
		}
		if changed {
			backoff = time.Second
			continue
		}
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
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-h.changed:
			timer.Stop()
			backoff = time.Second
		case <-timer.C:
			if backoff < 30*time.Second {
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
		}
	}
}

func (h *Hub) setState(state, lastError string, symbols int) {
	h.statusMu.Lock()
	h.state = state
	h.lastError = lastError
	h.subscribedSymbols = symbols
	h.statusMu.Unlock()
}

func (h *Hub) symbols() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	set := map[string]struct{}{}
	for s := range h.subs {
		for symbol := range s.symbols {
			set[symbol] = struct{}{}
		}
	}
	symbols := make([]string, 0, len(set))
	for symbol := range set {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return symbols
}

func (h *Hub) signal() {
	select {
	case h.changed <- struct{}{}:
	default:
	}
}
func (h *Hub) publish(event market.LiveEvent) {
	h.statusMu.Lock()
	if h.state == "connecting" {
		h.state = "connected"
		h.connectedAt = time.Now().UTC()
		h.lastError = ""
	}
	h.statusMu.Unlock()
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
	normalized := map[string]struct{}{}
	for _, symbol := range req.Symbols {
		var err error
		symbol, _, err = market.NormalizeSymbol(symbol)
		if err != nil {
			_ = c.Close(websocket.StatusPolicyViolation, "invalid symbol")
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
	allSymbols := map[string]struct{}{}
	for existing := range h.subs {
		for symbol := range existing.symbols {
			allSymbols[symbol] = struct{}{}
		}
	}
	for symbol := range s.symbols {
		allSymbols[symbol] = struct{}{}
	}
	if len(allSymbols) > maxProviderLiveSymbols {
		h.mu.Unlock()
		_ = c.Close(websocket.StatusPolicyViolation, "global live symbol capacity exceeded")
		return
	}
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	h.signal()
	defer func() {
		h.mu.Lock()
		delete(h.subs, s)
		h.mu.Unlock()
		h.signal()
	}()
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
