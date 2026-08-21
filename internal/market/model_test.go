package market

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDatasetSpecNormalizeAndHash(t *testing.T) {
	spec := DatasetSpec{Symbols: []string{"nvda.us", "AAPL", "aapl"}, Interval: "1m", From: time.Date(2025, 1, 1, 0, 0, 0, 0, time.FixedZone("x", 8*3600)), To: time.Date(2025, 1, 2, 0, 0, 0, 0, time.FixedZone("x", 8*3600))}
	n, err := spec.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Symbols; len(got) != 2 || got[0] != "AAPL" || got[1] != "NVDA" {
		t.Fatalf("symbols=%v", got)
	}
	a, _ := n.Hash(SchemaVersion, "v1")
	b, _ := spec.Hash(SchemaVersion, "v1")
	if a != b {
		t.Fatal("normalized hash is not deterministic")
	}
}
func TestDecimalJSON(t *testing.T) {
	want := DecimalFromFloat(123.456789)
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Decimal
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
