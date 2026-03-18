package state

import (
	"fmt"

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
// To reactivate: update validScoreDelta with a confirmed rule, set
// Enabled=true in config, and add a test proving the invariant against
// real telemetry.
type State006 struct {
	detect.BaseDetector

	prevBlueScore   int
	prevOrangeScore int
	prevGoals       map[string]int
	initialized     bool
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
			IsAutoEnforce:    true,
		},
		prevGoals: make(map[string]int),
	}
	return d
}

func (d *State006) Reset() {
	d.prevBlueScore = 0
	d.prevOrangeScore = 0
	d.prevGoals = make(map[string]int)
	d.initialized = false
}

func (d *State006) Configure(params map[string]any) error {
	return nil
}

// validScoreDelta returns true if the delta is a valid score change.
// CONFIRMED from real replay/profiler data:
// - Negative deltas are legitimate: scores reset between rounds/matches.
// - Large positive deltas occur when frames are skipped or rounds transition.
// - Delta=1 occurs legitimately in real profiler data (frame-boundary artifacts).
// - Delta=0 is no change (most common).
// All non-negative deltas are accepted; only impossible values would be flagged,
// and no delta value has been confirmed impossible across real telemetry.
func validScoreDelta(delta int) bool {
	return true
}

func (d *State006) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	// SUSPENDED: validScoreDelta currently accepts all deltas because no score
	// delta has been confirmed impossible in real profiler telemetry.
	//
	// When a confirmed invariant is found: update validScoreDelta with the
	// rule, uncomment the detection logic below, and set Enabled=true in config.
	return nil
}

// evaluateSuspended contains the original detection logic, preserved for
// reactivation once a confirmed impossible score invariant is established.
// To reactivate: rename to Evaluate, remove the no-op Evaluate above,
// update validScoreDelta, and set Enabled=true in config.
//
//nolint:unused // intentionally preserved for reactivation
func (d *State006) evaluateSuspended(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	// We need score data from any player frame
	// Use the first player to get score info
	for _, ps := range players {
		if !d.initialized {
			d.prevBlueScore = ps.PrevBlueScore
			d.prevOrangeScore = ps.PrevOrangeScore
			d.initialized = true
			break
		}
		break
	}

	if !d.initialized {
		return nil
	}

	// Check all players for score changes (scores should be consistent)
	for _, ps := range players {
		pid := ps.PlayerID

		prevGoals := d.prevGoals[pid]
		d.prevGoals[pid] = ps.PrevGoals

		blueDelta := ps.PrevBlueScore - d.prevBlueScore
		orangeDelta := ps.PrevOrangeScore - d.prevOrangeScore
		goalsDelta := ps.PrevGoals - prevGoals

		if blueDelta == 0 && orangeDelta == 0 {
			continue
		}

		scoreChanged := false
		invalidReason := ""

		if !validScoreDelta(blueDelta) {
			scoreChanged = true
			invalidReason = fmt.Sprintf("blue_score_delta=%d", blueDelta)
		}
		if !validScoreDelta(orangeDelta) {
			scoreChanged = true
			if invalidReason != "" {
				invalidReason += ", "
			}
			invalidReason += fmt.Sprintf("orange_score_delta=%d", orangeDelta)
		}

		if !scoreChanged {
			continue
		}

		metrics := map[string]float64{
			"blue_delta":   float64(blueDelta),
			"orange_delta": float64(orangeDelta),
			"goals_delta":  float64(goalsDelta),
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			1.0, 1.0,
			model.StateEvidence{
				DetectorSpecific: "score_manipulation",
				Metrics:          metrics,
			},
			fmt.Sprintf("invalid_score_delta: %s", invalidReason),
			"UNVERIFIED: valid score delta set — update when confirmed",
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  frameIdx - 1,
				FrameEnd:    frameIdx,
				AnomalyType: "score_manipulation",
			},
		)
		ev.AutoEnforce = true
		events = append(events, ev)

		break
	}

	for _, ps := range players {
		d.prevBlueScore = ps.PrevBlueScore
		d.prevOrangeScore = ps.PrevOrangeScore
		break
	}

	return events
}
