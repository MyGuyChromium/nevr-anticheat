package tests

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
)

// Fix pass 2, workstream pipeline-model: the deduplicator keeps the
// strongest emission whole (L0).

func throwEmission(frame int, severity, confidence float64, autoEnforce bool, weight float64) model.DetectionEvent {
	return model.DetectionEvent{
		PlayerID: "p1", DetectorID: "THROW_001",
		Severity: severity, Confidence: confidence, AutoEnforce: autoEnforce, EnforcementWeight: weight,
		FrameIndex: frame, FrameRangeStart: frame - 2, FrameRangeEnd: frame + 2,
		Evidence:  model.ThrowEvidence{ReleaseSpeed: 20 + severity*10},
		CausalKey: model.CausalKey{PlayerID: "p1", AnomalyType: "THROW_001", FrameStart: frame - 2, FrameEnd: frame + 2},
	}
}

// TestPipelineModel2_DedupMergeKeepsWinningEmissionWhole (L0): when two
// emissions of the same player+anomaly merge, the higher-severity one wins
// with ALL of its scoring fields (confidence, AutoEnforce,
// EnforcementWeight, evidence). Before the fix confidence was an
// independent max and AutoEnforce/EnforcementWeight were frozen from the
// first emission, so the incident was a pairing no emission produced.
func TestPipelineModel2_DedupMergeKeepsWinningEmissionWhole(t *testing.T) {
	d := pipeline.NewDeduplicator(20)
	first := throwEmission(100, 0.4, 0.9, false, 0.3)
	second := throwEmission(115, 0.9, 0.6, true, 0.8)
	if out := d.Deduplicate([]model.DetectionEvent{first}, 100); len(out) != 0 {
		t.Fatalf("first emission closed early: %+v", out)
	}
	if out := d.Deduplicate([]model.DetectionEvent{second}, 115); len(out) != 0 {
		t.Fatalf("second emission did not merge: %+v", out)
	}
	out := d.Flush()
	if len(out) != 1 {
		t.Fatalf("want one merged incident, got %d", len(out))
	}
	inc := out[0]
	if inc.Severity != 0.9 || inc.Confidence != 0.6 || !inc.AutoEnforce || inc.EnforcementWeight != 0.8 {
		t.Errorf("incident sev=%.2f conf=%.2f auto=%v weight=%.2f; want the frame-115 emission whole (0.9/0.6/true/0.8)",
			inc.Severity, inc.Confidence, inc.AutoEnforce, inc.EnforcementWeight)
	}
	if ev, ok := inc.Evidence.(model.ThrowEvidence); !ok || ev.ReleaseSpeed != 29 {
		t.Errorf("evidence %+v, want the winning throw's", inc.Evidence)
	}
	if inc.FrameIndex != 115 || inc.FrameRangeStart != 98 || inc.FrameRangeEnd != 117 || inc.MergedCount != 2 {
		t.Errorf("frame %d range [%d,%d] merged %d", inc.FrameIndex, inc.FrameRangeStart, inc.FrameRangeEnd, inc.MergedCount)
	}

	// A lower-severity late emission never overrides any field of the
	// winner (its 0.99 confidence does not leak in); at equal severity the
	// higher confidence wins whole (a sliding window whose severity
	// saturated while its confidence kept growing); an exact tie keeps the
	// earlier emission.
	d.Reset()
	d.Deduplicate([]model.DetectionEvent{throwEmission(100, 0.9, 0.1, true, 0.9)}, 100)
	d.Deduplicate([]model.DetectionEvent{throwEmission(101, 0.2, 0.99, false, 0.1)}, 101)
	d.Deduplicate([]model.DetectionEvent{throwEmission(102, 0.9, 0.5, false, 0.5)}, 102)
	d.Deduplicate([]model.DetectionEvent{throwEmission(103, 0.9, 0.5, true, 0.95)}, 103)
	out = d.Flush()
	if len(out) != 1 {
		t.Fatalf("want one incident, got %d", len(out))
	}
	inc = out[0]
	if inc.Severity != 0.9 || inc.Confidence != 0.5 || inc.AutoEnforce || inc.EnforcementWeight != 0.5 || inc.FrameIndex != 102 {
		t.Errorf("incident sev=%.2f conf=%.2f auto=%v weight=%.2f frame=%d; want the frame-102 emission whole (0.9/0.5/false/0.5)",
			inc.Severity, inc.Confidence, inc.AutoEnforce, inc.EnforcementWeight, inc.FrameIndex)
	}
	if inc.MergedCount != 4 {
		t.Errorf("merged %d, want 4", inc.MergedCount)
	}
}
