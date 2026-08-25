package localclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/redis/go-redis/v9"
	"github.com/scott4game/market-bridge/internal/config"
	"github.com/scott4game/market-bridge/internal/coverage"
	"github.com/scott4game/market-bridge/internal/market"
	_ "modernc.org/sqlite"
)

type Cache struct {
	cfg             config.Client
	db              *sql.DB
	redis           *redis.Client
	http            *http.Client
	mu              sync.Mutex
	inflight        map[string]*flight
	active          map[string]int
	datasetLocks    map[string]*datasetGuard
	clickhouse      HistoricalClickHouse
	capability      StorageCapability
	capabilityAt    time.Time
	capabilityStale bool
	capabilityError string
	coverage        *coverage.Store
	factorMu        sync.Mutex
	factorCache     map[string]cachedForwardFactors
}

type cachedForwardFactors struct {
	curve     market.ForwardFactors
	expiresAt time.Time
}

type HistoricalClickHouse interface {
	QueryBars(context.Context, market.DatasetSpec) ([]market.Bar, error)
	WriteBars(context.Context, market.AdjustmentMode, []market.Bar, uint64) error
	Write(context.Context, market.LiveEvent) error
	CleanupBefore(context.Context, time.Time) (int, error)
}

type StorageCapability struct {
	ClickHouse struct {
		Enabled bool   `json:"enabled"`
		Healthy bool   `json:"healthy"`
		Error   string `json:"error"`
	} `json:"clickhouse"`
	HistoryRevision uint64 `json:"history_revision"`
	DataVersion     string `json:"data_version"`
}

type flight struct {
	done     chan struct{}
	manifest market.Manifest
	err      error
}

type datasetGuard struct {
	lock sync.RWMutex
	refs int
}

func NewCache(cfg config.Client) (*Cache, error) {
	return NewCacheWithClickHouse(cfg, nil)
}

func NewCacheWithClickHouse(cfg config.Client, clickhouse HistoricalClickHouse) (*Cache, error) {
	root, err := filepath.Abs(cfg.CacheDir)
	if err != nil {
		return nil, err
	}
	if root == string(filepath.Separator) || root == "." {
		return nil, errors.New("unsafe cache directory")
	}
	cfg.CacheDir = root
	if err := os.MkdirAll(filepath.Join(root, "datasets"), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "cache.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE IF NOT EXISTS datasets (id TEXT PRIMARY KEY, spec_hash TEXT NOT NULL, manifest_json BLOB NOT NULL, last_accessed_at INTEGER NOT NULL, state TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS datasets_spec ON datasets(spec_hash, last_accessed_at)`,
	}
	for _, q := range stmts {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	coverageStore, err := coverage.New(db, "clickhouse_coverage_v3", "clickhouse_coverage", "clickhouse_coverage_v2")
	if err != nil {
		db.Close()
		return nil, err
	}
	c := &Cache{cfg: cfg, db: db, http: &http.Client{Timeout: 2 * time.Minute}, inflight: map[string]*flight{}, active: map[string]int{}, datasetLocks: map[string]*datasetGuard{}, clickhouse: clickhouse, coverage: coverageStore, factorCache: map[string]cachedForwardFactors{}}
	if cfg.RedisEnabled {
		c.redis = redis.NewClient(&redis.Options{Addr: cfg.RedisAddress, Username: cfg.RedisUsername, Password: cfg.RedisPassword, DB: cfg.RedisDB, DialTimeout: 300 * time.Millisecond, ReadTimeout: 500 * time.Millisecond, WriteTimeout: 500 * time.Millisecond})
	}
	if err := c.recoverInterrupted(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Cache) recoverInterrupted() error {
	root := filepath.Join(c.cfg.CacheDir, "datasets")
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".downloading") {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return err
			}
		}
	}
	rows, err := c.db.Query(`SELECT id FROM datasets WHERE state='deleting'`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := os.RemoveAll(filepath.Join(root, id)); err != nil {
			return err
		}
		if _, err := c.db.Exec(`DELETE FROM datasets WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cache) Close() error {
	if c.redis != nil {
		_ = c.redis.Close()
	}
	return c.db.Close()
}

func (c *Cache) Bars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, string, error) {
	spec, err := spec.Normalize()
	if err != nil {
		return nil, "", err
	}
	capability, capabilityErr := c.storageCapability(ctx)
	cutoff := time.Now().UTC().Add(-c.clickhouseRetention())
	if capabilityErr == nil && (spec.From.Before(cutoff) || spec.Interval == "1m" && (capability.ClickHouse.Enabled || c.clickhouse != nil)) {
		return c.routedBars(ctx, spec, capability)
	}
	return c.legacyBars(ctx, spec)
}

func (c *Cache) legacyBars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, string, error) {
	version, err := c.semanticCacheVersion(ctx, spec, "request")
	if err != nil {
		return nil, "", err
	}
	key, err := spec.Hash(market.SchemaVersion, version)
	if err != nil {
		return nil, "", err
	}
	if c.redis != nil {
		if b, err := c.redis.Get(ctx, "bars:"+key).Bytes(); err == nil {
			var bars []market.Bar
			if json.Unmarshal(b, &bars) == nil {
				if bars == nil {
					bars = []market.Bar{}
				}
				_ = c.touchBySpec(ctx, key)
				return bars, "redis", nil
			}
		}
	}
	m, ok, err := c.localManifest(ctx, key)
	if err != nil {
		return nil, "", err
	}
	source := "parquet"
	if !ok {
		m, err = c.ensureRemote(ctx, key, spec)
		if err != nil {
			return nil, "", err
		}
		source = "go-server"
	}
	datasetLock, releaseGuard := c.datasetLock(m.DatasetID)
	datasetLock.RLock()
	if _, err := os.Stat(filepath.Join(c.cfg.CacheDir, "datasets", m.DatasetID, "manifest.json")); err != nil {
		datasetLock.RUnlock()
		releaseGuard()
		m, err = c.ensureRemote(ctx, key, spec)
		if err != nil {
			return nil, "", err
		}
		datasetLock, releaseGuard = c.datasetLock(m.DatasetID)
		datasetLock.RLock()
	}
	defer releaseGuard()
	defer datasetLock.RUnlock()
	c.retain(m.DatasetID)
	defer c.release(m.DatasetID)
	bars, err := c.readManifest(m)
	if err != nil {
		return nil, "", err
	}
	if bars == nil {
		bars = []market.Bar{}
	}
	market.SortBars(bars)
	_ = c.touch(ctx, m.DatasetID)
	if c.redis != nil {
		if b, e := json.Marshal(bars); e == nil {
			_ = c.redis.Set(ctx, "bars:"+key, b, c.cfg.RedisTTL).Err()
		}
	}
	return bars, source, nil
}

func (c *Cache) routedBars(ctx context.Context, spec market.DatasetSpec, capability StorageCapability) ([]market.Bar, string, error) {
	cutoff := time.Now().UTC().Add(-c.clickhouseRetention())
	var bars []market.Bar
	var sources []string
	if spec.From.Before(cutoff) {
		archive := spec
		archive.To = minTime(spec.To, cutoff)
		part, source, err := c.archiveBars(ctx, archive, capability.DataVersion)
		if err != nil {
			return nil, "", err
		}
		bars = append(bars, part...)
		sources = append(sources, source)
	}
	if spec.To.After(cutoff) {
		recent := spec
		if recent.From.Before(cutoff) {
			recent.From = cutoff
		}
		part, source, err := c.recentBars(ctx, recent, capability)
		if err != nil {
			return nil, "", err
		}
		bars = append(bars, part...)
		sources = append(sources, source)
	}
	market.SortBars(bars)
	bars = deduplicateMarketBars(bars)
	if bars == nil {
		bars = []market.Bar{}
	}
	return bars, strings.Join(sources, "+"), nil
}

func (c *Cache) recentBars(ctx context.Context, spec market.DatasetSpec, capability StorageCapability) ([]market.Bar, string, error) {
	if spec.Interval != "1m" {
		version, err := c.semanticCacheVersion(ctx, spec, "provider-recent:"+capability.DataVersion)
		if err != nil {
			return nil, "", err
		}
		key, _ := spec.Hash(market.SchemaVersion, version)
		if bars, ok := c.redisBars(ctx, "recent:"+key); ok {
			return bars, "redis", nil
		}
		bars, source, err := c.remoteHistoryBars(ctx, spec, true)
		if err != nil {
			return nil, "", err
		}
		c.setRedisBars(ctx, "recent:"+key, bars, c.recentRedisTTL(bars))
		return bars, source, nil
	}
	mode := "local-clickhouse"
	version := capability.DataVersion
	if capability.ClickHouse.Enabled {
		mode = "server-clickhouse"
		version = fmt.Sprintf("%s:%d", capability.DataVersion, capability.HistoryRevision)
	}
	cacheVersion, err := c.semanticCacheVersion(ctx, spec, mode+":"+version)
	if err != nil {
		return nil, "", err
	}
	key, _ := spec.Hash(market.SchemaVersion, cacheVersion)
	if bars, ok := c.redisBars(ctx, "recent:"+key); ok {
		return bars, "redis", nil
	}
	if capability.ClickHouse.Enabled {
		if !capability.ClickHouse.Healthy {
			return nil, "", fmt.Errorf("remote ClickHouse is enabled but unhealthy: %s", capability.ClickHouse.Error)
		}
		bars, source, err := c.remoteHistoryBars(ctx, spec, false)
		if err != nil {
			return nil, "", err
		}
		c.setRedisBars(ctx, "recent:"+key, bars, c.recentRedisTTL(bars))
		return bars, source, nil
	}
	if c.clickhouse == nil {
		bars, source, err := c.remoteHistoryBars(ctx, spec, false)
		if err != nil {
			return nil, "", err
		}
		c.setRedisBars(ctx, "recent:"+key, bars, c.recentRedisTTL(bars))
		return bars, source, nil
	}
	storageSpec := spec
	applyForward := market.IsUSForwardAdjusted(spec)
	if applyForward {
		storageSpec.Adjustment = market.SplitAdjusted
	}
	missing, err := c.coverage.Missing(ctx, storageSpec, version)
	if err != nil {
		return nil, "", err
	}
	fetched := false
	for _, gap := range coverage.GroupMissing(missing) {
		part, _, err := c.remoteHistoryBars(ctx, gap, false)
		if err != nil {
			return nil, "", err
		}
		if len(part) > 0 {
			if err := c.clickhouse.WriteBars(ctx, gap.Adjustment, part, uint64(time.Now().UnixMilli())); err != nil {
				return nil, "", err
			}
		}
		ttl := c.cfg.EmptyCoverageTTL
		if ttl <= 0 {
			ttl = 15 * time.Minute
		}
		if err := c.coverage.Record(ctx, gap, version, part, ttl); err != nil {
			return nil, "", err
		}
		fetched = true
	}
	bars, err := c.clickhouse.QueryBars(ctx, storageSpec)
	if err != nil {
		return nil, "", err
	}
	if applyForward {
		bars, err = c.forwardAdjustBars(ctx, spec, bars)
		if err != nil {
			return nil, "", err
		}
	}
	source := "local-clickhouse"
	if fetched {
		source = "provider+local-clickhouse"
	}
	c.setRedisBars(ctx, "recent:"+key, bars, c.recentRedisTTL(bars))
	return bars, source, nil
}

func (c *Cache) recentRedisTTL(bars []market.Bar) time.Duration {
	ttl := c.cfg.RedisTTL
	if len(bars) == 0 {
		emptyTTL := c.cfg.EmptyCoverageTTL
		if emptyTTL <= 0 {
			emptyTTL = 15 * time.Minute
		}
		if ttl <= 0 || ttl > emptyTTL {
			ttl = emptyTTL
		}
	}
	return ttl
}

func (c *Cache) archiveBars(ctx context.Context, spec market.DatasetSpec, dataVersion string) ([]market.Bar, string, error) {
	var all []market.Bar
	allRedis := true
	for start := spec.From; start.Before(spec.To); {
		next := time.Date(start.Year(), start.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		if next.After(spec.To) {
			next = spec.To
		}
		chunk := spec
		chunk.From, chunk.To = start, next
		version, err := c.semanticCacheVersion(ctx, chunk, "provider-archive:"+dataVersion)
		if err != nil {
			return nil, "", err
		}
		key, _ := chunk.Hash(market.SchemaVersion, version)
		cached, ok := c.redisBars(ctx, "archive:"+key)
		if !ok {
			var err error
			cached, _, err = c.remoteHistoryBars(ctx, chunk, true)
			if err != nil {
				return nil, "", err
			}
			ttl := c.cfg.RedisTTL
			if len(cached) == 0 && (ttl <= 0 || ttl > time.Hour) {
				ttl = time.Hour
			}
			c.setRedisBars(ctx, "archive:"+key, cached, ttl)
			allRedis = false
		}
		all = append(all, cached...)
		start = next
	}
	if allRedis {
		return all, "redis", nil
	}
	return all, "provider", nil
}

func (c *Cache) forwardAdjustBars(ctx context.Context, spec market.DatasetSpec, bars []market.Bar) ([]market.Bar, error) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, err
	}
	curves, err := c.forwardFactorCurves(ctx, spec)
	if err != nil {
		return nil, err
	}
	return market.ApplyForwardFactors(bars, curves, location)
}

func (c *Cache) semanticCacheVersion(ctx context.Context, spec market.DatasetSpec, base string) (string, error) {
	if !market.IsUSForwardAdjusted(spec) {
		return market.SemanticDataVersion(spec, base, time.Now()), nil
	}
	curves, err := c.forwardFactorCurves(ctx, spec)
	if err != nil {
		return "", err
	}
	versions := make([]string, 0, len(spec.Symbols))
	for _, symbol := range spec.Symbols {
		versions = append(versions, symbol+"="+curves[symbol].Version)
	}
	return market.SemanticDataVersion(spec, base, time.Now(), versions...), nil
}

func (c *Cache) forwardFactorCurves(ctx context.Context, spec market.DatasetSpec) (map[string]market.ForwardFactors, error) {
	curves := make(map[string]market.ForwardFactors, len(spec.Symbols))
	for _, symbol := range spec.Symbols {
		curve, err := c.forwardFactors(ctx, symbol)
		if err != nil {
			return nil, err
		}
		curves[symbol] = curve
	}
	return curves, nil
}

func (c *Cache) forwardFactors(ctx context.Context, symbol string) (market.ForwardFactors, error) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return market.ForwardFactors{}, err
	}
	now := time.Now().In(location)
	c.factorMu.Lock()
	if cached, ok := c.factorCache[symbol]; ok && now.Before(cached.expiresAt) {
		c.factorMu.Unlock()
		return cached.curve, nil
	}
	c.factorMu.Unlock()

	var curve market.ForwardFactors
	if err := c.getJSON(ctx, "/v1/market-history/adjustments/"+url.PathEscape(symbol), &curve); err != nil {
		return market.ForwardFactors{}, err
	}
	curve, err = market.NormalizeForwardFactors(curve)
	if err != nil {
		return market.ForwardFactors{}, err
	}
	if curve.Symbol != symbol || curve.Mode != market.ForwardAdjusted || curve.Version == "" {
		return market.ForwardFactors{}, fmt.Errorf("invalid forward-adjustment response for %s", symbol)
	}
	if _, err := time.Parse("2006-01-02", curve.AsOf); err != nil {
		return market.ForwardFactors{}, fmt.Errorf("invalid forward-adjustment as_of for %s: %w", symbol, err)
	}
	expiresAt := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, location)
	c.factorMu.Lock()
	c.factorCache[symbol] = cachedForwardFactors{curve: curve, expiresAt: expiresAt}
	c.factorMu.Unlock()
	return curve, nil
}

func (c *Cache) remoteHistoryBars(ctx context.Context, spec market.DatasetSpec, providerOnly bool) ([]market.Bar, string, error) {
	body, _ := json.Marshal(map[string]any{"spec": spec, "provider_only": providerOnly})
	req, err := c.request(ctx, http.MethodPost, "/v1/history/bars", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	var payload struct {
		Source string       `json:"source"`
		Bars   []market.Bar `json:"bars"`
		Error  string       `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, "", err
	}
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("go-server returned %d: %s", resp.StatusCode, payload.Error)
	}
	if payload.Bars == nil {
		payload.Bars = []market.Bar{}
	}
	return payload.Bars, payload.Source, nil
}

func (c *Cache) storageCapability(ctx context.Context) (StorageCapability, error) {
	c.mu.Lock()
	interval := c.cfg.StorageCapabilityInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if time.Since(c.capabilityAt) < interval {
		capability := c.capability
		c.mu.Unlock()
		return capability, nil
	}
	c.mu.Unlock()
	var capability StorageCapability
	if err := c.getJSON(ctx, "/v1/storage/capabilities", &capability); err != nil {
		c.mu.Lock()
		if !c.capabilityAt.IsZero() {
			capability = c.capability
			c.capabilityAt = time.Now()
			c.capabilityStale = true
			c.capabilityError = err.Error()
			c.mu.Unlock()
			return capability, nil
		}
		c.mu.Unlock()
		return capability, err
	}
	c.mu.Lock()
	c.capability, c.capabilityAt = capability, time.Now()
	c.capabilityStale = false
	c.capabilityError = ""
	c.mu.Unlock()
	return capability, nil
}

func (c *Cache) StorageStatus(ctx context.Context) map[string]any {
	capability, err := c.storageCapability(ctx)
	if err != nil {
		return map[string]any{"mode": "unknown", "error": err.Error()}
	}
	mode := "local_clickhouse"
	if capability.ClickHouse.Enabled {
		mode = "remote_clickhouse"
		if !capability.ClickHouse.Healthy {
			mode = "remote_clickhouse_degraded"
		}
	} else if c.clickhouse == nil {
		mode = "provider_only"
	}
	c.mu.Lock()
	stale, capabilityError := c.capabilityStale, c.capabilityError
	c.mu.Unlock()
	return map[string]any{"mode": mode, "local_clickhouse_enabled": c.clickhouse != nil && !capability.ClickHouse.Enabled, "remote": capability.ClickHouse, "history_revision": capability.HistoryRevision, "data_version": capability.DataVersion, "capability_stale": stale, "capability_error": capabilityError}
}

func (c *Cache) Write(ctx context.Context, event market.LiveEvent) error {
	if c.clickhouse == nil {
		return nil
	}
	capability, err := c.storageCapability(ctx)
	if err != nil || capability.ClickHouse.Enabled {
		return nil
	}
	return c.clickhouse.Write(ctx, event)
}

func (c *Cache) redisBars(ctx context.Context, key string) ([]market.Bar, bool) {
	if c.redis == nil {
		return nil, false
	}
	raw, err := c.redis.Get(ctx, "bars:v2:"+key).Bytes()
	if err != nil {
		return nil, false
	}
	var bars []market.Bar
	if json.Unmarshal(raw, &bars) != nil {
		return nil, false
	}
	if bars == nil {
		bars = []market.Bar{}
	}
	return bars, true
}

func (c *Cache) setRedisBars(ctx context.Context, key string, bars []market.Bar, ttl time.Duration) {
	if c.redis == nil || ttl <= 0 {
		return
	}
	if raw, err := json.Marshal(bars); err == nil {
		_ = c.redis.Set(ctx, "bars:v2:"+key, raw, ttl).Err()
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func deduplicateMarketBars(input []market.Bar) []market.Bar {
	if len(input) < 2 {
		return input
	}
	output := input[:1]
	for _, bar := range input[1:] {
		last := &output[len(output)-1]
		if last.Symbol == bar.Symbol && last.Timestamp.Equal(bar.Timestamp) {
			*last = bar
			continue
		}
		output = append(output, bar)
	}
	return output
}

func (c *Cache) localManifest(ctx context.Context, specHash string) (market.Manifest, bool, error) {
	var raw []byte
	var last int64
	err := c.db.QueryRowContext(ctx, `SELECT manifest_json,last_accessed_at FROM datasets WHERE spec_hash=? AND state='ready' ORDER BY last_accessed_at DESC LIMIT 1`, specHash).Scan(&raw, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return market.Manifest{}, false, nil
	}
	if err != nil {
		return market.Manifest{}, false, err
	}
	if c.cfg.ParquetTTL > 0 && time.Unix(last, 0).Add(c.cfg.ParquetTTL).Before(time.Now()) {
		return market.Manifest{}, false, nil
	}
	var m market.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, false, err
	}
	for _, p := range m.Partitions {
		if _, err := os.Stat(c.partitionPath(m.DatasetID, p.Name)); err != nil {
			return m, false, nil
		}
	}
	return m, true, nil
}

func (c *Cache) ensureRemote(ctx context.Context, key string, spec market.DatasetSpec) (market.Manifest, error) {
	c.mu.Lock()
	if f, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return market.Manifest{}, ctx.Err()
		case <-f.done:
			return f.manifest, f.err
		}
	}
	f := &flight{done: make(chan struct{})}
	c.inflight[key] = f
	c.mu.Unlock()
	f.manifest, f.err = c.download(ctx, key, spec)
	close(f.done)
	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
	return f.manifest, f.err
}

func (c *Cache) download(ctx context.Context, key string, spec market.DatasetSpec) (market.Manifest, error) {
	b, _ := json.Marshal(spec)
	req, err := c.request(ctx, http.MethodPost, "/v1/datasets", bytes.NewReader(b))
	if err != nil {
		return market.Manifest{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return market.Manifest{}, err
	}
	var st market.DatasetStatus
	err = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if err != nil {
		return market.Manifest{}, err
	}
	if resp.StatusCode/100 != 2 {
		return market.Manifest{}, fmt.Errorf("go-server returned %d: %s", resp.StatusCode, st.Error)
	}
	for st.State == "building" {
		select {
		case <-ctx.Done():
			return market.Manifest{}, ctx.Err()
		case <-time.After(time.Second):
		}
		if err := c.getJSON(ctx, "/v1/datasets/"+st.DatasetID, &st); err != nil {
			return market.Manifest{}, err
		}
	}
	if st.State != "ready" {
		return market.Manifest{}, fmt.Errorf("dataset %s: %s", st.State, st.Error)
	}
	var m market.Manifest
	if err := c.getJSON(ctx, "/v1/datasets/"+st.DatasetID+"/manifest", &m); err != nil {
		return m, err
	}
	tmpRoot := filepath.Join(c.cfg.CacheDir, "datasets", st.DatasetID+".downloading")
	_ = os.RemoveAll(tmpRoot)
	if err := os.MkdirAll(filepath.Join(tmpRoot, "files"), 0o755); err != nil {
		return m, err
	}
	for _, p := range m.Partitions {
		path := filepath.Join(tmpRoot, "files", filepath.FromSlash(p.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return m, err
		}
		if err := c.downloadFile(ctx, "/v1/datasets/"+st.DatasetID+"/files/"+p.Name, path, p.SHA256); err != nil {
			return m, err
		}
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpRoot, "manifest.json"), mb, 0o644); err != nil {
		return m, err
	}
	final := filepath.Join(c.cfg.CacheDir, "datasets", st.DatasetID)
	datasetLock, releaseGuard := c.datasetLock(st.DatasetID)
	datasetLock.Lock()
	defer releaseGuard()
	defer datasetLock.Unlock()
	_ = os.RemoveAll(final)
	if err := os.Rename(tmpRoot, final); err != nil {
		return m, err
	}
	now := time.Now().Unix()
	if _, err := c.db.ExecContext(ctx, `INSERT OR REPLACE INTO datasets(id,spec_hash,manifest_json,last_accessed_at,state) VALUES(?,?,?,?,?)`, st.DatasetID, key, mb, now, "ready"); err != nil {
		return m, err
	}
	return m, nil
}

func (c *Cache) readManifest(m market.Manifest) ([]market.Bar, error) {
	out := make([]market.Bar, 0)
	for _, p := range m.Partitions {
		rows, err := parquet.ReadFile[market.ParquetBar](c.partitionPath(m.DatasetID, p.Name))
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, market.FromParquetBar(r))
		}
	}
	return out, nil
}
func (c *Cache) partitionPath(id, name string) string {
	return filepath.Join(c.cfg.CacheDir, "datasets", id, "files", filepath.FromSlash(name))
}
func (c *Cache) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.cfg.ServerURL, "/")+path, body)
	if err == nil && c.cfg.ServerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.ServerToken)
	}
	return req, err
}
func (c *Cache) getJSON(ctx context.Context, path string, v any) error {
	req, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("go-server returned %d: %s", resp.StatusCode, b)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *Cache) ProviderUsage(ctx context.Context) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.getJSON(ctx, "/v1/providers/massive/usage", &raw)
	return raw, err
}

func (c *Cache) Universe(ctx context.Context) ([]string, error) {
	var payload struct {
		Symbols []string `json:"symbols"`
	}
	if err := c.getJSON(ctx, "/v1/market-history/universe", &payload); err != nil {
		return nil, err
	}
	return payload.Symbols, nil
}

func (c *Cache) RunMarketHistorySync(ctx context.Context) {
	if !c.cfg.MarketHistorySyncEnabled || c.clickhouse == nil {
		return
	}
	interval := c.cfg.MarketHistorySyncInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	syncOnce := func() {
		capability, err := c.storageCapability(ctx)
		if err != nil || capability.ClickHouse.Enabled {
			return
		}
		symbols, err := c.Universe(ctx)
		if err != nil {
			return
		}
		from, to := time.Now().UTC().AddDate(0, 0, -2), time.Now().UTC()
		for _, symbol := range symbols {
			_, venue, err := market.NormalizeSymbol(symbol)
			if err != nil {
				continue
			}
			adjustment := market.ForwardAdjusted
			if venue == market.VenueUS {
				adjustment = market.SplitAdjusted
			}
			spec := market.DatasetSpec{Symbols: []string{symbol}, Interval: "1m", From: from, To: to, Session: market.RegularSession, Adjustment: adjustment}
			_, _, _ = c.recentBars(ctx, spec, capability)
			if ctx.Err() != nil {
				return
			}
		}
	}
	syncOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			syncOnce()
		}
	}
}

func (c *Cache) ServerJSON(ctx context.Context, method, path string, body io.Reader) (json.RawMessage, int, error) {
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusNoContent && len(raw) == 0 {
		return nil, resp.StatusCode, nil
	}
	if !json.Valid(raw) {
		return nil, resp.StatusCode, fmt.Errorf("go-server returned invalid JSON")
	}
	return json.RawMessage(raw), resp.StatusCode, nil
}
func (c *Cache) downloadFile(ctx context.Context, urlPath, path, want string) error {
	req, err := c.request(ctx, http.MethodGet, urlPath, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, hash), resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch: got %s want %s", got, want)
	}
	return nil
}
func (c *Cache) touch(ctx context.Context, id string) error {
	_, err := c.db.ExecContext(ctx, `UPDATE datasets SET last_accessed_at=? WHERE id=?`, time.Now().Unix(), id)
	return err
}
func (c *Cache) touchBySpec(ctx context.Context, key string) error {
	_, err := c.db.ExecContext(ctx, `UPDATE datasets SET last_accessed_at=? WHERE spec_hash=?`, time.Now().Unix(), key)
	return err
}
func (c *Cache) retain(id string) { c.mu.Lock(); c.active[id]++; c.mu.Unlock() }
func (c *Cache) release(id string) {
	c.mu.Lock()
	c.active[id]--
	if c.active[id] <= 0 {
		delete(c.active, id)
	}
	c.mu.Unlock()
}

func (c *Cache) datasetLock(id string) (*sync.RWMutex, func()) {
	c.mu.Lock()
	guard := c.datasetLocks[id]
	if guard == nil {
		guard = &datasetGuard{}
		c.datasetLocks[id] = guard
	}
	guard.refs++
	c.mu.Unlock()
	return &guard.lock, func() {
		c.mu.Lock()
		guard.refs--
		if guard.refs == 0 && c.datasetLocks[id] == guard {
			delete(c.datasetLocks, id)
		}
		c.mu.Unlock()
	}
}

type CacheEntry struct {
	DatasetID    string    `json:"dataset_id"`
	SpecHash     string    `json:"spec_hash,omitempty"`
	LastAccessed time.Time `json:"last_accessed"`
	State        string    `json:"state"`
}

func (c *Cache) List(ctx context.Context) ([]CacheEntry, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id,spec_hash,last_accessed_at,state FROM datasets ORDER BY last_accessed_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CacheEntry
	for rows.Next() {
		var x CacheEntry
		var ts int64
		if err := rows.Scan(&x.DatasetID, &x.SpecHash, &ts, &x.State); err != nil {
			return nil, err
		}
		x.LastAccessed = time.Unix(ts, 0).UTC()
		out = append(out, x)
	}
	return out, rows.Err()
}
func (c *Cache) Prune(ctx context.Context, expiredOnly bool) (int, error) {
	entries, err := c.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if expiredOnly && (c.cfg.ParquetTTL <= 0 || e.LastAccessed.Add(c.cfg.ParquetTTL).After(time.Now())) {
			continue
		}
		datasetLock, releaseGuard := c.datasetLock(e.DatasetID)
		if !datasetLock.TryLock() {
			releaseGuard()
			continue
		}
		if _, err := c.db.ExecContext(ctx, `UPDATE datasets SET state='deleting' WHERE id=?`, e.DatasetID); err != nil {
			datasetLock.Unlock()
			releaseGuard()
			return n, err
		}
		path := filepath.Join(c.cfg.CacheDir, "datasets", e.DatasetID)
		if err := os.RemoveAll(path); err != nil {
			datasetLock.Unlock()
			releaseGuard()
			return n, err
		}
		if c.redis != nil {
			_ = c.redis.Del(ctx, "bars:"+e.SpecHash).Err()
		}
		if _, err := c.db.ExecContext(ctx, `DELETE FROM datasets WHERE id=?`, e.DatasetID); err != nil {
			datasetLock.Unlock()
			releaseGuard()
			return n, err
		}
		n++
		datasetLock.Unlock()
		releaseGuard()
	}
	return n, nil
}

func (c *Cache) Manifest(ctx context.Context, id string) (market.Manifest, error) {
	var raw []byte
	if err := c.db.QueryRowContext(ctx, `SELECT manifest_json FROM datasets WHERE id=? AND state='ready'`, id).Scan(&raw); err != nil {
		return market.Manifest{}, err
	}
	var m market.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	return m, nil
}

func (c *Cache) Delete(ctx context.Context, id string) error {
	datasetLock, releaseGuard := c.datasetLock(id)
	if !datasetLock.TryLock() {
		releaseGuard()
		return errors.New("dataset is in use")
	}
	defer releaseGuard()
	defer datasetLock.Unlock()
	var key string
	if err := c.db.QueryRowContext(ctx, `SELECT spec_hash FROM datasets WHERE id=?`, id).Scan(&key); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `UPDATE datasets SET state='deleting' WHERE id=?`, id); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(c.cfg.CacheDir, "datasets", id)); err != nil {
		return err
	}
	if c.redis != nil {
		_ = c.redis.Del(ctx, "bars:"+key).Err()
	}
	_, err := c.db.ExecContext(ctx, `DELETE FROM datasets WHERE id=?`, id)
	return err
}
func (c *Cache) RunCleanup(ctx context.Context) {
	var parquetC, clickhouseC <-chan time.Time
	var parquetTicker, clickhouseTicker *time.Ticker
	coverageTicker := time.NewTicker(24 * time.Hour)
	defer coverageTicker.Stop()
	if c.cfg.ParquetTTL > 0 && c.cfg.CleanupInterval > 0 {
		parquetTicker = time.NewTicker(c.cfg.CleanupInterval)
		parquetC = parquetTicker.C
		defer parquetTicker.Stop()
	}
	if c.clickhouse != nil && c.cfg.ClickHouseCleanupInterval > 0 {
		clickhouseTicker = time.NewTicker(c.cfg.ClickHouseCleanupInterval)
		clickhouseC = clickhouseTicker.C
		defer clickhouseTicker.Stop()
	}
	retention := c.clickhouseRetention()
	cleanupCoverage := func() {
		_ = c.coverage.Cleanup(ctx, time.Now().UTC().Add(-retention))
	}
	if c.clickhouse != nil {
		_, _ = c.clickhouse.CleanupBefore(ctx, time.Now().UTC().Add(-retention))
	}
	cleanupCoverage()
	for {
		select {
		case <-ctx.Done():
			return
		case <-parquetC:
			_, _ = c.Prune(ctx, true)
		case <-clickhouseC:
			_, _ = c.clickhouse.CleanupBefore(ctx, time.Now().UTC().Add(-retention))
		case <-coverageTicker.C:
			cleanupCoverage()
		}
	}
}

func (c *Cache) clickhouseRetention() time.Duration {
	if c.cfg.ClickHouseRetention > 0 {
		return c.cfg.ClickHouseRetention
	}
	return 730 * 24 * time.Hour
}
