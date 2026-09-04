package throw

import (
	"fmt"
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func testCtx() *model.MatchContext {
	return &model.MatchContext{
		MatchID:         "throw-test",
		PlayerIDs:       []string{"p1", "p2"},
		TeamAssignments: map[string]string{"p1": "blue", "p2": "orange"},
		Physics:         model.DefaultPhysics(),
	}
}

func newState(pid string, frameIdx int) *model.PlayerState {
	return &model.PlayerState{
		PlayerID:     pid,
		Team:         "blue",
		Position:     model.Vec3{5, 0, 0},
		LastFrameIdx: frameIdx,
		FrameDt:      0.067, // replay rate: must not gate anything
	}
}

func mkThrow(pid string, frameIdx int, speed, angle float64) model.ThrowEvent {
	return model.ThrowEvent{
		ThrowerID:           pid,
		Attribution:         model.ThrowAttribution{PlayerID: pid, Confidence: 0.9, Method: "possession_track"},
		FrameIndex:          frameIdx,
		Timestamp:           float64(frameIdx) * 0.067,
		ReleasePosition:     model.Vec3{5, 0, 0},
		ReleaseVelocity:     model.Vec3{speed, 0, 0},
		ReleaseSpeed:        speed,
		ThrowingHand:        "right",
		HandPosition:        model.Vec3{5.3, 0.3, 0},
		HandVelocity:        model.Vec3{speed * 0.5, 0, 0},
		HandSpeed:           speed * 0.5,
		HandKinematicsValid: true,
		WristOrientation:    model.QuatIdentity(),
		PlayerPosition:      model.Vec3{5, 0, 0},
		HandToDiscDistance:  0.3,
		ReleaseAngle:        angle,
		PossessionDuration:  1.0,
	}
}

func withThrow(pid string, frameIdx int, speed, angle float64) map[string]*model.PlayerState {
	ps := newState(pid, frameIdx)
	te := mkThrow(pid, frameIdx, speed, angle)
	ps.LastThrow = &te
	return map[string]*model.PlayerState{pid: ps}
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// ---- THROW_001 ----

func TestThrow001_OverCapFiresAtReplayRate(t *testing.T) {
	d := NewThrow001(nil)
	mc := testCtx()
	events := d.Evaluate(mc, withThrow("p1", 100, 30.0, 0), 100)
	if len(events) != 1 {
		t.Fatalf("expected 1 over-cap event at dt=0.067, got %d", len(events))
	}
	ev := events[0]
	if ev.CausalKey.AnomalyType != "disc_speed" {
		t.Fatalf("anomaly type %q", ev.CausalKey.AnomalyType)
	}
	if ev.Severity < 0.99 || ev.AutoEnforce {
		t.Fatalf("severity=%v autoEnforce=%v; want ~1 and auto-enforce off by default", ev.Severity, ev.AutoEnforce)
	}
	if ev.DetectorVersion != d.Version() || ev.EnforcementWeight != d.Weight {
		t.Fatalf("event must carry the detector's version/weight")
	}
}

func TestThrow001_MissingHandKinematicsCannotInflateSpeedRatio(t *testing.T) {
	d := NewThrow001(nil)
	mc := testCtx()
	players := withThrow("p1", 100, 18.91, 0)
	players["p1"].LastThrow.ThrowingHand = "unknown"
	players["p1"].LastThrow.HandSpeed = 0
	players["p1"].LastThrow.HandKinematicsValid = false

	events := d.Evaluate(mc, players, 100)
	if len(events) != 1 {
		t.Fatalf("expected one slightly-over-cap event, got %d", len(events))
	}
	if events[0].Severity >= 0.7 {
		t.Fatalf("missing hand data inflated severity through a fake ratio: %.3f", events[0].Severity)
	}
	evidence := events[0].Evidence.(model.ThrowEvidence)
	if evidence.SpeedRatio != 0 || evidence.HandKinematicsValid {
		t.Fatalf("missing hand data produced ratio evidence: %+v", evidence)
	}
}

func TestThrow001_SubCapNoEvent(t *testing.T) {
	d := NewThrow001(nil)
	if ev := d.Evaluate(testCtx(), withThrow("p1", 100, 18.9, 0), 100); len(ev) != 0 {
		t.Fatalf("18.9 m/s (at the engine cap) fired: %+v", ev)
	}
}

func TestThrow001_Observed1991FiresEvenAtHighPing(t *testing.T) {
	d := NewThrow001(nil)
	players := withThrow("p1", 100, 19.91, 0)
	players["p1"].EstimatedPingMs = 300
	players["p1"].LastThrow.PlayerVelocity = model.Vec3{3, 0, 0}
	events := d.Evaluate(testCtx(), players, 100)
	if len(events) != 1 || events[0].CausalKey.AnomalyType != "disc_speed" {
		t.Fatalf("19.91 m/s must be reported above the 18.9 m/s cap: %+v", events)
	}
	evidence := events[0].Evidence.(model.ThrowEvidence)
	if !near(evidence.EffectiveCap, 18.9, 1e-9) {
		t.Fatalf("effective cap %.3f, want 18.9", evidence.EffectiveCap)
	}
	if !near(evidence.PlayerSpeed, 3, 1e-9) || !near(evidence.AlignedMovementSpeed, 3, 1e-9) ||
		!near(evidence.PlayerRelativeSpeed, 16.91, 1e-9) {
		t.Fatalf("movement-relative evidence is wrong: %+v", evidence)
	}
}

func TestThrow001_EngineTotalSpeedOverridesUnderCapDiscSample(t *testing.T) {
	d := NewThrow001(nil)
	players := withThrow("p1", 100, 18.7, 0)
	th := players["p1"].LastThrow
	th.SampledDiscSpeed = 18.7
	th.GameLastThrow = &model.GameThrowDetails{
		ArmSpeed: 12.4, TotalSpeed: 19.91, SpeedFromArm: 12,
		SpeedFromMovement: 4.2, SpeedFromWrist: 3.71,
	}
	events := d.Evaluate(testCtx(), players, 100)
	if len(events) != 1 || events[0].CausalKey.AnomalyType != "disc_speed" {
		t.Fatalf("engine-reported 19.91 m/s must fire over an 18.7 m/s sample: %+v", events)
	}
	evidence := events[0].Evidence.(model.ThrowEvidence)
	if evidence.ReleaseSpeed != 19.91 || evidence.SampledDiscSpeed != 18.7 || evidence.GameLastThrow == nil {
		t.Fatalf("engine and sampled evidence were not both preserved: %+v", evidence)
	}
	if evidence.GameLastThrow.SpeedFromMovement != 4.2 {
		t.Fatalf("movement contribution missing from evidence: %+v", evidence.GameLastThrow)
	}
}

func TestThrow001_CorroboratedExtremeSpeedIsNotDowngradedToArtifact(t *testing.T) {
	d := NewThrow001(nil)
	players := withThrow("p1", 100, 20, 0)
	th := players["p1"].LastThrow
	th.GameLastThrow = &model.GameThrowDetails{TotalSpeed: 50, SpeedFromArm: 40, SpeedFromMovement: 5, SpeedFromWrist: 5}
	events := d.Evaluate(testCtx(), players, 100)
	if len(events) != 1 || events[0].CausalKey.AnomalyType != "disc_speed" || events[0].Severity < 0.99 {
		t.Fatalf("engine-corroborated impossible speed was downgraded: %+v", events)
	}
}

func TestThrow001_EngineRecordCannotHideFasterDiscSample(t *testing.T) {
	d := NewThrow001(nil)
	players := withThrow("p1", 100, 25, 0)
	th := players["p1"].LastThrow
	th.SampledDiscSpeed = 25
	th.GameLastThrow = &model.GameThrowDetails{TotalSpeed: 18, SpeedFromArm: 12, SpeedFromMovement: 3, SpeedFromWrist: 3}
	events := d.Evaluate(testCtx(), players, 100)
	if len(events) != 1 || events[0].CausalKey.AnomalyType != "disc_speed" {
		t.Fatalf("a lower engine total must not hide a 25 m/s disc sample: %+v", events)
	}
	if evidence := events[0].Evidence.(model.ThrowEvidence); evidence.ReleaseSpeed != 25 {
		t.Fatalf("evaluated speed = %.2f, want the faster 25 m/s observation", evidence.ReleaseSpeed)
	}

	players = withThrow("p1", 200, 50, 0)
	th = players["p1"].LastThrow
	th.GameLastThrow = &model.GameThrowDetails{TotalSpeed: 18, SpeedFromArm: 12, SpeedFromMovement: 3, SpeedFromWrist: 3}
	events = d.Evaluate(testCtx(), players, 200)
	if len(events) != 1 || events[0].CausalKey.AnomalyType != "disc_speed_artifact" {
		t.Fatalf("an uncorroborated >2x sample should retain artifact handling: %+v", events)
	}
}

func TestThrow001_ArtifactAboveTwiceCapIsReportedNotDropped(t *testing.T) {
	d := NewThrow001(nil)
	mc := testCtx()
	events := d.Evaluate(mc, withThrow("p1", 100, 50.0, 0), 100)
	if len(events) != 1 {
		t.Fatalf("expected an artifact observation, got %d events", len(events))
	}
	ev := events[0]
	if ev.CausalKey.AnomalyType != "disc_speed_artifact" {
		t.Fatalf("anomaly type %q, want disc_speed_artifact", ev.CausalKey.AnomalyType)
	}
	if !near(ev.Severity, artifactSeverity, 1e-9) || ev.AutoEnforce {
		t.Fatalf("artifact must be low severity without auto-enforce: sev=%v ae=%v", ev.Severity, ev.AutoEnforce)
	}
	evd, ok := ev.Evidence.(model.ThrowEvidence)
	if !ok || !evd.ArtifactSuspected || evd.ArtifactCount != 1 {
		t.Fatalf("evidence should flag the artifact: %+v", ev.Evidence)
	}
	d.Evaluate(mc, withThrow("p1", 200, 60.0, 0), 200)
	if d.artifactCounts["p1"] != 2 {
		t.Fatalf("artifact count %d want 2", d.artifactCounts["p1"])
	}
}

func TestThrow001_RepeatedNearCapThrowsRemainLegal(t *testing.T) {
	d := NewThrow001(nil)
	mc := testCtx()
	for i := 0; i < 100; i++ {
		frame := i * 30
		// Repeatability close to the cap can be legitimate player skill. A
		// sub-cap release must never become a detection through repetition.
		if events := d.Evaluate(mc, withThrow("p1", frame, 18.89, 0), frame); len(events) != 0 {
			t.Fatalf("legal near-cap throw %d produced %+v", i, events)
		}
	}
}

func TestThrow001_AutoEnforceOnlyWhenEnabled(t *testing.T) {
	mc := testCtx()
	d := NewThrow001(nil)
	if d.AutoEnforce() {
		t.Fatal("THROW_001 auto-enforce must default to off")
	}
	d.IsAutoEnforce = true
	events := d.Evaluate(mc, withThrow("p1", 100, 30.0, 0), 100)
	if len(events) != 1 || !events[0].AutoEnforce {
		t.Fatalf("with auto-enforce enabled, a 30 m/s possession-tracked release should carry AutoEnforce: %+v", events)
	}
	// Marginal over-cap (< 5 m/s excess) never auto-enforces.
	events = d.Evaluate(mc, withThrow("p1", 200, 23.0, 0), 200)
	if len(events) != 1 || events[0].AutoEnforce {
		t.Fatalf("23 m/s should fire without AutoEnforce: %+v", events)
	}
	// A suspected telemetry artifact (> 2x cap) is an observation, never
	// auto-enforceable, even with the detector flag on.
	events = d.Evaluate(mc, withThrow("p1", 300, 60.0, 0), 300)
	if len(events) != 1 || events[0].CausalKey.AnomalyType != "disc_speed_artifact" {
		t.Fatalf("expected an artifact event: %+v", events)
	}
	if events[0].AutoEnforce {
		t.Fatal("disc_speed_artifact must never carry AutoEnforce")
	}
}

func TestThrow001_ExplicitPingToleranceCanRaiseEffectiveCap(t *testing.T) {
	d := NewThrow001(map[string]any{"ping_tolerance_scalar": 5.0})
	players := withThrow("p1", 100, 20.0, 0)
	players["p1"].EstimatedPingMs = 300 // optional +1.5 m/s tolerance -> cap 20.4
	if ev := d.Evaluate(testCtx(), players, 100); len(ev) != 0 {
		t.Fatalf("explicit legacy ping tolerance should raise the cap: %+v", ev)
	}
}

// ---- THROW_002 ----

func TestThrow002_UsesLastPreReleaseFrame(t *testing.T) {
	d := NewThrow002(nil)
	mc := testCtx()
	players := withThrow("p1", 100, 30.0, 0)
	players["p1"].LastThrow.PreReleaseFrames = []model.ThrowFrameSnapshot{
		{FrameIndex: 98, DiscVelocity: model.Vec3{0, 0, 0.5}},
		{FrameIndex: 99, DiscVelocity: model.Vec3{0, 0, 1.0}},
	}
	events := d.Evaluate(mc, players, 100)
	if len(events) != 1 {
		t.Fatalf("expected a disc_acceleration event, got %d", len(events))
	}
	evd := events[0].Evidence.(model.DiscAccelerationEvidence)
	if !near(evd.SpeedDelta, 29, 1e-9) || !near(evd.PreReleaseSpeed, 1, 1e-9) {
		t.Fatalf("delta=%v pre=%v", evd.SpeedDelta, evd.PreReleaseSpeed)
	}

	// Snapshot without disc data cannot be compared.
	players["p1"].LastThrow.PreReleaseFrames[1].DiscMissing = true
	if ev := d.Evaluate(mc, players, 100); len(ev) != 0 {
		t.Fatal("snapshot flagged disc-missing must be skipped")
	}
	// A snapshot that is the release frame itself is malformed and skipped.
	players["p1"].LastThrow.PreReleaseFrames[1] = model.ThrowFrameSnapshot{FrameIndex: 100, DiscVelocity: model.Vec3{0, 0, 0}}
	if ev := d.Evaluate(mc, players, 100); len(ev) != 0 {
		t.Fatal("snapshot at the release frame must be skipped")
	}
}

// ---- THROW_003 ----

func TestThrow003_BodyMotionGuard(t *testing.T) {
	d := NewThrow003(nil)
	mc := testCtx()
	players := withThrow("p1", 100, 10.0, 179.0)
	players["p1"].LastThrow.HandSpeed = 5
	events := d.Evaluate(mc, players, 100)
	if len(events) != 1 {
		t.Fatalf("179 deg with a stationary body should fire, got %d", len(events))
	}
	if events[0].Confidence <= 0 || events[0].Confidence > events[0].Severity {
		t.Fatalf("confidence %v vs severity %v", events[0].Confidence, events[0].Severity)
	}

	// Body moving as fast as the hand: the world-frame angle is meaningless.
	players["p1"].LastThrow.PlayerVelocity = model.Vec3{0, 0, 6}
	if ev := d.Evaluate(mc, players, 100); len(ev) != 0 {
		t.Fatal("body speed >= hand speed must suppress the release-angle event")
	}
	// Partial body motion scales confidence down.
	players["p1"].LastThrow.PlayerVelocity = model.Vec3{0, 0, 2.5}
	ev := d.Evaluate(mc, players, 100)
	if len(ev) != 1 || !near(ev[0].Confidence, events[0].Confidence*0.5, 1e-9) {
		t.Fatalf("expected confidence halved at body/hand = 0.5, got %+v", ev)
	}

	// New reconstructed events mark a geometrically ambiguous left/right
	// choice explicitly; hand-dependent evidence must not use an arbitrary tie.
	players["p1"].LastThrow.PlayerVelocity = model.Vec3{}
	players["p1"].LastThrow.HandTracked = true
	players["p1"].LastThrow.HandAttributionConfidence = 0
	if ev := d.Evaluate(mc, players, 100); len(ev) != 0 {
		t.Fatal("ambiguous throwing-hand attribution must suppress release-angle evidence")
	}
	players["p1"].LastThrow.HandAttributionConfidence = 0.5
	ev = d.Evaluate(mc, players, 100)
	if len(ev) != 1 || !near(ev[0].Confidence, events[0].Confidence*0.5, 1e-9) {
		t.Fatalf("hand-attribution confidence was not propagated: %+v", ev)
	}

	// A low-rate replay can first show the free disc after it has already hit
	// the player's head. That velocity is not a wrist-release vector.
	players["p1"].LastThrow.PossibleHeadContact = true
	if ev := d.Evaluate(mc, players, 100); len(ev) != 0 {
		t.Fatalf("possible headbutt produced release-angle evidence: %+v", ev)
	}
}

// ---- THROW_004 ----

func feedThrow004(d *Throw004, mc *model.MatchContext, n int, vary bool) []model.DetectionEvent {
	var all []model.DetectionEvent
	for i := 0; i < n; i++ {
		f := float64(i)
		if !vary {
			f = 0
		}
		frame := i * 40
		te := mkThrow("p1", frame, 8+f*0.7, 5+f*3)
		te.HandSpeed = 4 + f*0.4
		te.WristAngularVelocity = 0 // degenerate: identity hand rotation source
		te.PossessionDuration = 1 + f*0.2
		te.HandToDiscDistance = 0.3 + f*0.03
		ps := newState("p1", frame)
		ps.LastThrow = &te
		all = append(all, d.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, frame)...)
	}
	return all
}

func TestThrow004_DegenerateDimensionDoesNotCollapseProduct(t *testing.T) {
	mc := testCtx()
	d := NewThrow004(map[string]any{"min_throws": 6, "min_generalized_variance": 1e-8})
	events := feedThrow004(d, mc, 6, true)
	// Whether the varied player fires depends on the (UNSAFE) threshold; the
	// property under test is that the constant wrist dimension is excluded
	// rather than driving the product to the 1e-15 floor.
	for _, ev := range events {
		evd := ev.Evidence.(model.SignatureRepeatEvidence)
		if evd.InformativeDimensions != 5 || len(evd.DegenerateDimensions) != 1 || evd.DegenerateDimensions[0] != "wrist_angvel" {
			t.Fatalf("expected wrist_angvel excluded, got %+v", evd)
		}
		// The product is exactly the product of the informative dimensions'
		// variances: the constant wrist dimension is not multiplied in.
		want := 1.0
		for i, v := range evd.DimensionVariances {
			if i != 3 {
				want *= v
			}
		}
		if !near(evd.GeneralizedVariance, want, want*1e-9) || evd.GeneralizedVariance < 1e-14 {
			t.Fatalf("product %v want %v (wrist variance %v)", evd.GeneralizedVariance, want, evd.DimensionVariances[3])
		}
		if !near(evd.EffectiveThreshold, math.Pow(1e-8, 5.0/6.0), 1e-15) {
			t.Fatalf("threshold not rescaled to 5 dims: %v", evd.EffectiveThreshold)
		}
	}

	// Identical throws saturate severity; varied throws (if they fire) rank lower.
	d2 := NewThrow004(map[string]any{"min_throws": 6, "min_generalized_variance": 1e-8})
	same := feedThrow004(d2, mc, 6, false)
	// All-identical signatures leave < 3 informative dimensions: not evaluable.
	if len(same) != 0 {
		t.Fatalf("all-constant signature has no informative dimensions and must not fire: %+v", same)
	}
}

func TestThrow004_SeverityRanks(t *testing.T) {
	mc := testCtx()
	d := NewThrow004(map[string]any{"min_throws": 6, "min_generalized_variance": 1e-2})
	// Tight but non-degenerate cluster in every dimension.
	var events []model.DetectionEvent
	for i := 0; i < 6; i++ {
		f := float64(i) * 1e-3
		frame := i * 40
		te := mkThrow("p1", frame, 8+f, 5+f)
		te.HandSpeed = 4 + f
		te.WristAngularVelocity = 1 + f
		te.PossessionDuration = 1 + f
		te.HandToDiscDistance = 0.3 + f
		ps := newState("p1", frame)
		ps.LastThrow = &te
		events = append(events, d.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, frame)...)
	}
	if len(events) != 1 {
		t.Fatalf("expected one signature event, got %d", len(events))
	}
	if events[0].Severity < 0.99 {
		t.Fatalf("near-identical signatures should saturate severity, got %v", events[0].Severity)
	}
	if events[0].Severity == 0.5 {
		t.Fatal("severity is the old constant 0.5")
	}
}

// ---- THROW_005 ----

func feedThrow005(d *Throw005, mc *model.MatchContext, pid string, start, n int, speedOf, devOf func(i int) float64) []model.DetectionEvent {
	var all []model.DetectionEvent
	goal := model.Vec3{0, 0, 36.078}
	for i := 0; i < n; i++ {
		frame := (start + i) * 30
		te := mkThrow(pid, frame, speedOf(i), 5)
		te.TargetPosition = &goal
		te.TargetDeviation = devOf(i)
		ps := newState(pid, frame)
		ps.LastThrow = &te
		all = append(all, d.Evaluate(mc, map[string]*model.PlayerState{pid: ps}, frame)...)
	}
	return all
}

func corrEvents(events []model.DetectionEvent) []model.DetectionEvent {
	var out []model.DetectionEvent
	for _, ev := range events {
		if ev.CausalKey.AnomalyType == "speed_accuracy_correlation" {
			out = append(out, ev)
		}
	}
	return out
}

func TestThrow005_CorrelationGateNeeds30PairsAndFiresOnce(t *testing.T) {
	mc := testCtx()
	d := NewThrow005(nil)
	// Strong negative speed/deviation relationship, deviations 5-15 deg so
	// the precision path stays quiet.
	speed := func(i int) float64 { return 8 + float64(i%10) }
	dev := func(i int) float64 { return 15 - float64(i%10) }
	if ev := corrEvents(feedThrow005(d, mc, "p1", 0, 29, speed, dev)); len(ev) != 0 {
		t.Fatalf("correlation must not fire below %d pairs: %d events", corrMinPairs, len(ev))
	}
	ev := corrEvents(feedThrow005(d, mc, "p1", 29, 1, speed, dev))
	if len(ev) != 1 {
		t.Fatalf("expected the correlation gate to fire at pair 30, got %d", len(ev))
	}
	if ev[0].CausalKey.FrameStart != 0 || ev[0].CausalKey.FrameEnd != 29*30 {
		t.Fatalf("causal key should span the pair window: %+v", ev[0].CausalKey)
	}
	evd := ev[0].Evidence.(model.PrecisionEvidence)
	if evd.PairCount != 30 || evd.CorrelationUpperCI >= corrMaxUpperCI || evd.SpeedAccuracyCorrelation > -0.9 {
		t.Fatalf("evidence %+v", evd)
	}
	// Window resets: the next throws do not re-fire until 30 new pairs.
	if ev := corrEvents(feedThrow005(d, mc, "p1", 30, 29, speed, dev)); len(ev) != 0 {
		t.Fatalf("correlation re-fired within the reset window: %d", len(ev))
	}
}

func TestThrow005_ConstantSpeedIsUndefinedNotZeroCorrelation(t *testing.T) {
	mc := testCtx()
	d := NewThrow005(nil)
	speed := func(int) float64 { return 12 }
	dev := func(i int) float64 { return 5 + float64(i%7) }
	if ev := corrEvents(feedThrow005(d, mc, "p1", 0, 40, speed, dev)); len(ev) != 0 {
		t.Fatalf("identical speeds must not read as zero correlation: %d events", len(ev))
	}
}

func TestThrow005_WeakNoisyCorrelationDoesNotFire(t *testing.T) {
	mc := testCtx()
	d := NewThrow005(nil)
	// True positive relationship with noise: r well above the gate.
	speed := func(i int) float64 { return 8 + float64(i%10) }
	dev := func(i int) float64 { return 4 + float64(i%10)*0.8 + float64((i*7)%5) }
	if ev := corrEvents(feedThrow005(d, mc, "p1", 0, 40, speed, dev)); len(ev) != 0 {
		t.Fatalf("positively correlated human data fired: %+v", ev[0].ObservedValue)
	}
}

func TestThrow005_PrecisionResetsWindow(t *testing.T) {
	mc := testCtx()
	d := NewThrow005(nil)
	speed := func(i int) float64 { return 8 + float64(i) }
	dev := func(i int) float64 { return 0.3 + float64(i)*0.03 }
	events := feedThrow005(d, mc, "p1", 0, 8, speed, dev)
	if len(events) != 1 || events[0].CausalKey.AnomalyType != "target_precision" {
		t.Fatalf("expected one precision event on the 8th throw, got %+v", events)
	}
	if len(d.speedDeviationPairs["p1"]) != 0 {
		t.Fatal("pairs must reset with the precision window")
	}
	if _, ok := d.firstFrame["p1"]; ok {
		t.Fatal("firstFrame must be cleared so the next window re-initializes")
	}
	next := feedThrow005(d, mc, "p1", 8, 1, speed, dev)
	if len(next) != 0 {
		t.Fatalf("no event expected on the first throw of a new window: %+v", next)
	}
	if d.firstFrame["p1"] != 8*30 {
		t.Fatalf("new window should start at frame %d, got %d", 8*30, d.firstFrame["p1"])
	}
}

// ---- disc selection ----

func TestCurrentDisc_Deterministic(t *testing.T) {
	flying := &model.DiscState{Position: model.Vec3{0, 0, 10}, Velocity: model.Vec3{0, 0, 12}, Speed: 12}
	staleHeld := &model.DiscState{Position: model.Vec3{1, 0, 0}, IsHeld: true, PossessorID: "a"}

	// Stale player (left the match) sorts first but must not shadow a fresh copy.
	stale := newState("a", 5)
	stale.CurrentDisc = staleHeld
	stale.HasDisc = true
	fresh := newState("b", 100)
	fresh.CurrentDisc = flying
	players := map[string]*model.PlayerState{"a": stale, "b": fresh}
	for i := 0; i < 50; i++ {
		if got, held := currentDisc(players, 100); got != flying || held {
			t.Fatalf("iteration %d: picked stale disc", i)
		}
	}

	// Among fresh players the possessor's copy wins regardless of sort order.
	holder := newState("z", 100)
	holder.CurrentDisc = &model.DiscState{IsHeld: true, PossessorID: "z"}
	holder.HasDisc = true
	other := newState("a", 100)
	other.CurrentDisc = &model.DiscState{Velocity: model.Vec3{1, 0, 0}}
	players = map[string]*model.PlayerState{"a": other, "z": holder}
	if got, held := currentDisc(players, 100); got != holder.CurrentDisc || !held {
		t.Fatal("possessor's copy should be preferred and reported held")
	}

	// No possessor: first in sorted order.
	b := newState("b", 100)
	b.CurrentDisc = &model.DiscState{Velocity: model.Vec3{2, 0, 0}}
	players = map[string]*model.PlayerState{"b": b, "a": other}
	if got, held := currentDisc(players, 100); got != other.CurrentDisc || held {
		t.Fatal("expected first sorted player's disc, not held")
	}

	// No player with a frame at frameIdx (hand-built states): fall back to all.
	old := newState("q", 3)
	old.CurrentDisc = flying
	if got, held := currentDisc(map[string]*model.PlayerState{"q": old}, 100); got != flying || held {
		t.Fatal("fallback to non-fresh disc failed")
	}
	if got, held := currentDisc(map[string]*model.PlayerState{"q": newState("q", 100)}, 100); got != nil || held {
		t.Fatal("nil expected with no disc")
	}
}

func TestCurrentDisc_HeldFromHasPossessionAlone(t *testing.T) {
	// Contract E: a producer that omits is_held still signals the catch
	// through has_possession. The copy is shared (IsHeld=false everywhere).
	shared := &model.DiscState{Position: model.Vec3{10, 0, 0}, Velocity: model.Vec3{1, 0, 0}, Speed: 1}
	thrower := newState("a", 100)
	thrower.CurrentDisc = shared
	catcher := newState("b", 100)
	catcher.CurrentDisc = shared
	catcher.HasDisc = true
	if got, held := currentDisc(map[string]*model.PlayerState{"a": thrower, "b": catcher}, 100); got != shared || !held {
		t.Fatalf("has_possession alone must report held: got=%v held=%v", got, held)
	}
	// A fresh holder without a disc copy still reports held.
	noCopy := newState("b", 100)
	noCopy.HasDisc = true
	if got, held := currentDisc(map[string]*model.PlayerState{"a": thrower, "b": noCopy}, 100); got != shared || !held {
		t.Fatalf("fresh holder without a copy: got=%v held=%v", got, held)
	}
	// A stale holder (left the match) does not.
	stale := newState("b", 5)
	stale.HasDisc = true
	stale.CurrentDisc = shared
	if _, held := currentDisc(map[string]*model.PlayerState{"a": thrower, "b": stale}, 100); held {
		t.Fatal("stale player's possession must not end tracks")
	}
}

// ---- THROW_006 ----

// bendingFlight feeds a track for pid: release at frame `release` along +X,
// then `frames` frames each rotating the velocity by `degPerFrame` about Y at
// constant speed, positions moving away from the release point.
func bendingFlight(d *Throw006, mc *model.MatchContext, pid string, release, frames int, degPerFrame float64) []model.DetectionEvent {
	var all []model.DetectionEvent
	ps := newState(pid, release)
	te := mkThrow(pid, release, 12, 5)
	te.ReleasePosition = model.Vec3{0, 0, 0}
	te.ReleaseVelocity = model.Vec3{12, 0, 0}
	ps.LastThrow = &te
	ps.CurrentDisc = &model.DiscState{Position: model.Vec3{0, 0, 0}, Velocity: model.Vec3{12, 0, 0}, Speed: 12}
	all = append(all, d.Evaluate(mc, map[string]*model.PlayerState{pid: ps}, release)...)
	for j := 1; j <= frames; j++ {
		frame := release + j
		ang := float64(j) * degPerFrame * math.Pi / 180
		ps := newState(pid, frame)
		ps.CurrentDisc = &model.DiscState{
			Position: model.Vec3{3 * float64(j), 0, 0},
			Velocity: model.Vec3{12 * math.Cos(ang), 0, 12 * math.Sin(ang)},
			Speed:    12,
		}
		all = append(all, d.Evaluate(mc, map[string]*model.PlayerState{pid: ps}, frame)...)
	}
	return all
}

func TestThrow006_RegrabRethrowFinalizesOpenTrack(t *testing.T) {
	mc := testCtx()
	d := NewThrow006(map[string]any{"max_cumulative_change": 50.0})
	if ev := bendingFlight(d, mc, "p1", 0, 8, 10); len(ev) != 0 {
		t.Fatalf("track should still be open, got %d events", len(ev))
	}
	track := d.activeThrows["p1"]
	if track == nil || track.violationFrames < 5 || track.cumulativeAngle <= 50 {
		t.Fatalf("track not built as expected: %+v", track)
	}
	// New throw by the same player while the track is open: previous track
	// is finalized (and qualifies), not silently dropped.
	ps := newState("p1", 9)
	te := mkThrow("p1", 9, 12, 5)
	ps.LastThrow = &te
	ps.CurrentDisc = &model.DiscState{Position: model.Vec3{5, 0, 0}, Velocity: model.Vec3{12, 0, 0}, Speed: 12}
	events := d.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, 9)
	if len(events) != 1 || events[0].FrameRangeStart != 0 {
		t.Fatalf("expected the earlier track's event, got %+v", events)
	}
	if d.activeThrows["p1"].releaseFrame != 9 {
		t.Fatal("new track should replace the finalized one")
	}
	ev := events[0]
	if ev.DetectorVersion != d.Version() || ev.EnforcementWeight != d.Weight {
		t.Fatal("event must carry the detector's version/weight")
	}
	evd := ev.Evidence.(model.TrajectoryEvidence)
	if !near(evd.FinalSpeed, 12, 1e-9) || evd.ViolationFrameCount != 8 {
		t.Fatalf("evidence %+v", evd)
	}
	if ev.Confidence != ev.Severity { // 8 violation frames -> full confidence
		t.Fatalf("confidence %v severity %v", ev.Confidence, ev.Severity)
	}
}

func TestThrow006_ConfidenceScalesWithViolationFrames(t *testing.T) {
	mc := testCtx()
	d := NewThrow006(map[string]any{"max_cumulative_change": 40.0})
	bendingFlight(d, mc, "p1", 0, 5, 10) // 5 violation frames, 50 deg
	held := newState("p1", 6)
	held.CurrentDisc = &model.DiscState{IsHeld: true}
	events := d.Evaluate(mc, map[string]*model.PlayerState{"p1": held}, 6)
	if len(events) != 1 {
		t.Fatalf("expected event on catch, got %d", len(events))
	}
	want := events[0].Severity * 5.0 / fullConfidenceViolationFrames
	if !near(events[0].Confidence, want, 1e-9) {
		t.Fatalf("confidence %v want %v", events[0].Confidence, want)
	}
}

func TestThrow006_GoalFixedAtReleaseNoMidCourtFlip(t *testing.T) {
	mc := testCtx()
	d := NewThrow006(nil)
	goal := model.Vec3{0, 0, 36.078}
	// Straight throw from z=-5 across mid-court toward +Z.
	ps := newState("p1", 0)
	te := mkThrow("p1", 0, 12, 5)
	te.ReleasePosition = model.Vec3{0, 0, -5}
	te.ReleaseVelocity = model.Vec3{0, 0, 12}
	te.GoalPosition = goal
	te.GoalSelection = "angular"
	ps.LastThrow = &te
	ps.CurrentDisc = &model.DiscState{Position: te.ReleasePosition, Velocity: te.ReleaseVelocity, Speed: 12}
	d.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, 0)
	for j := 1; j <= 12; j++ {
		s := newState("p1", j)
		s.CurrentDisc = &model.DiscState{Position: model.Vec3{0, 0, -5 + float64(j)}, Velocity: model.Vec3{0, 0, 12}, Speed: 12}
		if ev := d.Evaluate(mc, map[string]*model.PlayerState{"p1": s}, j); len(ev) != 0 {
			t.Fatalf("straight flight fired at frame %d", j)
		}
	}
	track := d.activeThrows["p1"]
	if track == nil || len(track.goals) != 2 || track.goalLabel != goalLabelUnknownSide {
		t.Fatalf("side unknown: both goals should be candidates, got %+v", track)
	}
	// Both candidates stay fixed at +-GoalZ and neither sees an improvement.
	for _, g := range track.goals {
		if !g.set {
			t.Fatal("alignment should be tracked against every candidate goal")
		}
		if imp := g.improvement(); math.Abs(imp) > 0.01 {
			t.Fatalf("straight flight crossing z=0 fabricated alignment improvement %v against %v", imp, g.pos)
		}
		if math.Abs(g.pos.Z()) != goal.Z() || g.pos.X() != 0 {
			t.Fatalf("goal changed during flight: %v", g.pos)
		}
	}
	if best := track.bestGoal(); best == nil || best.improvement() > 0.01 {
		t.Fatalf("best-of-both improvement %v", best)
	}
}

// homingFlight releases along +X rotated by releaseDeg toward -Z (so the
// release points at the WRONG goal) and then bends the disc by degPerFrame
// toward +Z at constant speed, integrating the position along the velocity.
// It returns every event emitted plus the events from a final catch frame.
func homingFlight(d *Throw006, mc *model.MatchContext, sel string, goalPos model.Vec3, frames int) []model.DetectionEvent {
	var all []model.DetectionEvent
	const dt = 0.25
	release := 0
	dir := func(deg float64) model.Vec3 {
		a := deg * math.Pi / 180
		return model.Vec3{12 * math.Cos(a), 0, 12 * math.Sin(a)}
	}
	ps := newState("p1", release)
	te := mkThrow("p1", release, 12, 5)
	te.ReleasePosition = model.Vec3{0, 0, 0}
	te.ReleaseVelocity = dir(-20)
	te.GoalPosition = goalPos
	te.GoalSelection = sel
	ps.LastThrow = &te
	ps.CurrentDisc = &model.DiscState{Position: te.ReleasePosition, Velocity: te.ReleaseVelocity, Speed: 12}
	all = append(all, d.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, release)...)
	pos := te.ReleasePosition
	for j := 1; j <= frames; j++ {
		vel := dir(-20 + 10*float64(j))
		pos = pos.Add(vel.Scale(dt))
		s := newState("p1", j)
		s.CurrentDisc = &model.DiscState{Position: pos, Velocity: vel, Speed: 12}
		all = append(all, d.Evaluate(mc, map[string]*model.PlayerState{"p1": s}, j)...)
	}
	held := newState("p1", frames+1)
	held.CurrentDisc = &model.DiscState{IsHeld: true, Position: pos}
	held.HasDisc = true
	return append(all, d.Evaluate(mc, map[string]*model.PlayerState{"p1": held}, frames+1)...)
}

func TestThrow006_UnknownSideJudgesAgainstBothGoals(t *testing.T) {
	mc := testCtx()
	gz := mc.Physics.GoalZ
	wrongGoal := model.Vec3{0, 0, -gz}
	rightGoal := model.Vec3{0, 0, gz}

	// Side unknown: the extractor recorded the goal the release pointed at
	// (-Z), but the disc homes onto +Z. Production thresholds (no
	// override): the cumulative bend (~120 deg) stays under 130, so only
	// the alignment gate can fire.
	events := homingFlight(NewThrow006(nil), mc, model.GoalSelectionAngular, wrongGoal, 12)
	if len(events) != 1 {
		t.Fatalf("homing throw with the side unknown must fire once, got %d", len(events))
	}
	evd := events[0].Evidence.(model.TrajectoryEvidence)
	if evd.AlignmentImprovement <= alignmentImprovementGate || evd.CumulativeAngleChange > 130 {
		t.Fatalf("expected the alignment gate to carry the detection: %+v", evd)
	}
	if evd.CorrectionTarget != fmt.Sprintf("goal z=%+.1f (%s)", gz, goalLabelUnknownSide) {
		t.Fatalf("correction target %q", evd.CorrectionTarget)
	}

	// Side known and the attacked goal is +Z: same flight, same verdict,
	// labelled as the team goal.
	events = homingFlight(NewThrow006(nil), mc, model.GoalSelectionTeam, rightGoal, 12)
	if len(events) != 1 {
		t.Fatalf("homing throw with the side known must fire once, got %d", len(events))
	}
	evd = events[0].Evidence.(model.TrajectoryEvidence)
	if evd.CorrectionTarget != fmt.Sprintf("goal z=%+.1f (%s)", gz, model.GoalSelectionTeam) {
		t.Fatalf("correction target %q", evd.CorrectionTarget)
	}

	// Side known and the attacked goal is -Z: the configured/learned side is
	// kept, so a bend toward the OTHER goal is not an alignment improvement
	// (it is still reported by the cumulative-angle gate when large enough,
	// which this flight is not).
	events = homingFlight(NewThrow006(nil), mc, model.GoalSelectionTeam, wrongGoal, 12)
	if len(events) != 0 {
		t.Fatalf("known side must not be replaced by the better goal: %+v", events)
	}
}

func TestThrow006_StaleDiscCopyIgnored(t *testing.T) {
	mc := testCtx()
	d := NewThrow006(map[string]any{"max_cumulative_change": 50.0})
	// A departed player whose last copy says IsHeld would previously end the
	// track on every frame depending on map order.
	stale := newState("a", -100)
	stale.CurrentDisc = &model.DiscState{IsHeld: true, PossessorID: "a"}
	stale.HasDisc = true
	ps := newState("p1", 0)
	te := mkThrow("p1", 0, 12, 5)
	te.ReleasePosition = model.Vec3{0, 0, 0}
	te.ReleaseVelocity = model.Vec3{12, 0, 0}
	ps.LastThrow = &te
	ps.CurrentDisc = &model.DiscState{Position: model.Vec3{0, 0, 0}, Velocity: model.Vec3{12, 0, 0}, Speed: 12}
	d.Evaluate(mc, map[string]*model.PlayerState{"p1": ps, "a": stale}, 0)
	for j := 1; j <= 8; j++ {
		ang := float64(j) * 10 * math.Pi / 180
		s := newState("p1", j)
		s.CurrentDisc = &model.DiscState{Position: model.Vec3{3 * float64(j), 0, 0}, Velocity: model.Vec3{12 * math.Cos(ang), 0, 12 * math.Sin(ang)}, Speed: 12}
		d.Evaluate(mc, map[string]*model.PlayerState{"p1": s, "a": stale}, j)
	}
	if track := d.activeThrows["p1"]; track == nil || track.violationFrames != 8 {
		t.Fatalf("track was disturbed by the stale held copy: %+v", track)
	}
}

func TestThrow006_TrackEndsOnHasPossessionAlone(t *testing.T) {
	// A catcher whose producer omits is_held: the shared disc copy keeps
	// IsHeld=false and only the catcher's has_possession marks the catch.
	// The thrower's track must end there instead of consuming the held
	// disc's motion (a catcher turning toward the goal while holding).
	mc := testCtx()
	d := NewThrow006(nil)
	ps := newState("p1", 0)
	te := mkThrow("p1", 0, 12, 5)
	te.ReleasePosition = model.Vec3{0, 0, 0}
	te.ReleaseVelocity = model.Vec3{12, 0, 0}
	te.GoalPosition = model.Vec3{0, 0, 36}
	te.GoalSelection = "team"
	ps.LastThrow = &te
	ps.CurrentDisc = &model.DiscState{Position: model.Vec3{0, 0, 0}, Velocity: model.Vec3{12, 0, 0}, Speed: 12}
	d.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, 0)
	for j := 1; j <= 3; j++ {
		s := newState("p1", j)
		s.CurrentDisc = &model.DiscState{Position: model.Vec3{3 * float64(j), 0, 0}, Velocity: model.Vec3{12, 0, 0}, Speed: 12}
		d.Evaluate(mc, map[string]*model.PlayerState{"p1": s}, j)
	}
	// Frame 4: p2 catches (has_possession only), shared copy IsHeld=false.
	shared := &model.DiscState{Position: model.Vec3{12, 0, 0}, Velocity: model.Vec3{1, 0, 0}, Speed: 1}
	p1 := newState("p1", 4)
	p1.CurrentDisc = shared
	p2 := newState("p2", 4)
	p2.CurrentDisc = shared
	p2.HasDisc = true
	d.Evaluate(mc, map[string]*model.PlayerState{"p1": p1, "p2": p2}, 4)
	if _, open := d.activeThrows["p1"]; open {
		t.Fatal("track still open after catch signalled only by has_possession")
	}
	// The held disc turning toward the goal must not produce an event.
	for j := 5; j <= 20; j++ {
		ang := float64(j-4) * 10 * math.Pi / 180
		a := newState("p1", j)
		a.CurrentDisc = shared
		b := newState("p2", j)
		b.HasDisc = true
		b.CurrentDisc = &model.DiscState{Position: model.Vec3{12, 0, 0}, Velocity: model.Vec3{1 * math.Cos(ang), 0, 1 * math.Sin(ang)}, Speed: 1}
		if ev := d.Evaluate(mc, map[string]*model.PlayerState{"p1": a, "p2": b}, j); len(ev) != 0 {
			t.Fatalf("held-disc motion produced an event at frame %d: %+v", j, ev)
		}
	}
}

func TestThrow008_TrackEndsOnHasPossessionAlone(t *testing.T) {
	mc := testCtx()
	d := NewThrow008(nil)
	ps := newState("p1", 0)
	te := mkThrow("p1", 0, 8, 5)
	ps.LastThrow = &te
	ps.CurrentDisc = &model.DiscState{Position: model.Vec3{5, 0, 0}, Velocity: model.Vec3{8, 0, 0}, Speed: 8}
	d.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, 0)
	for j := 1; j <= 3; j++ {
		s := newState("p1", j)
		s.CurrentDisc = &model.DiscState{Position: model.Vec3{5 + float64(j), 0, 0}, Velocity: model.Vec3{8, 0, 0}, Speed: 8}
		d.Evaluate(mc, map[string]*model.PlayerState{"p1": s}, j)
	}
	// Frame 4: caught by p2, signalled by has_possession only.
	shared := &model.DiscState{Position: model.Vec3{9, 0, 0}, Velocity: model.Vec3{8, 0, 0}, Speed: 8}
	p1 := newState("p1", 4)
	p1.CurrentDisc = shared
	p2 := newState("p2", 4)
	p2.CurrentDisc = shared
	p2.HasDisc = true
	d.Evaluate(mc, map[string]*model.PlayerState{"p1": p1, "p2": p2}, 4)
	if _, open := d.activeTracks["p1"]; open {
		t.Fatal("track still open after catch signalled only by has_possession")
	}
	// Speed gains of the held disc (catcher accelerating) are never judged.
	for j := 5; j <= 15; j++ {
		spd := 8 + float64(j)*8
		a := newState("p1", j)
		a.CurrentDisc = shared
		b := newState("p2", j)
		b.HasDisc = true
		b.CurrentDisc = &model.DiscState{Position: model.Vec3{9, 0, 0}, Velocity: model.Vec3{spd, 0, 0}, Speed: spd}
		if ev := d.Evaluate(mc, map[string]*model.PlayerState{"p1": a, "p2": b}, j); len(ev) != 0 {
			t.Fatalf("held-disc speed gain produced an event at frame %d: %+v", j, ev)
		}
	}
}

// ---- THROW_008 ----

func TestThrow008_SamplesFromReleaseDistanceAndBounceOnFirstJudgedFrame(t *testing.T) {
	mc := testCtx()
	d := NewThrow008(nil)
	ps := newState("p1", 0)
	te := mkThrow("p1", 0, 12, 5)
	te.ReleasePosition = model.Vec3{0, 0, 0}
	te.ReleaseVelocity = model.Vec3{12, 0, 0}
	ps.LastThrow = &te
	ps.CurrentDisc = &model.DiscState{Position: model.Vec3{0, 0, 0}, Velocity: model.Vec3{12, 0, 0}, Speed: 12}
	d.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, 0)

	// Frames 1-2 straight; frame 3 is a 90 deg deflection with a +8 m/s
	// speed spike (a bounce): must be filtered, not counted as a violation.
	frames := []struct {
		vel model.Vec3
		spd float64
	}{
		{model.Vec3{12, 0, 0}, 12}, {model.Vec3{12, 0, 0}, 12},
		{model.Vec3{0, 0, 20}, 20}, {model.Vec3{0, 0, 19}, 19}, {model.Vec3{0, 0, 18}, 18},
	}
	for j, f := range frames {
		s := newState("p1", j+1)
		s.CurrentDisc = &model.DiscState{Position: model.Vec3{2 * float64(j+1), 0, 0}, Velocity: f.vel, Speed: f.spd}
		if ev := d.Evaluate(mc, map[string]*model.PlayerState{"p1": s}, j+1); len(ev) != 0 {
			t.Fatalf("unexpected event at frame %d", j+1)
		}
	}
	track := d.activeTracks["p1"]
	if track == nil {
		t.Fatal("track missing")
	}
	if track.violations != 0 {
		t.Fatalf("bounce on the first judged frame counted as a violation: %d", track.violations)
	}
	if len(track.samples) != 3 || !near(track.samples[0][0], 6, 1e-9) || !near(track.samples[0][1], 20, 1e-9) {
		t.Fatalf("samples should be (distance from release, speed) for frames >= 3: %v", track.samples)
	}
	if track.trackedFrames != 3 {
		t.Fatalf("tracked frames %d want 3", track.trackedFrames)
	}
}

func TestThrow008_RegrabRethrowFinalizesOpenTrack(t *testing.T) {
	mc := testCtx()
	d := NewThrow008(nil)
	ps := newState("p1", 0)
	te := mkThrow("p1", 0, 8, 5)
	ps.LastThrow = &te
	ps.CurrentDisc = &model.DiscState{Position: model.Vec3{5, 0, 0}, Velocity: model.Vec3{8, 0, 0}, Speed: 8}
	d.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, 0)
	for j := 1; j <= 10; j++ {
		spd := 8 + float64(j)*8
		s := newState("p1", j)
		s.CurrentDisc = &model.DiscState{Position: model.Vec3{5 + float64(j), 0, 0}, Velocity: model.Vec3{spd, 0, 0}, Speed: spd}
		d.Evaluate(mc, map[string]*model.PlayerState{"p1": s}, j)
	}
	if d.activeTracks["p1"].violations < 4 {
		t.Fatalf("expected >= 4 violations, got %d", d.activeTracks["p1"].violations)
	}
	// Re-throw at frame 11 while the track is open.
	ps2 := newState("p1", 11)
	te2 := mkThrow("p1", 11, 8, 5)
	ps2.LastThrow = &te2
	ps2.CurrentDisc = &model.DiscState{Position: model.Vec3{5, 0, 0}, Velocity: model.Vec3{8, 0, 0}, Speed: 8}
	events := d.Evaluate(mc, map[string]*model.PlayerState{"p1": ps2}, 11)
	if len(events) != 1 || events[0].FrameRangeStart != 0 || events[0].DetectorVersion != d.Version() {
		t.Fatalf("expected the earlier track's event, got %+v", events)
	}
	if d.activeTracks["p1"].releaseFrame != 11 {
		t.Fatal("new track should replace the finalized one")
	}
}

// ---- match-end flush ----

func TestFlushTracks_FinalizesOpenTracksAtMatchEnd(t *testing.T) {
	mc := testCtx()
	d6 := NewThrow006(map[string]any{"max_cumulative_change": 50.0})
	bendingFlight(d6, mc, "p1", 0, 8, 10)
	events := d6.FlushTracks(mc, 8)
	if len(events) != 1 || events[0].DetectorID != "THROW_006" || events[0].FrameRangeEnd != 8 {
		t.Fatalf("THROW_006 flush: %+v", events)
	}
	if len(d6.activeThrows) != 0 {
		t.Fatal("THROW_006 tracks not cleared by flush")
	}

	d8 := NewThrow008(nil)
	ps := newState("p1", 0)
	te := mkThrow("p1", 0, 8, 5)
	ps.LastThrow = &te
	ps.CurrentDisc = &model.DiscState{Position: model.Vec3{5, 0, 0}, Velocity: model.Vec3{8, 0, 0}, Speed: 8}
	d8.Evaluate(mc, map[string]*model.PlayerState{"p1": ps}, 0)
	for j := 1; j <= 10; j++ {
		spd := 8 + float64(j)*8
		s := newState("p1", j)
		s.CurrentDisc = &model.DiscState{Position: model.Vec3{5 + float64(j), 0, 0}, Velocity: model.Vec3{spd, 0, 0}, Speed: spd}
		d8.Evaluate(mc, map[string]*model.PlayerState{"p1": s}, j)
	}
	events = d8.FlushTracks(mc, 10)
	if len(events) != 1 || events[0].DetectorID != "THROW_008" {
		t.Fatalf("THROW_008 flush: %+v", events)
	}
	if len(d8.activeTracks) != 0 {
		t.Fatal("THROW_008 tracks not cleared by flush")
	}
	if ev := d8.FlushTracks(mc, 11); len(ev) != 0 {
		t.Fatal("second flush must be a no-op")
	}
}
