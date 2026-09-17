package pipeline

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/bio"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/state"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestClassifyDiscJumpRecognisesOnlyTheServerRespawn(t *testing.T) {
	goalPlane := model.Vec3{0.4, 1.2, 36.0}
	mm := func(v float64) float64 { return math.Round(v*1000) / 1000 } // 1 mm wire quantisation
	for _, tc := range []struct {
		name              string
		previous, current model.Vec3
		active            bool
		want              string
	}{
		{"orange-side nest", goalPlane, model.Vec3{0, 4.536, 27.5}, false, DiscJumpServerRespawn},
		{"blue-side nest", model.Vec3{-0.4, 1.2, -36.0}, model.Vec3{0, 4.536, -27.5}, false, DiscJumpServerRespawn},
		{"float32 metres", goalPlane, model.Vec3{float64(float32(0)), float64(float32(4.536)), float64(float32(-27.5))}, false, DiscJumpServerRespawn},
		{"1 mm quantised, sampled a moment late", goalPlane, model.Vec3{mm(0.0004), mm(4.5364), mm(27.4996)}, false, DiscJumpServerRespawn},
		{"inside tolerance", goalPlane, model.Vec3{0.04, 4.50, 27.54}, false, DiscJumpServerRespawn},
		{"same landing during live play is not classified", goalPlane, model.Vec3{0, 4.536, 27.5}, true, ""},
		{"lands elsewhere: unclassified", goalPlane, model.Vec3{0, 4.536, 20}, false, ""},
		{"handicapped nest outside tolerance: unclassified", goalPlane, model.Vec3{0, 4.536, 25.0}, false, ""},
		{"off-axis landing: unclassified", goalPlane, model.Vec3{0.2, 4.536, 27.5}, false, ""},
		{"already resting at the nest", model.Vec3{0, 4.536, 27.5}, model.Vec3{0.001, 4.536, 27.5}, false, ""},
		{"drifts in slowly: motion, not a placement", model.Vec3{0, 4.536, 26.0}, model.Vec3{0, 4.536, 27.5}, false, ""},
		{"non-finite sample", model.Vec3{math.NaN(), 0, 0}, model.Vec3{0, 4.536, 27.5}, false, ""},
	} {
		if got := classifyDiscJump(tc.previous, tc.current, tc.active); got != tc.want {
			t.Errorf("%s: classified %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTraceDiscJumpRecordsRespawnOnlyForDiscDetectors(t *testing.T) {
	c := newCoverageTracker([]detect.Detector{state.NewState008(nil), bio.NewBio001(nil)}, config.DefaultConfig(), []string{"p"})
	at := func(v model.Vec3) *model.DiscState { return &model.DiscState{Position: v} }
	goal, nest := model.Vec3{0.4, 1.2, 36}, model.Vec3{0, 4.536, -27.5}

	c.traceDiscJump("p", 10, nil, at(nest), false)              // no previous sample
	c.traceDiscJump("p", 11, at(goal), nil, false)              // disc missing now
	c.traceDiscJump("p", 12, at(goal), at(nest), true)          // live play
	c.traceDiscJump("p", 13, at(goal), at(model.Vec3{}), false) // a miss is unclassified
	for _, d := range c.player("p").Detectors {
		if d.DecisionTrace == nil {
			continue
		}
		for _, r := range d.DecisionTrace.Reasons {
			if r.Code == DiscJumpServerRespawn {
				t.Fatalf("%s recorded a respawn without evidence: %+v", d.DetectorID, r)
			}
		}
	}

	c.traceDiscJump("p", 20, at(goal), at(nest), false)
	c.traceDiscJump("p", 900, at(model.Vec3{0, 1, 36}), at(model.Vec3{0, 4.536, 27.5}), false)
	found := false
	for _, d := range c.player("p").Detectors {
		if d.DecisionTrace == nil {
			continue
		}
		for _, r := range d.DecisionTrace.Reasons {
			if r.Code != DiscJumpServerRespawn {
				continue
			}
			if d.DetectorID != "STATE_008" {
				t.Fatalf("respawn trace attached to a detector that does not use the disc: %s", d.DetectorID)
			}
			if r.Count != 2 || r.FirstFrame != 20 || r.LastFrame != 900 {
				t.Fatalf("respawn history misrecorded: %+v", r)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("recognised server respawn was not recorded")
	}
}
