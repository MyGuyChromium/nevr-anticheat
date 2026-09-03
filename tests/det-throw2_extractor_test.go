package tests

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
)

// Feature-extractor gap semantics (contract B): SetMaxFrameDt, throw
// detection across a gap, and snapshot frame-index labels.

func dt2Ctx() *model.MatchContext {
	return &model.MatchContext{
		MatchID:         "dt2-test",
		PlayerIDs:       []string{"p1", "p2", "p3"},
		TeamAssignments: map[string]string{"p1": "blue", "p2": "orange", "p3": "blue"},
		Physics:         model.DefaultPhysics(),
	}
}

func dt2Frame(pid string, idx int, ts float64, pos model.Vec3) model.PlayerTelemetryFrame {
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

func dt2Approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestExtractor_SetMaxFrameDt(t *testing.T) {
	fe := pipeline.NewFeatureExtractor(30)
	if fe.MaxFrameDtSeconds() != pipeline.MaxFrameDt {
		t.Fatalf("default gap threshold %v, want %v", fe.MaxFrameDtSeconds(), pipeline.MaxFrameDt)
	}
	mc := dt2Ctx()
	run := func(fe *pipeline.FeatureExtractor) *model.PlayerState {
		ps := &model.PlayerState{PlayerID: "p1"}
		f0 := dt2Frame("p1", 0, 0, model.Vec3{0, 1, 0})
		fe.UpdatePlayerState(ps, &f0, mc)
		f1 := dt2Frame("p1", 1, 1.2, model.Vec3{4.8, 1, 0}) // 1.2 s later, 4 m/s
		fe.UpdatePlayerState(ps, &f1, mc)
		return ps
	}
	// Default: 1.2 s is a gap.
	ps := run(fe)
	if ps.Speed != 0 || !dt2Approx(ps.FrameDt, pipeline.MaxFrameDt, 1e-12) {
		t.Fatalf("default: speed=%v dt=%v, want cleared kinematics and dt clamped to %v", ps.Speed, ps.FrameDt, pipeline.MaxFrameDt)
	}
	// Raised threshold: the same spacing is a known, unclamped dt.
	fe.SetMaxFrameDt(2.0)
	if fe.MaxFrameDtSeconds() != 2.0 {
		t.Fatalf("threshold not applied: %v", fe.MaxFrameDtSeconds())
	}
	ps = run(fe)
	if !dt2Approx(ps.FrameDt, 1.2, 1e-9) || !dt2Approx(ps.Speed, 4, 1e-9) {
		t.Fatalf("with max_frame_dt=2: dt=%v speed=%v, want 1.2 / 4", ps.FrameDt, ps.Speed)
	}
	// Lowered threshold: a 0.1 s step becomes a gap.
	fe.SetMaxFrameDt(0.05)
	ps2 := &model.PlayerState{PlayerID: "p2"}
	f0 := dt2Frame("p2", 0, 0, model.Vec3{0, 1, 0})
	fe.UpdatePlayerState(ps2, &f0, mc)
	f1 := dt2Frame("p2", 1, 0.1, model.Vec3{1, 1, 0})
	fe.UpdatePlayerState(ps2, &f1, mc)
	if ps2.Speed != 0 || !dt2Approx(ps2.FrameDt, 0.05, 1e-12) {
		t.Fatalf("with max_frame_dt=0.05: dt=%v speed=%v, want gap", ps2.FrameDt, ps2.Speed)
	}
	// Invalid values restore the default.
	for _, bad := range []float64{0, -1, pipeline.MinFrameDt, math.NaN(), math.Inf(1)} {
		fe.SetMaxFrameDt(bad)
		if fe.MaxFrameDtSeconds() != pipeline.MaxFrameDt {
			t.Fatalf("SetMaxFrameDt(%v) should restore the default, got %v", bad, fe.MaxFrameDtSeconds())
		}
	}
}

func TestExtractor_SnapshotFrameIndexFollowsSeenFrames(t *testing.T) {
	// Histories hold one entry per frame SEEN: with frame 98 missing
	// (rejected by the validator, dropped poll) the snapshot labels must be
	// the real frame indices, matching their timestamps.
	fe := pipeline.NewFeatureExtractor(30)
	mc := dt2Ctx()
	ps := &model.PlayerState{PlayerID: "p1", Team: "blue"}
	pos := model.Vec3{0, 1, -10}
	const dt = 0.067
	hold := func(idx int) {
		f := dt2Frame("p1", idx, float64(idx)*dt, pos)
		f.HasPossession = true
		f.Disc = &model.DiscState{Position: pos.Add(model.Vec3{0.4, 0.2, 0}), IsHeld: true, PossessorID: "p1"}
		fe.UpdatePlayerState(ps, &f, mc)
	}
	for idx := 90; idx <= 97; idx++ {
		hold(idx)
	}
	hold(99) // frame 98 never seen (0.134 s spacing: still under the gap threshold)
	f := dt2Frame("p1", 100, 100*dt, pos)
	f.Disc = &model.DiscState{Position: pos.Add(model.Vec3{1, 0.2, 0}), Velocity: model.Vec3{0, 0, 15}, Speed: 15}
	fe.UpdatePlayerState(ps, &f, mc)
	if ps.LastThrow == nil {
		t.Fatal("expected a throw at frame 100")
	}
	want := []int{94, 95, 96, 97, 99}
	snaps := ps.LastThrow.PreReleaseFrames
	if len(snaps) != len(want) {
		t.Fatalf("got %d snapshots, want %d", len(snaps), len(want))
	}
	for k, snap := range snaps {
		if snap.FrameIndex != want[k] {
			t.Errorf("snapshot %d labelled frame %d, want %d", k, snap.FrameIndex, want[k])
		}
		if !dt2Approx(snap.Timestamp, float64(want[k])*dt, 1e-9) {
			t.Errorf("snapshot %d timestamp %v does not match frame %d", k, snap.Timestamp, want[k])
		}
	}
}

func TestExtractor_ReleaseAcrossGapIsNotAThrow(t *testing.T) {
	fe := pipeline.NewFeatureExtractor(30)
	mc := dt2Ctx()
	pos := model.Vec3{0, 1, -10}
	const dt = 0.067
	hold := func(ps *model.PlayerState, idx int, ts float64) {
		f := dt2Frame(ps.PlayerID, idx, ts, pos)
		f.HasPossession = true
		f.Disc = &model.DiscState{Position: pos.Add(model.Vec3{0.4, 0.2, 0}), IsHeld: true, PossessorID: ps.PlayerID}
		fe.UpdatePlayerState(ps, &f, mc)
	}
	release := func(ps *model.PlayerState, idx int, ts float64) {
		f := dt2Frame(ps.PlayerID, idx, ts, pos)
		f.Disc = &model.DiscState{Position: pos.Add(model.Vec3{20, 0.2, 0}), Velocity: model.Vec3{15, 0, 0}, Speed: 15}
		fe.UpdatePlayerState(ps, &f, mc)
	}

	// Broadcaster stall of 2 s between the last held frame and the first
	// free frame: the release moment was never observed.
	ps := &model.PlayerState{PlayerID: "p1", Team: "blue"}
	for idx := 0; idx < 10; idx++ {
		hold(ps, idx, float64(idx)*dt)
	}
	release(ps, 10, 9*dt+2.0)
	if ps.LastThrow != nil || ps.ThrowCount != 0 {
		t.Fatalf("release across a %.1f s gap must not be a throw: %+v", 2.0, ps.LastThrow)
	}
	// Duplicate timestamp (dt unknown) on the release frame: same.
	ps2 := &model.PlayerState{PlayerID: "p2", Team: "orange"}
	for idx := 0; idx < 10; idx++ {
		hold(ps2, idx, float64(idx)*dt)
	}
	release(ps2, 10, 9*dt)
	if ps2.LastThrow != nil {
		t.Fatal("release on a duplicate-timestamp frame must not be a throw")
	}
	// The next possession after the skipped release is a normal throw.
	for idx := 11; idx < 21; idx++ {
		hold(ps, idx, 9*dt+2.0+float64(idx-10)*dt)
	}
	release(ps, 21, 9*dt+2.0+11*dt)
	if ps.LastThrow == nil || ps.LastThrow.FrameIndex != 21 || ps.ThrowCount != 1 {
		t.Fatalf("throw after the gap should be detected normally: %+v", ps.LastThrow)
	}
	// A raised gap threshold makes the same stall an observed release.
	fe2 := pipeline.NewFeatureExtractor(30)
	fe2.SetMaxFrameDt(3.0)
	ps3 := &model.PlayerState{PlayerID: "p3", Team: "blue"}
	for idx := 0; idx < 10; idx++ {
		hold(ps3, idx, float64(idx)*dt)
	}
	f := dt2Frame("p3", 10, 9*dt+2.0, pos)
	f.Disc = &model.DiscState{Position: pos.Add(model.Vec3{20, 0.2, 0}), Velocity: model.Vec3{15, 0, 0}, Speed: 15}
	fe2.UpdatePlayerState(ps3, &f, mc)
	if ps3.LastThrow == nil {
		t.Fatal("with max_frame_dt=3 a 2 s spacing is not a gap")
	}
}
