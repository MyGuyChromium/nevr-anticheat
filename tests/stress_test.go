package tests

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// TestStress_10000FrameMatch processes a very long match.
func TestStress_10000FrameMatch(t *testing.T) {
	h := testutil.NewHarness(t).WithAllDetectors()
	frames := testutil.NewFrameBuilder("player1").
		WithStartPos(model.Vec3{5, 1.6, 0}).
		NormalMovingPlayer(10000, 5.0)

	start := time.Now()
	result := h.Run(t, frames)
	elapsed := time.Since(start)

	t.Logf("10000 frames: %v elapsed, %d events, %d frames processed",
		elapsed, len(result.Events), result.Result.FramesProcessed)

	// Must complete in under 5 seconds
	if elapsed > 5*time.Second {
		t.Errorf("too slow: %v (max 5s)", elapsed)
	}
	// Synthetic data may produce low-severity BIO_004 wobble detections due to
	// deterministic hand rotation. Ensure score stays well below review threshold.
	result.AssertScoreBelow("player1", 60.0)
	// No high-severity detections on clean synthetic data
	for _, ev := range result.Events {
		if ev.Severity > 0.7 {
			t.Errorf("unexpected high-severity detection on clean data: %s sev=%.2f frame=%d",
				ev.DetectorID, ev.Severity, ev.FrameIndex)
		}
	}
}

// TestStress_50Players processes a match with 50 simultaneous players.
func TestStress_50Players(t *testing.T) {
	// Generate frames for 50 players, 200 frames each
	var allFrames []model.PlayerTelemetryFrame
	playerIDs := make([]string, 50)
	for i := 0; i < 50; i++ {
		pid := fmt.Sprintf("player%d", i)
		playerIDs[i] = pid
		fb := testutil.NewFrameBuilder(pid).
			WithStartPos(model.Vec3{float64(i%10)*3 - 15, 1.6, float64(i/10)*3 - 7})
		frames := fb.NormalMovingPlayer(200, 3.0+float64(i%5))
		allFrames = append(allFrames, frames...)
	}

	mc := testutil.NewMatchContext()
	mc.PlayerIDs = playerIDs
	mc.TeamAssignments = make(map[string]string)
	for _, pid := range playerIDs {
		mc.TeamAssignments[pid] = "blue"
	}

	h := testutil.NewHarness(t).WithAllDetectors().WithMatchContext(mc)
	start := time.Now()
	result := h.Run(t, allFrames)
	elapsed := time.Since(start)

	t.Logf("50 players x 200 frames: %v elapsed, %d events",
		elapsed, len(result.Events))

	if elapsed > 10*time.Second {
		t.Errorf("too slow: %v (max 10s)", elapsed)
	}
}

// TestStress_HighPacketLoss processes frames with 30% packet loss.
func TestStress_HighPacketLoss(t *testing.T) {
	h := testutil.NewHarness(t).WithAllDetectors()
	frames := testutil.NewFrameBuilder("player1").
		WithStartPos(model.Vec3{5, 1.6, 0}).
		PacketLossFrames(500, 0.3) // 30% loss

	result := h.Run(t, frames)

	t.Logf("high packet loss: %d events, score=%.1f",
		len(result.Events), result.PlayerScores["player1"].TotalScore)

	// Should not produce high-severity detections on packet loss alone
	for _, ev := range result.Events {
		if ev.Severity > 0.8 && !ev.IsShadow {
			t.Errorf("high-severity detection on packet loss: %s sev=%.2f",
				ev.DetectorID, ev.Severity)
		}
	}
}

// TestStress_ExtremeFrameJitter processes frames with severe timing jitter.
func TestStress_ExtremeFrameJitter(t *testing.T) {
	h := testutil.NewHarness(t).WithAllDetectors()
	// Generate frames with extreme jitter (0.5m position noise)
	frames := testutil.NewFrameBuilder("player1").
		WithStartPos(model.Vec3{5, 1.6, 0}).
		JitteryTelemetry(500, 0.5)

	result := h.Run(t, frames)

	t.Logf("extreme jitter: %d events, score=%.1f",
		len(result.Events), result.PlayerScores["player1"].TotalScore)

	// May produce some low-severity events, but should not reach enforcement threshold
	result.AssertScoreBelow("player1", 60.0) // below review threshold for jitter
}

// TestStress_MaliciousTelemetry attempts to crash the pipeline with bad data.
func TestStress_MaliciousTelemetry(t *testing.T) {
	h := testutil.NewHarness(t).WithAllDetectors()

	// Create frames with NaN, Inf, negative timestamps, extreme values
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < 100; i++ {
		f := model.PlayerTelemetryFrame{
			PlayerID:          "player1",
			FrameIndex:        i,
			Timestamp:         float64(i) * 0.067,
			DeltaTime:         0.067,
			Position:          model.Vec3{5, 1.6, 0},
			Rotation:          model.QuatIdentity(),
			LeftHandPosition:  model.Vec3{4.7, 1.9, 0.2},
			RightHandPosition: model.Vec3{5.3, 1.9, -0.2},
			LeftHandRotation:  model.QuatIdentity(),
			RightHandRotation: model.QuatIdentity(),
			GamePhase:         "playing",
		}

		// Inject malicious values at specific frames
		switch {
		case i == 10:
			f.Position = model.Vec3{math.NaN(), 0, 0}
		case i == 20:
			f.Position = model.Vec3{math.Inf(1), 0, 0}
		case i == 30:
			f.Timestamp = -1.0
		case i == 40:
			f.Position = model.Vec3{99999, 99999, 99999}
		case i == 50:
			f.Position = model.Vec3{0, 0, 0} // zero vec
		case i == 60:
			f.DeltaTime = -5.0
		case i == 70:
			f.Rotation = model.Quat{0, 0, 0, 0} // zero quat
		}

		frames = append(frames, f)
	}

	// Must not panic
	result := h.Run(t, frames)
	t.Logf("malicious telemetry: %d events, %d frames processed",
		len(result.Events), result.Result.FramesProcessed)

	// Pipeline should have rejected at least the NaN, Inf, and negative-timestamp frames
	if result.Result.InvalidFrames == 0 {
		t.Errorf("expected some frames rejected, got %d invalid out of %d",
			result.Result.InvalidFrames, 100)
	}
	t.Logf("invalid frames: %d, processed: %d", result.Result.InvalidFrames, result.Result.FramesProcessed)
}

// TestStress_ExtremeFrameRate simulates a match at 120fps (way over normal 15fps).
func TestStress_ExtremeFrameRate(t *testing.T) {
	h := testutil.NewHarness(t).WithAllDetectors()

	// 120fps for 10 seconds = 1200 frames with very small dt
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < 1200; i++ {
		ts := 1.0 + float64(i)*0.00833 // ~120fps
		x := 5.0 + float64(i)*0.0083*3.0 // ~3 m/s
		f := model.PlayerTelemetryFrame{
			PlayerID:          "player1",
			FrameIndex:        i,
			Timestamp:         ts,
			DeltaTime:         0.00833,
			Position:          model.Vec3{x, 1.6, 0},
			Rotation:          model.QuatIdentity(),
			LeftHandPosition:  model.Vec3{x - 0.3, 1.9, 0.2},
			RightHandPosition: model.Vec3{x + 0.3, 1.9, -0.2},
			LeftHandRotation:  model.Quat{0, math.Sin(float64(i) * 0.01), 0, math.Cos(float64(i) * 0.01)},
			RightHandRotation: model.Quat{0, math.Sin(float64(i) * 0.012), 0, math.Cos(float64(i) * 0.012)},
			GamePhase:         "playing",
		}
		frames = append(frames, f)
	}

	start := time.Now()
	result := h.Run(t, frames)
	elapsed := time.Since(start)

	t.Logf("120fps x 10s: %v elapsed, %d events, %d frames processed",
		elapsed, len(result.Events), result.Result.FramesProcessed)

	if elapsed > 3*time.Second {
		t.Errorf("too slow for high frame rate: %v", elapsed)
	}
}
