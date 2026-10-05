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
	if version == "v1" || !strings.Contains(version, "us-qfq-v4") || !IsUSForwardAdjusted(normalized) {
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
		{EffectiveDate: "2026-08-20", Factor: FactorFromFloat(0.8)},
		{EffectiveDate: "2026-08-10", Factor: FactorFromFloat(0.9)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if curve.Factors[0].EffectiveDate != "2026-08-10" || !curve.Factors[0].Factor.Equal(FactorFromFloat(0.72)) || !curve.Factors[1].Factor.Equal(FactorFromFloat(0.8)) {
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
		{Factors: []ForwardFactor{{EffectiveDate: "bad", Factor: FactorFromFloat(1)}}},
		{Factors: []ForwardFactor{{EffectiveDate: "2026-08-20", Factor: FactorFromFloat(0)}}},
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

func TestCumulativeFactorsPrecisionDuplicatesAndLegacy(t *testing.T) {
	tiny, _ := ParseAdjustmentFactor("0.0000000123456789")
	curve, err := NormalizeForwardFactors(ForwardFactors{Factors: []ForwardFactor{
		{EffectiveDate: "2026-01-02", Factor: tiny},
		{EffectiveDate: "2026-01-02", Factor: tiny},
	}})
	if err != nil || len(curve.Factors) != 1 || !curve.Factors[0].Factor.Equal(tiny) {
		t.Fatalf("%+v %v", curve, err)
	}
	bars, err := ApplyForwardFactors([]Bar{{Symbol: "A", Timestamp: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Close: DecimalFromFloat(1000000)}}, map[string]ForwardFactors{"A": curve}, time.UTC)
	if err != nil || bars[0].Close != DecimalFromFloat(.012346) {
		t.Fatalf("%+v %v", bars, err)
	}
	curve.Factors = append(curve.Factors, ForwardFactor{EffectiveDate: "2026-01-02", Factor: FactorFromFloat(.9)})
	if _, err := NormalizeForwardFactors(curve); err == nil {
		t.Fatal("conflicting factors accepted")
	}
	if _, err := NormalizeForwardFactors(ForwardFactors{Version: "massive-qfq-v3:A:old"}); err == nil {
		t.Fatal("legacy curve accepted")
	}
}
