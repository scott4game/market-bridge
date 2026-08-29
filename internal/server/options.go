package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/scott4game/market-bridge/internal/provider"
	_ "modernc.org/sqlite"
)

type optionCacheCall struct {
	done chan struct{}
	raw  []byte
	err  error
}

type OptionCatalog struct {
	db       *sql.DB
	provider provider.OptionsProvider
	now      func() time.Time

	mu       sync.Mutex
	inflight map[string]*optionCacheCall
}

func OpenOptionCatalog(path string, source provider.OptionsProvider) (*OptionCatalog, error) {
	if source == nil {
		return nil, errors.New("options provider is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS option_cache (cache_key TEXT PRIMARY KEY, kind TEXT NOT NULL, payload BLOB NOT NULL, fetched_at INTEGER NOT NULL, expires_at INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS option_cache_expiry ON option_cache(expires_at)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &OptionCatalog{db: db, provider: source, now: time.Now, inflight: map[string]*optionCacheCall{}}, nil
}

func (c *OptionCatalog) Close() error { return c.db.Close() }

func (c *OptionCatalog) Contracts(ctx context.Context, query provider.OptionContractQuery) ([]provider.OptionContract, string, error) {
	query, err := query.Normalize()
	if err != nil {
		return nil, "", err
	}
	encoded, _ := json.Marshal(query)
	key := optionCacheKey("contracts", encoded)
	raw, source, err := c.load(ctx, key, "contracts", 24*time.Hour, func(ctx context.Context) ([]byte, time.Duration, error) {
		contracts, err := c.provider.Contracts(ctx, query)
		if err != nil {
			return nil, 0, err
		}
		payload, err := json.Marshal(contracts)
		return payload, 24 * time.Hour, err
	})
	if err != nil {
		return nil, "", err
	}
	var contracts []provider.OptionContract
	if err := json.Unmarshal(raw, &contracts); err != nil {
		return nil, "", err
	}
	return contracts, source, nil
}

func (c *OptionCatalog) Bars(ctx context.Context, contract string, from, to time.Time) ([]provider.OptionBar, string, error) {
	contract = strings.ToUpper(strings.TrimSpace(contract))
	identity := struct {
		Contract string    `json:"contract"`
		From     time.Time `json:"from"`
		To       time.Time `json:"to"`
	}{contract, from.UTC(), to.UTC()}
	encoded, _ := json.Marshal(identity)
	key := optionCacheKey("bars", encoded)
	raw, source, err := c.load(ctx, key, "bars", 30*24*time.Hour, func(ctx context.Context) ([]byte, time.Duration, error) {
		bars, err := c.provider.OptionBars(ctx, contract, from, to)
		if err != nil {
			return nil, 0, err
		}
		ttl := 30 * 24 * time.Hour
		for _, bar := range bars {
			if !bar.Completed {
				ttl = 15 * time.Minute
				break
			}
		}
		payload, err := json.Marshal(bars)
		return payload, ttl, err
	})
	if err != nil {
		return nil, "", err
	}
	var bars []provider.OptionBar
	if err := json.Unmarshal(raw, &bars); err != nil {
		return nil, "", err
	}
	return bars, source, nil
}

func optionCacheKey(kind string, identity []byte) string {
	digest := sha256.Sum256(identity)
	return kind + ":" + hex.EncodeToString(digest[:])
}

func (c *OptionCatalog) load(ctx context.Context, key, kind string, defaultTTL time.Duration, fetch func(context.Context) ([]byte, time.Duration, error)) ([]byte, string, error) {
	if raw, ok, err := c.cached(ctx, key); err != nil {
		return nil, "", err
	} else if ok {
		return raw, "cache", nil
	}
	c.mu.Lock()
	if running := c.inflight[key]; running != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-running.done:
			return append([]byte(nil), running.raw...), "cache", running.err
		}
	}
	call := &optionCacheCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.inflight, key)
		close(call.done)
		c.mu.Unlock()
	}()

	if raw, ok, err := c.cached(ctx, key); err != nil {
		call.err = err
		return nil, "", err
	} else if ok {
		call.raw = raw
		return append([]byte(nil), raw...), "cache", nil
	}
	raw, ttl, err := fetch(ctx)
	if err != nil {
		call.err = err
		return nil, "", err
	}
	if ttl <= 0 {
		ttl = defaultTTL
	}
	now := c.now()
	if _, err := c.db.ExecContext(ctx, `INSERT OR REPLACE INTO option_cache(cache_key,kind,payload,fetched_at,expires_at) VALUES(?,?,?,?,?)`, key, kind, raw, now.Unix(), now.Add(ttl).Unix()); err != nil {
		call.err = err
		return nil, "", fmt.Errorf("cache options response: %w", err)
	}
	call.raw = append([]byte(nil), raw...)
	return raw, "massive", nil
}

func (c *OptionCatalog) cached(ctx context.Context, key string) ([]byte, bool, error) {
	var raw []byte
	var expires int64
	err := c.db.QueryRowContext(ctx, `SELECT payload,expires_at FROM option_cache WHERE cache_key=?`, key).Scan(&raw, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if expires <= c.now().Unix() {
		_, _ = c.db.ExecContext(ctx, `DELETE FROM option_cache WHERE cache_key=?`, key)
		return nil, false, nil
	}
	return raw, true, nil
}

func (c *OptionCatalog) Prune(ctx context.Context) (int64, error) {
	result, err := c.db.ExecContext(ctx, `DELETE FROM option_cache WHERE expires_at<=?`, c.now().Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
