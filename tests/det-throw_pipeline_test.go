package tests

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// THROW_001 must fire on replay-rate (15 fps, dt=0.067) data: release speed
// comes from the game-reported disc velocity, so there is no dt gate.
func TestDetThrow_Throw001FiresAtReplayRate(t *testing.T) {
	frames := AimbotThrows(8) // 35 m/s releases at dt=0.067
	hr := testutil.NewHarness(t).
		WithDetectors("THROW_001").
		WithMatchContext(matchContextForPlayer("player1")).
		Run(t, frames)
	hr.AssertDetectorFired("THROW_001")
	for _, ev := range hr.Events {
		if ev.CausalKey.AnomalyType != "disc_speed" {
			t.Errorf("35 m/s release should be an over-cap event, got %q", ev.CausalKey.AnomalyType)
		}
		if ev.AutoEnforce {
			t.Error("THROW_001 auto-enforce must be off by default")
		}
	}
}

// THROW_002 compares the release speed against the frame BEFORE release as
// built by the feature extractor; with a held disc at rest and a 35 m/s
// release the delta is 35 m/s and the detector fires end to end.
func TestDetThrow_Throw002FiresOnExtractorSnapshots(t *testing.T) {
	frames := AimbotThrows(4)
	hr := testutil.NewHarness(t).
		WithDetectors("THROW_002").
		WithMatchContext(matchContextForPlayer("player1")).
		Run(t, frames)
	hr.AssertDetectorFired("THROW_002")
	ev := hr.Events[0]
	evd, ok := ev.Evidence.(model.DiscAccelerationEvidence)
	if !ok {
		t.Fatalf("evidence type %T", ev.Evidence)
	}
	if evd.PreReleaseSpeed != 0 || evd.SpeedDelta < 30 {
		t.Fatalf("pre-release speed %v delta %v; the release frame leaked into the snapshots", evd.PreReleaseSpeed, evd.SpeedDelta)
	}
	for _, snap := range evd.PreReleaseFrames {
		if snap.FrameIndex >= ev.FrameIndex {
			t.Fatalf("snapshot %d is not before release frame %d", snap.FrameIndex, ev.FrameIndex)
		}
	}
}

// Releases > 2x the physics cap are observations, not silent drops.
func TestDetThrow_ArtifactReleasesAreObserved(t *testing.T) {
	frames := AimbotThrows(3)
	for i := range frames {
		if frames[i].Disc != nil && frames[i].Disc.Speed > 30 {
			frames[i].Disc.Velocity = frames[i].Disc.Velocity.Normalized().Scale(60)
			frames[i].Disc.Speed = 60
		}
	}
	hr := testutil.NewHarness(t).
		WithDetectors("THROW_001").
		WithMatchContext(matchContextForPlayer("player1")).
		Run(t, frames)
	hr.AssertDetectorFired("THROW_001")
	for _, ev := range hr.Events {
		if ev.CausalKey.AnomalyType != "disc_speed_artifact" {
			t.Errorf("60 m/s release should be reported as a suspected artifact, got %q", ev.CausalKey.AnomalyType)
		}
		if ev.Severity > 0.25 {
			t.Errorf("artifact severity should be low, got %v", ev.Severity)
		}
	}
}

// Legitimate throw sequences stay clean with the updated detectors.
func TestDetThrow_NormalThrowsStayClean(t *testing.T) {
	hr := testutil.NewHarness(t).
		WithDetectors("THROW_001", "THROW_002", "THROW_003", "THROW_005", "THROW_006", "THROW_008").
		WithMatchContext(matchContextForPlayer("player1")).
		Run(t, NormalThrowSequence(6))
	hr.AssertNoDetections()
}
