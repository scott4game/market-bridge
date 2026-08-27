package main

import (
	"fmt"
	"log"
	"strings"
	"sync/atomic"

	"github.com/scott4game/market-bridge/internal/config"
)

const (
	longbridgeLevelDebug int32 = -1
	longbridgeLevelInfo  int32 = 0
	longbridgeLevelWarn  int32 = 1
	longbridgeLevelError int32 = 2
)

// longbridgeLogger gives the SDK's transport-only messages enough context to
// identify the connection and the application features that depend on it.
type longbridgeLogger struct {
	affected string
	level    atomic.Int32
}

func newLongbridgeLogger(affected string) *longbridgeLogger {
	l := &longbridgeLogger{affected: affected}
	l.level.Store(longbridgeLevelInfo)
	return l
}

func longbridgeAffectedFeatures(cfg config.Server, liveProviders []string) string {
	features := []string{"recent_trades", "security_directory"}
	if cfg.LongbridgeHistoryEnabled {
		features = append(features, "HK_CN_history_klines")
	}
	if cfg.IndexProvider == "longbridge" {
		features = append(features, "index_history_klines")
	}
	if contains(liveProviders, "longbridge") {
		features = append(features, "live_quotes", "live_trades")
		if cfg.LongbridgeDepthEnabled {
			features = append(features, "market_depth")
		}
	}
	return strings.Join(features, ",")
}

func (l *longbridgeLogger) SetLevel(value string) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		l.level.Store(longbridgeLevelDebug)
	case "warn":
		l.level.Store(longbridgeLevelWarn)
	case "error":
		l.level.Store(longbridgeLevelError)
	default:
		l.level.Store(longbridgeLevelInfo)
	}
}

func (l *longbridgeLogger) Info(msg string)  { l.write(longbridgeLevelInfo, "INFO", msg) }
func (l *longbridgeLogger) Error(msg string) { l.write(longbridgeLevelError, "ERR", msg) }
func (l *longbridgeLogger) Warn(msg string)  { l.write(longbridgeLevelWarn, "WARN", msg) }
func (l *longbridgeLogger) Debug(msg string) { l.write(longbridgeLevelDebug, "DEBUG", msg) }

func (l *longbridgeLogger) Infof(format string, args ...interface{}) {
	l.Info(fmt.Sprintf(format, args...))
}

func (l *longbridgeLogger) Errorf(format string, args ...interface{}) {
	l.Error(fmt.Sprintf(format, args...))
}

func (l *longbridgeLogger) Warnf(format string, args ...interface{}) {
	l.Warn(fmt.Sprintf(format, args...))
}

func (l *longbridgeLogger) Debugf(format string, args ...interface{}) {
	l.Debug(fmt.Sprintf(format, args...))
}

func (l *longbridgeLogger) write(messageLevel int32, label, message string) {
	if l.level.Load() > messageLevel {
		return
	}
	message = strings.TrimSpace(message)
	switch {
	case strings.HasPrefix(message, "close conn, err:"):
		cause := strings.TrimSpace(strings.TrimPrefix(message, "close conn, err:"))
		log.Printf("[%s] Longbridge quote WebSocket disconnected: affected=%s; impact=requests and live streams temporarily interrupted; recovery=automatic reconnect; cause=%s", label, l.affected, cause)
	case message == "start reconnecting." || message == "start reconnecting":
		log.Printf("[%s] Longbridge quote WebSocket reconnecting: affected=%s; status=temporarily unavailable", label, l.affected)
	case message == "reconnect success":
		log.Printf("[%s] Longbridge quote WebSocket transport reconnected: affected=%s; status=restoring subscriptions", label, l.affected)
	case strings.Contains(message, "faield to do sub") || strings.Contains(message, "failed to do sub"):
		log.Printf("[%s] Longbridge quote WebSocket subscription restore failed: affected=%s; status=live streams degraded; detail=%s", label, l.affected, message)
	default:
		log.Printf("[%s] Longbridge quote SDK: affected=%s; detail=%s", label, l.affected, message)
	}
}
