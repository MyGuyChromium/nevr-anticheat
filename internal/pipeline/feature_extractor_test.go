package pipeline

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func feTestCtx() *model.MatchContext {
	return &model.MatchContext{
		MatchID:         "fe-test",
		PlayerIDs:       []string{"p1", "p2"},
		TeamAssignments: map[string]string{"p1": "blue", "p2": "orange"},
		Physics:         model.DefaultPhysics(),
	}
}

func feFrame(pid string, idx int, ts float64, pos model.Vec3) model.PlayerTelemetryFrame {
	return model.PlayerTelemetryFrame{
		PlayerID:          pid,
		FrameIndex:        idx,
		Timestamp:         ts,
		Position:          pos,
		Rotation:          model.QuatIdentity(),
		LeftHandPosition:  pos.Add(model.Vec3{-0.3, 0.3, 0.2}),
		RightHandPosition: pos.Add(model.Vec3{0.3, 0.3, -0.2}),
		LeftHandRotation:  model.QuatIdentity(),
		RightHandRotation: model.QuatIdentity(),
		GamePhase:         "playing",
		Disc:              &model.DiscState{Position: model.Vec3{0, 2, 0}},
	}
}

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func vecPtr(v model.Vec3) *model.Vec3 { return &v }

func TestExtractor_PlayspaceMotionSubtractsGameVelocity(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p1"}
	dt := 0.067

	// Pure game motion (including stacking) advances the pose exactly by the
	// raw velocity and therefore leaves no physical playspace residual.
	for i := 0; i < 4; i++ {
		pos := model.Vec3{1 + 2*dt*float64(i), 1.6, 1}
		f := feFrame("p1", i, dt*float64(i), pos)
		f.ReportedVelocity = vecPtr(model.Vec3{2, 0, 0})
		fe.UpdatePlayerState(ps, &f, mc)
	}
	if !ps.PlayspaceValid || ps.PlayspaceSpeed > 1e-9 || ps.PlayspaceDistance > 1e-9 || ps.MovementOrigin != "game_velocity" {
		t.Fatalf("pure game motion classified as playspace: %+v", ps)
	}
}

func TestExtractor_PlayspaceWalkMovesWholeTrackedRig(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p1"}
	dt := 0.067

	// The game contributes 2 m/s while the player physically steps another
	// 0.10 m per tick. Head and both hands translate together.
	for i := 0; i < 5; i++ {
		pos := model.Vec3{1 + (2*dt+0.10)*float64(i), 1.6, 1}
		f := feFrame("p1", i, dt*float64(i), pos)
		f.ReportedVelocity = vecPtr(model.Vec3{2, 0, 0})
		fe.UpdatePlayerState(ps, &f, mc)
	}
	if !ps.PlayspaceValid || ps.PlayspaceSpeed < 1.45 || ps.PlayspaceDistance < 0.35 ||
		ps.PlayspaceRigCoherence < 0.99 || ps.PlayspaceTrackedHands != 2 || ps.MovementOrigin != "mixed" {
		t.Fatalf("physical step was not isolated: speed=%.3f distance=%.3f coherence=%.3f hands=%d origin=%q",
			ps.PlayspaceSpeed, ps.PlayspaceDistance, ps.PlayspaceRigCoherence, ps.PlayspaceTrackedHands, ps.MovementOrigin)
	}
}

func TestExtractor_PlayspaceRequiresRawVelocityAndContinuousSamples(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p1"}
	f0 := feFrame("p1", 0, 0, model.Vec3{1, 1.6, 1})
	f0.ReportedVelocity = vecPtr(model.Vec3{})
	fe.UpdatePlayerState(ps, &f0, mc)
	f1 := feFrame("p1", 1, 0.067, model.Vec3{1.1, 1.6, 1})
	fe.UpdatePlayerState(ps, &f1, mc)
	if ps.PlayspaceValid {
		t.Fatal("missing raw player.velocity must disable playspace reconstruction")
	}
	f2 := feFrame("p1", 2, 0.2, model.Vec3{1.2, 1.6, 1})
	f2.ReportedVelocity = vecPtr(model.Vec3{})
	fe.UpdatePlayerState(ps, &f2, mc)
	if ps.PlayspaceValid || ps.PlayspaceDistance != 0 {
		t.Fatal("sample gap at or above 100 ms must reset the playspace anchor")
	}
}

// throwSequence feeds `hold` frames of possession with a distinct disc
// velocity per frame, then a release frame with the given velocity.
// Returns the release frame index.
func throwSequence(fe *FeatureExtractor, ps *model.PlayerState, mc *model.MatchContext, pos model.Vec3, hold int, releaseVel model.Vec3, phase string, startIdx int, dt float64) int {
	for i := 0; i < hold; i++ {
		idx := startIdx + i
		f := feFrame(ps.PlayerID, idx, float64(idx)*dt, pos)
		f.HasPossession = true
		f.Disc = &model.DiscState{
			Position:    pos.Add(model.Vec3{0.4, 0.2, 0.01 * float64(i)}),
			Velocity:    model.Vec3{0, 0, float64(i)},
			Speed:       float64(i),
			PossessorID: ps.PlayerID,
			IsHeld:      true,
		}
		fe.UpdatePlayerState(ps, &f, mc)
	}
	idx := startIdx + hold
	f := feFrame(ps.PlayerID, idx, float64(idx)*dt, pos)
	f.HasPossession = false
	f.GamePhase = phase
	f.RightHandPosition = pos.Add(model.Vec3{0.8, 0.5, -0.1})
	f.Disc = &model.DiscState{
		Position: pos.Add(model.Vec3{1, 0.2, 0}),
		Velocity: releaseVel,
		Speed:    releaseVel.Magnitude(),
	}
	fe.UpdatePlayerState(ps, &f, mc)
	return idx
}

func TestExtractor_DtClampingAndUnknown(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p1"}

	f0 := feFrame("p1", 0, 0, model.Vec3{0, 1, 0})
	fe.UpdatePlayerState(ps, &f0, mc)
	if ps.FrameDt != 0 {
		t.Fatalf("first frame dt must be unknown (0), got %v", ps.FrameDt)
	}

	// Normal step: 0.1 s, 1 m in X -> 10 m/s.
	f1 := feFrame("p1", 1, 0.1, model.Vec3{1, 1, 0})
	fe.UpdatePlayerState(ps, &f1, mc)
	if !approx(ps.FrameDt, 0.1, 1e-9) || !approx(ps.Speed, 10, 1e-9) {
		t.Fatalf("dt=%v speed=%v, want 0.1 / 10", ps.FrameDt, ps.Speed)
	}

	// Tiny step clamps to MinFrameDt.
	f2 := feFrame("p1", 2, 0.101, model.Vec3{1.001, 1, 0})
	fe.UpdatePlayerState(ps, &f2, mc)
	if !approx(ps.FrameDt, MinFrameDt, 1e-12) {
		t.Fatalf("tiny dt should clamp to %v, got %v", MinFrameDt, ps.FrameDt)
	}
	if !approx(ps.Speed, 0.001/MinFrameDt, 1e-9) {
		t.Fatalf("speed should use clamped dt, got %v", ps.Speed)
	}

	// Large gap (> MaxFrameDt): dt clamps to MaxFrameDt, kinematics cleared.
	f3 := feFrame("p1", 3, 1.2, model.Vec3{5, 1, 0})
	fe.UpdatePlayerState(ps, &f3, mc)
	if !approx(ps.FrameDt, MaxFrameDt, 1e-12) {
		t.Fatalf("large gap dt should clamp to %v, got %v", MaxFrameDt, ps.FrameDt)
	}
	if ps.Speed != 0 || !ps.Velocity.IsZero() || ps.RightHandSpeed != 0 {
		t.Fatalf("kinematics must be cleared across a large gap: speed=%v vel=%v", ps.Speed, ps.Velocity)
	}
	if ps.SpeedHistory[len(ps.SpeedHistory)-1] != 0 || !ps.VelocityHistory[len(ps.VelocityHistory)-1].IsZero() {
		t.Fatalf("stale kinematics leaked into histories: %v", ps.SpeedHistory)
	}

	// In-range step after the gap recovers.
	f4 := feFrame("p1", 4, 1.4, model.Vec3{5.4, 1, 0})
	fe.UpdatePlayerState(ps, &f4, mc)
	if !approx(ps.FrameDt, 0.2, 1e-9) || !approx(ps.Speed, 2, 1e-9) {
		t.Fatalf("dt=%v speed=%v after gap, want 0.2 / 2", ps.FrameDt, ps.Speed)
	}

	// Duplicate timestamp: unknown dt, no kinematics.
	f5 := feFrame("p1", 5, 1.4, model.Vec3{6, 1, 0})
	fe.UpdatePlayerState(ps, &f5, mc)
	if ps.FrameDt != 0 || ps.Speed != 0 {
		t.Fatalf("duplicate timestamp must be unknown: dt=%v speed=%v", ps.FrameDt, ps.Speed)
	}
	// Out-of-order timestamp: unknown dt.
	f6 := feFrame("p1", 6, 1.3, model.Vec3{6.5, 1, 0})
	fe.UpdatePlayerState(ps, &f6, mc)
	if ps.FrameDt != 0 || ps.Speed != 0 {
		t.Fatalf("out-of-order timestamp must be unknown: dt=%v speed=%v", ps.FrameDt, ps.Speed)
	}
	// Histories stay aligned regardless.
	if len(ps.PositionHistory) != 7 || len(ps.DiscVelocityHistory) != 7 || len(ps.TimestampHistory) != 7 {
		t.Fatalf("history lengths diverged: pos=%d disc=%d ts=%d", len(ps.PositionHistory), len(ps.DiscVelocityHistory), len(ps.TimestampHistory))
	}
}

func TestExtractor_NoFixedTickRateAssumption(t *testing.T) {
	// Same displacement at 60 fps and 15 fps must give speeds in the ratio of
	// the real dt, not a constant.
	mc := feTestCtx()
	for _, dt := range []float64{1.0 / 60.0, 0.067, 0.25} {
		fe := NewFeatureExtractor(30)
		ps := &model.PlayerState{PlayerID: "p1"}
		f0 := feFrame("p1", 0, 0, model.Vec3{0, 1, 0})
		fe.UpdatePlayerState(ps, &f0, mc)
		f1 := feFrame("p1", 1, dt, model.Vec3{0.5, 1, 0})
		fe.UpdatePlayerState(ps, &f1, mc)
		if !approx(ps.Speed, 0.5/dt, 1e-6) {
			t.Errorf("dt=%v: speed=%v want %v", dt, ps.Speed, 0.5/dt)
		}
	}
}

func TestExtractor_SeparatesWorldAndPlayerRelativeHandSpeed(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p1"}

	f0 := feFrame("p1", 0, 0, model.Vec3{1, 1, 0})
	fe.UpdatePlayerState(ps, &f0, mc)
	f1 := feFrame("p1", 1, 0.1, model.Vec3{2, 1, 0})
	fe.UpdatePlayerState(ps, &f1, mc)
	if !approx(ps.LeftHandSpeed, 10, 1e-9) || !approx(ps.LeftHandRelativeSpeed, 0, 1e-9) {
		t.Fatalf("hand following body: world=%v relative=%v, want 10/0", ps.LeftHandSpeed, ps.LeftHandRelativeSpeed)
	}

	f2 := feFrame("p1", 2, 0.2, model.Vec3{3, 1, 0})
	f2.RightHandPosition[0] += 1 // another 10 m/s relative to the body
	fe.UpdatePlayerState(ps, &f2, mc)
	if !approx(ps.RightHandSpeed, 20, 1e-9) || !approx(ps.RightHandRelativeSpeed, 10, 1e-9) {
		t.Fatalf("independent hand motion: world=%v relative=%v, want 20/10", ps.RightHandSpeed, ps.RightHandRelativeSpeed)
	}
}

func TestExtractor_HighPingThreshold(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p1"}
	f := feFrame("p1", 0, 0, model.Vec3{0, 1, 0})
	f.EstimatedPingMs = 160
	fe.UpdatePlayerState(ps, &f, mc)
	if !ps.IsHighPing {
		t.Fatal("160 ms should be high ping at the default 150 ms threshold")
	}
	fe.SetHighPingThreshold(200)
	fe.UpdatePlayerState(ps, &f, mc)
	if ps.IsHighPing {
		t.Fatal("160 ms should not be high ping at a 200 ms threshold")
	}
	fe.SetHighPingThreshold(0)
	if fe.HighPingThreshold() != DefaultHighPingThresholdMs {
		t.Fatalf("non-positive threshold should restore default, got %v", fe.HighPingThreshold())
	}
}

func TestExtractor_ThrowSnapshotsExcludeReleaseFrame(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p1", Team: "blue"}
	pos := model.Vec3{0, 1, -10}
	releaseVel := model.Vec3{0, 0, 15}
	release := throwSequence(fe, ps, mc, pos, 10, releaseVel, "playing", 0, 0.067)

	if ps.LastThrow == nil || ps.ThrowCount != 1 {
		t.Fatalf("expected one throw, got count=%d last=%v", ps.ThrowCount, ps.LastThrow)
	}
	th := ps.LastThrow
	if th.FrameIndex != release || th.ReleaseSpeed != 15 || th.ThrowingHand != "right" {
		t.Fatalf("throw frame=%d speed=%v hand=%s", th.FrameIndex, th.ReleaseSpeed, th.ThrowingHand)
	}
	if len(th.PreReleaseFrames) != 5 {
		t.Fatalf("expected 5 pre-release snapshots, got %d", len(th.PreReleaseFrames))
	}
	for k, snap := range th.PreReleaseFrames {
		wantIdx := release - 5 + k
		if snap.FrameIndex != wantIdx {
			t.Errorf("snapshot %d labelled frame %d, want %d", k, snap.FrameIndex, wantIdx)
		}
		if snap.FrameIndex >= release {
			t.Errorf("snapshot %d includes the release frame", k)
		}
		if !approx(snap.Timestamp, float64(wantIdx)*0.067, 1e-9) {
			t.Errorf("snapshot %d timestamp %v want %v", k, snap.Timestamp, float64(wantIdx)*0.067)
		}
		// Disc velocity/position are the held-frame values of that frame.
		if !approx(snap.DiscVelocity.Z(), float64(wantIdx), 1e-9) {
			t.Errorf("snapshot %d disc velocity z=%v want %v", k, snap.DiscVelocity.Z(), float64(wantIdx))
		}
		wantDiscPos := pos.Add(model.Vec3{0.4, 0.2, 0.01 * float64(wantIdx)})
		if snap.DiscPosition.Distance(wantDiscPos) > 1e-9 {
			t.Errorf("snapshot %d disc position %v want %v", k, snap.DiscPosition, wantDiscPos)
		}
		if snap.DiscMissing {
			t.Errorf("snapshot %d flagged disc missing", k)
		}
		// Throwing hand (right) position of that frame, not the left hand.
		wantHand := pos.Add(model.Vec3{0.3, 0.3, -0.2})
		if snap.HandPosition.Distance(wantHand) > 1e-9 {
			t.Errorf("snapshot %d hand position %v want right hand %v", k, snap.HandPosition, wantHand)
		}
		if !snap.HandRotation.IsUnit() {
			t.Errorf("snapshot %d hand rotation not populated", k)
		}
	}
	last := th.PreReleaseFrames[4]
	if last.DiscVelocity.Magnitude() == th.ReleaseSpeed {
		t.Fatal("last pre-release snapshot must not carry the release velocity")
	}
	if delta := th.ReleaseSpeed - last.DiscVelocity.Magnitude(); !approx(delta, 15-9, 1e-9) {
		t.Fatalf("THROW_002 delta should be release - frame N-1 = 6, got %v", delta)
	}
}

func TestExtractor_ThrowingHandUsesPriorHeldDiscAnchor(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p1"}
	pos := model.Vec3{1, 1, 0}
	var previousLeft model.Vec3
	for i := 0; i < 3; i++ {
		f := feFrame("p1", i, float64(i)*0.067, pos)
		f.HasPossession = true
		f.Disc = &model.DiscState{Position: f.RightHandPosition, IsHeld: true, PossessorID: "p1"}
		previousLeft = f.LeftHandPosition
		fe.UpdatePlayerState(ps, &f, mc)
	}
	// The first free-disc sample is deliberately nearer the left hand. The
	// last held-disc sample still proves that the right hand released it.
	release := feFrame("p1", 3, 3*0.067, pos)
	release.Disc = &model.DiscState{Position: previousLeft, Velocity: model.Vec3{0, 0, 12}, Speed: 12}
	fe.UpdatePlayerState(ps, &release, mc)
	got := ps.LastThrow
	if got == nil {
		t.Fatal("release was not reconstructed")
	}
	if got.ThrowingHand != "right" || got.HandAttributionAnchor != "previous_held_disc" {
		t.Fatalf("hand=%q anchor=%q, want right/previous_held_disc", got.ThrowingHand, got.HandAttributionAnchor)
	}
	if got.HandToDiscDistance > 1e-9 || got.HandAttributionConfidence < 0.9 {
		t.Fatalf("aligned hand evidence distance=%v confidence=%v", got.HandToDiscDistance, got.HandAttributionConfidence)
	}
	if got.PossibleHeadContact {
		t.Fatal("release sampled at a tracked hand was classified as a head contact")
	}
}

func TestExtractor_MarksPossibleHeadContactAfterSampledRelease(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p1"}
	pos := model.Vec3{1, 1, 0}
	for i := 0; i < 3; i++ {
		f := feFrame("p1", i, float64(i)*0.067, pos)
		f.HasPossession = true
		f.Disc = &model.DiscState{Position: f.RightHandPosition, IsHeld: true, PossessorID: "p1"}
		fe.UpdatePlayerState(ps, &f, mc)
	}

	// The recorder did not sample the instant of release. On its next tick the
	// free disc is beside the head and farther from both controllers, which
	// means its velocity may already include a headbutt.
	release := feFrame("p1", 3, 3*0.067, pos)
	release.LeftHandPosition = pos.Add(model.Vec3{-0.7, 0.2, 0})
	release.RightHandPosition = pos.Add(model.Vec3{0.7, 0.2, 0})
	release.Disc = &model.DiscState{
		Position: pos.Add(model.Vec3{0, 0.08, 0.1}),
		Velocity: model.Vec3{0, 0, -12}, Speed: 12,
	}
	fe.UpdatePlayerState(ps, &release, mc)
	got := ps.LastThrow
	if got == nil {
		t.Fatal("release was not reconstructed")
	}
	if !got.PossibleHeadContact {
		t.Fatalf("head contact not marked: head distance=%v hand distance=%v", got.HeadToDiscDistance, got.ReleaseHandToDiscDistance)
	}
	if got.HeadToDiscDistance >= got.ReleaseHandToDiscDistance {
		t.Fatalf("bad contact geometry: head distance=%v hand distance=%v", got.HeadToDiscDistance, got.ReleaseHandToDiscDistance)
	}
}

func TestExtractor_MissingHandsRemainUnknownOnThrow(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p1"}
	pos := model.Vec3{1, 1, 0}
	for i := 0; i < 3; i++ {
		f := feFrame("p1", i, float64(i)*0.067, pos)
		f.LeftHandPosition = model.Vec3{}
		f.RightHandPosition = model.Vec3{}
		f.HasPossession = true
		f.Disc = &model.DiscState{Position: pos, IsHeld: true, PossessorID: "p1"}
		fe.UpdatePlayerState(ps, &f, mc)
	}
	release := feFrame("p1", 3, 3*0.067, pos)
	release.LeftHandPosition = model.Vec3{}
	release.RightHandPosition = model.Vec3{}
	release.Disc = &model.DiscState{Position: pos.Add(model.Vec3{0, 0, 0.2}), Velocity: model.Vec3{0, 0, 12}, Speed: 12}
	fe.UpdatePlayerState(ps, &release, mc)
	got := ps.LastThrow
	if got == nil || got.ThrowingHand != "unknown" || got.HandTracked || got.HandAttributionConfidence != 0 {
		t.Fatalf("missing tracking fabricated a throwing hand: %+v", got)
	}
	for _, snap := range got.PreReleaseFrames {
		if !snap.HandPosition.IsZero() || !snap.HandVelocity.IsZero() {
			t.Fatalf("unknown-hand snapshot contains fabricated hand evidence: %+v", snap)
		}
	}
}

func TestSelectThrowingHand_OneTrackedHandIsStillAmbiguous(t *testing.T) {
	hand, _, _, confidence := selectThrowingHand(
		model.Vec3{1, 1, 1}, model.Vec3{}, model.Vec3{1, 1, 1}, 1,
	)
	if hand != "unknown" || confidence != 0 {
		t.Fatalf("one tracked hand cannot exclude the missing hand: hand=%q confidence=%v", hand, confidence)
	}
}

func TestExtractor_DiscHistoryAlignedWhenDiscMissing(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p1"}
	pos := model.Vec3{0, 1, -10}
	// Frames 0-8 held with disc, frame 9 held without disc data, release at 10.
	for i := 0; i < 10; i++ {
		f := feFrame("p1", i, float64(i)*0.067, pos)
		f.HasPossession = true
		if i == 9 {
			f.Disc = nil
		} else {
			f.Disc = &model.DiscState{Position: pos.Add(model.Vec3{0.4, 0.2, 0}), Velocity: model.Vec3{0, 0, float64(i)}, Speed: float64(i), IsHeld: true, PossessorID: "p1"}
		}
		fe.UpdatePlayerState(ps, &f, mc)
	}
	if len(ps.DiscVelocityHistory) != len(ps.PositionHistory) {
		t.Fatalf("disc velocity history (%d) not aligned with position history (%d)", len(ps.DiscVelocityHistory), len(ps.PositionHistory))
	}
	f := feFrame("p1", 10, 10*0.067, pos)
	f.HasPossession = false
	f.Disc = &model.DiscState{Position: pos.Add(model.Vec3{1, 0.2, 0}), Velocity: model.Vec3{0, 0, 15}, Speed: 15}
	fe.UpdatePlayerState(ps, &f, mc)
	th := ps.LastThrow
	if th == nil || len(th.PreReleaseFrames) != 5 {
		t.Fatalf("expected throw with 5 snapshots, got %v", th)
	}
	if !th.PreReleaseFrames[4].DiscMissing {
		t.Fatal("snapshot for the disc-less frame must be flagged missing")
	}
	if th.PreReleaseFrames[3].DiscMissing || !approx(th.PreReleaseFrames[3].DiscVelocity.Z(), 8, 1e-9) {
		t.Fatalf("snapshot for frame 8 should carry velocity z=8, got %v (missing=%v)", th.PreReleaseFrames[3].DiscVelocity, th.PreReleaseFrames[3].DiscMissing)
	}
}

func TestExtractor_NonActivePhaseReleaseIsNotAThrow(t *testing.T) {
	mc := feTestCtx()
	for _, tc := range []struct {
		phase string
		want  int
	}{
		{"round_over", 0}, {"score", 0}, {"pre_match", 0}, {"playing", 1}, {"", 1},
	} {
		fe := NewFeatureExtractor(30)
		ps := &model.PlayerState{PlayerID: "p1"}
		throwSequence(fe, ps, mc, model.Vec3{0, 1, -10}, 6, model.Vec3{0, 0, 15}, tc.phase, 0, 0.067)
		if ps.ThrowCount != tc.want {
			t.Errorf("phase %q: throw count %d want %d", tc.phase, ps.ThrowCount, tc.want)
		}
	}
}

func TestExtractor_PossessionFlickerIsNotAThrow(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p1"}
	pos := model.Vec3{0, 1, -10}
	f0 := feFrame("p1", 0, 0, pos)
	fe.UpdatePlayerState(ps, &f0, mc)
	f1 := feFrame("p1", 1, 0.067, pos)
	f1.HasPossession = true
	f1.Disc = &model.DiscState{Position: pos, IsHeld: true, PossessorID: "p1"}
	fe.UpdatePlayerState(ps, &f1, mc)
	f2 := feFrame("p1", 2, 0.134, pos)
	f2.Disc = &model.DiscState{Position: pos, Velocity: model.Vec3{0, 0, 15}, Speed: 15}
	fe.UpdatePlayerState(ps, &f2, mc)
	if ps.ThrowCount != 0 {
		t.Fatalf("1-frame possession flicker counted as a throw")
	}
}

func TestExtractor_GoalSelection(t *testing.T) {
	goalZ := model.DefaultPhysics().GoalZ
	if goalZ != 36.078 {
		t.Fatalf("DefaultPhysics.GoalZ = %v, want 36.078", goalZ)
	}
	mc := feTestCtx()

	t.Run("angular fallback when side unknown", func(t *testing.T) {
		fe := NewFeatureExtractor(30)
		ps := &model.PlayerState{PlayerID: "p1", Team: "blue"}
		// Released at z=-10 toward +Z: the +Z goal is chosen even though the
		// -Z goal is nearer by distance.
		throwSequence(fe, ps, mc, model.Vec3{0, 1, -10}, 6, model.Vec3{0, 0, 15}, "playing", 0, 0.067)
		th := ps.LastThrow
		if th.GoalSelection != GoalSelectionAngular || !approx(th.GoalPosition.Z(), goalZ, 1e-9) {
			t.Fatalf("goal=%v selection=%q, want +GoalZ/angular", th.GoalPosition, th.GoalSelection)
		}
		if th.TargetPosition == nil || th.TargetDeviation > 2 {
			t.Fatalf("throw straight at the goal should be goal-directed with small deviation, got dev=%v target=%v", th.TargetDeviation, th.TargetPosition)
		}
		// Same spot, thrown toward -Z: -Z goal.
		fe2 := NewFeatureExtractor(30)
		ps2 := &model.PlayerState{PlayerID: "p1", Team: "blue"}
		throwSequence(fe2, ps2, mc, model.Vec3{0, 1, -10}, 6, model.Vec3{0, 0, -15}, "playing", 0, 0.067)
		if !approx(ps2.LastThrow.GoalPosition.Z(), -goalZ, 1e-9) {
			t.Fatalf("throw toward -Z should pick -GoalZ, got %v", ps2.LastThrow.GoalPosition)
		}
	})

	t.Run("deviation measured against GoalZ not the arena wall", func(t *testing.T) {
		fe := NewFeatureExtractor(30)
		ps := &model.PlayerState{PlayerID: "p1", Team: "blue"}
		// From (3,1,10) aim exactly at the goal centre (0,0,GoalZ).
		pos := model.Vec3{3, 1, 10}
		releasePos := pos.Add(model.Vec3{1, 0.2, 0})
		aim := model.Vec3{0, 0, goalZ}.Sub(releasePos).Normalized().Scale(15)
		throwSequence(fe, ps, mc, pos, 6, aim, "playing", 0, 0.067)
		if ps.LastThrow.TargetDeviation > 0.01 {
			t.Fatalf("dead-centre shot should read ~0 deviation, got %v", ps.LastThrow.TargetDeviation)
		}
	})

	t.Run("configured side wins over release direction", func(t *testing.T) {
		fe := NewFeatureExtractor(30)
		fe.SetBlueGoalSide(-1)
		ps := &model.PlayerState{PlayerID: "p1", Team: "blue"}
		throwSequence(fe, ps, mc, model.Vec3{0, 1, -10}, 6, model.Vec3{0, 0, 15}, "playing", 0, 0.067)
		th := ps.LastThrow
		if th.GoalSelection != GoalSelectionTeam || !approx(th.GoalPosition.Z(), -goalZ, 1e-9) {
			t.Fatalf("blue configured to attack -Z: goal=%v selection=%q", th.GoalPosition, th.GoalSelection)
		}
		if th.TargetPosition != nil {
			t.Fatal("throw away from the attacked goal must not be goal-directed")
		}
		// Orange attacks the opposite goal; team from TeamAssignments when PlayerState.Team is empty.
		ps2 := &model.PlayerState{PlayerID: "p2"}
		throwSequence(fe, ps2, mc, model.Vec3{0, 1, -10}, 6, model.Vec3{0, 0, 15}, "playing", 0, 0.067)
		if !approx(ps2.LastThrow.GoalPosition.Z(), goalZ, 1e-9) || ps2.LastThrow.GoalSelection != GoalSelectionTeam {
			t.Fatalf("orange should attack +Z: %v %q", ps2.LastThrow.GoalPosition, ps2.LastThrow.GoalSelection)
		}
	})

	t.Run("side learned from a scored goal", func(t *testing.T) {
		fe := NewFeatureExtractor(30)
		ps := &model.PlayerState{PlayerID: "p1", Team: "blue"}
		pos := model.Vec3{0, 1, 0}
		f0 := feFrame("p1", 0, 0, pos)
		fe.UpdatePlayerState(ps, &f0, mc)
		// Score change with the disc reset to centre carries no information.
		f1 := feFrame("p1", 1, 0.067, pos)
		f1.BlueScore = 2
		f1.Disc = &model.DiscState{Position: model.Vec3{0, 0, 0}}
		fe.UpdatePlayerState(ps, &f1, mc)
		if _, known := fe.TeamGoalZ(mc, "blue"); known {
			t.Fatal("must not learn a side from a disc at centre")
		}
		// Orange scores with the disc deep in the -Z goal: blue attacks +Z.
		f2 := feFrame("p1", 2, 0.134, pos)
		f2.BlueScore = 2
		f2.OrangeScore = 3
		f2.Disc = &model.DiscState{Position: model.Vec3{0, 0, -35}}
		fe.UpdatePlayerState(ps, &f2, mc)
		z, known := fe.TeamGoalZ(mc, "blue")
		if !known || !approx(z, goalZ, 1e-9) {
			t.Fatalf("blue goal z=%v known=%v, want +GoalZ", z, known)
		}
		if z, _ := fe.TeamGoalZ(mc, "orange"); !approx(z, -goalZ, 1e-9) {
			t.Fatalf("orange goal z=%v, want -GoalZ", z)
		}
		// Learned side is per match.
		other := feTestCtx()
		other.MatchID = "other"
		if _, known := fe.TeamGoalZ(other, "blue"); known {
			t.Fatal("learned side must not leak into another match")
		}
		// Subsequent throws use the learned side even when thrown the other way.
		throwSequence(fe, ps, mc, pos, 6, model.Vec3{0, 0, -15}, "playing", 3, 0.067)
		th := ps.LastThrow
		if th.GoalSelection != GoalSelectionTeam || !approx(th.GoalPosition.Z(), goalZ, 1e-9) || th.TargetPosition != nil {
			t.Fatalf("learned side not applied: goal=%v sel=%q target=%v", th.GoalPosition, th.GoalSelection, th.TargetPosition)
		}
	})
}

func TestExtractor_ReleaseSpeedFromGameVelocity(t *testing.T) {
	// Speed derived from the reported velocity when the producer left Speed
	// zero; release speed never depends on dt.
	mc := feTestCtx()
	for _, dt := range []float64{1.0 / 60.0, 0.067, 0.2} {
		fe := NewFeatureExtractor(30)
		ps := &model.PlayerState{PlayerID: "p1"}
		pos := model.Vec3{0, 1, -10}
		for i := 0; i < 4; i++ {
			f := feFrame("p1", i, float64(i)*dt, pos)
			f.HasPossession = true
			f.Disc = &model.DiscState{Position: pos, IsHeld: true, PossessorID: "p1"}
			fe.UpdatePlayerState(ps, &f, mc)
		}
		f := feFrame("p1", 4, 4*dt, pos)
		f.Disc = &model.DiscState{Position: pos.Add(model.Vec3{1, 0, 0}), Velocity: model.Vec3{30, 0, 0}}
		fe.UpdatePlayerState(ps, &f, mc)
		if ps.LastThrow == nil || !approx(ps.LastThrow.ReleaseSpeed, 30, 1e-9) {
			t.Fatalf("dt=%v: release speed %v want 30", dt, ps.LastThrow)
		}
	}
}

func TestExtractor_GameLastThrowOverridesSampleAndStrengthensAttribution(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p1"}
	pos := model.Vec3{0, 1, -10}
	dt := 0.067
	for i := 0; i < 4; i++ {
		f := feFrame("p1", i, float64(i)*dt, pos)
		f.HasPossession = true
		f.Disc = &model.DiscState{Position: pos, IsHeld: true, PossessorID: "p1"}
		fe.UpdatePlayerState(ps, &f, mc)
	}
	f := feFrame("p1", 4, 4*dt, pos)
	f.Disc = &model.DiscState{Position: pos.Add(model.Vec3{1, 0, 0}), Velocity: model.Vec3{18.7, 0, 0}, Speed: 18.7}
	f.GameLastThrow = &model.GameThrowDetails{
		ArmSpeed: 12.4, TotalSpeed: 19.91, SpeedFromArm: 12,
		SpeedFromMovement: 4.2, SpeedFromWrist: 3.71,
	}
	fe.UpdatePlayerState(ps, &f, mc)

	th := ps.LastThrow
	if th == nil || !approx(th.ReleaseSpeed, 19.91, 1e-9) || !approx(th.SampledDiscSpeed, 18.7, 1e-9) {
		t.Fatalf("engine/sample speeds not retained correctly: %+v", th)
	}
	if th.GameLastThrow == nil || th.GameLastThrow.SpeedFromMovement != 4.2 {
		t.Fatalf("engine component breakdown missing: %+v", th)
	}
	if th.Attribution.Method != "game_last_throw" || th.Attribution.Confidence != 1 || th.Attribution.LookbackDepth != 0 {
		t.Fatalf("local engine attribution not applied: %+v", th.Attribution)
	}
}

func TestExtractor_TeamFromFrame(t *testing.T) {
	fe := NewFeatureExtractor(30)
	mc := feTestCtx()
	ps := &model.PlayerState{PlayerID: "p9"}
	f := feFrame("p9", 0, 0, model.Vec3{0, 1, 0})
	f.Team = "orange"
	fe.UpdatePlayerState(ps, &f, mc)
	if ps.Team != "orange" {
		t.Fatalf("team from frame not applied: %q", ps.Team)
	}
}
