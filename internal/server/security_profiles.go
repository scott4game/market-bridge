package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/provider"
)

type SecurityProfileRecord struct {
	provider.SecurityProfile
	FetchedAt time.Time `json:"fetched_at"`
	Stale     bool      `json:"stale"`
}

type SecurityProfileError struct {
	Symbol string `json:"symbol"`
	Error  string `json:"error"`
}

type SecurityProfileResponse struct {
	Source    string                  `json:"source"`
	UpdatedAt time.Time               `json:"updated_at"`
	Complete  bool                    `json:"complete"`
	Profiles  []SecurityProfileRecord `json:"profiles"`
	Errors    []SecurityProfileError  `json:"errors,omitempty"`
}

type SecurityProfileCatalog struct {
	db       *sql.DB
	store    *Store
	ttl      time.Duration
	maxStale time.Duration
	workers  int
	mu       sync.Mutex
}

func OpenSecurityProfileCatalog(path string, store *Store, ttl, maxStale time.Duration, workers int) (*SecurityProfileCatalog, error) {
	if store == nil {
		return nil, errors.New("security profile store is required")
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if maxStale < ttl {
		maxStale = 30 * 24 * time.Hour
	}
	if workers < 1 {
		workers = 16
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS security_profiles (
		symbol TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		cik TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT '',
		active INTEGER NOT NULL,
		locale TEXT NOT NULL DEFAULT '',
		market TEXT NOT NULL DEFAULT '',
		primary_exchange TEXT NOT NULL DEFAULT '',
		market_cap REAL NOT NULL DEFAULT 0,
		sic_code TEXT NOT NULL DEFAULT '',
		sic_description TEXT NOT NULL DEFAULT '',
		provider TEXT NOT NULL DEFAULT '',
		fetched_at INTEGER NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return &SecurityProfileCatalog{db: db, store: store, ttl: ttl, maxStale: maxStale, workers: workers}, nil
}

func (c *SecurityProfileCatalog) Close() error { return c.db.Close() }

func (c *SecurityProfileCatalog) Ensure(ctx context.Context) (SecurityProfileResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	securities, err := c.store.SecurityProfileUniverse(ctx)
	if err != nil {
		return SecurityProfileResponse{}, err
	}
	symbols := activeUSSymbols(securities)
	cached, err := c.load(ctx, symbols)
	if err != nil {
		return SecurityProfileResponse{}, err
	}
	now := time.Now().UTC()
	refresh := make([]string, 0)
	for _, symbol := range symbols {
		if record, ok := cached[symbol]; !ok || now.Sub(record.FetchedAt) >= c.ttl {
			refresh = append(refresh, symbol)
		}
	}

	fetched, failures := c.fetch(ctx, refresh, now)
	for symbol, record := range fetched {
		cached[symbol] = record
	}
	profiles := make([]SecurityProfileRecord, 0, len(symbols))
	for _, symbol := range symbols {
		record, ok := cached[symbol]
		if !ok || now.Sub(record.FetchedAt) > c.maxStale {
			if _, failed := failures[symbol]; !failed {
				failures[symbol] = "security profile is unavailable"
			}
			continue
		}
		record.Stale = now.Sub(record.FetchedAt) >= c.ttl
		profiles = append(profiles, record)
	}
	errorsOut := make([]SecurityProfileError, 0, len(failures))
	for symbol, message := range failures {
		errorsOut = append(errorsOut, SecurityProfileError{Symbol: symbol, Error: message})
	}
	sort.Slice(errorsOut, func(i, j int) bool { return errorsOut[i].Symbol < errorsOut[j].Symbol })
	source := "cache"
	if len(fetched) > 0 && len(cached)-len(fetched) > 0 {
		source = "cache+massive"
	} else if len(fetched) > 0 {
		source = "massive"
	}
	return SecurityProfileResponse{
		Source: source, UpdatedAt: now, Complete: len(errorsOut) == 0 && len(profiles) == len(symbols), Profiles: profiles, Errors: errorsOut,
	}, nil
}

func activeUSSymbols(securities []provider.Security) []string {
	seen := make(map[string]struct{}, len(securities))
	var symbols []string
	for _, security := range securities {
		symbol, venue, err := market.NormalizeSymbol(security.Symbol)
		if err != nil || venue != market.VenueUS {
			continue
		}
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return symbols
}

func (c *SecurityProfileCatalog) fetch(ctx context.Context, symbols []string, now time.Time) (map[string]SecurityProfileRecord, map[string]string) {
	profiles := make(map[string]SecurityProfileRecord, len(symbols))
	failures := make(map[string]string)
	if len(symbols) == 0 {
		return profiles, failures
	}
	type result struct {
		symbol  string
		profile provider.SecurityProfile
		err     error
	}
	jobs := make(chan string)
	results := make(chan result)
	workerCount := min(c.workers, len(symbols))
	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for symbol := range jobs {
				profile, err := c.store.SecurityProfile(ctx, symbol)
				results <- result{symbol: symbol, profile: profile, err: err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, symbol := range symbols {
			select {
			case jobs <- symbol:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	for item := range results {
		if item.err != nil {
			failures[item.symbol] = item.err.Error()
			continue
		}
		profile := item.profile
		profile.Symbol = strings.ToUpper(strings.TrimSpace(profile.Symbol))
		if profile.Symbol != item.symbol || !profile.Active || !strings.EqualFold(profile.Type, "CS") || !strings.EqualFold(profile.Locale, "us") || !strings.EqualFold(profile.Market, "stocks") {
			failures[item.symbol] = "provider returned a non-active US common stock profile"
			continue
		}
		record := SecurityProfileRecord{SecurityProfile: profile, FetchedAt: now}
		if err := c.upsert(ctx, record); err != nil {
			failures[item.symbol] = err.Error()
			continue
		}
		profiles[item.symbol] = record
	}
	if err := ctx.Err(); err != nil {
		for _, symbol := range symbols {
			if _, ok := profiles[symbol]; !ok {
				if _, failed := failures[symbol]; !failed {
					failures[symbol] = err.Error()
				}
			}
		}
	}
	return profiles, failures
}

func (c *SecurityProfileCatalog) load(ctx context.Context, symbols []string) (map[string]SecurityProfileRecord, error) {
	wanted := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		wanted[symbol] = struct{}{}
	}
	rows, err := c.db.QueryContext(ctx, `SELECT symbol,name,cik,type,active,locale,market,primary_exchange,market_cap,sic_code,sic_description,provider,fetched_at FROM security_profiles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	output := make(map[string]SecurityProfileRecord, len(symbols))
	for rows.Next() {
		var record SecurityProfileRecord
		var active int
		var fetchedAt int64
		if err := rows.Scan(&record.Symbol, &record.Name, &record.CIK, &record.Type, &active, &record.Locale, &record.Market, &record.PrimaryExchange, &record.MarketCap, &record.SICCode, &record.SICDescription, &record.Provider, &fetchedAt); err != nil {
			return nil, err
		}
		if _, ok := wanted[record.Symbol]; !ok {
			continue
		}
		record.Active = active != 0
		record.FetchedAt = time.Unix(fetchedAt, 0).UTC()
		output[record.Symbol] = record
	}
	return output, rows.Err()
}

func (c *SecurityProfileCatalog) upsert(ctx context.Context, record SecurityProfileRecord) error {
	_, err := c.db.ExecContext(ctx, `INSERT INTO security_profiles(symbol,name,cik,type,active,locale,market,primary_exchange,market_cap,sic_code,sic_description,provider,fetched_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(symbol) DO UPDATE SET name=excluded.name,cik=excluded.cik,type=excluded.type,active=excluded.active,locale=excluded.locale,market=excluded.market,primary_exchange=excluded.primary_exchange,market_cap=excluded.market_cap,sic_code=excluded.sic_code,sic_description=excluded.sic_description,provider=excluded.provider,fetched_at=excluded.fetched_at`,
		record.Symbol, record.Name, record.CIK, record.Type, record.Active, record.Locale, record.Market, record.PrimaryExchange, record.MarketCap, record.SICCode, record.SICDescription, record.Provider, record.FetchedAt.Unix())
	if err != nil {
		return fmt.Errorf("cache security profile %s: %w", record.Symbol, err)
	}
	return nil
}
