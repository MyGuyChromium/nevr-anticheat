package tests

import (
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// The synthetic generators live in internal/testutil (one suite, validated
// against the production FrameValidator in generators_test.go). The tests
// here run them through the production pipeline with the production
// detector set (DefaultConfig's enabled detectors, enforce mode) unless a
// detector is named explicitly.

// matchContextForPlayer returns a match context for a single-player test.
func matchContextForPlayer(playerID string) *model.MatchContext {
	return &model.MatchContext{
		MatchID:   "test-harness-match",
		Map:       "mpl_arena_a",
		GameMode:  "Echo_Arena",
		IsRanked:  true,
		StartTime: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
		Duration:  10 * time.Minute,
		PlayerIDs: []string{playerID},
		TeamAssignments: map[string]string{
			playerID: "blue",
		},
		TickRate: 15.0,
		Source:   "test",
		Physics:  model.DefaultPhysics(),
	}
}

// player1 returns a FrameBuilder for the harness player at a mid-arena
// start position.
func player1() *testutil.FrameBuilder {
	return testutil.NewFrameBuilder("player1").WithStartPos(model.Vec3{2, 1.6, 0})
}

// runEnabled runs frames through the production-enabled detector set.
func runEnabled(t *testing.T, frames []model.PlayerTelemetryFrame) *testutil.HarnessResult {
	t.Helper()
	hr := testutil.NewHarness(t).WithEnabledDetectors().
		WithMatchContext(matchContextForPlayer("player1")).
		Run(t, frames)
	hr.AssertAllFramesValid(len(frames))
	return hr
}

// runOnly runs frames through exactly the named detectors.
func runOnly(t *testing.T, frames []model.PlayerTelemetryFrame, ids ...string) *testutil.HarnessResult {
	t.Helper()
	hr := testutil.NewHarness(t).WithDetectors(ids...).
		WithMatchContext(matchContextForPlayer("player1")).
		Run(t, frames)
	hr.AssertAllFramesValid(len(frames))
	return hr
}

// ============================================================================
// Legitimate Gameplay -- Must NOT Fire (production-enabled detectors)
// ============================================================================

func TestLegit_IdlePlayer_NoDetections(t *testing.T) {
	runEnabled(t, player1().NormalIdlePlayer(300)).AssertNoDetections()
}

func TestLegit_MovingPlayer_NoDetections(t *testing.T) {
	runEnabled(t, player1().NormalMovingPlayer(600, 5.0)).AssertNoDetections()
}

func TestLegit_FastMovingPlayer_NoDetections(t *testing.T) {
	// 30 m/s (+-20 %) is fast but under every movement threshold.
	runEnabled(t, player1().NormalMovingPlayer(600, 30.0)).AssertNoDetections()
}

func TestLegit_NormalThrows_NoDetections(t *testing.T) {
	runEnabled(t, player1().NormalThrowSequence(8)).AssertNoDetections()
}

func TestLegit_EliteThrows_NoDetections(t *testing.T) {
	runEnabled(t, player1().EliteThrowSequence(12)).AssertNoDetections()
}

func TestLegit_RegrabStacking_NoSpeedDetection(t *testing.T) {
	hr := runOnly(t, player1().RegrabStackingBurst(150), "MOV_001", "MOV_002", "BIO_002", "MOV_004")
	hr.AssertNoDetections()
}

func TestLegit_FastWristFlick_NoBioDetection(t *testing.T) {
	runOnly(t, player1().FastWristFlick(150), "BIO_001", "BIO_004").AssertNoDetections()
}

func TestLegit_SteadyHands_NoJitterDetection(t *testing.T) {
	runOnly(t, player1().SteadyHandPlayer(300), "BIO_003", "BIO_004").AssertNoDetections()
}

func TestLegit_NormalBoosts_NoSpamDetection(t *testing.T) {
	// MOV_004/MOV_005 are disabled by default (telemetry dependent); enabled
	// explicitly so a legitimate boost rhythm is proven silent on them.
	runOnly(t, player1().NormalBoostSequence(4), "MOV_004", "MOV_005", "MOV_001").AssertNoDetections()
}

func TestLegit_NormalStuns_NoRecoveryDetection(t *testing.T) {
	runOnly(t, player1().NormalStunCycle(450, 45), "STATE_002", "MOV_002").AssertNoDetections()
}

func TestLegit_NormalShieldUse_NoDetection(t *testing.T) {
	runOnly(t, player1().NormalShieldCycle(4), "STATE_003", "STATE_005").AssertNoDetections()
}

func TestLegit_RespawnImmunity_NoDetection(t *testing.T) {
	// A respawn is a 30 m jump under immunity: MOV_002 skips immune players,
	// BIO_002 resets on immunity and STATE_004's 225-frame limit is far
	// above the 22-frame window.
	runOnly(t, player1().RespawnImmunity(200, 100), "MOV_002", "MOV_001", "BIO_002", "STATE_004").AssertNoDetections()
}

// ============================================================================
// Noise/Artifact Tolerance -- Must NOT Fire
// ============================================================================

func TestNoise_JitteryTelemetry_NoDetections(t *testing.T) {
	runEnabled(t, player1().JitteryTelemetry(300, 0.05)).AssertNoDetections()
}

func TestNoise_PacketLoss_NoDetections(t *testing.T) {
	frames := player1().PacketLossFrames(300, 0.3)
	if len(frames) > 225 || len(frames) < 195 { // ~30 % dropped
		t.Fatalf("packet loss generator kept %d of 300 frames", len(frames))
	}
	runEnabled(t, frames).AssertNoDetections()
}

func TestNoise_LargeFrameGap_NoTeleport(t *testing.T) {
	// A 2 s stall at frame 100: the player is 10 m further along (over the
	// 8 m teleport threshold) and the frame index jumps by 30 (over
	// max_frame_gap 5). The frame is accepted, no kinematics are derived
	// across the gap and MOV_002 skips it.
	frames := player1().LargeFrameGap(200, 100, 2.0)
	if got := frames[100].FrameIndex - frames[99].FrameIndex; got != 31 {
		t.Fatalf("gap frame index delta = %d, want 31", got)
	}
	if jump := frames[100].Position.Distance(frames[99].Position); jump < 8.0 {
		t.Fatalf("gap displacement %.2f m is under the teleport threshold; the test could not fail", jump)
	}
	hr := runOnly(t, frames, "MOV_002", "MOV_001")
	hr.AssertNoDetections()
}

func TestNoise_InterpolationArtifact_NoSpeedHack(t *testing.T) {
	// Three single-frame 4 m body glitches: ~60 m/s for two frames each,
	// visible to MOV_001 but under its 5-frame burst minimum, and under
	// MOV_002's 8 m threshold.
	frames := player1().InterpolationArtifact(300)
	glitches := 0
	for i := 1; i < len(frames); i++ {
		if frames[i].Position.Distance(frames[i-1].Position) > 3.0 {
			glitches++
		}
	}
	if glitches != 6 { // out and back, three times
		t.Fatalf("expected 6 glitch transitions, got %d", glitches)
	}
	runOnly(t, frames, "MOV_001", "MOV_002").AssertNoDetections()
}

// A body glitch moves the hands with the body, and BIO_002's
// min_violation_frames (2) is exactly the out-and-back pair a single-frame
// glitch produces. This pins the production behaviour (reported as a
// false-positive risk; see the workstream report) so a threshold change
// is visible.
func TestNoise_InterpolationArtifact_Bio002FiresOnGlitch(t *testing.T) {
	hr := runOnly(t, player1().InterpolationArtifact(300), "BIO_002")
	hr.AssertDetectorFiredN("BIO_002", 3)
	for _, ev := range hr.DetectorEvents("BIO_002") {
		if ev.Confidence > 0.5 {
			t.Errorf("a two-frame glitch should stay at the minimum confidence, got %.2f", ev.Confidence)
		}
	}
}

func TestNoise_HighPing_NoFalsePositives(t *testing.T) {
	runEnabled(t, player1().HighPingPlayer(300, 200)).AssertNoDetections()
}

func TestNoise_PingSpike_NoFalsePositives(t *testing.T) {
	runEnabled(t, player1().PingSpikeSequence(300, 100, 400)).AssertNoDetections()
}

// ============================================================================
// Cheat Scenarios -- MUST Fire at production parameters
// ============================================================================

func TestCheat_SpeedHack_Detected(t *testing.T) {
	hr := runOnly(t, player1().SpeedHackFrames(150, 80), "MOV_001")
	hr.AssertDetectorFiredN("MOV_001", 4) // one per full 30-frame window after warmup
	hr.AssertMinSeverity("MOV_001", 0.85)
	hr.AssertMinConfidence("MOV_001", 0.99)
	hr.AssertScoreAbove("player1", 1.0)
}

func TestCheat_Teleport_Detected(t *testing.T) {
	// Production params (min_incidents 5): a 10 m jump every 30 frames over
	// 300 frames is 9 jumps, the 5th..9th are reported.
	hr := runOnly(t, player1().TeleportCheat(300, 30, 10.0), "MOV_002")
	hr.AssertDetectorFiredN("MOV_002", 5)
	hr.AssertMinSeverity("MOV_002", 0.4)
	hr.AssertMinConfidence("MOV_002", 0.99)
}

func TestCheat_Teleport_SlowCadence_Detected(t *testing.T) {
	// 11 m every 70 frames over 600 frames: 8 jumps, 4 reported, and an
	// 11 m jump sits above the severity midpoint of the accepted range.
	hr := runOnly(t, player1().TeleportCheat(600, 70, 11.0), "MOV_002")
	hr.AssertDetectorFiredN("MOV_002", 4)
	hr.AssertMinSeverity("MOV_002", 0.5)
}

func TestCheat_Aimbot_Detected(t *testing.T) {
	hr := runOnly(t, player1().AimbotThrows(8), "THROW_001", "THROW_003", "THROW_005")
	hr.AssertDetectorFiredN("THROW_001", 8)
	hr.AssertMinSeverity("THROW_001", 0.99)
	// The releases are aimed 1 degree off the goal: THROW_005 fires on the
	// 8th goal-directed throw (mean deviation < 2, stddev < 1.5).
	hr.AssertDetectorFiredN("THROW_005", 1)
	// The hand moves along the disc's path, so the release angle is small.
	hr.AssertDetectorNotFired("THROW_003")
}

func TestCheat_Magnetism_Detected(t *testing.T) {
	// Production thresholds, no overrides. The attacked goal must be known
	// (production learns it from the first scored goal): with it the disc's
	// alignment with the goal improves from ~0 to 1 over the tracked flight.
	hr := testutil.NewHarness(t).WithDetectors("THROW_006").WithBlueGoalSide(1).
		WithMatchContext(matchContextForPlayer("player1")).
		Run(t, player1().MagnetismCheat(4))
	hr.AssertDetectorFiredN("THROW_006", 4)
	hr.AssertMinSeverity("THROW_006", 0.9)
	hr.AssertMinConfidence("THROW_006", 0.9)
}

func TestCheat_StunBypass_Detected(t *testing.T) {
	hr := runOnly(t, player1().StunBypass(200), "STATE_002")
	hr.AssertDetectorFiredN("STATE_002", 4) // 5 short stuns, reported from the 2nd
	hr.AssertMinSeverity("STATE_002", 0.9)
}

func TestCheat_GodMode_Detected(t *testing.T) {
	hr := runOnly(t, player1().GodMode(600), "STATE_004")
	hr.AssertDetectorFiredN("STATE_004", 4) // 231 frames, then every 112
	hr.AssertMinSeverity("STATE_004", 0.99)
}

func TestCheat_ScoreManipulation_SuspendedDetectorStaysSilent(t *testing.T) {
	// STATE_006 is SUSPENDED: no confirmed impossible score invariant, so a
	// +7 jump produces nothing even with the detector enabled.
	runOnly(t, player1().ScoreJump(50, 7), "STATE_006").AssertDetectorNotFired("STATE_006")
}

func TestCheat_InfiniteBoost_Detected(t *testing.T) {
	hr := runOnly(t, player1().InfiniteBoost(300), "MOV_005")
	hr.AssertDetectorFiredN("MOV_005", 1)
}

func TestCheat_BotBehavior_Detected(t *testing.T) {
	hr := runOnly(t, player1().BotBehavior(600), "BIO_003")
	hr.AssertDetectorFiredN("BIO_003", 2)
	hr.AssertMinSeverity("BIO_003", 0.8)
}

func TestCheat_ExtendedReach_Detected(t *testing.T) {
	hr := runOnly(t, player1().ExtendedReach(100), "PAT_005")
	hr.AssertDetectorFiredN("PAT_005", 3) // at 30, 60 and 90 sustained frames
	hr.AssertMinConfidence("PAT_005", 0.79)
}

// ============================================================================
// Scoring Integration
// ============================================================================

func TestScoring_MultipleCheatTypes_HighScore(t *testing.T) {
	// Movement (MOV_001), state (STATE_001) and throw (THROW_001) evidence
	// on one player: three categories reach PAT_004 and the review tier.
	hr := runEnabled(t, player1().CompositeCheater())
	hr.AssertDetectorFired("MOV_001")
	hr.AssertDetectorFired("STATE_001")
	hr.AssertDetectorFired("THROW_001")
	hr.AssertDetectorFiredN("PAT_004", 1)
	levels := testutil.NewHarness(t).Config().Scoring.LevelTable()
	hr.AssertScoreAbove("player1", levels.HighRisk)
}

func TestScoring_SingleSoftSignal_LowScore(t *testing.T) {
	// One 10 m jump is below MOV_002's min_incidents (5): no event, no score.
	frames := player1().NormalMovingPlayer(120, 4.0)
	testutil.ShiftFrom(frames, 60, model.Vec3{0, 0, 10})
	hr := runOnly(t, frames, "MOV_002")
	hr.AssertNoDetections()
	if sc, ok := hr.PlayerScores["player1"]; ok && sc.TotalScore != 0 {
		t.Errorf("a single teleport must not score, got %.2f", sc.TotalScore)
	}
	levels := testutil.NewHarness(t).Config().Scoring.LevelTable()
	hr.AssertScoreBelow("player1", levels.Informational)
}

// ============================================================================
// Shadow Mode
// ============================================================================

func TestShadow_DefaultMode_EventsNotScored(t *testing.T) {
	hr := testutil.NewHarness(t).WithDetectors("MOV_001").
		WithShadowMode().
		WithMatchContext(matchContextForPlayer("player1")).
		Run(t, player1().SpeedHackFrames(150, 80))
	hr.AssertDetectorFiredN("MOV_001", 4)
	hr.AssertAllShadow()
	if score, ok := hr.PlayerScores["player1"]; ok && score.TotalScore > 0 {
		t.Errorf("expected zero score in shadow mode, got %.2f", score.TotalScore)
	}
}

func TestShadow_EnforceMode_EventsScored(t *testing.T) {
	hr := runOnly(t, player1().SpeedHackFrames(150, 80), "MOV_001")
	hr.AssertDetectorFiredN("MOV_001", 4)
	for _, ev := range hr.Events {
		if ev.IsShadow {
			t.Errorf("enforce-mode event %s at frame %d is marked shadow", ev.DetectorID, ev.FrameIndex)
		}
	}
	hr.AssertScoreAbove("player1", 1.0)
}
