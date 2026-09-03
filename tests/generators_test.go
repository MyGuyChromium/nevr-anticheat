package tests

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// TestGenerators_EveryScenarioPassesTheValidator: every synthetic generator
// models a producer honouring the telemetry contract, so the production
// FrameValidator must accept every frame (a rejected frame leaves the
// player state stale and the detectors under test never see it).
func TestGenerators_EveryScenarioPassesTheValidator(t *testing.T) {
	for _, sc := range testutil.AllScenarios() {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			if len(sc.Frames) == 0 {
				t.Fatal("generator produced no frames")
			}
			invalid, reasons := testutil.ValidateFrames(sc.Frames)
			if invalid != sc.ExpectInvalid {
				t.Fatalf("validator rejected %d frames (want %d): %v", invalid, sc.ExpectInvalid, reasons)
			}
			// The pipeline must process every frame index of the stream.
			hr := testutil.NewHarness(t).WithEnabledDetectors().
				WithMatchContext(matchContextForPlayer("player1")).Run(t, sc.Frames)
			hr.AssertAllFramesValid(len(sc.Frames))
			if n := len(hr.Result.SanitizedFrames); n != 0 {
				t.Errorf("generator needed sanitizing: %v", hr.Result.SanitizedFrames)
			}
		})
	}
}

// TestGenerators_FramesAreInternallyConsistent: hands ride on the body
// (hand-to-head under PAT_005's 1.6 m on every legit generator), rotations
// are unit quaternions, frame indices and timestamps are monotonic and
// DeltaTime is the real spacing (contract 1).
func TestGenerators_FramesAreInternallyConsistent(t *testing.T) {
	legit := map[string]bool{
		"NormalIdlePlayer": true, "NormalMovingPlayer": true, "NormalMovingPlayer/fast": true,
		"NormalThrowSequence": true, "EliteThrowSequence": true, "NormalBoostSequence": true,
		"NormalStunCycle": true, "NormalShieldCycle": true, "RespawnImmunity": true,
		"JitteryTelemetry": true, "PacketLossFrames": true, "LargeFrameGap": true,
		"HighPingPlayer": true, "PingSpikeSequence": true, "RegrabStackingBurst": true,
		"FastWristFlick": true, "SteadyHandPlayer": true,
	}
	for _, sc := range testutil.AllScenarios() {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			for i, f := range sc.Frames {
				if !f.Rotation.IsUnit() || !f.LeftHandRotation.IsUnit() || !f.RightHandRotation.IsUnit() {
					t.Fatalf("frame %d: non-unit rotation", i)
				}
				if legit[sc.Name] {
					if d := math.Max(f.LeftHandPosition.Distance(f.Position), f.RightHandPosition.Distance(f.Position)); d > 1.6 {
						t.Fatalf("frame %d: hand %.2f m from head on a legit generator", i, d)
					}
				}
				if i == 0 {
					if f.DeltaTime != 0 {
						t.Fatalf("first frame DeltaTime %.4f, want 0 (unknown)", f.DeltaTime)
					}
					continue
				}
				prev := sc.Frames[i-1]
				if f.FrameIndex <= prev.FrameIndex || f.Timestamp <= prev.Timestamp {
					t.Fatalf("frame %d: index/timestamp not increasing (%d/%.3f after %d/%.3f)", i, f.FrameIndex, f.Timestamp, prev.FrameIndex, prev.Timestamp)
				}
				if math.Abs(f.DeltaTime-(f.Timestamp-prev.Timestamp)) > 1e-9 {
					t.Fatalf("frame %d: DeltaTime %.4f but timestamps are %.4f apart", i, f.DeltaTime, f.Timestamp-prev.Timestamp)
				}
			}
		})
	}
}

// TestGenerators_PacketLossPerturbsTheTimeBase (F163): dropped frames must
// leave gaps in FrameIndex AND Timestamp, because the feature extractor
// derives kinematics from timestamp deltas; a doubled DeltaTime alone
// would be invisible to it.
func TestGenerators_PacketLossPerturbsTheTimeBase(t *testing.T) {
	frames := player1().PacketLossFrames(500, 0.3)
	gaps, indexGaps := 0, 0
	for i := 1; i < len(frames); i++ {
		if frames[i].Timestamp-frames[i-1].Timestamp > 0.1 {
			gaps++
		}
		if frames[i].FrameIndex-frames[i-1].FrameIndex > 1 {
			indexGaps++
		}
	}
	if gaps < 80 || indexGaps != gaps {
		t.Fatalf("expected >= 80 timestamp gaps matching index gaps, got %d timestamp / %d index gaps", gaps, indexGaps)
	}
	if dropped := 500 - len(frames); dropped < 125 || dropped > 175 {
		t.Fatalf("dropped %d of 500 frames, want ~30 %%", dropped)
	}
	// The extractor sees the real interval: velocity across a gap is the
	// displacement over the real elapsed time, not over one nominal tick.
	fe := pipeline.NewFeatureExtractor(30)
	mc := matchContextForPlayer("player1")
	ps := &model.PlayerState{PlayerID: "player1"}
	checked := 0
	for i := range frames {
		prev := ps.Position
		fe.UpdatePlayerState(ps, &frames[i], mc)
		if i == 0 {
			continue
		}
		want := frames[i].Timestamp - frames[i-1].Timestamp
		if math.Abs(ps.FrameDt-want) > 1e-9 {
			t.Fatalf("frame %d: FrameDt %.4f, want real spacing %.4f", i, ps.FrameDt, want)
		}
		if want > 0.1 {
			v := frames[i].Position.Sub(prev).Scale(1 / want)
			if ps.Velocity.Sub(v).Magnitude() > 1e-6 {
				t.Fatalf("frame %d: velocity %v across a %.3f s gap, want %v", i, ps.Velocity, want, v)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no gap was checked")
	}
}

// TestGenerators_HighPingJittersTimestamps (F163): sample timing must be
// irregular at the timestamp level, and the extractor must follow it.
func TestGenerators_HighPingJittersTimestamps(t *testing.T) {
	frames := player1().HighPingPlayer(200, 200)
	irregular := 0
	for i := 1; i < len(frames); i++ {
		if d := frames[i].Timestamp - frames[i-1].Timestamp; math.Abs(d-1.0/15.0) > 0.002 {
			irregular++
		}
		if frames[i].EstimatedPingMs != 200 {
			t.Fatalf("frame %d ping %.0f", i, frames[i].EstimatedPingMs)
		}
	}
	if irregular < 150 {
		t.Fatalf("only %d of 199 intervals are jittered", irregular)
	}
}

// TestGenerators_LargeFrameGapIsASampleGap (F168): the stall frame is
// accepted by the validator, the extractor derives no kinematics across it
// (Speed 0 on the gap frame) and the index gap exceeds MOV_002's
// max_frame_gap so the 10 m displacement is never a teleport candidate.
func TestGenerators_LargeFrameGapIsASampleGap(t *testing.T) {
	frames := player1().LargeFrameGap(200, 100, 2.0)
	if invalid, reasons := testutil.ValidateFrames(frames); invalid != 0 {
		t.Fatalf("stall frame rejected: %v", reasons)
	}
	fe := pipeline.NewFeatureExtractor(30)
	mc := matchContextForPlayer("player1")
	ps := &model.PlayerState{PlayerID: "player1"}
	for i := 0; i <= 100; i++ {
		fe.UpdatePlayerState(ps, &frames[i], mc)
	}
	if math.Abs(ps.FrameDt-pipeline.MaxFrameDt) > 1e-9 {
		t.Errorf("gap FrameDt %.3f, want clamped to MaxFrameDt %.3f", ps.FrameDt, pipeline.MaxFrameDt)
	}
	if ps.Speed != 0 || !ps.Velocity.IsZero() {
		t.Errorf("kinematics derived across a %.1f s gap: speed %.1f", 2.0, ps.Speed)
	}
	if got := frames[100].FrameIndex - frames[99].FrameIndex; got <= 5 {
		t.Errorf("index gap %d does not exceed max_frame_gap 5", got)
	}
}

// TestGenerators_ConcatIsContinuous: joined segments keep index/timestamp
// monotonic and never introduce a jump at the seam.
func TestGenerators_ConcatIsContinuous(t *testing.T) {
	a := player1().NormalMovingPlayer(50, 5)
	b := player1().NormalThrowSequence(1)
	c := player1().SpeedHackFrames(40, 75)
	all := testutil.Concat(a, b, c)
	if len(all) != len(a)+len(b)+len(c) {
		t.Fatalf("len %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].FrameIndex != all[i-1].FrameIndex+1 {
			t.Fatalf("frame %d: index %d after %d", i, all[i].FrameIndex, all[i-1].FrameIndex)
		}
		if all[i].Timestamp <= all[i-1].Timestamp {
			t.Fatalf("frame %d: timestamp not increasing", i)
		}
	}
	seam := len(a)
	if d := all[seam].Position.Distance(all[seam-1].Position); d > 0.01 {
		t.Errorf("seam displacement %.3f m", d)
	}
	if invalid, reasons := testutil.ValidateFrames(all); invalid != 0 {
		t.Errorf("concat produced invalid frames: %v", reasons)
	}
}

// TestGenerators_ThrowSequenceProducesThrowEvents: the throw engine's
// possession transitions are recognised by the feature extractor as throws
// with the game-reported release speed and the requested goal deviation.
func TestGenerators_ThrowSequenceProducesThrowEvents(t *testing.T) {
	specs := []testutil.ThrowSpec{
		{HoldFrames: 10, FlightFrames: 15, GapFrames: 5, ReleaseSpeed: 12, DeviationDeg: 7.5, HandSpeed: 6},
		{HoldFrames: 12, FlightFrames: 15, GapFrames: 5, ReleaseSpeed: 16, DeviationDeg: 0.4, HandSpeed: 6},
		{HoldFrames: 12, FlightFrames: 15, GapFrames: 5, ReleaseSpeed: 9, DeviationDeg: 25, HandSpeed: 4, HandDeviationDeg: 20},
	}
	frames := player1().ThrowSequence(specs)
	h := testutil.NewHarness(t).WithDetectors("THROW_001").WithMatchContext(matchContextForPlayer("player1"))
	p, _ := h.NewPipeline()
	if _, err := p.ProcessMatch(t.Context(), h.MatchContext(frames), frames); err != nil {
		t.Fatal(err)
	}
	ps := p.Players()["player1"]
	if ps == nil || ps.ThrowCount != len(specs) {
		t.Fatalf("throw count = %d, want %d", ps.ThrowCount, len(specs))
	}
	for i, te := range ps.ThrowHistory {
		s := specs[i]
		if math.Abs(te.ReleaseSpeed-s.ReleaseSpeed) > 1e-6 {
			t.Errorf("throw %d: release speed %.2f, want %.2f", i, te.ReleaseSpeed, s.ReleaseSpeed)
		}
		if math.Abs(te.TargetDeviation-s.DeviationDeg) > 0.05 {
			t.Errorf("throw %d: target deviation %.2f, want %.2f", i, te.TargetDeviation, s.DeviationDeg)
		}
		if te.TargetPosition == nil {
			t.Errorf("throw %d: not goal-directed", i)
		}
		if math.Abs(te.HandSpeed-s.HandSpeed) > 0.5 {
			t.Errorf("throw %d: hand speed %.2f, want ~%.2f", i, te.HandSpeed, s.HandSpeed)
		}
		if s.HandDeviationDeg > 0 && math.Abs(te.ReleaseAngle-s.HandDeviationDeg) > 4 {
			t.Errorf("throw %d: release angle %.1f, want ~%.1f", i, te.ReleaseAngle, s.HandDeviationDeg)
		}
		if te.ThrowingHand != "right" {
			t.Errorf("throw %d: throwing hand %q", i, te.ThrowingHand)
		}
	}
}
