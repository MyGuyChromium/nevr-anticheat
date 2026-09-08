package pipeline

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/state"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestAutopocketCoverageRequiresActualWholeRosterInputCheck(t *testing.T) {
	c := newCoverageTracker([]detect.Detector{state.NewState008(nil)}, config.DefaultConfig(), []string{"p"})
	ps := &model.PlayerState{PlayerID: "p"}
	index := c.index["STATE_008"]
	for frame := 0; frame < 5; frame++ {
		c.candidate("STATE_008", ps, frame)
		c.trace("STATE_008", "p", frame, "catch_input_unavailable")
	}
	if got := c.player("p").Detectors[index]; !got.InputCheck || got.InputFrames != 0 || got.CandidateFrames != 5 {
		t.Fatalf("dispatch alone must not count available catch inputs: %+v", got)
	}
	for _, frame := range []int{5, 5, 6, 5, 7} {
		c.trace("STATE_008", "p", frame, "catch_inputs_ready")
	}
	if got := c.player("p").Detectors[index].InputFrames; got != 3 {
		t.Fatalf("only newly observed valid frames count, got %d", got)
	}
	c.trace("STATE_008", "p", 7, "catch_approach_evaluated")
	if got := c.player("p").Detectors[index].InputFrames; got != 3 {
		t.Fatalf("catch completion must not double count inputs, got %d", got)
	}
	for _, r := range c.player("p").Detectors[index].DecisionTrace.Reasons {
		if r.Description == "Additional detector diagnostic" {
			t.Fatalf("catch decision has no human-readable explanation: %s", r.Code)
		}
	}
}

func TestAutopocketUnknownInputsRemainInsufficientCoverage(t *testing.T) {
	c := newCoverageTracker([]detect.Detector{state.NewState008(nil)}, config.DefaultConfig(), []string{"p"})
	c.candidate("STATE_008", &model.PlayerState{PlayerID: "p"}, 0)
	c.trace("STATE_008", "p", 0, "catch_tracking_unavailable")
	got := c.finish(TelemetryQualityReport{})["p"].Detectors[c.index["STATE_008"]]
	if got.Status != "insufficient_data" || got.InputFrames != 0 {
		t.Fatalf("unknown head/contact data cannot be reported as checked: %+v", got)
	}
}
