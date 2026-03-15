package state

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// State006 detects score manipulation - invalid score deltas (STATE_006).
// Hard rule: severity=1, confidence=1 on invalid deltas.
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
// Accepts 0 (no change), 2 or 3 (single goal), and 4-6 (multi-goal in a frame gap).
// Rejects negative deltas and delta=1 (impossible score).
func validScoreDelta(delta int) bool {
	return delta == 0 || (delta >= 2 && delta <= 6)
}

func (d *State006) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	// We need score data from any player frame
	// Use the first player to get score info
	for _, ps := range players {
		if !d.initialized {
			d.prevBlueScore = ps.PrevBlueScore
			d.prevOrangeScore = ps.PrevOrangeScore
			d.initialized = true
			// Store current as "previous" for next frame
			// Use PrevBlueScore/PrevOrangeScore which are tracked per player
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

		// Track goals delta per player
		prevGoals := d.prevGoals[pid]
		d.prevGoals[pid] = ps.PrevGoals // will be current frame goals

		// Check blue score delta
		// We use the PlayerState's PrevBlueScore/PrevOrangeScore
		// which represent frame-over-frame deltas tracked by the pipeline

		// Score deltas between frames
		// The pipeline tracks previous scores on PlayerState
		blueDelta := ps.PrevBlueScore - d.prevBlueScore
		orangeDelta := ps.PrevOrangeScore - d.prevOrangeScore
		goalsDelta := ps.PrevGoals - prevGoals

		// Only check if there was a score change
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

		// Negative deltas are always invalid
		if blueDelta < 0 || orangeDelta < 0 {
			scoreChanged = true
			if invalidReason == "" {
				invalidReason = fmt.Sprintf("negative_delta: blue=%d orange=%d", blueDelta, orangeDelta)
			}
		}

		if !scoreChanged {
			continue
		}

		// Hard rule: severity=1, confidence=1
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
			"score_delta: {0, 2, 3}",
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  frameIdx - 1,
				FrameEnd:    frameIdx,
				AnomalyType: "score_manipulation",
			},
		)
		ev.AutoEnforce = true
		events = append(events, ev)

		// Only fire once per frame change
		break
	}

	// Update stored scores from any player
	for _, ps := range players {
		d.prevBlueScore = ps.PrevBlueScore
		d.prevOrangeScore = ps.PrevOrangeScore
		break
	}

	return events
}
