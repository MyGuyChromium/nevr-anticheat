package tests

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// These are synthetic invariants of the shipped decision rules, not labelled
// real-player ground truth or proof that the model captures every legal move.
func legalMechanicsReasons(t *testing.T, result *testutil.HarnessResult, detectorID string) map[string]int {
	t.Helper()
	coverage := result.Result.PlayerCoverage["synthetic-player"]
	if coverage == nil {
		t.Fatal("missing actual pipeline coverage")
	}
	for _, detector := range coverage.Detectors {
		if detector.DetectorID != detectorID {
			continue
		}
		if detector.CandidateFrames == 0 || detector.DecisionTrace == nil || !detector.DecisionTrace.InternalBranches {
			t.Fatalf("fixture bypassed detector: %+v", detector)
		}
		counts := map[string]int{}
		for _, reason := range detector.DecisionTrace.Reasons {
			counts[reason.Code] = reason.Count
		}
		return counts
	}
	t.Fatalf("missing detector %s", detectorID)
	return nil
}

func legalMechanicsFrame(i int, pos model.Vec3) model.PlayerTelemetryFrame {
	return model.PlayerTelemetryFrame{
		PlayerID: "synthetic-player", Team: "blue", FrameIndex: i,
		Timestamp: float64(i) / 15, DeltaTime: 1.0 / 15,
		Position: pos, Rotation: model.QuatIdentity(),
		LeftHandPosition: pos.Add(model.Vec3{-.3, .3, .2}), RightHandPosition: pos.Add(model.Vec3{.3, .3, -.2}),
		LeftHandRotation: model.QuatIdentity(), RightHandRotation: model.QuatIdentity(),
		GamePhase: "playing", Disc: &model.DiscState{Position: model.Vec3{0, 2, 0}},
	}
}

func TestLegalMechanicsGameVelocityDoesNotBecomePhysicalWalking(t *testing.T) {
	for _, kind := range []string{"stack", "small block push", "boost", "short coherent lean"} {
		t.Run(kind, func(t *testing.T) {
			frames := make([]model.PlayerTelemetryFrame, 70)
			pos := model.Vec3{1, 1.7, -20}
			for i := range frames {
				speed := 5.0
				if kind == "stack" {
					speed = 12
				}
				if (kind == "small block push" || kind == "boost") && i >= 25 && i < 40 {
					speed += 3
				}
				v := model.Vec3{0, 0, speed}
				if i > 0 {
					pos = pos.Add(v.Scale(1.0 / 15))
				}
				observed := pos
				// A brief, coherent 0.30m displacement deliberately passes the
				// residual-speed gate while staying below the displacement gate.
				if kind == "short coherent lean" && i >= 25 && i < 28 {
					observed[0] += .1 * float64(i-24)
				}
				frames[i] = legalMechanicsFrame(i, observed)
				frames[i].ReportedVelocity = &v
				frames[i].IsBoosting = kind == "boost" && i >= 25 && i < 40
			}
			result := testutil.NewHarness(t).WithDetectors("MOV_006").WithShadowMode().Run(t, frames)
			result.AssertNoDetections()
			counts := legalMechanicsReasons(t, result, "MOV_006")
			if counts["playspace_speed_below_gate"] < 40 {
				t.Fatalf("game movement failed to subtract: %v", counts)
			}
			if kind == "short coherent lean" && counts["playspace_distance_below_gate"] == 0 {
				t.Fatalf("lean fixture never reached displacement guard: %v", counts)
			}
		})
	}
}

func TestLegalMechanicsPossibleHeadContactIsNotWristAngleEvidence(t *testing.T) {
	pos := model.Vec3{1, 1.7, 0}
	frames := make([]model.PlayerTelemetryFrame, 21)
	for i := range frames {
		frames[i] = legalMechanicsFrame(i, pos)
		zero := model.Vec3{}
		frames[i].ReportedVelocity = &zero
		frames[i].HasPossession = i < 20
		frames[i].Disc = &model.DiscState{Position: frames[i].RightHandPosition, IsHeld: i < 20, PossessorID: "synthetic-player"}
	}
	// The first free sample is beside the head and not either controller.
	// The pipeline must reconstruct and guard this release, not simply fail
	// to warm up or ignore the throw due to missing telemetry.
	release := &frames[20]
	release.LeftHandPosition = pos.Add(model.Vec3{-.7, .2, 0})
	release.RightHandPosition = pos.Add(model.Vec3{.7, .2, 0})
	release.Disc = &model.DiscState{Position: pos.Add(model.Vec3{0, .08, .1}), Velocity: model.Vec3{0, 0, -12}, Speed: 12}
	result := testutil.NewHarness(t).WithDetectors("THROW_003").WithShadowMode().Run(t, frames)
	result.AssertNoDetections()
	counts := legalMechanicsReasons(t, result, "THROW_003")
	if counts["possible_head_contact"] != 1 || counts["release_angle_candidate"] != 0 {
		t.Fatalf("head contact did not use actual release guard: %v", counts)
	}
}

func TestLegalMechanicsNearCapReleasesAndWristFlickStayBelowRules(t *testing.T) {
	throwResult := testutil.NewHarness(t).WithDetectors("THROW_001").WithShadowMode().Run(t, testutil.NewFrameBuilder("synthetic-player").NearCapThrows(8))
	throwResult.AssertNoDetections()
	if counts := legalMechanicsReasons(t, throwResult, "THROW_001"); counts["release_at_or_below_cap"] < 8 {
		t.Fatalf("not all actual releases were checked: %v", counts)
	}
	// 60Hz makes the wrist threshold representable. Silence must not be an
	// accidental consequence of the 15Hz angular-distance sampling ceiling.
	wristResult := testutil.NewHarness(t).WithDetectors("BIO_001").WithShadowMode().Run(t, testutil.NewFrameBuilder("synthetic-player").WithTickRate(60).FastWristFlick(150))
	wristResult.AssertNoDetections()
	if counts := legalMechanicsReasons(t, wristResult, "BIO_001"); counts["wrist_at_or_below_threshold"] == 0 {
		t.Fatalf("wrist fixture did not evaluate: %v", counts)
	}
	for _, detector := range wristResult.Result.PlayerCoverage["synthetic-player"].Detectors {
		if detector.DetectorID == "BIO_001" && detector.InputFrames == 0 {
			t.Fatal("wrist fixture had no expressible observations")
		}
	}
}

func TestLegalMechanicsDroppedSampleBreaksPlayspaceDuration(t *testing.T) {
	// At 30Hz one missing sample still falls within the extractor's 0.1s
	// reconstruction limit. It must nevertheless break a continuous burst.
	// Two nine-frame bursts each last only 0.267s, below the 0.3s rule.
	var frames []model.PlayerTelemetryFrame
	pos := model.Vec3{1, 1.7, 0}
	for i := 0; i < 60; i++ {
		physicalSpeed := 0.9
		if i >= 41 {
			physicalSpeed = 1.4
		}
		if i > 0 {
			pos[2] += physicalSpeed / 30
		}
		if i == 50 {
			continue
		}
		frame := legalMechanicsFrame(i, pos)
		frame.Timestamp, frame.DeltaTime = float64(i)/30, 1.0/30
		zero := model.Vec3{}
		frame.ReportedVelocity = &zero
		frames = append(frames, frame)
	}
	result := testutil.NewHarness(t).WithDetectors("MOV_006").WithShadowMode().Run(t, frames)
	result.AssertNoDetections()
	counts := legalMechanicsReasons(t, result, "MOV_006")
	if counts["playspace_observation_gap"] != 1 || counts["playspace_duration_pending"] == 0 {
		t.Fatalf("fixture bypassed burst continuity guard: %v", counts)
	}
}
