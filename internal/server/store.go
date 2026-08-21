package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parquet-go/parquet-go"
	"massive-go/internal/market"
	"massive-go/internal/provider"
)

type Store struct {
	root     string
	provider provider.Provider
	mu       sync.RWMutex
	tasks    map[string]market.DatasetStatus
}

func NewStore(root string, p provider.Provider) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "datasets"), 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root, provider: p, tasks: map[string]market.DatasetStatus{}}, nil
}

func (s *Store) Ensure(ctx context.Context, spec market.DatasetSpec) (market.DatasetStatus, error) {
	spec, err := spec.Normalize()
	if err != nil {
		return market.DatasetStatus{}, err
	}
	id, err := spec.Hash(market.SchemaVersion, s.provider.DataVersion())
	if err != nil {
		return market.DatasetStatus{}, err
	}
	if _, err := s.Manifest(id); err == nil {
		return market.DatasetStatus{DatasetID: id, State: "ready"}, nil
	}
	s.mu.Lock()
	if status, ok := s.tasks[id]; ok {
		s.mu.Unlock()
		return status, nil
	}
	status := market.DatasetStatus{DatasetID: id, State: "building"}
	s.tasks[id] = status
	s.mu.Unlock()
	go s.generate(context.WithoutCancel(ctx), id, spec)
	return status, nil
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

func (s *Store) generate(ctx context.Context, id string, spec market.DatasetSpec) {
	fail := func(err error) {
		s.mu.Lock()
		s.tasks[id] = market.DatasetStatus{DatasetID: id, State: "failed", Error: err.Error()}
		s.mu.Unlock()
	}
	bars, err := s.provider.Bars(ctx, spec)
	if err != nil {
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
	if err := os.MkdirAll(filepath.Join(tmp, "files"), 0o755); err != nil {
		fail(err)
		return
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	m := market.Manifest{DatasetID: id, Spec: spec, Provider: s.provider.Name(), SchemaVersion: market.SchemaVersion, DataVersion: s.provider.DataVersion(), GeneratedAt: time.Now().UTC()}
	for _, name := range names {
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
	s.tasks[id] = market.DatasetStatus{DatasetID: id, State: "ready"}
	s.mu.Unlock()
}

func (s *Store) RunCleanup(ctx context.Context, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.prune(ttl)
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
