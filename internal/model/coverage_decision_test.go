package model

import (
	"encoding/json"
	"testing"
)

func TestCoverageDecisionTraceAdditiveJSON(t *testing.T) {
	var legacy PlayerCoverage
	if err := json.Unmarshal([]byte(`{"version":1,"status":"limited","detectors":[{"detector_id":"THROW_001","enabled":true,"candidate_frames":5}]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Detectors[0].DecisionTrace != nil {
		t.Fatal("legacy analysis invented a branch history")
	}
	legacy.Detectors[0].DecisionTrace = &DetectorDecisionTrace{Version: 1, InternalBranches: true, Reasons: []DetectorDecisionReason{{Code: "release_above_cap", Description: "Observed release speed exceeded the configured effective cap", Count: 2, FirstFrame: 10, LastFrame: 30}}}
	b, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var restored PlayerCoverage
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	trace := restored.Detectors[0].DecisionTrace
	if trace == nil || trace.Version != 1 || !trace.InternalBranches || trace.Reasons[0].Count != 2 || trace.Reasons[0].FirstFrame != 10 || trace.Reasons[0].LastFrame != 30 {
		t.Fatalf("lost persisted diagnostic fields: %s", b)
	}
}
