package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestServerFromEnv(t *testing.T) {
	t.Setenv("GO_SERVER_LISTEN", ":27601")
	t.Setenv("GO_SERVER_CLICKHOUSE_RETENTION", "")
	t.Setenv("GO_SERVER_DATASET_TTL", "48h")
	t.Setenv("GO_SERVER_DATASET_WORKERS", "4")
	t.Setenv("GO_SERVER_DATASET_QUEUE_SIZE", "50")
	t.Setenv("CLICKHOUSE_DATABASE", "prices")
	t.Setenv("CLICKHOUSE_USER", "bridge")
	t.Setenv("CLICKHOUSE_PASSWORD", "secret")
	t.Setenv("CLICKHOUSE_URL", "http://clickhouse.example:8123")
	t.Setenv("MASSIVE_PLAN_NAME", "stocks_developer")
	t.Setenv("MASSIVE_REQUESTS_PER_MINUTE", "0")
	t.Setenv("MASSIVE_REQUESTS_PER_MONTH", "10000")

	got := ServerFromEnv()
	if got.Listen != ":27601" || got.DatasetTTL != 48*time.Hour {
		t.Fatalf("unexpected server settings: %+v", got)
	}
	if got.DatasetWorkers != 4 || got.DatasetQueueSize != 50 {
		t.Fatalf("unexpected dataset scheduler settings: %+v", got)
	}
	if got.ClickHouseDatabase != "prices" || got.ClickHouseUser != "bridge" || got.ClickHousePassword != "secret" {
		t.Fatalf("unexpected ClickHouse settings: %+v", got)
	}
	if got.ClickHouseEnabled {
		t.Fatal("ClickHouse must remain disabled unless explicitly enabled")
	}
	if got.ClickHouseRetention != 730*24*time.Hour {
		t.Fatalf("unexpected server ClickHouse retention: %v", got.ClickHouseRetention)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("disabled ClickHouse settings must be ignored: %v", err)
	}
	if got.MassivePlanName != "stocks_developer" || got.MassivePerMinute != 0 || got.MassivePerMonth != 10000 {
		t.Fatalf("unexpected Massive usage settings: %+v", got)
	}
}

func TestServerClickHouseOptInValidation(t *testing.T) {
	t.Setenv("GO_SERVER_CLICKHOUSE_ENABLED", "TRUE")
	t.Setenv("CLICKHOUSE_URL", "")
	if err := ServerFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), "CLICKHOUSE_URL") {
		t.Fatalf("expected missing URL error, got %v", err)
	}

	t.Setenv("CLICKHOUSE_URL", "http://clickhouse.example:8123")
	t.Setenv("CLICKHOUSE_DATABASE", "market")
	t.Setenv("CLICKHOUSE_USER", "market")
	t.Setenv("CLICKHOUSE_PASSWORD", "secret")
	cfg := ServerFromEnv()
	if !cfg.ClickHouseEnabled {
		t.Fatal("ClickHouse should be enabled")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestEffectiveLiveProviders(t *testing.T) {
	cfg := Server{LiveProvider: "mock", LiveProviders: []string{" Longbridge ", "BINANCE", "longbridge"}}
	if got := cfg.EffectiveLiveProviders(); !reflect.DeepEqual(got, []string{"longbridge", "binance"}) {
		t.Fatalf("unexpected live providers: %#v", got)
	}
	cfg.LiveProviders = nil
	if got := cfg.EffectiveLiveProviders(); !reflect.DeepEqual(got, []string{"mock"}) {
		t.Fatalf("legacy provider fallback failed: %#v", got)
	}
}

func TestClientFromEnv(t *testing.T) {
	t.Setenv("GO_CLIENT_PARQUET_TTL", "168h")
	t.Setenv("GO_CLIENT_CLICKHOUSE_RETENTION", "")
	t.Setenv("GO_CLIENT_REDIS_ENABLED", "true")
	t.Setenv("GO_CLIENT_REDIS_ADDRESS", "redis.example:6379")
	t.Setenv("GO_CLIENT_REDIS_USERNAME", "bridge")
	t.Setenv("GO_CLIENT_REDIS_PASSWORD", "secret")
	t.Setenv("GO_CLIENT_REDIS_DB", "3")
	t.Setenv("GO_CLIENT_REDIS_TTL", "12h")
	t.Setenv("GO_CLIENT_CLICKHOUSE_ENABLED", "true")
	t.Setenv("GO_CLIENT_CLICKHOUSE_COMPLETED_BARS_ONLY", "false")
	t.Setenv("CLICKHOUSE_URL", "http://clickhouse.example:8123")
	t.Setenv("CLICKHOUSE_DATABASE", "prices")
	t.Setenv("CLICKHOUSE_USER", "bridge")
	t.Setenv("CLICKHOUSE_PASSWORD", "secret")

	got := ClientFromEnv()
	if got.ParquetTTL != 168*time.Hour || got.RedisTTL != 12*time.Hour {
		t.Fatalf("unexpected cache TTLs: %+v", got)
	}
	if !got.RedisEnabled || got.RedisAddress != "redis.example:6379" || got.RedisUsername != "bridge" || got.RedisPassword != "secret" || got.RedisDB != 3 {
		t.Fatalf("unexpected Redis settings: %+v", got)
	}
	if !got.ClickHouseEnabled || got.ClickHouseCompletedBarsOnly {
		t.Fatalf("unexpected client ClickHouse settings: %+v", got)
	}
	if got.ClickHouseRetention != 730*24*time.Hour {
		t.Fatalf("unexpected client ClickHouse retention: %v", got.ClickHouseRetention)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("unexpected client validation error: %v", err)
	}
}

func TestClientClickHouseDoesNotRequirePersistentLiveSymbols(t *testing.T) {
	t.Setenv("GO_CLIENT_CLICKHOUSE_ENABLED", "true")
	t.Setenv("CLICKHOUSE_URL", "http://clickhouse.example:8123")
	t.Setenv("CLICKHOUSE_DATABASE", "market")
	t.Setenv("CLICKHOUSE_USER", "market")
	t.Setenv("CLICKHOUSE_PASSWORD", "secret")
	if err := ClientFromEnv().Validate(); err != nil {
		t.Fatalf("ClickHouse should support on-demand live data without a persistent watchlist: %v", err)
	}
}

func TestInvalidValuesUseDefaults(t *testing.T) {
	t.Setenv("GO_CLIENT_REDIS_ENABLED", "not-a-bool")
	t.Setenv("GO_CLIENT_REDIS_DB", "not-an-int")
	t.Setenv("GO_CLIENT_REDIS_TTL", "not-a-duration")

	got := ClientFromEnv()
	if !got.RedisEnabled || got.RedisDB != 0 || got.RedisTTL != 24*time.Hour {
		t.Fatalf("invalid values did not fall back to defaults: %+v", got)
	}
}
