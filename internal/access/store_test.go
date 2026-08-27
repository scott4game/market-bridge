package access

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAPIKeyLifecycleAndQuotas(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "auth.db"), "legacy-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	legacy, err := store.Authenticate(ctx, "legacy-secret")
	if err != nil || legacy.Role != "admin" {
		t.Fatalf("legacy=%+v err=%v", legacy, err)
	}
	user, err := store.CreateUser(ctx, "alice", "member")
	if err != nil {
		t.Fatal(err)
	}
	token, key, err := store.CreateKey(ctx, user.Name, "laptop", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || key.Prefix == "" {
		t.Fatal("key was not generated")
	}
	p, err := store.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "alice" || !p.HasScope("history:read") || !p.HasScope("news:read") || p.HasScope("admin") {
		t.Fatalf("principal=%+v", p)
	}

	want := Quotas{RequestsPerMinute: 7, DatasetsPerMinute: 3, ConcurrentBuilds: 1, LiveConnections: 2, LiveSymbols: 4}
	if err := store.SetQuotas(ctx, "alice", want); err != nil {
		t.Fatal(err)
	}
	p, err = store.Authenticate(ctx, token)
	if err != nil || p.Quotas != want {
		t.Fatalf("quotas=%+v err=%v", p.Quotas, err)
	}
	if err := store.SetWatchlist(ctx, user.ID, []string{"AAPL", "NVDA"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Watchlist(ctx, user.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("watchlist=%v err=%v", got, err)
	}

	if err := store.RevokeKey(ctx, key.Prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, token); err == nil {
		t.Fatal("revoked key authenticated")
	}
}

func TestOpenAddsNewsScopeToExistingKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "auth.db")
	store, err := Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.CreateUser(ctx, "legacy-member", "member")
	token, _, err := store.CreateKey(ctx, "legacy-member", "old-key", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE api_keys SET scopes='history:read,live:read,profile:read'`); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p, err := store.Authenticate(ctx, token)
	if err != nil || !p.HasScope("news:read") {
		t.Fatalf("principal=%+v err=%v", p, err)
	}
}

func TestMemberDefaultAllowsLargeLiveWatchlist(t *testing.T) {
	if got := DefaultQuotas("member").LiveSymbols; got != 200 {
		t.Fatalf("member live symbol quota=%d, want 200", got)
	}
}

func TestDisabledAndExpiredKeysAreRejected(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "auth.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _ = store.CreateUser(ctx, "bob", "member")
	token, key, err := store.CreateKey(ctx, "bob", "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserEnabled(ctx, "bob", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, token); err == nil {
		t.Fatal("disabled user authenticated")
	}
	if err := store.SetUserEnabled(ctx, "bob", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE api_keys SET expires_at=? WHERE prefix=?`, time.Now().Add(-time.Hour).Unix(), key.Prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, token); err == nil {
		t.Fatal("expired key authenticated")
	}
}

func TestLimiterTracksRequestDatasetAndLiveQuotas(t *testing.T) {
	l := NewLimiter()
	p := Principal{UserID: "u", Quotas: Quotas{RequestsPerMinute: 1, DatasetsPerMinute: 1, LiveConnections: 1, LiveSymbols: 2}}
	if !l.AllowRequest(p) || l.AllowRequest(p) {
		t.Fatal("request quota mismatch")
	}
	if !l.AllowDataset(p) || l.AllowDataset(p) {
		t.Fatal("dataset quota mismatch")
	}
	if !l.AcquireLive(p, 2) || l.AcquireLive(p, 1) {
		t.Fatal("live quota mismatch")
	}
	l.ReleaseLive(p.UserID, 2)
	if !l.AcquireLive(p, 1) {
		t.Fatal("live quota was not released")
	}
}

func TestLimiterDoesNotCreateOrRetainIdleUsers(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	l.Snapshot("unknown")
	l.ReleaseLive("unknown", 1)
	if len(l.users) != 0 {
		t.Fatalf("read paths created counters: %d", len(l.users))
	}
	p := Principal{UserID: "u", Quotas: Quotas{RequestsPerMinute: 2}}
	if !l.AllowRequest(p) || len(l.users) != 1 {
		t.Fatal("request counter was not created")
	}
	now = now.Add(time.Minute)
	l.Snapshot("unknown")
	if len(l.users) != 0 {
		t.Fatalf("expired counter was retained: %d", len(l.users))
	}
}

func TestLimiterSweepsAtMostOncePerMinuteAndRetainsLiveUsers(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	p := Principal{UserID: "live", Quotas: Quotas{RequestsPerMinute: 2, LiveConnections: 1, LiveSymbols: 1}}
	if !l.AcquireLive(p, 1) {
		t.Fatal("live connection was rejected")
	}
	firstSweep := l.lastSweepMinute
	if !l.AllowRequest(p) || !l.lastSweepMinute.Equal(firstSweep) {
		t.Fatal("limiter swept more than once in the same minute")
	}
	now = now.Add(time.Minute)
	l.Snapshot("unknown")
	if _, ok := l.users[p.UserID]; !ok {
		t.Fatal("live user was removed during minute sweep")
	}
	l.ReleaseLive(p.UserID, 1)
	now = now.Add(time.Minute)
	l.Snapshot("unknown")
	if _, ok := l.users[p.UserID]; ok {
		t.Fatal("released live user was retained after the next sweep")
	}
}
