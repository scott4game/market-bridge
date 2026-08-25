package localclient

import (
	"context"
	"testing"

	"github.com/scott4game/market-bridge/internal/config"
)

func TestDeleteRejectsDatasetWithReadLock(t *testing.T) {
	cache, err := NewCache(config.Client{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	lock, release := cache.datasetLock("busy")
	lock.RLock()
	defer release()
	defer lock.RUnlock()
	if err := cache.Delete(context.Background(), "busy"); err == nil || err.Error() != "dataset is in use" {
		t.Fatalf("delete error=%v", err)
	}
}
