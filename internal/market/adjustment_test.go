package market

import (
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
	if version == "v1" || !IsUSForwardAdjusted(normalized) {
		t.Fatalf("version=%q normalized=%+v", version, normalized)
	}
}
