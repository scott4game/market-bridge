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
		if indicator.Kind != "template" || indicator.TemplateKey != wantKeys[index] || !indicator.Enabled {
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
