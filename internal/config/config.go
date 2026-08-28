package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	Listen                    string
	DataDir                   string
	AuthDB                    string
	Provider                  string
	IndexProvider             string
	DataVersion               string
	BearerToken               string
	MassiveAPIKey             string
	MassiveBaseURL            string
	MassivePlanName           string
	MassivePerMinute          int
	MassivePerMonth           int
	AShareProvider            string
	HKProvider                string
	TushareToken              string
	TushareBaseURL            string
	TusharePerMinute          int
	NewsProvider              string
	FMPAPIKey                 string
	FMPBaseURL                string
	FMPNewsPollInterval       time.Duration
	NewsRetention             time.Duration
	LiveProvider              string
	LiveProviders             []string
	LongbridgeHistoryEnabled  bool
	LongbridgeDepthEnabled    bool
	LongbridgeAppKey          string
	LongbridgeAppSecret       string
	LongbridgeAccessToken     string
	BinanceEnabled            bool
	BinanceRESTURL            string
	BinanceWSURL              string
	ClickHouseEnabled         bool
	ClickHouseURL             string
	ClickHouseDatabase        string
	ClickHouseUser            string
	ClickHousePassword        string
	ClickHouseRetention       time.Duration
	ClickHouseCleanupInterval time.Duration
	RedisEnabled              bool
	RedisAddress              string
	RedisUsername             string
	RedisPassword             string
	RedisDB                   int
	RedisTTL                  time.Duration
	MarketHistorySyncEnabled  bool
	MarketHistorySyncInterval time.Duration
	DatasetTTL                time.Duration
	DatasetWorkers            int
	DatasetQueueSize          int
	DatasetBuildTimeout       time.Duration
	EmptyCoverageTTL          time.Duration
	SecurityProfileTTL        time.Duration
	SecurityProfileMaxStale   time.Duration
	SecurityProfileWorkers    int
}

func ServerFromEnv() Server {
	legacyLongbridgeHistory := boolean("GO_SERVER_LONGBRIDGE_HISTORY_ENABLED", false)
	return Server{
		Listen: env("GO_SERVER_LISTEN", ":17601"), DataDir: env("GO_SERVER_DATA_DIR", "./data/server"), AuthDB: os.Getenv("GO_SERVER_AUTH_DB"),
		Provider: env("GO_SERVER_PROVIDER", "mock"), IndexProvider: strings.ToLower(strings.TrimSpace(env("GO_SERVER_INDEX_PROVIDER", "disabled"))), DataVersion: env("GO_SERVER_DATA_VERSION", "market-v1"),
		BearerToken: os.Getenv("GO_SERVER_TOKEN"), MassiveAPIKey: os.Getenv("MASSIVE_API_KEY"), MassiveBaseURL: env("MASSIVE_BASE_URL", "https://api.massive.com"),
		MassivePlanName: env("MASSIVE_PLAN_NAME", "stocks_basic"), MassivePerMinute: integer("MASSIVE_REQUESTS_PER_MINUTE", 5), MassivePerMonth: integer("MASSIVE_REQUESTS_PER_MONTH", 0),
		AShareProvider: historyProvider("GO_SERVER_A_SHARE_PROVIDER", legacyLongbridgeHistory), HKProvider: historyProvider("GO_SERVER_HK_PROVIDER", legacyLongbridgeHistory),
		TushareToken: os.Getenv("TUSHARE_TOKEN"), TushareBaseURL: env("TUSHARE_BASE_URL", "https://api.tushare.pro"), TusharePerMinute: integer("TUSHARE_REQUESTS_PER_MINUTE", 200),
		NewsProvider: env("GO_SERVER_NEWS_PROVIDER", "disabled"), FMPAPIKey: os.Getenv("FMP_API_KEY"), FMPBaseURL: env("FMP_BASE_URL", "https://financialmodelingprep.com"), FMPNewsPollInterval: duration("FMP_NEWS_POLL_INTERVAL", time.Minute), NewsRetention: duration("GO_SERVER_NEWS_RETENTION", 30*24*time.Hour),
		LiveProvider: env("GO_SERVER_LIVE_PROVIDER", "mock"), LiveProviders: split(os.Getenv("GO_SERVER_LIVE_PROVIDERS")),
		LongbridgeHistoryEnabled: legacyLongbridgeHistory, LongbridgeDepthEnabled: boolean("GO_SERVER_LONGBRIDGE_DEPTH_ENABLED", false),
		LongbridgeAppKey: os.Getenv("LONGBRIDGE_APP_KEY"), LongbridgeAppSecret: os.Getenv("LONGBRIDGE_APP_SECRET"), LongbridgeAccessToken: os.Getenv("LONGBRIDGE_ACCESS_TOKEN"),
		BinanceEnabled: boolean("GO_SERVER_BINANCE_ENABLED", false), BinanceRESTURL: env("BINANCE_REST_BASE_URL", "https://data-api.binance.vision"), BinanceWSURL: env("BINANCE_WS_URL", "wss://data-stream.binance.vision"),
		ClickHouseEnabled: boolean("GO_SERVER_CLICKHOUSE_ENABLED", false),
		ClickHouseURL:     os.Getenv("CLICKHOUSE_URL"), ClickHouseDatabase: env("CLICKHOUSE_DATABASE", "market"), ClickHouseUser: env("CLICKHOUSE_USER", "market"), ClickHousePassword: os.Getenv("CLICKHOUSE_PASSWORD"),
		ClickHouseRetention: duration("GO_SERVER_CLICKHOUSE_RETENTION", 1825*24*time.Hour), ClickHouseCleanupInterval: duration("GO_SERVER_CLICKHOUSE_CLEANUP_INTERVAL", 720*time.Hour),
		RedisEnabled: boolean("GO_SERVER_REDIS_ENABLED", false), RedisAddress: os.Getenv("GO_SERVER_REDIS_ADDRESS"), RedisUsername: os.Getenv("GO_SERVER_REDIS_USERNAME"), RedisPassword: os.Getenv("GO_SERVER_REDIS_PASSWORD"), RedisDB: integer("GO_SERVER_REDIS_DB", 0), RedisTTL: duration("GO_SERVER_REDIS_TTL", 24*time.Hour),
		MarketHistorySyncEnabled: boolean("GO_SERVER_MARKET_HISTORY_SYNC_ENABLED", false), MarketHistorySyncInterval: duration("GO_SERVER_MARKET_HISTORY_SYNC_INTERVAL", 24*time.Hour),
		DatasetTTL: duration("GO_SERVER_DATASET_TTL", 24*time.Hour), DatasetWorkers: integer("GO_SERVER_DATASET_WORKERS", 2), DatasetQueueSize: integer("GO_SERVER_DATASET_QUEUE_SIZE", 100),
		DatasetBuildTimeout: duration("GO_SERVER_DATASET_BUILD_TIMEOUT", 10*time.Minute), EmptyCoverageTTL: duration("GO_SERVER_EMPTY_COVERAGE_TTL", 15*time.Minute),
		SecurityProfileTTL: duration("GO_SERVER_SECURITY_PROFILE_TTL", 24*time.Hour), SecurityProfileMaxStale: duration("GO_SERVER_SECURITY_PROFILE_MAX_STALE", 30*24*time.Hour), SecurityProfileWorkers: integer("GO_SERVER_SECURITY_PROFILE_WORKERS", 16),
	}
}

func (s Server) EffectiveLiveProviders() []string {
	values := s.LiveProviders
	if len(values) == 0 {
		values = []string{s.LiveProvider}
	}
	seen := map[string]struct{}{}
	providers := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if _, ok := seen[value]; value == "" || ok {
			continue
		}
		seen[value] = struct{}{}
		providers = append(providers, value)
	}
	return providers
}

func (s Server) Validate() error {
	if s.AShareProvider != "" && s.AShareProvider != "disabled" && s.AShareProvider != "longbridge" && s.AShareProvider != "tushare" {
		return fmt.Errorf("unsupported A-share provider %q", s.AShareProvider)
	}
	if s.HKProvider != "" && s.HKProvider != "disabled" && s.HKProvider != "longbridge" {
		return fmt.Errorf("unsupported HK provider %q", s.HKProvider)
	}
	if s.AShareProvider == "tushare" && strings.TrimSpace(s.TushareToken) == "" {
		return fmt.Errorf("TUSHARE_TOKEN is required when GO_SERVER_A_SHARE_PROVIDER=tushare")
	}
	if s.TusharePerMinute < 0 {
		return fmt.Errorf("TUSHARE_REQUESTS_PER_MINUTE must be non-negative")
	}
	switch s.IndexProvider {
	case "disabled", "longbridge", "fmp", "massive", "mock":
	default:
		return fmt.Errorf("unsupported index provider %q", s.IndexProvider)
	}
	if s.NewsProvider != "disabled" && s.NewsProvider != "fmp" {
		return fmt.Errorf("unsupported news provider %q", s.NewsProvider)
	}
	if (s.NewsProvider == "fmp" || s.IndexProvider == "fmp") && strings.TrimSpace(s.FMPAPIKey) == "" {
		return fmt.Errorf("FMP_API_KEY is required when an FMP provider is enabled")
	}
	if (s.Provider == "massive" || s.IndexProvider == "massive") && strings.TrimSpace(s.MassiveAPIKey) == "" {
		return fmt.Errorf("MASSIVE_API_KEY is required when a Massive provider is enabled")
	}
	liveProviders := s.EffectiveLiveProviders()
	for _, name := range liveProviders {
		if name != "mock" && name != "longbridge" && name != "binance" {
			return fmt.Errorf("unsupported live provider %q", name)
		}
		if name == "mock" && len(liveProviders) > 1 {
			return fmt.Errorf("mock live provider cannot be combined with real providers")
		}
	}
	if s.AShareProvider == "longbridge" || s.HKProvider == "longbridge" || s.IndexProvider == "longbridge" || containsString(liveProviders, "longbridge") {
		values := []struct{ name, value string }{
			{"LONGBRIDGE_APP_KEY", s.LongbridgeAppKey},
			{"LONGBRIDGE_APP_SECRET", s.LongbridgeAppSecret},
			{"LONGBRIDGE_ACCESS_TOKEN", s.LongbridgeAccessToken},
		}
		for _, item := range values {
			if strings.TrimSpace(item.value) == "" {
				return fmt.Errorf("%s is required when Longbridge history, index, or live data is enabled", item.name)
			}
		}
	}
	if s.RedisEnabled && strings.TrimSpace(s.RedisAddress) == "" {
		return fmt.Errorf("GO_SERVER_REDIS_ADDRESS is required when GO_SERVER_REDIS_ENABLED=true")
	}
	if s.RedisEnabled && s.RedisDB < 0 {
		return fmt.Errorf("GO_SERVER_REDIS_DB must be non-negative")
	}
	if s.RedisEnabled && s.RedisTTL <= 0 {
		return fmt.Errorf("GO_SERVER_REDIS_TTL must be greater than zero")
	}
	if s.ClickHouseEnabled {
		values := []struct{ name, value string }{
			{"CLICKHOUSE_URL", s.ClickHouseURL},
			{"CLICKHOUSE_DATABASE", s.ClickHouseDatabase},
			{"CLICKHOUSE_USER", s.ClickHouseUser},
			{"CLICKHOUSE_PASSWORD", s.ClickHousePassword},
		}
		for _, item := range values {
			if strings.TrimSpace(item.value) == "" {
				return fmt.Errorf("%s is required when GO_SERVER_CLICKHOUSE_ENABLED=true", item.name)
			}
		}
	}
	if s.SecurityProfileTTL <= 0 {
		return fmt.Errorf("GO_SERVER_SECURITY_PROFILE_TTL must be greater than zero")
	}
	if s.SecurityProfileMaxStale < s.SecurityProfileTTL {
		return fmt.Errorf("GO_SERVER_SECURITY_PROFILE_MAX_STALE must be at least GO_SERVER_SECURITY_PROFILE_TTL")
	}
	if s.SecurityProfileWorkers < 1 || s.SecurityProfileWorkers > 128 {
		return fmt.Errorf("GO_SERVER_SECURITY_PROFILE_WORKERS must be between 1 and 128")
	}
	return nil
}

func historyProvider(name string, legacyLongbridge bool) string {
	if value := strings.ToLower(strings.TrimSpace(os.Getenv(name))); value != "" {
		return value
	}
	if legacyLongbridge {
		return "longbridge"
	}
	return "disabled"
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func split(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

type Client struct {
	Listen                      string
	CacheDir                    string
	ServerURL                   string
	ServerToken                 string
	ParquetTTL                  time.Duration
	CleanupInterval             time.Duration
	RedisEnabled                bool
	RedisAddress                string
	RedisUsername               string
	RedisPassword               string
	RedisDB                     int
	RedisTTL                    time.Duration
	ClickHouseEnabled           bool
	ClickHouseURL               string
	ClickHouseDatabase          string
	ClickHouseUser              string
	ClickHousePassword          string
	ClickHouseCompletedBarsOnly bool
	ClickHouseRetention         time.Duration
	ClickHouseCleanupInterval   time.Duration
	StorageCapabilityInterval   time.Duration
	MarketHistorySyncEnabled    bool
	MarketHistorySyncInterval   time.Duration
	EmptyCoverageTTL            time.Duration
	NewsRetention               time.Duration
}

func ClientFromEnv() Client {
	return Client{
		Listen: env("GO_CLIENT_LISTEN", "127.0.0.1:17600"), CacheDir: env("GO_CLIENT_CACHE_DIRECTORY", "./data/client"),
		ServerURL: env("GO_CLIENT_SERVER_URL", "http://127.0.0.1:17601"), ServerToken: os.Getenv("GO_CLIENT_SERVER_TOKEN"),
		ParquetTTL: duration("GO_CLIENT_PARQUET_TTL", 720*time.Hour), CleanupInterval: duration("GO_CLIENT_CLEANUP_INTERVAL", 6*time.Hour),
		RedisEnabled: boolean("GO_CLIENT_REDIS_ENABLED", true), RedisAddress: env("GO_CLIENT_REDIS_ADDRESS", "127.0.0.1:6379"), RedisUsername: os.Getenv("GO_CLIENT_REDIS_USERNAME"), RedisPassword: os.Getenv("GO_CLIENT_REDIS_PASSWORD"), RedisDB: integer("GO_CLIENT_REDIS_DB", 0), RedisTTL: duration("GO_CLIENT_REDIS_TTL", 24*time.Hour),
		ClickHouseEnabled: boolean("GO_CLIENT_CLICKHOUSE_ENABLED", false), ClickHouseURL: firstEnv("GO_CLIENT_CLICKHOUSE_URL", "CLICKHOUSE_URL"), ClickHouseDatabase: env("CLICKHOUSE_DATABASE", "market"), ClickHouseUser: env("CLICKHOUSE_USER", "market"), ClickHousePassword: os.Getenv("CLICKHOUSE_PASSWORD"),
		ClickHouseCompletedBarsOnly: boolean("GO_CLIENT_CLICKHOUSE_COMPLETED_BARS_ONLY", true),
		ClickHouseRetention:         duration("GO_CLIENT_CLICKHOUSE_RETENTION", 1825*24*time.Hour), ClickHouseCleanupInterval: duration("GO_CLIENT_CLICKHOUSE_CLEANUP_INTERVAL", 720*time.Hour), StorageCapabilityInterval: duration("GO_CLIENT_STORAGE_CAPABILITY_INTERVAL", 5*time.Minute),
		MarketHistorySyncEnabled: boolean("GO_CLIENT_MARKET_HISTORY_SYNC_ENABLED", false), MarketHistorySyncInterval: duration("GO_CLIENT_MARKET_HISTORY_SYNC_INTERVAL", 24*time.Hour),
		EmptyCoverageTTL: duration("GO_CLIENT_EMPTY_COVERAGE_TTL", 15*time.Minute),
		NewsRetention:    duration("GO_CLIENT_NEWS_RETENTION", 30*24*time.Hour),
	}
}

func (c Client) Validate() error {
	if !c.ClickHouseEnabled {
		return nil
	}
	values := []struct{ name, value string }{
		{"GO_CLIENT_CLICKHOUSE_URL or CLICKHOUSE_URL", c.ClickHouseURL},
		{"CLICKHOUSE_DATABASE", c.ClickHouseDatabase},
		{"CLICKHOUSE_USER", c.ClickHouseUser},
		{"CLICKHOUSE_PASSWORD", c.ClickHousePassword},
	}
	for _, item := range values {
		if strings.TrimSpace(item.value) == "" {
			return fmt.Errorf("%s is required when GO_CLIENT_CLICKHOUSE_ENABLED=true", item.name)
		}
	}
	return nil
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}
func duration(k string, fallback time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
func boolean(k string, fallback bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}
func integer(k string, fallback int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
