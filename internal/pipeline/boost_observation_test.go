package pipeline

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestBoostTimingRequiresKnownConsecutiveEdges(t *testing.T) {
	fe, ps := NewFeatureExtractor(30), &model.PlayerState{PlayerID: "p1"}
	known := true
	for i, step := range []struct{ known, boost bool }{{false, false}, {true, true}, {true, false}, {true, true}, {false, false}, {true, true}} {
		f := feFrame("p1", i, float64(i)*0.05, model.Vec3{1, 2, 3})
		f.IsBoosting = step.boost
		f.IsBoostingKnown = nil
		if step.known {
			f.IsBoostingKnown = &known
		}
		fe.UpdatePlayerState(ps, &f, feTestCtx())
		want := 0
		if i == 3 {
			want = 1
		}
		if len(ps.BoostTimestamps) != want {
			t.Fatalf("frame%d boost activations=%v want%d", i, ps.BoostTimestamps, want)
		}
		if !step.known && ps.LastBoostFrame != -1 {
			t.Fatal("unknown retained a valid activation frame")
		}
	}
}

func TestBoostTimingDiscontinuityClearsOnlyBoostHistory(t *testing.T) {
	fe, ps := NewFeatureExtractor(30), &model.PlayerState{PlayerID: "p1"}
	for i := 0; i < 3; i++ {
		f := feFrame("p1", i, float64(i)*.05, model.Vec3{1, 2, 3})
		f.IsBoosting = i == 1
		fe.UpdatePlayerState(ps, &f, feTestCtx())
	}
	if len(ps.BoostTimestamps) != 1 {
		t.Fatal("fixture activation missing")
	}
	f := feFrame("p1", 5, .25, model.Vec3{1, 2, 3})
	f.IsBoosting = true
	fe.UpdatePlayerState(ps, &f, feTestCtx())
	if len(ps.BoostTimestamps) != 0 || ps.LastBoostFrame != -1 || len(ps.PositionHistory) != 4 {
		t.Fatal("gap bridged boost timing or cleared unrelated pose history")
	}
}

func TestLegalMotionContextDoesNotInterpretUnknownBoost(t *testing.T) {
	f := feFrame("p1", 1, .1, model.Vec3{1, 2, 3})
	ps := &model.PlayerState{IsBoosting: true, HasReportedVelocity: true, ReportedVelocity: model.Vec3{4, 0, 0}, PlayspaceValid: true, PlayspaceRigCoherence: 1, PlayspaceSpeed: 1, PlayspaceDistance: .5, LeftHandRelativeSpeed: 2}
	for _, rawBoost := range []bool{false, true} {
		ps.IsBoosting = rawBoost
		c := buildLegalMotionContext(ps, &f, model.Vec3{}, true, .1)
		if c.BoostingKnown || c.Boosting || c.PlayspaceStep || c.PossibleSlapOrPush || !c.CannotDistinguishContact || !c.GameLocomotion {
			t.Fatalf("unknown boost manufactured interpretation: %+v", c)
		}
	}
	ps.IsBoostingKnown = true
	ps.IsBoosting = false
	c := buildLegalMotionContext(ps, &f, model.Vec3{}, true, .1)
	if !c.BoostingKnown || !c.PlayspaceStep || !c.PossibleSlapOrPush {
		t.Fatalf("known off-state fixture not evaluated: %+v", c)
	}
}
