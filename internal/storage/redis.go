package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/scott4game/market-bridge/internal/market"
)

const serverRedisBarsPrefix = "bars:v3:server:"

// RedisBarCache is the shared, disposable hot cache used by go-server.
type RedisBarCache struct {
	client *redis.Client
}

func NewRedisBarCache(address, username, password string, db int) *RedisBarCache {
	return &RedisBarCache{client: redis.NewClient(&redis.Options{
		Addr: address, Username: username, Password: password, DB: db,
		DialTimeout: 300 * time.Millisecond, ReadTimeout: 500 * time.Millisecond, WriteTimeout: 500 * time.Millisecond,
	})}
}

func (c *RedisBarCache) Healthy(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

func (c *RedisBarCache) Get(ctx context.Context, key string) ([]market.Bar, bool, error) {
	raw, err := c.client.Get(ctx, serverRedisBarsPrefix+key).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var bars []market.Bar
	if err := json.Unmarshal(raw, &bars); err != nil {
		return nil, false, err
	}
	if bars == nil {
		bars = []market.Bar{}
	}
	return bars, true, nil
}

func (c *RedisBarCache) Set(ctx context.Context, key string, bars []market.Bar, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	raw, err := json.Marshal(bars)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, serverRedisBarsPrefix+key, raw, ttl).Err()
}

func (c *RedisBarCache) Close() error {
	return c.client.Close()
}
