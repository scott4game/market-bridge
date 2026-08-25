package access

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPersonalIndicatorsAreIsolatedAndRevisioned(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "auth.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	alice, _ := store.CreateUser(ctx, "indicator-alice", "member")
	bob, _ := store.CreateUser(ctx, "indicator-bob", "member")

	defaults, err := store.Indicators(ctx, alice.ID)
	if err != nil || len(defaults) != 2 || defaults[0].Kind != "template" {
		t.Fatalf("defaults=%+v err=%v", defaults, err)
	}
	mutation := IndicatorMutation{
		Name: "MA Test", Pane: "main", Formula: "M:MA(CLOSE,N);", Enabled: true, SortOrder: 100,
		Parameters: []IndicatorParameter{{Name: "N", Default: 5, Min: 1, Max: 500, Step: 1, Value: 5}},
	}
	created, err := store.CreateIndicator(ctx, alice.ID, mutation)
	if err != nil || created.Revision != 1 || created.Kind != "personal" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	mutation.Revision = created.Revision
	mutation.Name = "MA Updated"
	updated, err := store.UpdateIndicator(ctx, alice.ID, created.ID, mutation)
	if err != nil || updated.Revision != 2 || updated.Name != mutation.Name {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if _, err := store.UpdateIndicator(ctx, alice.ID, created.ID, mutation); !errors.Is(err, ErrIndicatorConflict) {
		t.Fatalf("stale update err=%v", err)
	}
	if _, err := store.CopyIndicator(ctx, bob.ID, created.ID, ""); !errors.Is(err, ErrIndicatorNotFound) {
		t.Fatalf("cross-account copy err=%v", err)
	}
	copy, err := store.CopyIndicator(ctx, alice.ID, created.ID, "")
	if err != nil || copy.ID == created.ID || copy.Kind != "personal" {
		t.Fatalf("copy=%+v err=%v", copy, err)
	}
	if err := store.DeleteIndicator(ctx, alice.ID, created.ID, updated.Revision); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIndicator(ctx, alice.ID, defaults[0].ID, defaults[0].Revision); !errors.Is(err, ErrIndicatorTemplate) {
		t.Fatalf("template delete err=%v", err)
	}
}

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
	if p.Name != "alice" || !p.HasScope("history:read") || p.HasScope("admin") {
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
