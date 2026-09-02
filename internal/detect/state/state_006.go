package state

import (
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// State006 detects score manipulation - invalid score deltas (STATE_006).
//
// STATUS: SUSPENDED — no confirmed impossible score invariant exists.
//
// Previously flagged delta=1 as impossible (goals are 2 or 3 pts), but real
// Echo VR profiler data proved delta=1 occurs legitimately (frame-boundary
// artifacts). No score delta has yet been confirmed impossible across real
// telemetry. Until a confirmed invariant is established from production data,
// Evaluate unconditionally returns nil.
//
// To reactivate: add detection logic with a confirmed invariant from real
// profiler data, set Enabled=true in config, and add a test proving the
// invariant against real telemetry.
type State006 struct {
	detect.BaseDetector
}

// NewState006 creates a new STATE_006 Score Manipulation detector.
func NewState006(params map[string]any) *State006 {
	d := &State006{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "STATE_006",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Score Manipulation",
			DetectorCategory: "state",
			Inputs:           []string{"score_state"},
			Warmup:           2,
			Weight:           1.0,
			// A suspended detector with no confirmed invariant must never
			// advertise auto-enforce eligibility (config: auto_enforce = false).
			IsAutoEnforce: false,
		},
	}
	return d
}

func (d *State006) Reset()                                {}
func (d *State006) Configure(params map[string]any) error { return nil }

func (d *State006) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	// SUSPENDED: no score delta has been confirmed impossible in real profiler
	// telemetry. To reactivate: implement detection logic with a confirmed
	// invariant, set Enabled=true in config.
	return nil
}
