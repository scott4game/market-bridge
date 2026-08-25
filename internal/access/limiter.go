package access

import (
	"sync"
	"time"
)

type counters struct {
	window      time.Time
	requests    int
	datasets    int
	connections int
	symbols     int
}

type Limiter struct {
	mu    sync.Mutex
	users map[string]*counters
	now   func() time.Time
}

func NewLimiter() *Limiter { return &Limiter{users: map[string]*counters{}, now: time.Now} }

func (l *Limiter) counter(userID string, create bool) *counters {
	now := l.now().Truncate(time.Minute)
	for id, item := range l.users {
		if item.window.Before(now) && item.connections == 0 && item.symbols == 0 {
			delete(l.users, id)
		}
	}
	c := l.users[userID]
	if c == nil && create {
		c = &counters{window: now}
		l.users[userID] = c
	}
	if c != nil && !c.window.Equal(now) {
		c.window, c.requests, c.datasets = now, 0, 0
	}
	return c
}

func (l *Limiter) AllowRequest(p Principal) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.counter(p.UserID, true)
	if c.requests >= p.Quotas.RequestsPerMinute {
		return false
	}
	c.requests++
	return true
}

func (l *Limiter) AllowDataset(p Principal) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.counter(p.UserID, true)
	if c.datasets >= p.Quotas.DatasetsPerMinute {
		return false
	}
	c.datasets++
	return true
}

func (l *Limiter) AcquireLive(p Principal, symbols int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.counter(p.UserID, true)
	if c.connections >= p.Quotas.LiveConnections || c.symbols+symbols > p.Quotas.LiveSymbols {
		return false
	}
	c.connections++
	c.symbols += symbols
	return true
}

func (l *Limiter) ReleaseLive(userID string, symbols int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.counter(userID, false)
	if c == nil {
		return
	}
	if c.connections > 0 {
		c.connections--
	}
	c.symbols -= symbols
	if c.symbols < 0 {
		c.symbols = 0
	}
}

func (l *Limiter) Snapshot(userID string) (requests, datasets, connections, symbols int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.counter(userID, false)
	if c == nil {
		return 0, 0, 0, 0
	}
	return c.requests, c.datasets, c.connections, c.symbols
}
