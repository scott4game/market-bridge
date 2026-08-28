package market

import (
	"strings"
	"testing"
	"time"
)

func TestUSForwardAdjustmentIsAcceptedAndVersionedDaily(t *testing.T) {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	spec := DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1h", From: from, To: from.Add(time.Hour), Session: RegularSession, Adjustment: ForwardAdjusted}
	normalized, err := spec.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	version := SemanticDataVersion(normalized, "v1", time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC))
	if version == "v1" || !strings.Contains(version, "us-qfq-v3") || !IsUSForwardAdjusted(normalized) {
		t.Fatalf("version=%q normalized=%+v", version, normalized)
	}
}

func TestAsiaForwardAdjustmentIsVersionedDaily(t *testing.T) {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	spec, err := (DatasetSpec{Symbols: []string{"600519.SH"}, Interval: "1d", From: from, To: from.Add(24 * time.Hour), Session: RegularSession, Adjustment: ForwardAdjusted}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	first := SemanticDataVersion(spec, "v1", time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC))
	second := SemanticDataVersion(spec, "v1", time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC))
	if first == second || !strings.Contains(first, "asia-qfq-v1") {
		t.Fatalf("first=%q second=%q", first, second)
	}
}

func TestForwardAdjustmentAccumulatesEventsAndPreservesNonPriceFields(t *testing.T) {
	curve, err := AccumulateForwardFactors(ForwardFactors{Symbol: "SNDK", Mode: ForwardAdjusted, Factors: []ForwardFactor{
		{EffectiveDate: "2026-08-20", Factor: DecimalFromFloat(0.8)},
		{EffectiveDate: "2026-08-10", Factor: DecimalFromFloat(0.9)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if curve.Factors[0].EffectiveDate != "2026-08-10" || curve.Factors[0].Factor != DecimalFromFloat(0.72) || curve.Factors[1].Factor != DecimalFromFloat(0.8) {
		t.Fatalf("curve=%+v", curve.Factors)
	}
	location, _ := time.LoadLocation("America/New_York")
	turnover := DecimalFromFloat(1234)
	makeBar := func(date string) Bar {
		ts, _ := time.ParseInLocation("2006-01-02", date, location)
		price := DecimalFromFloat(100)
		return Bar{Symbol: "SNDK", Timestamp: ts, Open: price, High: price, Low: price, Close: price, Volume: 77, Turnover: &turnover}
	}
	bars, err := ApplyForwardFactors([]Bar{makeBar("2026-08-09"), makeBar("2026-08-10"), makeBar("2026-08-20")}, map[string]ForwardFactors{"SNDK": curve}, location)
	if err != nil {
		t.Fatal(err)
	}
	want := []Decimal{DecimalFromFloat(72), DecimalFromFloat(80), DecimalFromFloat(100)}
	for index := range bars {
		if bars[index].Open != want[index] || bars[index].Volume != 77 || bars[index].Turnover == nil || *bars[index].Turnover != turnover {
			t.Fatalf("bar %d=%+v", index, bars[index])
		}
	}
}

func TestForwardAdjustmentRejectsInvalidFactors(t *testing.T) {
	for _, curve := range []ForwardFactors{
		{Factors: []ForwardFactor{{EffectiveDate: "bad", Factor: DecimalFromFloat(1)}}},
		{Factors: []ForwardFactor{{EffectiveDate: "2026-08-20", Factor: 0}}},
	} {
		if _, err := AccumulateForwardFactors(curve); err == nil || !strings.Contains(err.Error(), "invalid adjustment") {
			t.Fatalf("curve=%+v err=%v", curve, err)
		}
	}
}

func TestSchemaV2InvalidatesV1DatasetIdentity(t *testing.T) {
	if SchemaVersion != "2" {
		t.Fatalf("schema version=%s", SchemaVersion)
	}
	now := time.Now()
	spec := DatasetSpec{Symbols: []string{"SNDK"}, Interval: "1d", From: now.Add(-time.Hour), To: now, Session: RegularSession, Adjustment: ForwardAdjusted}
	v1, err := spec.Hash("1", "provider")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := spec.Hash(SchemaVersion, "provider")
	if err != nil {
		t.Fatal(err)
	}
	if v1 == v2 {
		t.Fatal("v1 cache identity still matches schema v2")
	}
}
