package tests

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/bio"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/catalog"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// Per-detector production-parameter tests: every one of the 29 catalogued
// detectors is built by the catalog from DefaultConfig params (no threshold
// overrides anywhere in this file) and run on one cheat generator that must
// fire and one legit generator that must stay silent. Detectors that
// DefaultConfig disables (UNSAFE / TELEMETRY_DEPENDENT / UNVERIFIED /
// SUSPENDED / STUB / CROSS_MATCH_DEPENDENT) are proven absent from the
// production build first, then exercised with the detector enabled and its
// production params. Event counts are pinned to the measured values so a
// threshold change is a test change.

// detectorStatus is DefaultConfig's documented status of every detector.
var detectorStatus = map[string]string{
	"THROW_001": "enabled", "THROW_002": "unverified", "THROW_003": "enabled", "THROW_004": "unsafe",
	"THROW_005": "enabled", "THROW_006": "enabled", "THROW_007": "stub", "THROW_008": "enabled",
	"BIO_001": "enabled", "BIO_002": "enabled", "BIO_003": "enabled", "BIO_004": "enabled",
	"MOV_001": "enabled", "MOV_002": "enabled", "MOV_003": "unsafe", "MOV_004": "telemetry_dependent", "MOV_005": "telemetry_dependent",
	"STATE_001": "enabled", "STATE_002": "enabled", "STATE_003": "telemetry_dependent", "STATE_004": "telemetry_dependent",
	"STATE_005": "telemetry_dependent", "STATE_006": "suspended", "STATE_007": "telemetry_dependent",
	"PAT_001": "unsafe", "PAT_002": "unsafe", "PAT_003": "cross_match_dependent", "PAT_004": "enabled", "PAT_005": "enabled",
}

// TestDetectorStatus_MatchesDefaultConfig: the status table above is the
// config's Enabled flag, the catalog builds exactly the enabled set, and
// every detector in this file has a status.
func TestDetectorStatus_MatchesDefaultConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	if len(detectorStatus) != 29 || len(cfg.Detectors) != 29 {
		t.Fatalf("%d statuses, %d config blocks", len(detectorStatus), len(cfg.Detectors))
	}
	built := map[string]bool{}
	for _, d := range catalog.Build(cfg, nil) {
		built[d.ID()] = true
	}
	for id, status := range detectorStatus {
		enabled := status == "enabled"
		if cfg.Detectors[id].Enabled != enabled {
			t.Errorf("%s: config enabled=%v, status %q", id, cfg.Detectors[id].Enabled, status)
		}
		if built[id] != enabled {
			t.Errorf("%s: built=%v, want %v", id, built[id], enabled)
		}
		if cfg.Detectors[id].AutoEnforce {
			t.Errorf("%s: auto_enforce on in the default config", id)
		}
	}
}

// ---------------------------------------------------------------------
// THROW
// ---------------------------------------------------------------------

func TestDetector_THROW_001_ImpossibleReleaseVelocity(t *testing.T) {
	hr := runOnly(t, player1().AimbotThrows(8), "THROW_001")
	hr.AssertDetectorFiredN("THROW_001", 8)
	for _, ev := range hr.Events {
		if ev.CausalKey.AnomalyType != "disc_speed" || ev.Severity < 0.99 || ev.Confidence < 0.89 {
			t.Errorf("event %+v", ev.ObservedValue)
		}
		if ev.AutoEnforce {
			t.Error("auto-enforce must stay off at the default config")
		}
	}
	// Releases over twice the physics cap are observations, not drops.
	hr = runOnly(t, player1().ArtifactThrows(3), "THROW_001")
	hr.AssertDetectorFiredN("THROW_001", 3)
	for _, ev := range hr.Events {
		if ev.CausalKey.AnomalyType != "disc_speed_artifact" || ev.Severity > 0.25 {
			t.Errorf("60 m/s release: %s sev %.2f", ev.CausalKey.AnomalyType, ev.Severity)
		}
	}
	// Sub-cap releases pinned just under the cap: one cap-riding event.
	hr = runOnly(t, player1().CapRidingThrows(10), "THROW_001")
	hr.AssertDetectorFiredN("THROW_001", 1)
	if ev := hr.Events[0]; ev.CausalKey.AnomalyType != "cap_riding" || math.Abs(ev.Severity-0.3) > 1e-9 {
		t.Errorf("cap riding event: %s sev %.2f", ev.CausalKey.AnomalyType, ev.Severity)
	}
	// Negative: normal and elite throws.
	runOnly(t, player1().NormalThrowSequence(8), "THROW_001").AssertNoDetections()
	runOnly(t, player1().EliteThrowSequence(10), "THROW_001").AssertNoDetections()
}

func TestDetector_THROW_002_DiscAcceleration(t *testing.T) {
	// UNVERIFIED (v2 single-delta): disabled in production; with the
	// detector enabled a 35 m/s release from a held disc is a 35 m/s jump.
	hr := runOnly(t, player1().AimbotThrows(4), "THROW_002")
	hr.AssertDetectorFiredN("THROW_002", 4)
	hr.AssertMinSeverity("THROW_002", 0.6)
	hr.AssertMinConfidence("THROW_002", 0.5)
	runOnly(t, player1().NormalThrowSequence(8), "THROW_002").AssertNoDetections()
	runOnly(t, player1().EliteThrowSequence(8), "THROW_002").AssertNoDetections()
}

func TestDetector_THROW_003_UnnaturalReleaseAngle(t *testing.T) {
	hr := runOnly(t, player1().ReverseHandThrows(4), "THROW_003")
	hr.AssertDetectorFiredN("THROW_003", 4)
	hr.AssertMinSeverity("THROW_003", 0.5)
	hr.AssertMinConfidence("THROW_003", 0.45)
	for _, ev := range hr.Events {
		evd, ok := ev.Evidence.(model.ReleaseAngleEvidence)
		if !ok || evd.ReleaseAngle <= 177.0 {
			t.Errorf("evidence %#v", ev.Evidence)
		}
	}
	runOnly(t, player1().NormalThrowSequence(8), "THROW_003").AssertNoDetections()
	runOnly(t, player1().AimbotThrows(4), "THROW_003").AssertNoDetections()
}

func TestDetector_THROW_004_RepeatedReleaseSignatures(t *testing.T) {
	// UNSAFE, disabled in production. Enabled: a scripted macro fires.
	hr := runOnly(t, player1().MacroThrows(14), "THROW_004")
	hr.AssertDetectorFiredN("THROW_004", 1)
	hr.AssertMinSeverity("THROW_004", 0.99)
	// The documented reason it is unsafe: at production params a human
	// thrower with centimetre release spread and 8-16 m/s speed spread
	// still sits under min_generalized_variance (1e-8 over six dimensions).
	hr = runOnly(t, player1().EliteThrowSequence(14), "THROW_004")
	hr.AssertDetectorFiredN("THROW_004", 1)
	hr = runOnly(t, player1().NormalThrowSequence(14), "THROW_004")
	hr.AssertDetectorFiredN("THROW_004", 1)
}

func TestDetector_THROW_005_SuperhumanPrecision(t *testing.T) {
	hr := runOnly(t, player1().PrecisionAimbot(10), "THROW_005")
	hr.AssertDetectorFiredN("THROW_005", 1)
	hr.AssertMinSeverity("THROW_005", 0.85)
	hr.AssertMinConfidence("THROW_005", 0.4)
	evd, ok := hr.Events[0].Evidence.(model.PrecisionEvidence)
	if !ok || evd.GoalDirectedThrows != 8 || evd.MeanDeviation >= 2.0 || evd.StddevDeviation >= 1.5 {
		t.Errorf("evidence %#v", hr.Events[0].Evidence)
	}
	// Human precision (3-12 degrees) over the same number of throws.
	runOnly(t, player1().NormalThrowSequence(10), "THROW_005").AssertNoDetections()
	runOnly(t, player1().EliteThrowSequence(10), "THROW_005").AssertNoDetections()
}

func TestDetector_THROW_006_TrajectoryCorrection(t *testing.T) {
	// F169: production thresholds, no overrides. The attacked goal is
	// configured as production learns it from the first scored goal.
	hr := testutil.NewHarness(t).WithDetectors("THROW_006").WithBlueGoalSide(1).
		WithMatchContext(matchContextForPlayer("player1")).Run(t, player1().MagnetismCheat(4))
	hr.AssertAllFramesValid(128)
	hr.AssertDetectorFiredN("THROW_006", 4)
	hr.AssertMinSeverity("THROW_006", 0.9)
	hr.AssertMinConfidence("THROW_006", 0.9)
	for _, ev := range hr.Events {
		evd, ok := ev.Evidence.(model.TrajectoryEvidence)
		if !ok || evd.ViolationFrameCount < 5 || evd.AlignmentImprovement <= 0.7 || evd.MaxSingleFrameChange > 15 {
			t.Errorf("evidence %+v", ev.Evidence)
		}
	}
	// Side unknown (no SetBlueGoalSide, no score yet): the flight is judged
	// against BOTH goals and the better alignment improvement is used, so a
	// homing throw released away from its target still fires.
	hr = runOnly(t, player1().MagnetismCheat(4), "THROW_006")
	hr.AssertDetectorFiredN("THROW_006", 4)
	hr.AssertMinSeverity("THROW_006", 0.9)
	for _, ev := range hr.Events {
		evd, ok := ev.Evidence.(model.TrajectoryEvidence)
		if !ok || evd.AlignmentImprovement <= 0.7 {
			t.Errorf("unknown-side evidence %+v", ev.Evidence)
		}
	}
	// Negative: straight throws, with and without the goal side.
	runOnly(t, player1().NormalThrowSequence(8), "THROW_006").AssertNoDetections()
	testutil.NewHarness(t).WithDetectors("THROW_006").WithBlueGoalSide(1).
		WithMatchContext(matchContextForPlayer("player1")).Run(t, player1().EliteThrowSequence(8)).AssertNoDetections()
}

func TestDetector_THROW_007_PenaltyFieldStub(t *testing.T) {
	// STUB: Evaluate returns nil; no penalty-field telemetry exists.
	runOnly(t, player1().AimbotThrows(4), "THROW_007").AssertNoDetections()
	runOnly(t, player1().AcceleratingDisc(3), "THROW_007").AssertNoDetections()
}

func TestDetector_THROW_008_SpeedDistanceAnomaly(t *testing.T) {
	hr := runOnly(t, player1().AcceleratingDisc(3), "THROW_008")
	hr.AssertDetectorFiredN("THROW_008", 3)
	hr.AssertMinSeverity("THROW_008", 0.99)
	hr.AssertMinConfidence("THROW_008", 0.79)
	for _, ev := range hr.Events {
		evd, ok := ev.Evidence.(model.SpeedDistanceEvidence)
		if !ok || evd.SpeedIncreaseCount < 4 || evd.MaxSpeedIncrease < 5.9 {
			t.Errorf("evidence %+v", ev.Evidence)
		}
	}
	runOnly(t, player1().NormalThrowSequence(8), "THROW_008").AssertNoDetections()
	runOnly(t, player1().AimbotThrows(4), "THROW_008").AssertNoDetections()
}

// ---------------------------------------------------------------------
// BIO
// ---------------------------------------------------------------------

func TestDetector_BIO_001_WristRotation(t *testing.T) {
	// Unreachable at the 15 Hz production rate: the quaternion distance
	// saturates at pi per frame = 46.9 rad/s < 50 rad/s.
	d := bio.NewBio001(config.DefaultConfig().GetDetectorConfig("BIO_001").Params)
	if d.Reachable(1.0 / 15.0) {
		t.Fatal("BIO_001 should be unreachable at 15 Hz")
	}
	runOnly(t, player1().WristSpin(100, math.Pi), "BIO_001").AssertNoDetections()
	// At 60 Hz a 1.5 rad/frame spin is 90 rad/s for 8 frames.
	frames := testutil.NewFrameBuilder("player1").WithTickRate(60).WithStartPos(model.Vec3{2, 1.6, 0}).WristSpin(200, 1.5)
	hr := runOnly(t, frames, "BIO_001")
	hr.AssertDetectorFiredN("BIO_001", 1)
	hr.AssertMinSeverity("BIO_001", 0.99)
	hr.AssertMinConfidence("BIO_001", 0.8)
	runOnly(t, player1().FastWristFlick(150), "BIO_001").AssertNoDetections()
}

func TestDetector_BIO_002_HandSpeed(t *testing.T) {
	hr := runOnly(t, player1().HandSpeedHack(100), "BIO_002")
	hr.AssertDetectorFiredN("BIO_002", 1)
	hr.AssertMinSeverity("BIO_002", 0.7)
	hr.AssertMinConfidence("BIO_002", 0.8)
	if hr.Events[0].MergedCount != 3 {
		t.Errorf("expected the 2nd/4th/6th-frame emissions merged into one incident, got %d", hr.Events[0].MergedCount)
	}
	runOnly(t, player1().NormalMovingPlayer(300, 30), "BIO_002").AssertNoDetections()
	runOnly(t, player1().RegrabStackingBurst(150), "BIO_002").AssertNoDetections()
	runOnly(t, player1().EliteThrowSequence(6), "BIO_002").AssertNoDetections()
}

func TestDetector_BIO_003_ZeroHandJitter(t *testing.T) {
	hr := runOnly(t, player1().BotBehavior(600), "BIO_003")
	hr.AssertDetectorFiredN("BIO_003", 2)
	hr.AssertMinSeverity("BIO_003", 0.8)
	hr.AssertMinConfidence("BIO_003", 0.7)
	runOnly(t, player1().SteadyHandPlayer(300), "BIO_003").AssertNoDetections()
	runOnly(t, player1().NormalMovingPlayer(600, 5), "BIO_003").AssertNoDetections()
	// An idle body never arms the activity gate, however still the hands.
	runOnly(t, player1().NormalIdlePlayer(300), "BIO_003").AssertNoDetections()
}

func TestDetector_BIO_004_ZeroAimWobble(t *testing.T) {
	hr := runOnly(t, player1().FrozenAim(600), "BIO_004")
	hr.AssertDetectorFiredN("BIO_004", 2)
	hr.AssertMinSeverity("BIO_004", 0.4)
	runOnly(t, player1().NormalMovingPlayer(600, 5), "BIO_004").AssertNoDetections()
	runOnly(t, player1().FastWristFlick(150), "BIO_004").AssertNoDetections()
}

// ---------------------------------------------------------------------
// MOV
// ---------------------------------------------------------------------

func TestDetector_MOV_001_ImpossiblePlayerSpeed(t *testing.T) {
	hr := runOnly(t, player1().SpeedHackFrames(150, 80), "MOV_001")
	hr.AssertDetectorFiredN("MOV_001", 4)
	hr.AssertMinSeverity("MOV_001", 0.85)
	for _, ev := range hr.Events {
		if ev.CausalKey.AnomalyType != "impossible_speed" {
			t.Errorf("sustained hack reported as %s", ev.CausalKey.AnomalyType)
		}
	}
	hr = runOnly(t, player1().OscillatingSpeedHack(300), "MOV_001")
	hr.AssertDetectorFiredN("MOV_001", 4)
	hr.AssertMinSeverity("MOV_001", 0.6)
	for _, ev := range hr.Events {
		if ev.CausalKey.AnomalyType != "oscillating_speed" {
			t.Errorf("burst hack reported as %s", ev.CausalKey.AnomalyType)
		}
	}
	runOnly(t, player1().RegrabStackingBurst(150), "MOV_001").AssertNoDetections()
	runOnly(t, player1().NormalMovingPlayer(600, 30), "MOV_001").AssertNoDetections()
	runOnly(t, player1().InterpolationArtifact(300), "MOV_001").AssertNoDetections()
}

func TestDetector_MOV_002_Teleportation(t *testing.T) {
	hr := runOnly(t, player1().TeleportCheat(300, 30, 10.0), "MOV_002")
	hr.AssertDetectorFiredN("MOV_002", 5)
	hr.AssertMinConfidence("MOV_002", 0.99)
	hr = runOnly(t, player1().TeleportCheat(600, 70, 11.0), "MOV_002")
	hr.AssertDetectorFiredN("MOV_002", 4)
	hr.AssertMinSeverity("MOV_002", 0.5)
	if hr.Events[0].AutoEnforce {
		t.Error("MOV_002 auto-enforce must be off at the default config")
	}
	runOnly(t, player1().LargeFrameGap(200, 100, 2.0), "MOV_002").AssertNoDetections()
	runOnly(t, player1().RespawnImmunity(200, 100), "MOV_002").AssertNoDetections()
	runOnly(t, player1().OscillatingSpeedHack(300), "MOV_002").AssertNoDetections()
	runOnly(t, player1().NormalMovingPlayer(600, 30), "MOV_002").AssertNoDetections()
}

func TestDetector_MOV_003_ZeroInertia(t *testing.T) {
	// UNSAFE (wall bounces), disabled in production.
	hr := runOnly(t, player1().ZeroInertiaReversals(300), "MOV_003")
	hr.AssertDetectorFiredN("MOV_003", 6)
	hr.AssertMinSeverity("MOV_003", 0.85)
	hr.AssertMinConfidence("MOV_003", 0.6)
	// A legit reversal ramps through zero speed.
	runOnly(t, player1().NormalMovingPlayer(600, 30), "MOV_003").AssertNoDetections()
	runOnly(t, player1().RegrabStackingBurst(150), "MOV_003").AssertNoDetections()
}

func TestDetector_MOV_004_BoostSpeedCap(t *testing.T) {
	// TELEMETRY_DEPENDENT (is_boosting absent from the API), disabled.
	hr := runOnly(t, player1().BoostCapViolation(300), "MOV_004")
	hr.AssertDetectorFiredN("MOV_004", 3)
	hr.AssertMinSeverity("MOV_004", 0.8)
	hr.AssertMinConfidence("MOV_004", 0.8)
	runOnly(t, player1().NormalBoostSequence(4), "MOV_004").AssertNoDetections()
	runOnly(t, player1().RegrabStackingBurst(150), "MOV_004").AssertNoDetections()
}

func TestDetector_MOV_005_BoostSpam(t *testing.T) {
	// TELEMETRY_DEPENDENT, disabled.
	hr := runOnly(t, player1().InfiniteBoost(300), "MOV_005")
	hr.AssertDetectorFiredN("MOV_005", 1)
	hr.AssertMinSeverity("MOV_005", 0.3)
	runOnly(t, player1().NormalBoostSequence(4), "MOV_005").AssertNoDetections()
	runOnly(t, player1().BoostCapViolation(300), "MOV_005").AssertNoDetections()
}

// ---------------------------------------------------------------------
// STATE
// ---------------------------------------------------------------------

func TestDetector_STATE_001_GrabDistance(t *testing.T) {
	hr := runOnly(t, player1().ImpossibleGrabs(4), "STATE_001")
	hr.AssertDetectorFiredN("STATE_001", 3) // the first grab is inside the 5-frame warmup
	hr.AssertMinConfidence("STATE_001", 0.75)
	hr.AssertMinSeverity("STATE_001", 0.3)
	runOnly(t, player1().NormalThrowSequence(8), "STATE_001").AssertNoDetections()
	runOnly(t, player1().EliteThrowSequence(8), "STATE_001").AssertNoDetections()
}

func TestDetector_STATE_002_StunRecovery(t *testing.T) {
	hr := runOnly(t, player1().StunBypass(200), "STATE_002")
	hr.AssertDetectorFiredN("STATE_002", 4)
	hr.AssertMinSeverity("STATE_002", 0.9)
	hr.AssertMinConfidence("STATE_002", 0.6)
	runOnly(t, player1().NormalStunCycle(450, 45), "STATE_002").AssertNoDetections()
	// A stun exactly at the minimum (20 frames) is legitimate.
	runOnly(t, player1().NormalStunCycle(450, 20), "STATE_002").AssertNoDetections()
}

func TestDetector_STATE_003_ShieldDuration(t *testing.T) {
	// TELEMETRY_DEPENDENT (shield_active unconfirmed), disabled.
	hr := runOnly(t, player1().ShieldAbuse(700), "STATE_003")
	hr.AssertDetectorFiredN("STATE_003", 3)
	if s := hr.MaxSeverity("STATE_003"); math.Abs(s-0.95) > 1e-9 {
		t.Errorf("impossible tier severity %.2f", s)
	}
	runOnly(t, player1().NormalShieldCycle(6), "STATE_003").AssertNoDetections()
}

func TestDetector_STATE_004_DamageImmunity(t *testing.T) {
	// TELEMETRY_DEPENDENT (is_immune unconfirmed), disabled.
	hr := runOnly(t, player1().GodMode(600), "STATE_004")
	hr.AssertDetectorFiredN("STATE_004", 4)
	hr.AssertMinSeverity("STATE_004", 0.99)
	hr.AssertMinConfidence("STATE_004", 0.89)
	runOnly(t, player1().RespawnImmunity(200, 100), "STATE_004").AssertNoDetections()
}

func TestDetector_STATE_005_CooldownBypass(t *testing.T) {
	// TELEMETRY_DEPENDENT, disabled.
	hr := runOnly(t, player1().CooldownBypass(20), "STATE_005")
	hr.AssertDetectorFiredN("STATE_005", 1)
	hr.AssertMinSeverity("STATE_005", 0.9)
	if hr.Events[0].MergedCount != 5 {
		t.Errorf("violations 15-19 should merge into one incident, got %d", hr.Events[0].MergedCount)
	}
	runOnly(t, player1().NormalShieldCycle(6), "STATE_005").AssertNoDetections()
}

func TestDetector_STATE_006_ScoreManipulationSuspended(t *testing.T) {
	// SUSPENDED: delta=1 proved legitimate and no other invariant is
	// confirmed, so Evaluate returns nil for every delta.
	for _, delta := range []int{1, 2, 3, 7, 50} {
		runOnly(t, player1().ScoreJump(50, delta), "STATE_006").AssertNoDetections()
	}
}

func TestDetector_STATE_007_PunchRange(t *testing.T) {
	// TELEMETRY_DEPENDENT (per-frame stun stat granularity unconfirmed),
	// disabled. The multi-player version lives in multiplayer_test.go;
	// here: a lone player can never be attributed a victim.
	frames := player1().NormalIdlePlayer(200)
	for i := range frames {
		if i >= 60 {
			frames[i].Stuns = 1 + (i-60)/50
		}
	}
	runOnly(t, frames, "STATE_007").AssertNoDetections()
}

// ---------------------------------------------------------------------
// PAT
// ---------------------------------------------------------------------

func TestDetector_PAT_001_FramePerfectTiming(t *testing.T) {
	// UNSAFE (regrab rhythm), disabled.
	hr := runOnly(t, player1().MacroThrows(14), "PAT_001")
	hr.AssertDetectorFiredN("PAT_001", 1)
	hr.AssertMinSeverity("PAT_001", 0.6)
	runOnly(t, player1().NormalThrowSequence(14), "PAT_001").AssertNoDetections()
}

func TestDetector_PAT_002_IdenticalReleasePoints(t *testing.T) {
	// UNSAFE (consistent form), disabled.
	hr := runOnly(t, player1().MacroThrows(14), "PAT_002")
	hr.AssertDetectorFiredN("PAT_002", 1)
	hr.AssertMinSeverity("PAT_002", 0.5)
	runOnly(t, player1().EliteThrowSequence(14), "PAT_002").AssertNoDetections()
	runOnly(t, player1().NormalThrowSequence(14), "PAT_002").AssertNoDetections()
}

// fakeHistory is a PAT_003 HistoryProvider serving canned prior events.
type fakeHistory struct{ events []model.DetectionEvent }

func (f fakeHistory) GetPlayerDetections(string, int) ([]model.DetectionEvent, error) {
	return f.events, nil
}

func TestDetector_PAT_003_CrossMatchConsistency(t *testing.T) {
	// CROSS_MATCH_DEPENDENT, disabled. Without a provider it is inert.
	runOnly(t, player1().NormalMovingPlayer(60, 3), "PAT_003").AssertNoDetections()

	history := func(shadow bool) fakeHistory {
		var evs []model.DetectionEvent
		for _, m := range []string{"m1", "m2", "m3"} {
			ev := testutil.MakeDetectionEvent("MOV_001", "player1", 0.8, 0.8)
			ev.MatchID = m
			ev.IsShadow = shadow
			evs = append(evs, ev)
		}
		return fakeHistory{evs}
	}
	hr := testutil.NewHarness(t).WithDetectors("PAT_003").WithHistoryProvider(history(false)).
		WithMatchContext(matchContextForPlayer("player1")).Run(t, player1().NormalMovingPlayer(60, 3))
	hr.AssertDetectorFiredN("PAT_003", 1)
	hr.AssertMinSeverity("PAT_003", 0.7)
	hr.AssertMinConfidence("PAT_003", 0.55)
	// Shadow history was never scored and must not be laundered.
	testutil.NewHarness(t).WithDetectors("PAT_003").WithHistoryProvider(history(true)).
		WithMatchContext(matchContextForPlayer("player1")).Run(t, player1().NormalMovingPlayer(60, 3)).AssertNoDetections()
}

func TestDetector_PAT_004_CompositeMultiCheat(t *testing.T) {
	hr := runOnly(t, player1().CompositeCheater(), "MOV_001", "STATE_001", "THROW_001", "PAT_004")
	hr.AssertDetectorFiredN("PAT_004", 1)
	hr.AssertMinSeverity("PAT_004", 0.5)
	evd, ok := hr.DetectorEvents("PAT_004")[0].Evidence.(model.PatternEvidence)
	if !ok || evd.Metrics["category_count"] != 3 {
		t.Errorf("evidence %+v", hr.DetectorEvents("PAT_004")[0].Evidence)
	}
	// Two categories are not enough, and a speed hack alone is one.
	runOnly(t, player1().CompositeCheater(), "MOV_001", "THROW_001", "PAT_004").AssertDetectorNotFired("PAT_004")
	runOnly(t, player1().SpeedHackFrames(150, 80), "MOV_001", "BIO_002", "PAT_004").AssertDetectorNotFired("PAT_004")
}

func TestDetector_PAT_005_PlayspaceAbuse(t *testing.T) {
	hr := runOnly(t, player1().ExtendedReach(100), "PAT_005")
	hr.AssertDetectorFiredN("PAT_005", 3)
	hr.AssertMinConfidence("PAT_005", 0.79)
	hr.AssertMinSeverity("PAT_005", 0.5)
	runOnly(t, player1().NormalThrowSequence(6), "PAT_005").AssertNoDetections()
	runOnly(t, player1().NormalMovingPlayer(300, 5), "PAT_005").AssertNoDetections()
}
