package news

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/scott4game/market-bridge/internal/market"
)

type subscriber struct {
	query Query
	queue chan Event
}

type Status struct {
	State               string     `json:"state"`
	Provider            string     `json:"provider,omitempty"`
	PollingInterval     string     `json:"polling_interval,omitempty"`
	LastPollAt          *time.Time `json:"last_poll_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastArticleAt       *time.Time `json:"last_article_at,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	Error               string     `json:"error,omitempty"`
}

type Service struct {
	Store     *Store
	Provider  Provider
	Interval  time.Duration
	Retention time.Duration

	mu     sync.RWMutex
	subs   map[*subscriber]struct{}
	status Status
}

func NewService(store *Store, provider Provider, interval, retention time.Duration) *Service {
	if interval <= 0 {
		interval = time.Minute
	}
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	state := "disabled"
	providerName := ""
	if provider != nil {
		state, providerName = "starting", provider.Name()
	}
	return &Service{Store: store, Provider: provider, Interval: interval, Retention: retention, subs: map[*subscriber]struct{}{}, status: Status{State: state, Provider: providerName, PollingInterval: interval.String()}}
}

func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *Service) Run(ctx context.Context) {
	if s.Provider == nil || s.Store == nil {
		return
	}
	broadcast := s.Store.LatestSequence(ctx) != 0
	cleanup := time.NewTicker(6 * time.Hour)
	defer cleanup.Stop()
	for {
		pollErr := s.poll(ctx, broadcast)
		broadcast = true
		delay := s.Interval
		if pollErr != nil {
			var upstreamErr *UpstreamError
			if errors.As(pollErr, &upstreamErr) && upstreamErr.RetryAfter > delay {
				delay = upstreamErr.RetryAfter
			} else {
				s.mu.RLock()
				failures := s.status.ConsecutiveFailures
				s.mu.RUnlock()
				for i := 1; i < failures && delay < 30*time.Minute; i++ {
					delay *= 2
				}
				if delay > 30*time.Minute {
					delay = 30 * time.Minute
				}
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-cleanup.C:
			timer.Stop()
			if _, err := s.Store.Cleanup(ctx, time.Now().UTC().Add(-s.Retention)); err != nil {
				log.Printf("news cleanup: %v", err)
			}
		}
	}
}

func (s *Service) poll(ctx context.Context, broadcast bool) error {
	s.mu.Lock()
	pollAt := time.Now().UTC()
	s.status.LastPollAt = &pollAt
	s.mu.Unlock()
	var pollErr error
	// Prefer the official company classification when the same URL appears in both feeds.
	for _, kind := range []Kind{PressRelease, StockNews} {
		var collected []Article
		for page := 0; page < 5; page++ {
			rows, err := s.Provider.Latest(ctx, kind, page, 100)
			if err != nil {
				pollErr = err
				break
			}
			known := false
			for _, row := range rows {
				if s.Store.Exists(ctx, row.ID) {
					known = true
				}
				collected = append(collected, row)
			}
			if known || len(rows) < 100 || !broadcast {
				break
			}
		}
		for i := len(collected) - 1; i >= 0; i-- {
			article, inserted, err := s.Store.Insert(ctx, collected[i])
			if err != nil {
				pollErr = err
				continue
			}
			if inserted {
				s.mu.Lock()
				if s.status.LastArticleAt == nil || article.ReceivedAt.After(*s.status.LastArticleAt) {
					receivedAt := article.ReceivedAt
					s.status.LastArticleAt = &receivedAt
				}
				s.mu.Unlock()
				if broadcast {
					s.Broadcast(article)
				}
			}
		}
	}
	s.mu.Lock()
	if pollErr != nil {
		s.status.State = "degraded"
		s.status.ConsecutiveFailures++
		s.status.Error = pollErr.Error()
	} else {
		s.status.State = "enabled"
		successAt := time.Now().UTC()
		s.status.LastSuccessAt = &successAt
		s.status.ConsecutiveFailures = 0
		s.status.Error = ""
	}
	s.mu.Unlock()
	return pollErr
}

func (s *Service) IngestRemote(ctx context.Context, article Article) (bool, error) {
	stored, inserted, err := s.Store.InsertRemote(ctx, article)
	if err == nil && inserted {
		s.Broadcast(stored)
	}
	return inserted, err
}

func (s *Service) Broadcast(article Article) {
	event := Event{Type: "news", Action: "created", Sequence: article.Sequence, Article: &article}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for sub := range s.subs {
		if !matches(article, sub.query) {
			continue
		}
		select {
		case sub.queue <- event:
		default:
			select {
			case <-sub.queue:
			default:
			}
			select {
			case sub.queue <- Event{Type: "gap", Reason: "slow_consumer", Sequence: article.Sequence}:
			default:
			}
		}
	}
}

func (s *Service) BroadcastStatus(state, detail string) {
	s.mu.Lock()
	s.status.State = state
	if state == "connected" {
		s.status.Error = ""
	} else {
		s.status.Error = detail
	}
	defer s.mu.Unlock()
	for sub := range s.subs {
		select {
		case sub.queue <- Event{Type: "status", State: state, Detail: detail}:
		default:
		}
	}
}

func matches(a Article, q Query) bool {
	if len(q.Kinds) > 0 {
		ok := false
		for _, kind := range q.Kinds {
			ok = ok || a.Kind == kind
		}
		if !ok {
			return false
		}
	}
	if len(q.Symbols) > 0 {
		for _, want := range q.Symbols {
			for _, got := range a.Symbols {
				if want == got {
					return true
				}
			}
		}
		return false
	}
	return true
}

func (s *Service) subscribe(ctx context.Context, q Query) ([]Article, <-chan Event, func(), error) {
	sub := &subscriber{query: q, queue: make(chan Event, 256)}
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	watermark := s.Store.LatestSequence(ctx)
	s.mu.Unlock()
	replayQuery := q
	replayQuery.UntilSequence = watermark
	replayQuery.Limit = 1000
	replay, err := s.Store.List(ctx, replayQuery)
	if err != nil {
		s.mu.Lock()
		delete(s.subs, sub)
		s.mu.Unlock()
		return nil, nil, nil, err
	}
	cancel := func() {
		s.mu.Lock()
		delete(s.subs, sub)
		s.mu.Unlock()
	}
	return replay, sub.queue, cancel, nil
}

func (s *Service) ListHTTP(w http.ResponseWriter, r *http.Request) {
	q, err := queryFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	rows, err := s.Store.List(r.Context(), q)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	response := ListResponse{News: rows, LatestSequence: s.Store.LatestSequence(r.Context())}
	if len(rows) == q.Limit && q.AfterSequence == 0 {
		response.NextBeforeSequence = rows[len(rows)-1].Sequence
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Service) ServeWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(32 << 10)
	_, raw, err := c.Read(r.Context())
	if err != nil {
		return
	}
	var request struct {
		Symbols       []string `json:"symbols"`
		Kinds         []Kind   `json:"kinds"`
		AfterSequence int64    `json:"after_sequence"`
		Status        bool     `json:"status"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		_ = c.Close(websocket.StatusPolicyViolation, "invalid subscription")
		return
	}
	symbols, symbolErr := normalizeSymbolsStrict(request.Symbols)
	kinds, kindErr := normalizeKindsStrict(request.Kinds)
	if symbolErr != nil || kindErr != nil || request.AfterSequence < 0 {
		_ = c.Close(websocket.StatusPolicyViolation, "invalid news filter")
		return
	}
	q := Query{Symbols: symbols, Kinds: kinds, AfterSequence: request.AfterSequence}
	replay, events, cancel, err := s.subscribe(r.Context(), q)
	if err != nil {
		return
	}
	defer cancel()
	connectionCtx := c.CloseRead(r.Context())
	if request.Status {
		status := s.Status()
		state, detail := status.State, status.Error
		if state == "enabled" || state == "starting" {
			state, detail = "connected", "新闻流已连接"
		}
		if err := writeWS(connectionCtx, c, Event{Type: "status", State: state, Detail: detail}); err != nil {
			return
		}
	}
	for _, article := range replay {
		if err := writeWS(connectionCtx, c, Event{Type: "news", Action: "created", Sequence: article.Sequence, Article: &article}); err != nil {
			return
		}
	}
	for {
		select {
		case <-connectionCtx.Done():
			return
		case event := <-events:
			if err := writeWS(connectionCtx, c, event); err != nil {
				return
			}
		}
	}
}

func (s *Service) ServeSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	q, err := queryFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if id := strings.TrimSpace(r.Header.Get("Last-Event-ID")); id != "" {
		q.AfterSequence, err = strconv.ParseInt(id, 10, 64)
		if err != nil || q.AfterSequence < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid Last-Event-ID"})
			return
		}
	}
	replay, events, cancel, err := s.subscribe(r.Context(), q)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event Event) error {
		data, _ := json.Marshal(event)
		_, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, data)
		flusher.Flush()
		return err
	}
	for _, article := range replay {
		if send(Event{Type: "news", Action: "created", Sequence: article.Sequence, Article: &article}) != nil {
			return
		}
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case event := <-events:
			if send(event) != nil {
				return
			}
		}
	}
}

func queryFromRequest(r *http.Request) (Query, error) {
	q := Query{Limit: 50}
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		q.Limit, err = strconv.Atoi(raw)
		if err != nil || q.Limit < 1 || q.Limit > 500 {
			return q, errors.New("limit must be between 1 and 500")
		}
	}
	for _, raw := range strings.Split(r.URL.Query().Get("symbols"), ",") {
		if strings.TrimSpace(raw) != "" {
			q.Symbols = append(q.Symbols, raw)
		}
	}
	q.Symbols, err = normalizeSymbolsStrict(q.Symbols)
	if err != nil {
		return q, err
	}
	for _, raw := range strings.Split(r.URL.Query().Get("kinds"), ",") {
		if raw != "" {
			q.Kinds = append(q.Kinds, Kind(raw))
		}
	}
	q.Kinds, err = normalizeKindsStrict(q.Kinds)
	if err != nil {
		return q, err
	}
	if raw := r.URL.Query().Get("after_sequence"); raw != "" {
		q.AfterSequence, err = strconv.ParseInt(raw, 10, 64)
	}
	if err == nil {
		if raw := r.URL.Query().Get("before_sequence"); raw != "" {
			q.BeforeSequence, err = strconv.ParseInt(raw, 10, 64)
		}
	}
	if err != nil || q.AfterSequence < 0 || q.BeforeSequence < 0 || (q.AfterSequence > 0 && q.BeforeSequence > 0) {
		return q, errors.New("invalid news cursor")
	}
	return q, nil
}

func normalizeSymbolsStrict(values []string) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		symbol, venue, err := market.NormalizeSymbol(value)
		if err != nil || venue != market.VenueUS {
			return nil, fmt.Errorf("invalid US news symbol %q", value)
		}
		if _, ok := seen[symbol]; !ok {
			seen[symbol] = struct{}{}
			out = append(out, symbol)
		}
	}
	return out, nil
}

func normalizeKindsStrict(values []Kind) ([]Kind, error) {
	var out []Kind
	for _, kind := range values {
		if kind != StockNews && kind != PressRelease {
			return nil, fmt.Errorf("invalid news kind %q", kind)
		}
		out = append(out, kind)
	}
	return out, nil
}

func writeWS(ctx context.Context, c *websocket.Conn, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Write(writeCtx, websocket.MessageText, b)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
