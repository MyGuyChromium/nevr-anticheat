package tests

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// Wall-clock bounds are advisory on shared CI hardware: they are generous
// (10x the observed local time) and skipped under -short. Every stress test
// asserts correctness first: all frames processed, no invalid frames, no
// detections on clean data.

// checkElapsed fails when a run exceeds the generous bound (unless -short).
func checkElapsed(t *testing.T, elapsed, bound time.Duration) {
	t.Helper()
	if testing.Short() {
		return
	}
	if elapsed > bound {
		t.Errorf("too slow: %v (bound %v)", elapsed, bound)
	}
}

// TestStress_10000FrameMatch processes a very long clean match through the
// production detector set.
func TestStress_10000FrameMatch(t *testing.T) {
	h := testutil.NewHarness(t).WithEnabledDetectors()
	frames := player1().NormalMovingPlayer(10000, 5.0)

	start := time.Now()
	result := h.Run(t, frames)
	elapsed := time.Since(start)
	t.Logf("10000 frames: %v elapsed, %d events", elapsed, len(result.Events))

	result.AssertAllFramesValid(10000)
	result.AssertNoDetections()
	checkElapsed(t, elapsed, 30*time.Second)
}

// TestStress_50Players processes a match with 50 simultaneous players.
func TestStress_50Players(t *testing.T) {
	mb := testutil.NewMatchBuilder("stress-50")
	for i := 0; i < 50; i++ {
		fb := testutil.NewFrameBuilder(fmt.Sprintf("player%02d", i)).
			WithStartPos(model.Vec3{float64(i%5)*2 - 4, 1.6, float64(i/5)*12 - 55})
		mb.AddPlayer([]string{"blue", "orange"}[i%2], fb.NormalMovingPlayer(200, 3.0+float64(i%5)))
	}
	mc, frames := mb.Build()

	h := testutil.NewHarness(t).WithEnabledDetectors().WithMatchContext(mc)
	start := time.Now()
	result := h.Run(t, frames)
	elapsed := time.Since(start)
	t.Logf("50 players x 200 frames: %v elapsed, %d events", elapsed, len(result.Events))

	result.AssertAllFramesValid(200)
	result.AssertNoDetections()
	checkElapsed(t, elapsed, 60*time.Second)
}

// TestStress_HighPacketLoss processes frames with 30% packet loss: gaps
// in both frame index and timestamp (F163).
func TestStress_HighPacketLoss(t *testing.T) {
	h := testutil.NewHarness(t).WithEnabledDetectors()
	frames := player1().PacketLossFrames(500, 0.3)
	if len(frames) > 375 || len(frames) < 325 {
		t.Fatalf("30%% loss kept %d of 500 frames", len(frames))
	}
	result := h.Run(t, frames)
	result.AssertAllFramesValid(len(frames))
	result.AssertNoDetections()
}

// TestStress_ExtremeFrameJitter processes 0.5 m position noise on body and
// hands. It is allowed to produce low-severity events, but never a
// score in the review tier or a high-severity event.
func TestStress_ExtremeFrameJitter(t *testing.T) {
	h := testutil.NewHarness(t).WithEnabledDetectors()
	frames := player1().JitteryTelemetry(500, 0.5)
	result := h.Run(t, frames)
	result.AssertAllFramesValid(500)
	levels := h.Config().Scoring.LevelTable()
	result.AssertScoreBelow("player1", levels.HighRisk)
	for _, ev := range result.Events {
		if ev.Severity > 0.8 {
			t.Errorf("high-severity detection on jitter: %s sev=%.2f frame=%d", ev.DetectorID, ev.Severity, ev.FrameIndex)
		}
	}
	t.Logf("extreme jitter: %d events, score=%.1f", len(result.Events), result.PlayerScores["player1"].TotalScore)
}

// TestStress_MaliciousTelemetry: bad values must not panic, must be
// counted, and the surviving stream must stay clean. The precise per-reason
// accounting lives in validator_hardening_test.go.
func TestStress_MaliciousTelemetry(t *testing.T) {
	h := testutil.NewHarness(t).WithEnabledDetectors()
	frames := player1().NormalMovingPlayer(100, 4.0)
	nan := math.NaN()
	frames[10].Position = model.Vec3{nan, 0, 0}
	frames[20].Position = model.Vec3{math.Inf(1), 0, 0}
	frames[30].Timestamp = -1.0
	frames[40].Position = model.Vec3{99999, 99999, 99999}
	frames[50].Position = model.Vec3{}
	frames[60].DeltaTime = -5.0
	frames[70].Rotation = model.Quat{}
	frames[80].Timestamp = nan

	result := h.Run(t, frames)
	// NaN/Inf/out-of-bounds/zero positions and the NaN timestamp are
	// rejected; the negative timestamp, negative dt and zero quaternion are
	// accepted (documented in validator_hardening_test.go).
	if result.Result.InvalidFrames != 5 {
		t.Errorf("invalid frames %d, want 5: %v", result.Result.InvalidFrames, result.Result.InvalidFrameReasons)
	}
	if result.Result.FramesProcessed != 95 {
		t.Errorf("processed %d, want 95", result.Result.FramesProcessed)
	}
	result.AssertNoDetections()
}

// TestStress_ExtremeFrameRate simulates 120 fps for 10 s. Every frame must
// be processed (the path stays in the arena) and stay clean.
func TestStress_ExtremeFrameRate(t *testing.T) {
	h := testutil.NewHarness(t).WithEnabledDetectors()
	frames := testutil.NewFrameBuilder("player1").WithTickRate(120).WithStartPos(model.Vec3{2, 1.6, 0}).NormalMovingPlayer(1200, 3.0)

	start := time.Now()
	result := h.Run(t, frames)
	elapsed := time.Since(start)
	t.Logf("120fps x 10s: %v elapsed, %d events", elapsed, len(result.Events))

	result.AssertAllFramesValid(1200)
	result.AssertNoDetections()
	checkElapsed(t, elapsed, 30*time.Second)
}
