package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	Listen             string
	DataDir            string
	Provider           string
	DataVersion        string
	BearerToken        string
	MassiveAPIKey      string
	MassiveBaseURL     string
	MassivePlanName    string
	MassivePerMinute   int
	MassivePerMonth    int
	LiveProvider       string
	Watchlist          []string
	ClickHouseEnabled  bool
	ClickHouseURL      string
	ClickHouseDatabase string
	ClickHouseUser     string
	ClickHousePassword string
	DatasetTTL         time.Duration
}

func ServerFromEnv() Server {
	return Server{
		Listen: env("GO_SERVER_LISTEN", ":17601"), DataDir: env("GO_SERVER_DATA_DIR", "./data/server"),
		Provider: env("GO_SERVER_PROVIDER", "mock"), DataVersion: env("GO_SERVER_DATA_VERSION", time.Now().UTC().Format("2006-01-02")),
		BearerToken: os.Getenv("GO_SERVER_TOKEN"), MassiveAPIKey: os.Getenv("MASSIVE_API_KEY"), MassiveBaseURL: env("MASSIVE_BASE_URL", "https://api.massive.com"),
		MassivePlanName: env("MASSIVE_PLAN_NAME", "stocks_basic"), MassivePerMinute: integer("MASSIVE_REQUESTS_PER_MINUTE", 5), MassivePerMonth: integer("MASSIVE_REQUESTS_PER_MONTH", 0),
		LiveProvider: env("GO_SERVER_LIVE_PROVIDER", "mock"), Watchlist: split(env("GO_SERVER_WATCHLIST", "AAPL,NVDA")),
		ClickHouseEnabled: env("GO_SERVER_CLICKHOUSE_ENABLED", "false") == "true",
		ClickHouseURL:     os.Getenv("CLICKHOUSE_URL"), ClickHouseDatabase: env("CLICKHOUSE_DATABASE", "market"), ClickHouseUser: env("CLICKHOUSE_USER", "market"), ClickHousePassword: os.Getenv("CLICKHOUSE_PASSWORD"),
		DatasetTTL: duration("GO_SERVER_DATASET_TTL", 24*time.Hour),
	}
}

func (s Server) Validate() error {
	if !s.ClickHouseEnabled {
		return nil
	}
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
	return nil
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
	Listen          string
	CacheDir        string
	ServerURL       string
	ServerToken     string
	ParquetTTL      time.Duration
	CleanupInterval time.Duration
	RedisEnabled    bool
	RedisAddress    string
	RedisUsername   string
	RedisPassword   string
	RedisDB         int
	RedisTTL        time.Duration
}

func ClientFromEnv() Client {
	return Client{
		Listen: env("GO_CLIENT_LISTEN", "127.0.0.1:17600"), CacheDir: env("GO_CLIENT_CACHE_DIRECTORY", "./data/client"),
		ServerURL: env("GO_CLIENT_SERVER_URL", "http://127.0.0.1:17601"), ServerToken: os.Getenv("GO_CLIENT_SERVER_TOKEN"),
		ParquetTTL: duration("GO_CLIENT_PARQUET_TTL", 720*time.Hour), CleanupInterval: duration("GO_CLIENT_CLEANUP_INTERVAL", 6*time.Hour),
		RedisEnabled: boolean("GO_CLIENT_REDIS_ENABLED", true), RedisAddress: env("GO_CLIENT_REDIS_ADDRESS", "127.0.0.1:6379"), RedisUsername: os.Getenv("GO_CLIENT_REDIS_USERNAME"), RedisPassword: os.Getenv("GO_CLIENT_REDIS_PASSWORD"), RedisDB: integer("GO_CLIENT_REDIS_DB", 0), RedisTTL: duration("GO_CLIENT_REDIS_TTL", 24*time.Hour),
	}
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
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
