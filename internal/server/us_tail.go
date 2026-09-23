package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/scott4game/market-bridge/internal/market"
)

func (h *HTTP) tailWindow() time.Duration {
	if h.USTail == nil {
		return 0
	}
	return h.USTail.TailWindow()
}

// writeHistory overlays only the response. Durable writes and historical cache
// entries have already been processed and never receive provisional candles.
func (h *HTTP) writeHistory(w http.ResponseWriter, r *http.Request, spec market.DatasetSpec, payload map[string]any) {
	bars, _ := payload["bars"].([]market.Bar)
	if h.USTail != nil && h.USTail.Eligible(spec) {
		tail, meta, err := h.USTail.Bars(r.Context(), spec)
		if market.IsUSForwardAdjusted(spec) && len(tail) > 0 {
			var adjustErr error
			tail, adjustErr = h.forwardAdjustBars(r.Context(), spec, tail)
			err = errors.Join(err, adjustErr)
		}
		if err != nil && meta != nil {
			meta.Status = "degraded"
		}
		if meta != nil {
			payload["tail"] = meta
		}
		// Copy the base: Redis implementations may retain ownership of their slice.
		merged := append([]market.Bar(nil), bars...)
		type key struct {
			symbol string
			time   int64
		}
		positions := map[key]int{}
		for i, b := range merged {
			positions[key{b.Symbol, b.Timestamp.UnixMilli()}] = i
		}
		for _, b := range tail {
			k := key{b.Symbol, b.Timestamp.UnixMilli()}
			if i, ok := positions[k]; ok {
				merged[i] = b
			} else {
				positions[k] = len(merged)
				merged = append(merged, b)
			}
		}
		market.SortBars(merged)
		bars = merged
		payload["bars"] = nonNilBars(bars)
		if len(tail) > 0 {
			payload["source"] = payload["source"].(string) + "+longbridge-tail"
		}
		if err != nil {
			warning, _ := payload["warning"].(string)
			if warning != "" {
				warning += "; "
			}
			payload["warning"] = warning + err.Error()
		}
		w.Header().Set("Cache-Control", "no-store")
	}
	if len(bars) == 0 {
		if warning, ok := payload["warning"].(string); ok && warning != "" {
			writeProviderError(w, errors.New(warning))
			return
		}
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *HTTP) tailCapability() map[string]any {
	result := map[string]any{"enabled": h.USTail != nil, "window_seconds": h.tailWindow().Seconds()}
	if h.USTail != nil {
		result["stats"] = h.USTail.Stats()
	}
	return result
}
