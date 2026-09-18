package pipeline

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/state"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestIndependentStunReviewLogSurvivesLegacyDetectorDiscovery(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		var detectors []detect.Detector
		if enabled {
			detectors = append(detectors, state.NewState007(nil))
		}
		coverage := newCoverageTracker(detectors, config.DefaultConfig(), []string{"synthetic-victim"})
		coverage.mechanicsRecord("STATE_007", "synthetic-victim", model.MechanicsAssessment{
			Kind: model.MechanicsStunContact, Result: model.MechanicsInconclusive, Reason: "stun_end_of_stream",
			RuleVersion: "synthetic-review-v1", PlayerID: "synthetic-victim", EventID: "stun:synthetic-victim:1",
			FrameIndex: 1, Timestamp: .1, IntervalStart: 0, IntervalEnd: .1,
		})
		player := coverage.players["synthetic-victim"]
		d := player.Detectors[coverage.index["STATE_007"]]
		if d.Enabled != enabled || !d.ReviewOnlyDiagnostics || d.MechanicsReview == nil || d.MechanicsReview.Total != 1 || d.MechanicsReview.Inconclusive != 1 {
			t.Fatalf("legacy enabled=%v lost independent review diagnostics: %+v", enabled, d)
		}
	}
}
