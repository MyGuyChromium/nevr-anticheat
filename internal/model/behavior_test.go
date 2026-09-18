package model

import (
	"encoding/json"
	"testing"
)

func TestDetectorBehaviorSeparatesObservedMechanisms(t *testing.T) {
	want := map[string]string{
		"THROW_003": BehaviorReleaseDirection,
		"THROW_005": BehaviorShotTargeting,
		"THROW_006": BehaviorFreeFlight,
		"STATE_008": BehaviorCatchPath,
	}
	seen := map[string]bool{}
	for id, kind := range want {
		b, ok := DetectorBehavior(id)
		if !ok || b.ID != kind || seen[b.ID] || b.Label == "" || b.Observes == "" || b.DoesNotEstablish == "" {
			t.Fatalf("missing, conflated or incomplete behavior for %s: %+v", id, b)
		}
		seen[b.ID] = true
		raw, err := json.Marshal(b)
		var decoded BehaviorDescriptor
		if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded != b {
			t.Fatalf("behavior did not round trip: %s", id)
		}
		b.Label = "mutated"
		fresh, _ := DetectorBehavior(id)
		if fresh.Label == b.Label {
			t.Fatal("behavior factory leaked mutable shared state")
		}
	}
	for _, id := range []string{"", "autopocket", "STATE_001", "UNKNOWN"} {
		if got, ok := DetectorBehavior(id); ok || got != (BehaviorDescriptor{}) {
			t.Fatalf("unsupported detector inferred a behavior: %q %+v", id, got)
		}
	}
}
