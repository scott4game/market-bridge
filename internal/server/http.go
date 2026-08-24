package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/provider"
)

type UsageReader interface {
	Snapshot(context.Context, string) (provider.UsageSnapshot, error)
}

type HTTP struct {
	Store *Store
	Token string
	Live  http.Handler
	Usage UsageReader
}

func (h *HTTP) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /v1/datasets", h.auth(h.create))
	mux.HandleFunc("GET /v1/datasets/{id}", h.auth(h.status))
	mux.HandleFunc("GET /v1/datasets/{id}/manifest", h.auth(h.manifest))
	mux.HandleFunc("GET /v1/datasets/{id}/files/{name...}", h.auth(h.file))
	mux.HandleFunc("GET /v1/live/ws", h.auth(h.live))
	mux.HandleFunc("GET /v1/providers/massive/usage", h.auth(h.usage))
	return mux
}
func (h *HTTP) usage(w http.ResponseWriter, r *http.Request) {
	if h.Usage == nil {
		writeJSON(w, 404, map[string]string{"error": "Massive usage tracking is not enabled"})
		return
	}
	snapshot, err := h.Usage.Snapshot(r.Context(), "massive")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, snapshot)
}

func (h *HTTP) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.Token != "" && r.Header.Get("Authorization") != "Bearer "+h.Token {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}
func (h *HTTP) create(w http.ResponseWriter, r *http.Request) {
	var spec market.DatasetSpec
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&spec); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	st, err := h.Store.Ensure(r.Context(), spec)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	code := 202
	if st.State == "ready" {
		code = 200
	}
	w.Header().Set("Location", "/v1/datasets/"+st.DatasetID)
	writeJSON(w, code, st)
}
func (h *HTTP) status(w http.ResponseWriter, r *http.Request) {
	st := h.Store.Status(r.PathValue("id"))
	code := 200
	if st.State == "not_found" {
		code = 404
	}
	writeJSON(w, code, st)
}
func (h *HTTP) manifest(w http.ResponseWriter, r *http.Request) {
	m, err := h.Store.Manifest(r.PathValue("id"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, 200, m)
}
func (h *HTTP) file(w http.ResponseWriter, r *http.Request) {
	path, err := h.Store.PartitionPath(r.PathValue("id"), r.PathValue("name"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeJSON(w, 410, map[string]string{"error": "partition expired"})
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	w.Header().Set("ETag", fmt.Sprintf("\"%x-%x\"", info.Size(), info.ModTime().UnixNano()))
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
}
func (h *HTTP) live(w http.ResponseWriter, r *http.Request) {
	if h.Live != nil {
		h.Live.ServeHTTP(w, r)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	defer c.CloseNow()
	var sub struct {
		Symbols []string `json:"symbols"`
	}
	if err := wsRead(r, c, &sub); err != nil {
		return
	}
	if len(sub.Symbols) == 0 {
		sub.Symbols = []string{"AAPL"}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	seq := int64(0)
	for {
		select {
		case <-r.Context().Done():
			return
		case ts := <-ticker.C:
			for _, symbol := range sub.Symbols {
				seq++
				v := market.DecimalFromFloat(100 + float64(seq%20)/10)
				event := map[string]any{"type": "bar", "symbol": strings.ToUpper(symbol), "timestamp": ts.UTC(), "cursor": map[string]any{"stream_epoch": "mock", "event_type": "bar", "symbol": strings.ToUpper(symbol), "sequence": seq}, "bar": market.Bar{Symbol: strings.ToUpper(symbol), Timestamp: ts.UTC().Truncate(time.Minute), Open: v, High: v, Low: v, Close: v, Volume: seq, Session: market.RegularSession, Source: "mock-live", Completed: false}}
				if err := wsWrite(r, c, event); err != nil {
					return
				}
			}
		}
	}
}

func wsRead(r *http.Request, c *websocket.Conn, v any) error {
	_, b, err := c.Read(r.Context())
	if err == nil {
		err = json.Unmarshal(b, v)
	}
	return err
}
func wsWrite(r *http.Request, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(r.Context(), websocket.MessageText, b)
}
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
