package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

type versionedIndexStore struct {
	HistoricalClickHouse // Legacy methods must never be called for index requests.
	rows                 map[string][]market.Bar
	readVersion          string
}

func (s *versionedIndexStore) QueryIndexBars(_ context.Context, _ market.DatasetSpec, version string) ([]market.Bar, error) {
	s.readVersion = version
	return s.rows[version], nil
}
func (s *versionedIndexStore) WriteIndexBars(_ context.Context, _ string, _ market.AdjustmentMode, bars []market.Bar, _ uint64, version string) error {
	s.rows[version] = bars
	return nil
}

type failedIndexProvider struct{}

func (failedIndexProvider) Name() string        { return "failed-index" }
func (failedIndexProvider) DataVersion() string { return "new-route" }
func (failedIndexProvider) Bars(context.Context, market.DatasetSpec) ([]market.Bar, error) {
	return nil, errors.New("new source unavailable")
}

func TestIndexHistoryDoesNotReturnOldSourceOnFailure(t *testing.T) {
	store, err := NewStore(t.TempDir(), failedIndexProvider{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := OpenHistoryCatalog(t.TempDir() + "/history.db")
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	sink := &versionedIndexStore{rows: map[string][]market.Bar{"old-route": {{Symbol: "I:HSI", Source: "old-source"}}}}
	h := &HTTP{Store: store, ClickHouse: sink, ClickHouseEnabled: true, HistoryCatalog: catalog, DataVersion: "new-route"}
	now := time.Now().UTC()
	body, _ := json.Marshal(map[string]any{"spec": market.DatasetSpec{Symbols: []string{"I:HSI"}, Interval: "1m", From: now.Add(-time.Hour), To: now}})
	w := httptest.NewRecorder()
	h.historyBars(w, httptest.NewRequest(http.MethodPost, "/v1/history/bars", strings.NewReader(string(body))))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "old-source") || sink.readVersion == "old-route" || sink.readVersion == "" {
		t.Fatalf("status=%d body=%s version=%s", w.Code, w.Body.String(), sink.readVersion)
	}
}
