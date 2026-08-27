package localclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/news"
)

type NewsProxy struct {
	cfg     config.Client
	Service *news.Service
}

func NewNewsProxy(cfg config.Client, store *news.Store) *NewsProxy {
	if cfg.NewsRetention <= 0 {
		cfg.NewsRetention = 30 * 24 * time.Hour
	}
	return &NewsProxy{cfg: cfg, Service: news.NewService(store, nil, time.Minute, cfg.NewsRetention)}
}

func (p *NewsProxy) Run(ctx context.Context) {
	backoff := 500 * time.Millisecond
	go p.runCleanup(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		p.Service.BroadcastStatus("connecting", "正在同步上游新闻")
		if err := p.catchUp(ctx); err != nil {
			p.Service.BroadcastStatus("reconnecting", "新闻同步失败，正在重试："+err.Error())
			if !waitLiveReconnect(ctx, backoff) {
				return
			}
			backoff = nextLiveBackoff(backoff)
			continue
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
		u.Path = "/v1/news/ws"
		headers := http.Header{}
		if p.cfg.ServerToken != "" {
			headers.Set("Authorization", "Bearer "+p.cfg.ServerToken)
		}
		conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: headers})
		if err != nil {
			p.Service.BroadcastStatus("reconnecting", "上游新闻连接失败，正在重试")
			if !waitLiveReconnect(ctx, backoff) {
				return
			}
			backoff = nextLiveBackoff(backoff)
			continue
		}
		after := p.Service.Store.LatestSequence(ctx)
		payload, _ := json.Marshal(map[string]any{"after_sequence": after, "status": true})
		if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
			conn.CloseNow()
			continue
		}
		p.Service.BroadcastStatus("connected", "上游新闻流已连接")
		backoff = 500 * time.Millisecond
		for {
			_, raw, readErr := conn.Read(ctx)
			if readErr != nil {
				err = readErr
				break
			}
			var event news.Event
			if json.Unmarshal(raw, &event) != nil {
				continue
			}
			switch event.Type {
			case "news":
				if event.Article != nil {
					_, _ = p.Service.IngestRemote(ctx, *event.Article)
				}
			case "gap":
				p.Service.BroadcastStatus("reconnecting", "新闻流出现缺口，正在补齐")
				_ = p.catchUp(ctx)
			case "status":
				p.Service.BroadcastStatus(event.State, event.Detail)
			}
		}
		conn.CloseNow()
		p.Service.BroadcastStatus("reconnecting", "上游新闻连接已断开，正在重试："+err.Error())
		if !waitLiveReconnect(ctx, backoff) {
			return
		}
		backoff = nextLiveBackoff(backoff)
	}
}

func (p *NewsProxy) runCleanup(ctx context.Context) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = p.Service.Store.Cleanup(ctx, time.Now().UTC().Add(-p.cfg.NewsRetention))
		}
	}
}

func (p *NewsProxy) catchUp(ctx context.Context) error {
	after := p.Service.Store.LatestSequence(ctx)
	before := int64(0)
	initial := after == 0
	for {
		u, err := url.Parse(strings.TrimRight(p.cfg.ServerURL, "/") + "/v1/news")
		if err != nil {
			return err
		}
		q := u.Query()
		q.Set("limit", "500")
		if initial && before > 0 {
			q.Set("before_sequence", strconv.FormatInt(before, 10))
		} else if after > 0 {
			q.Set("after_sequence", strconv.FormatInt(after, 10))
		}
		u.RawQuery = q.Encode()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if p.cfg.ServerToken != "" {
			req.Header.Set("Authorization", "Bearer "+p.cfg.ServerToken)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("news catch-up status %d", resp.StatusCode)
		}
		var result news.ListResponse
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return err
		}
		for _, article := range result.News {
			_, _ = p.Service.IngestRemote(ctx, article)
			if article.Sequence > after {
				after = article.Sequence
			}
			if before == 0 || article.Sequence < before {
				before = article.Sequence
			}
		}
		if len(result.News) < 500 {
			return nil
		}
	}
}
