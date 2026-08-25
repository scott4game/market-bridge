package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/scott4game/market-bridge/internal/market"
	"github.com/scott4game/market-bridge/internal/provider"
)

type Store struct {
	root     string
	provider provider.Provider
	ctx      context.Context
	timeout  time.Duration
	mu       sync.RWMutex
	tasks    map[string]market.DatasetStatus
	failedAt map[string]time.Time
	queue    chan buildJob
	active   map[string]int
}

type HistoricalBarWriter interface {
	WriteBars(context.Context, market.AdjustmentMode, []market.Bar, uint64) error
}

type buildJob struct {
	id           string
	spec         market.DatasetSpec
	userID       string
	providerName string
	dataVersion  string
	factorCurves map[string]market.ForwardFactors
}

type datasetDescription struct {
	provider.Description
	factorCurves map[string]market.ForwardFactors
}

var ErrBuildQuota = errors.New("concurrent dataset build quota exceeded")
var ErrBuildQueueFull = errors.New("dataset build queue is full")
var ErrDatasetRateQuota = errors.New("dataset creation quota exceeded")

func NewStore(root string, p provider.Provider) (*Store, error) {
	return NewStoreWithOptions(root, p, 2, 100)
}

func NewStoreWithOptions(root string, p provider.Provider, workers, queueSize int) (*Store, error) {
	return NewStoreWithBuildOptions(context.Background(), root, p, workers, queueSize, 10*time.Minute)
}

func NewStoreWithBuildOptions(ctx context.Context, root string, p provider.Provider, workers, queueSize int, timeout time.Duration) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "datasets"), 0o755); err != nil {
		return nil, err
	}
	if workers < 1 {
		workers = 1
	}
	if queueSize < 1 {
		queueSize = 1
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	s := &Store{root: root, provider: p, ctx: ctx, timeout: timeout, tasks: map[string]market.DatasetStatus{}, failedAt: map[string]time.Time{}, queue: make(chan buildJob, queueSize), active: map[string]int{}}
	entries, _ := os.ReadDir(filepath.Join(root, "datasets"))
	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".building") {
			_ = os.RemoveAll(filepath.Join(root, "datasets", entry.Name()))
		}
	}
	for range workers {
		go s.worker()
	}
	return s, nil
}

func (s *Store) Ensure(ctx context.Context, spec market.DatasetSpec) (market.DatasetStatus, error) {
	return s.EnsureFor(ctx, spec, "system", 1<<30)
}

func (s *Store) EnsureFor(ctx context.Context, spec market.DatasetSpec, userID string, maxConcurrent int) (market.DatasetStatus, error) {
	return s.EnsureForAdmission(ctx, spec, userID, maxConcurrent, nil)
}

func (s *Store) EnsureForAdmission(ctx context.Context, spec market.DatasetSpec, userID string, maxConcurrent int, admit func() bool) (market.DatasetStatus, error) {
	spec, err := spec.Normalize()
	if err != nil {
		return market.DatasetStatus{}, err
	}
	described, err := s.describeDataset(ctx, spec)
	if err != nil {
		return market.DatasetStatus{}, err
	}
	id, err := spec.Hash(market.SchemaVersion, described.DataVersion)
	if err != nil {
		return market.DatasetStatus{}, err
	}
	if _, err := s.Manifest(id); err == nil {
		return market.DatasetStatus{DatasetID: id, State: "ready"}, nil
	}
	s.mu.Lock()
	if status, ok := s.tasks[id]; ok {
		if status.State != "failed" {
			s.mu.Unlock()
			return status, nil
		}
		delete(s.tasks, id)
		delete(s.failedAt, id)
	}
	if admit != nil && !admit() {
		s.mu.Unlock()
		return market.DatasetStatus{}, ErrDatasetRateQuota
	}
	if s.active[userID] >= maxConcurrent {
		s.mu.Unlock()
		return market.DatasetStatus{}, ErrBuildQuota
	}
	status := market.DatasetStatus{DatasetID: id, State: "building"}
	s.tasks[id] = status
	s.active[userID]++
	s.mu.Unlock()
	select {
	case s.queue <- buildJob{id: id, spec: spec, userID: userID, providerName: described.Name, dataVersion: described.DataVersion, factorCurves: described.factorCurves}:
	default:
		s.mu.Lock()
		delete(s.tasks, id)
		s.active[userID]--
		if s.active[userID] <= 0 {
			delete(s.active, userID)
		}
		s.mu.Unlock()
		return market.DatasetStatus{}, ErrBuildQueueFull
	}
	return status, nil
}

func (s *Store) worker() {
	for job := range s.queue {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					s.mu.Lock()
					s.tasks[job.id] = market.DatasetStatus{DatasetID: job.id, State: "failed", Error: fmt.Sprintf("dataset worker panic: %v", recovered)}
					s.failedAt[job.id] = time.Now()
					s.mu.Unlock()
				}
				s.mu.Lock()
				s.active[job.userID]--
				if s.active[job.userID] <= 0 {
					delete(s.active, job.userID)
				}
				s.mu.Unlock()
			}()
			buildCtx, cancel := context.WithTimeout(s.ctx, s.timeout)
			defer cancel()
			s.generate(buildCtx, job)
		}()
	}
}

func (s *Store) ActiveBuilds(userID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active[userID]
}

func (s *Store) ProviderBars(ctx context.Context, spec market.DatasetSpec) ([]market.Bar, error) {
	return s.provider.Bars(ctx, spec)
}

func (s *Store) ForwardAdjustmentFactors(ctx context.Context, symbol string) (market.ForwardFactors, error) {
	return provider.ForwardAdjustmentFactors(ctx, s.provider, symbol)
}

func (s *Store) describe(ctx context.Context, spec market.DatasetSpec) (provider.Description, error) {
	described, err := s.describeDataset(ctx, spec)
	return described.Description, err
}

func (s *Store) describeDataset(ctx context.Context, spec market.DatasetSpec) (datasetDescription, error) {
	description, err := provider.Describe(s.provider, spec)
	if err != nil {
		return datasetDescription{}, err
	}
	if !market.IsUSForwardAdjusted(spec) {
		return datasetDescription{Description: description}, nil
	}
	versions := make([]string, 0, len(spec.Symbols))
	curves := make(map[string]market.ForwardFactors, len(spec.Symbols))
	for _, symbol := range spec.Symbols {
		curve, err := s.ForwardAdjustmentFactors(ctx, symbol)
		if err != nil {
			return datasetDescription{}, err
		}
		curves[symbol] = curve
		versions = append(versions, symbol+"="+curve.Version)
	}
	description.DataVersion = market.SemanticDataVersion(spec, description.DataVersion, time.Now(), versions...)
	return datasetDescription{Description: description, factorCurves: curves}, nil
}

func (s *Store) Universe(ctx context.Context) ([]string, error) {
	lister, ok := s.provider.(provider.UniverseLister)
	if !ok {
		return nil, errors.New("configured provider does not expose a security universe")
	}
	return lister.Universe(ctx)
}

func (s *Store) SyncRecentUniverse(ctx context.Context, writer HistoricalBarWriter, catalog *HistoryCatalog, dataVersion string, days int, emptyCoverageTTL time.Duration) error {
	symbols, err := s.Universe(ctx)
	if err != nil {
		return err
	}
	if days < 1 {
		days = 2
	}
	if emptyCoverageTTL <= 0 {
		emptyCoverageTTL = 15 * time.Minute
	}
	from, to := time.Now().UTC().AddDate(0, 0, -days), time.Now().UTC()
	failed := 0
	wrote := false
	var firstFailure error
	for index, symbol := range symbols {
		_, venue, err := market.NormalizeSymbol(symbol)
		if err != nil {
			failed++
			if firstFailure == nil {
				firstFailure = err
			}
			continue
		}
		adjustment := market.ForwardAdjusted
		if venue == market.VenueUS {
			adjustment = market.SplitAdjusted
		}
		spec := market.DatasetSpec{Symbols: []string{symbol}, Interval: "1m", From: from, To: to, Session: market.RegularSession, Adjustment: adjustment}
		bars, err := s.ProviderBars(ctx, spec)
		if err != nil {
			failed++
			if firstFailure == nil {
				firstFailure = fmt.Errorf("sync %s: %w", symbol, err)
			}
			continue
		}
		if len(bars) > 0 {
			if err := writer.WriteBars(ctx, adjustment, bars, uint64(time.Now().UnixMilli())+uint64(index)); err != nil {
				failed++
				if firstFailure == nil {
					firstFailure = fmt.Errorf("write %s: %w", symbol, err)
				}
				continue
			}
			wrote = true
		}
		if catalog != nil {
			if err := catalog.RecordCoverage(ctx, spec, dataVersion, bars, emptyCoverageTTL); err != nil {
				failed++
				if firstFailure == nil {
					firstFailure = fmt.Errorf("record %s coverage: %w", symbol, err)
				}
				continue
			}
		}
	}
	if catalog != nil && wrote {
		_, err = catalog.Bump(ctx)
		if err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d symbols failed; first failure: %w", failed, len(symbols), firstFailure)
	}
	return nil
}

func (s *Store) Status(id string) market.DatasetStatus {
	if _, err := s.Manifest(id); err == nil {
		return market.DatasetStatus{DatasetID: id, State: "ready"}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if x, ok := s.tasks[id]; ok {
		return x
	}
	return market.DatasetStatus{DatasetID: id, State: "not_found"}
}

func (s *Store) Manifest(id string) (market.Manifest, error) {
	var m market.Manifest
	b, err := os.ReadFile(filepath.Join(s.root, "datasets", id, "manifest.json"))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

func (s *Store) PartitionPath(id, name string) (string, error) {
	clean := filepath.Clean(name)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("invalid partition")
	}
	path := filepath.Join(s.root, "datasets", id, "files", clean)
	base := filepath.Join(s.root, "datasets", id, "files") + string(os.PathSeparator)
	if !strings.HasPrefix(path, base) {
		return "", fmt.Errorf("invalid partition")
	}
	return path, nil
}

func (s *Store) generate(ctx context.Context, job buildJob) {
	id, spec := job.id, job.spec
	fail := func(err error) {
		s.mu.Lock()
		s.tasks[id] = market.DatasetStatus{DatasetID: id, State: "failed", Error: err.Error()}
		s.failedAt[id] = time.Now()
		s.mu.Unlock()
	}
	bars, err := provider.BarsWithForwardFactors(ctx, s.provider, spec, job.factorCurves)
	if err != nil {
		fail(err)
		return
	}
	if err := ctx.Err(); err != nil {
		fail(err)
		return
	}
	groups := map[string][]market.ParquetBar{}
	for _, b := range bars {
		name := fmt.Sprintf("%s/%d.parquet", b.Symbol, b.Timestamp.Year())
		groups[name] = append(groups[name], market.ToParquetBar(b))
	}
	tmp := filepath.Join(s.root, "datasets", id+".building")
	_ = os.RemoveAll(tmp)
	defer os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "files"), 0o755); err != nil {
		fail(err)
		return
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	m := market.Manifest{DatasetID: id, Spec: spec, Provider: job.providerName, SchemaVersion: market.SchemaVersion, DataVersion: job.dataVersion, GeneratedAt: time.Now().UTC()}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			fail(err)
			return
		}
		rows := groups[name]
		path := filepath.Join(tmp, "files", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fail(err)
			return
		}
		if err := parquet.WriteFile(path, rows); err != nil {
			fail(err)
			return
		}
		b, err := os.ReadFile(path)
		if err != nil {
			fail(err)
			return
		}
		sum := sha256.Sum256(b)
		info, _ := os.Stat(path)
		from, to := time.UnixMilli(rows[0].TimestampMS).UTC(), time.UnixMilli(rows[len(rows)-1].TimestampMS).UTC()
		m.Partitions = append(m.Partitions, market.Partition{Name: name, Symbol: rows[0].Symbol, Year: from.Year(), Rows: len(rows), From: from, To: to, SHA256: hex.EncodeToString(sum[:]), SizeBytes: info.Size()})
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := ctx.Err(); err != nil {
		fail(err)
		return
	}
	if err := os.WriteFile(filepath.Join(tmp, "manifest.json"), b, 0o644); err != nil {
		fail(err)
		return
	}
	final := filepath.Join(s.root, "datasets", id)
	if err := os.Rename(tmp, final); err != nil {
		if _, statErr := os.Stat(final); statErr != nil {
			fail(err)
			return
		}
	}
	s.mu.Lock()
	delete(s.tasks, id)
	delete(s.failedAt, id)
	s.mu.Unlock()
}

func (s *Store) RunCleanup(ctx context.Context, ttl time.Duration) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupFailed()
			if ttl > 0 {
				s.prune(ttl)
			}
		}
	}
}

func (s *Store) prune(ttl time.Duration) {
	root := filepath.Join(s.root, "datasets")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-ttl)
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasSuffix(entry.Name(), ".building") {
			continue
		}
		m, err := s.Manifest(entry.Name())
		if err != nil || m.GeneratedAt.After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		s.mu.Lock()
		delete(s.tasks, entry.Name())
		s.mu.Unlock()
	}
}

func (s *Store) cleanupFailed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, failedAt := range s.failedAt {
		if failedAt.Before(time.Now().Add(-time.Hour)) {
			delete(s.failedAt, id)
			delete(s.tasks, id)
		}
	}
}
