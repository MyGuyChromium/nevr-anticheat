package tests

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// mechanicsObservedFrames declares synthetic source provenance, never engine
// verification. These fields exercise the real observation/pipeline path.
func mechanicsObservedFrames(frames []model.PlayerTelemetryFrame) []model.PlayerTelemetryFrame {
	for i := range frames {
		f := &frames[i]
		f.Observation = &model.ObservationContext{Source: "synthetic", Authority: "client_reported", TimeBasis: "capture", SessionID: "synthetic-mechanics", FrameIndex: f.FrameIndex, Timestamp: f.Timestamp}
		if f.Disc != nil {
			disc := *f.Disc
			disc.Attachment = f.Disc.Attachment.Clone()
			bounce := 0
			disc.BounceCount, disc.SampledPlayerCount = &bounce, 1
			f.Disc = &disc
		}
	}
	return frames
}

func assertMechanicsOnly(t *testing.T, hr *testutil.HarnessResult, detector string) *model.MechanicsReviewLog {
	t.Helper()
	hr.AssertNoDetections()
	for _, score := range hr.PlayerScores {
		if score.TotalScore != 0 || score.EventCount != 0 {
			t.Fatalf("mechanics diagnostic influenced score: %+v", score)
		}
	}
	coverage := hr.Result.PlayerCoverage["player1"]
	if coverage == nil {
		t.Fatal("missing player coverage")
	}
	for _, d := range coverage.Detectors {
		if d.DetectorID == detector {
			if d.MechanicsReview == nil || d.MechanicsReview.Total == 0 {
				t.Fatalf("no diagnostic for %s: %+v", detector, d)
			}
			if err := d.MechanicsReview.Validate(); err != nil {
				t.Fatal(err)
			}
			return d.MechanicsReview
		}
	}
	t.Fatalf("missing detector %s", detector)
	return nil
}
