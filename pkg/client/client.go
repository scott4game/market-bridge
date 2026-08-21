package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type Config struct {
	BaseURL    string
	HTTPClient *http.Client
}
type Client struct {
	baseURL string
	http    *http.Client
}
type Dataset struct {
	Bars   []market.Bar
	Source string
}
type Subscription struct {
	Symbols []string           `json:"symbols"`
	Events  []market.EventType `json:"events"`
}
type Stream struct {
	conn   *websocket.Conn
	events chan market.LiveEvent
	errs   chan error
	cancel context.CancelFunc
}

type DatasetSpec = market.DatasetSpec
type Bar = market.Bar
type AdjustmentMode = market.AdjustmentMode
type Session = market.Session

const (
	Raw             = market.Raw
	SplitAdjusted   = market.SplitAdjusted
	RegularSession  = market.RegularSession
	ExtendedSession = market.ExtendedSession
)

func NewLocalClient(cfg Config) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "http://127.0.0.1:17600"
	}
	h := cfg.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: 10 * time.Minute}
	}
	return &Client{baseURL: base, http: h}
}
func (c *Client) EnsureDataset(ctx context.Context, spec DatasetSpec) (*Dataset, error) {
	b, _ := json.Marshal(spec)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/datasets/ensure", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var payload struct {
		Source string       `json:"source"`
		Bars   []market.Bar `json:"bars"`
		Error  string       `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("go-client: %s", payload.Error)
	}
	return &Dataset{Bars: payload.Bars, Source: payload.Source}, nil
}
func (d *Dataset) ScanBars(ctx context.Context, fn func(Bar) error) error {
	for _, bar := range d.Bars {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := fn(bar); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) Subscribe(ctx context.Context, sub Subscription) (*Stream, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/v1/live/ws"
	streamCtx, cancel := context.WithCancel(ctx)
	conn, _, err := websocket.Dial(streamCtx, u.String(), nil)
	if err != nil {
		cancel()
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{"action": "subscribe", "symbols": sub.Symbols, "events": sub.Events})
	if err := conn.Write(streamCtx, websocket.MessageText, payload); err != nil {
		conn.CloseNow()
		cancel()
		return nil, err
	}
	s := &Stream{conn: conn, events: make(chan market.LiveEvent, 128), errs: make(chan error, 1), cancel: cancel}
	go s.read(streamCtx)
	return s, nil
}
func (s *Stream) read(ctx context.Context) {
	defer close(s.events)
	defer close(s.errs)
	for {
		_, raw, err := s.conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.errs <- err
			}
			return
		}
		var event market.LiveEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			s.errs <- err
			return
		}
		select {
		case s.events <- event:
		case <-ctx.Done():
			return
		}
	}
}
func (s *Stream) Events() <-chan market.LiveEvent { return s.events }
func (s *Stream) Errors() <-chan error            { return s.errs }
func (s *Stream) Close() error {
	s.cancel()
	return s.conn.Close(websocket.StatusNormalClosure, "closed")
}
