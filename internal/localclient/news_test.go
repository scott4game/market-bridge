package localclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/news"
)

func TestNewsProxyCatchUpPersistsRemoteArticles(t *testing.T) {
	article := news.Article{ID: "article-1", Sequence: 17, Kind: news.StockNews, Symbols: []string{"AAPL"}, Title: "Apple", URL: "https://example.com/apple", PublishedAt: time.Now().UTC(), ReceivedAt: time.Now().UTC(), Provider: "fmp"}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/news" || r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(news.ListResponse{News: []news.Article{article}, LatestSequence: article.Sequence})
	}))
	defer upstream.Close()
	store, err := news.OpenStore(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	proxy := NewNewsProxy(config.Client{ServerURL: upstream.URL, ServerToken: "token", NewsRetention: 30 * 24 * time.Hour}, store)
	if err := proxy.catchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := store.List(context.Background(), news.Query{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Sequence != 17 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}
