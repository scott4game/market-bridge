package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/scott4game/market-bridge/internal/market"
)

type BinanceSource struct {
	URL         string
	OnConnected func()
}

func (s *BinanceSource) SetOnConnected(fn func()) { s.OnConnected = fn }

func (s *BinanceSource) Run(ctx context.Context, symbols []string, emit func(market.LiveEvent)) error {
	connectionCtx, cancel := context.WithTimeout(ctx, 23*time.Hour+50*time.Minute)
	defer cancel()
	endpoint := strings.TrimRight(s.URL, "/")
	if endpoint == "" {
		endpoint = "wss://data-stream.binance.vision"
	}
	conn, response, err := websocket.Dial(connectionCtx, endpoint+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"User-Agent": []string{"market-bridge"}}})
	if err != nil {
		if response != nil {
			return fmt.Errorf("binance websocket status %d: %w", response.StatusCode, err)
		}
		return err
	}
	defer conn.CloseNow()
	streams := make([]string, 0, len(symbols)*3)
	allowed := map[string]string{}
	for _, canonical := range symbols {
		venue, err := market.VenueOf(canonical)
		if err != nil || venue != market.VenueBinance {
			continue
		}
		exchange := strings.TrimSuffix(canonical, ".BINANCE")
		allowed[exchange] = canonical
		lower := strings.ToLower(exchange)
		streams = append(streams, lower+"@kline_1m", lower+"@aggTrade", lower+"@bookTicker")
	}
	if len(streams) == 0 {
		return fmt.Errorf("binance watchlist is empty")
	}
	request, _ := json.Marshal(map[string]any{"method": "SUBSCRIBE", "params": streams, "id": 1})
	if err := conn.Write(connectionCtx, websocket.MessageText, request); err != nil {
		return err
	}
	if s.OnConnected != nil {
		s.OnConnected()
	}
	for {
		_, raw, err := conn.Read(connectionCtx)
		if err != nil {
			if ctx.Err() == nil && connectionCtx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("scheduled Binance websocket rotation")
			}
			return err
		}
		var envelope struct {
			Event     string `json:"e"`
			EventTime int64  `json:"E"`
			Symbol    string `json:"s"`
			AggID     int64  `json:"a"`
			UpdateID  int64  `json:"u"`
			TradeTime int64  `json:"T"`
			Kline     *struct {
				OpenTime int64  `json:"t"`
				Open     string `json:"o"`
				High     string `json:"h"`
				Low      string `json:"l"`
				Close    string `json:"c"`
				Volume   string `json:"v"`
				Turnover string `json:"q"`
				Closed   bool   `json:"x"`
			} `json:"k"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.Symbol == "" {
			continue
		}
		canonical, ok := allowed[envelope.Symbol]
		if !ok {
			continue
		}
		switch envelope.Event {
		case "kline":
			if envelope.Kline == nil {
				continue
			}
			o, e1 := market.DecimalFromString(envelope.Kline.Open)
			h, e2 := market.DecimalFromString(envelope.Kline.High)
			l, e3 := market.DecimalFromString(envelope.Kline.Low)
			c, e4 := market.DecimalFromString(envelope.Kline.Close)
			turnover, e5 := market.DecimalFromString(envelope.Kline.Turnover)
			if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
				continue
			}
			var volume float64
			_, _ = fmt.Sscan(envelope.Kline.Volume, &volume)
			bar := &market.Bar{Symbol: canonical, Timestamp: time.UnixMilli(envelope.Kline.OpenTime).UTC(), Open: o, High: h, Low: l, Close: c, Volume: int64(volume), VolumeDecimal: envelope.Kline.Volume, Turnover: &turnover, Session: market.ContinuousSession, Source: "binance-live", Completed: envelope.Kline.Closed}
			sequence := envelope.EventTime
			emit(market.LiveEvent{Type: market.BarEvent, Symbol: canonical, Timestamp: bar.Timestamp, Cursor: market.LiveCursor{StreamEpoch: "binance", EventType: market.BarEvent, Symbol: canonical, Sequence: sequence}, Bar: bar})
		case "aggTrade":
			emit(market.LiveEvent{Type: market.TradeEvent, Symbol: canonical, Timestamp: time.UnixMilli(envelope.TradeTime).UTC(), Cursor: market.LiveCursor{StreamEpoch: "binance", EventType: market.TradeEvent, Symbol: canonical, Sequence: envelope.AggID}, Trade: append(json.RawMessage(nil), raw...)})
		default:
			// bookTicker has no event name in the Binance payload.
			emit(market.LiveEvent{Type: market.DepthEvent, Symbol: canonical, Timestamp: time.Now().UTC(), Cursor: market.LiveCursor{StreamEpoch: "binance", EventType: market.DepthEvent, Symbol: canonical, Sequence: envelope.UpdateID}, Depth: append(json.RawMessage(nil), raw...)})
		}
	}
}
