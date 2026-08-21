package config

import (
	"reflect"
	"testing"
	"time"
)

func TestServerFromEnv(t *testing.T) {
	t.Setenv("GO_SERVER_LISTEN", ":27601")
	t.Setenv("GO_SERVER_WATCHLIST", " AAPL, ,NVDA ")
	t.Setenv("GO_SERVER_DATASET_TTL", "48h")
	t.Setenv("CLICKHOUSE_DATABASE", "prices")
	t.Setenv("CLICKHOUSE_USER", "bridge")
	t.Setenv("CLICKHOUSE_PASSWORD", "secret")

	got := ServerFromEnv()
	if got.Listen != ":27601" || got.DatasetTTL != 48*time.Hour {
		t.Fatalf("unexpected server settings: %+v", got)
	}
	if got.ClickHouseDatabase != "prices" || got.ClickHouseUser != "bridge" || got.ClickHousePassword != "secret" {
		t.Fatalf("unexpected ClickHouse settings: %+v", got)
	}
	if !reflect.DeepEqual(got.Watchlist, []string{"AAPL", "NVDA"}) {
		t.Fatalf("unexpected watchlist: %#v", got.Watchlist)
	}
}

func TestClientFromEnv(t *testing.T) {
	t.Setenv("GO_CLIENT_PARQUET_TTL", "168h")
	t.Setenv("GO_CLIENT_REDIS_ENABLED", "true")
	t.Setenv("GO_CLIENT_REDIS_ADDRESS", "redis.example:6379")
	t.Setenv("GO_CLIENT_REDIS_USERNAME", "bridge")
	t.Setenv("GO_CLIENT_REDIS_PASSWORD", "secret")
	t.Setenv("GO_CLIENT_REDIS_DB", "3")
	t.Setenv("GO_CLIENT_REDIS_TTL", "12h")

	got := ClientFromEnv()
	if got.ParquetTTL != 168*time.Hour || got.RedisTTL != 12*time.Hour {
		t.Fatalf("unexpected cache TTLs: %+v", got)
	}
	if !got.RedisEnabled || got.RedisAddress != "redis.example:6379" || got.RedisUsername != "bridge" || got.RedisPassword != "secret" || got.RedisDB != 3 {
		t.Fatalf("unexpected Redis settings: %+v", got)
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
