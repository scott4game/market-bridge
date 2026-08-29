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
	if got.IndexProvider != "disabled" {
		t.Fatalf("default index provider=%q", got.IndexProvider)
	}
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
	if got.ClickHouseRetention != 1825*24*time.Hour {
		t.Fatalf("unexpected server ClickHouse retention: %v", got.ClickHouseRetention)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("disabled ClickHouse settings must be ignored: %v", err)
	}
	if got.MassivePlanName != "stocks_developer" || got.MassivePerMinute != 0 || got.MassivePerMonth != 10000 {
		t.Fatalf("unexpected Massive usage settings: %+v", got)
	}
	for provider, years := range got.HistoryMaxYears() {
		if years != 5 {
			t.Fatalf("default %s history years=%d", provider, years)
		}
	}
}

func TestProviderHistoryYearConfiguration(t *testing.T) {
	t.Setenv("MASSIVE_HISTORY_MAX_YEARS", "7")
	t.Setenv("LONGBRIDGE_HISTORY_MAX_YEARS", "3")
	t.Setenv("TUSHARE_HISTORY_MAX_YEARS", "2")
	t.Setenv("FMP_HISTORY_MAX_YEARS", "9")
	t.Setenv("BINANCE_HISTORY_MAX_YEARS", "4")
	t.Setenv("MOCK_HISTORY_MAX_YEARS", "1")
	cfg := ServerFromEnv()
	want := map[string]int{"massive": 7, "longbridge": 3, "tushare": 2, "fmp": 9, "binance": 4, "mock": 1}
	if !reflect.DeepEqual(cfg.HistoryMaxYears(), want) {
		t.Fatalf("years=%v", cfg.HistoryMaxYears())
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MASSIVE_HISTORY_MAX_YEARS", "0")
	if err := ServerFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), "MASSIVE_HISTORY_MAX_YEARS") {
		t.Fatalf("err=%v", err)
	}
}

func TestIndexProviderValidation(t *testing.T) {
	t.Setenv("GO_SERVER_INDEX_PROVIDER", "unknown")
	if err := ServerFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), "unsupported index provider") {
		t.Fatalf("err=%v", err)
	}

	t.Setenv("GO_SERVER_INDEX_PROVIDER", "fmp")
	t.Setenv("FMP_API_KEY", "")
	if err := ServerFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), "FMP_API_KEY") {
		t.Fatalf("err=%v", err)
	}
	t.Setenv("FMP_API_KEY", "secret")
	if err := ServerFromEnv().Validate(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GO_SERVER_INDEX_PROVIDER", "massive")
	t.Setenv("MASSIVE_API_KEY", "")
	if err := ServerFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), "MASSIVE_API_KEY") {
		t.Fatalf("err=%v", err)
	}
}

func TestMassiveOptionsConfiguration(t *testing.T) {
	t.Setenv("GO_SERVER_OPTIONS_PROVIDER", "massive")
	t.Setenv("MASSIVE_OPTIONS_PLAN_NAME", "options_basic")
	t.Setenv("MASSIVE_OPTIONS_REQUESTS_PER_MINUTE", "5")
	t.Setenv("MASSIVE_OPTIONS_REQUESTS_PER_MONTH", "0")
	t.Setenv("MASSIVE_API_KEY", "")
	if err := ServerFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), "MASSIVE_API_KEY") {
		t.Fatalf("err=%v", err)
	}
	t.Setenv("MASSIVE_API_KEY", "secret")
	cfg := ServerFromEnv()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.OptionsProvider != "massive" || cfg.MassiveOptionsPlanName != "options_basic" || cfg.MassiveOptionsPerMinute != 5 {
		t.Fatalf("cfg=%+v", cfg)
	}

	t.Setenv("GO_SERVER_OPTIONS_PROVIDER", "unknown")
	if err := ServerFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), "unsupported options provider") {
		t.Fatalf("err=%v", err)
	}
}

func TestHistoricalMarketProviderConfiguration(t *testing.T) {
	t.Setenv("GO_SERVER_A_SHARE_PROVIDER", "tushare")
	t.Setenv("GO_SERVER_HK_PROVIDER", "longbridge")
	t.Setenv("TUSHARE_TOKEN", "")
	if err := ServerFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), "TUSHARE_TOKEN") {
		t.Fatalf("err=%v", err)
	}
	t.Setenv("TUSHARE_TOKEN", "secret")
	t.Setenv("LONGBRIDGE_APP_KEY", "app-key")
	t.Setenv("LONGBRIDGE_APP_SECRET", "app-secret")
	t.Setenv("LONGBRIDGE_ACCESS_TOKEN", "access-token")
	cfg := ServerFromEnv()
	if cfg.AShareProvider != "tushare" || cfg.HKProvider != "longbridge" || cfg.TushareBaseURL != "https://api.tushare.pro" || cfg.TusharePerMinute != 200 {
		t.Fatalf("cfg=%+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyLongbridgeHistoryConfiguresBothMarkets(t *testing.T) {
	t.Setenv("GO_SERVER_A_SHARE_PROVIDER", "")
	t.Setenv("GO_SERVER_HK_PROVIDER", "")
	t.Setenv("GO_SERVER_LONGBRIDGE_HISTORY_ENABLED", "true")
	cfg := ServerFromEnv()
	if cfg.AShareProvider != "longbridge" || cfg.HKProvider != "longbridge" {
		t.Fatalf("legacy mapping failed: %+v", cfg)
	}
}

func TestLongbridgeIndexRequiresCredentialsIndependently(t *testing.T) {
	t.Setenv("GO_SERVER_INDEX_PROVIDER", "longbridge")
	t.Setenv("GO_SERVER_LONGBRIDGE_HISTORY_ENABLED", "false")
	t.Setenv("LONGBRIDGE_APP_KEY", "")
	if err := ServerFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), "LONGBRIDGE_APP_KEY") {
		t.Fatalf("err=%v", err)
	}
	t.Setenv("LONGBRIDGE_APP_KEY", "app-key")
	t.Setenv("LONGBRIDGE_APP_SECRET", "app-secret")
	t.Setenv("LONGBRIDGE_ACCESS_TOKEN", "token")
	if err := ServerFromEnv().Validate(); err != nil {
		t.Fatal(err)
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

func TestServerRedisOptInValidation(t *testing.T) {
	t.Setenv("GO_SERVER_REDIS_ENABLED", "true")
	t.Setenv("GO_SERVER_REDIS_ADDRESS", "")
	if err := ServerFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), "GO_SERVER_REDIS_ADDRESS") {
		t.Fatalf("expected missing Redis address error, got %v", err)
	}
	t.Setenv("GO_SERVER_REDIS_ADDRESS", "redis.example:6379")
	t.Setenv("GO_SERVER_REDIS_USERNAME", "market-bridge")
	t.Setenv("GO_SERVER_REDIS_PASSWORD", "secret")
	t.Setenv("GO_SERVER_REDIS_DB", "4")
	t.Setenv("GO_SERVER_REDIS_TTL", "12h")
	cfg := ServerFromEnv()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !cfg.RedisEnabled || cfg.RedisAddress != "redis.example:6379" || cfg.RedisUsername != "market-bridge" || cfg.RedisPassword != "secret" || cfg.RedisDB != 4 || cfg.RedisTTL != 12*time.Hour {
		t.Fatalf("unexpected Redis config: %+v", cfg)
	}
}

func TestFMPNewsRequiresKeyAndReadsPollingConfig(t *testing.T) {
	t.Setenv("GO_SERVER_NEWS_PROVIDER", "fmp")
	t.Setenv("FMP_API_KEY", "")
	if err := ServerFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), "FMP_API_KEY") {
		t.Fatalf("expected missing FMP key, got %v", err)
	}
	t.Setenv("FMP_API_KEY", "secret")
	t.Setenv("FMP_NEWS_POLL_INTERVAL", "90s")
	cfg := ServerFromEnv()
	if err := cfg.Validate(); err != nil || cfg.FMPNewsPollInterval != 90*time.Second {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
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

func TestLongbridgeCredentialsAreRequiredWhenEnabled(t *testing.T) {
	t.Setenv("GO_SERVER_LONGBRIDGE_HISTORY_ENABLED", "true")
	t.Setenv("LONGBRIDGE_APP_KEY", "")
	if err := ServerFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), "LONGBRIDGE_APP_KEY") {
		t.Fatalf("expected missing Longbridge key error, got %v", err)
	}

	t.Setenv("LONGBRIDGE_APP_KEY", "app-key")
	t.Setenv("LONGBRIDGE_APP_SECRET", "app-secret")
	t.Setenv("LONGBRIDGE_ACCESS_TOKEN", "access-token")
	cfg := ServerFromEnv()
	if !cfg.LongbridgeHistoryEnabled {
		t.Fatal("Longbridge history should be enabled")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected Longbridge validation error: %v", err)
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
	if got.ClickHouseRetention != 1825*24*time.Hour {
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
