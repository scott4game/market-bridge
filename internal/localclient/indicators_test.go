package localclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/scott4game/market-bridge/internal/config"
)

func TestLocalIndicatorDefaultsAndPersistence(t *testing.T) {
	root := t.TempDir()
	cache, err := NewCache(config.Client{CacheDir: root, RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := cache.LocalIndicators(context.Background())
	if err != nil || len(defaults) != 6 {
		t.Fatalf("defaults=%+v err=%v", defaults, err)
	}
	wantKeys := []string{"ma-v1", "ema-v1", "boll-v1", "vol-v1", "rsi-v1", "kdj-v1"}
	for index, indicator := range defaults {
		wantEnabled := indicator.TemplateKey == "vol-v1"
		if indicator.Kind != "template" || indicator.TemplateKey != wantKeys[index] || indicator.Enabled != wantEnabled {
			t.Fatalf("default[%d]=%+v", index, indicator)
		}
	}
	created, err := cache.CreateLocalIndicator(context.Background(), localIndicatorMutation{
		Name: "Private Formula", Pane: "main", Formula: "M:MA(CLOSE,N);", Enabled: true, SortOrder: 100,
		Parameters: []localIndicatorParameter{{Name: "N", Default: 5, Min: 1, Max: 500, Step: 1, Value: 5}},
	})
	if err != nil || created.Kind != "personal" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewCache(config.Client{CacheDir: root, RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	indicators, err := reopened.LocalIndicators(context.Background())
	if err != nil || len(indicators) != 7 || indicators[6].Name != "Private Formula" || indicators[6].Formula != "M:MA(CLOSE,N);" {
		t.Fatalf("reopened=%+v err=%v", indicators, err)
	}
}

func TestLocalIndicatorTemplateUpgradePreservesExistingEnabledState(t *testing.T) {
	root := t.TempDir()
	cache, err := NewCache(config.Client{CacheDir: root, RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.LocalIndicators(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.db.Exec(`UPDATE local_indicators SET enabled=1 WHERE template_key='ma-v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.db.Exec(`UPDATE local_indicators SET enabled=0 WHERE template_key='vol-v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.db.Exec(`UPDATE local_indicator_state SET version=2 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewCache(config.Client{CacheDir: root, RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	items, err := reopened.LocalIndicators(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.TemplateKey == "ma-v1" && !item.Enabled {
			t.Fatal("template migration overwrote the existing enabled state")
		}
		if item.TemplateKey == "vol-v1" && !item.Enabled {
			t.Fatal("template migration did not enable the default volume pane")
		}
	}
}

func TestResetLocalIndicatorDisplayIsIdempotent(t *testing.T) {
	cache, err := NewCache(config.Client{CacheDir: t.TempDir(), RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	created, err := cache.CreateLocalIndicator(context.Background(), localIndicatorMutation{
		Name: "Enabled Formula", Pane: "main", Formula: "M:MA(CLOSE,N);", Enabled: true, SortOrder: 100,
		Parameters: []localIndicatorParameter{{Name: "N", Default: 5, Min: 1, Max: 500, Step: 1, Value: 5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := cache.ResetLocalIndicatorDisplay(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var reset localIndicator
	for _, item := range first {
		if item.ID == created.ID {
			reset = item
		}
		if item.Enabled != (item.TemplateKey == "vol-v1") {
			t.Fatalf("indicator does not match default display after reset: %+v", item)
		}
	}
	if reset.ID == "" || reset.Revision != created.Revision+1 {
		t.Fatalf("reset indicator=%+v created=%+v", reset, created)
	}
	second, err := cache.ResetLocalIndicatorDisplay(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range second {
		if item.ID == created.ID && item.Revision != reset.Revision {
			t.Fatalf("idempotent reset changed revision: first=%d second=%d", reset.Revision, item.Revision)
		}
	}
}

func TestPrivateIndicatorSeedIsLocalAndDoesNotOverwriteEdits(t *testing.T) {
	root := t.TempDir()
	document := `{"indicators":[{"name":"Local Only","pane":"sub","formula":"X:EMA(CLOSE,N);","parameters":[{"name":"N","default":9,"min":1,"max":500,"step":1,"value":9}],"enabled":true,"sort_order":100}]}`
	if err := os.WriteFile(filepath.Join(root, "private-indicators.json"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	cache, err := NewCache(config.Client{CacheDir: root, RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	items, err := cache.LocalIndicators(context.Background())
	if err != nil || len(items) != 7 || items[6].Name != "Local Only" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	private := items[6]
	private.Formula = "X:EMA(CLOSE,20);"
	updated, err := cache.UpdateLocalIndicator(context.Background(), private.ID, localIndicatorMutation{
		Name: private.Name, Pane: private.Pane, Formula: private.Formula, Parameters: private.Parameters, Enabled: private.Enabled, SortOrder: private.SortOrder, Revision: private.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	cache.Close()

	reopened, err := NewCache(config.Client{CacheDir: root, RedisEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.localIndicator(context.Background(), updated.ID)
	if err != nil || got.Formula != "X:EMA(CLOSE,20);" {
		t.Fatalf("private edit was overwritten: %+v err=%v", got, err)
	}
}
