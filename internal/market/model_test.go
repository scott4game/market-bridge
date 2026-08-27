package market

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
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

func TestParquetBarReadsLegacySchemaWithoutDecimalVolume(t *testing.T) {
	type legacyParquetBar struct {
		Symbol      string `parquet:"symbol"`
		TimestampMS int64  `parquet:"timestamp_ms,timestamp(millisecond:utc)"`
		Open        int64  `parquet:"open_micros"`
		High        int64  `parquet:"high_micros"`
		Low         int64  `parquet:"low_micros"`
		Close       int64  `parquet:"close_micros"`
		Volume      int64  `parquet:"volume"`
		Turnover    int64  `parquet:"turnover_micros"`
		HasTurnover bool   `parquet:"has_turnover"`
		Session     string `parquet:"session"`
		Source      string `parquet:"source"`
		Completed   bool   `parquet:"completed"`
	}
	path := filepath.Join(t.TempDir(), "legacy.parquet")
	if err := parquet.WriteFile(path, []legacyParquetBar{{Symbol: "AAPL", TimestampMS: 1, Volume: 10}}); err != nil {
		t.Fatal(err)
	}
	rows, err := parquet.ReadFile[ParquetBar](path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Volume != 10 || rows[0].VolumeDecimal != "" {
		t.Fatalf("unexpected rows: %+v", rows)
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

func TestDecimalNegativeRoundingAndRange(t *testing.T) {
	if got := DecimalFromFloat(-1.5000005); got != -1_500_001 {
		t.Fatalf("negative rounding=%d", got)
	}
	if got, err := DecimalFromString("-1.5000005"); err != nil || got != -1_500_001 {
		t.Fatalf("string negative rounding=%d err=%v", got, err)
	}
	if _, err := DecimalFromString("9223372036854.775808"); err == nil {
		t.Fatal("expected Decimal64 overflow")
	}
	if got := DecimalFromFloat(float64(math.MaxInt32)); got <= 0 {
		t.Fatalf("unexpected in-range conversion %d", got)
	}
}

func TestMultiMarketNormalizationDefaults(t *testing.T) {
	base := DatasetSpec{Interval: "1m", From: time.Now().Add(-time.Hour), To: time.Now()}
	tests := []struct {
		symbol     string
		wantSymbol string
		session    Session
		adjustment AdjustmentMode
	}{
		{"AAPL.US", "AAPL", RegularSession, SplitAdjusted},
		{"700.hk", "700.HK", RegularSession, ForwardAdjusted},
		{"600519.sh", "600519.SH", RegularSession, ForwardAdjusted},
		{"000001.sz", "000001.SZ", RegularSession, ForwardAdjusted},
		{"btcusdt.binance", "BTCUSDT.BINANCE", ContinuousSession, Raw},
		{"i:vix", "I:VIX", RegularSession, Raw},
		{"f:mnqz6", "F:MNQZ6", ContinuousSession, Raw},
		{"BRK.B", "BRK.B", RegularSession, SplitAdjusted},
		{"BF.B.US", "BF.B", RegularSession, SplitAdjusted},
	}
	for _, test := range tests {
		spec := base
		spec.Symbols = []string{test.symbol}
		spec.Adjustment = AutoAdjusted
		normalized, err := spec.Normalize()
		if err != nil {
			t.Fatalf("symbol=%s err=%v", test.symbol, err)
		}
		if normalized.Symbols[0] != test.wantSymbol || normalized.Session != test.session || normalized.Adjustment != test.adjustment {
			t.Fatalf("symbol=%s normalized=%+v", test.symbol, normalized)
		}
	}
}

func TestIndicesAndFuturesRejectAdjustedData(t *testing.T) {
	base := DatasetSpec{Interval: "1m", From: time.Now().Add(-time.Hour), To: time.Now(), Adjustment: SplitAdjusted}
	for _, symbol := range []string{"I:VIX", "F:MNQZ6"} {
		spec := base
		spec.Symbols = []string{symbol}
		if _, err := spec.Normalize(); err == nil || !strings.Contains(err.Error(), "only support raw") {
			t.Fatalf("symbol=%s err=%v", symbol, err)
		}
	}
}

func TestMultiMarketNormalizationRejectsAmbiguity(t *testing.T) {
	base := DatasetSpec{Interval: "1m", From: time.Now().Add(-time.Hour), To: time.Now()}
	for _, symbols := range [][]string{{"000001"}, {"AAPL", "BTCUSDT.BINANCE"}} {
		spec := base
		spec.Symbols = symbols
		_, err := spec.Normalize()
		if len(symbols) == 1 {
			// Bare symbols remain intentionally compatible with US tickers.
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatalf("symbols=%v should fail", symbols)
		}
	}
}
