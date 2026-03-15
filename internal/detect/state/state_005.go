package state

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// State005 detects cooldown bypass - shield reactivated too quickly (STATE_005).
type State005 struct {
	detect.BaseDetector
	minCooldownFrames int
	minViolations     int
	sigmoidSteepness  float64

	wasShieldActive  map[string]bool
	shieldOffFrame   map[string]int
	violations       map[string]int
}

// NewState005 creates a new STATE_005 Cooldown Bypass detector.
func NewState005(params map[string]any) *State005 {
	d := &State005{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "STATE_005",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Cooldown Bypass",
			DetectorCategory: "state",
			Inputs:           []string{"shield_state"},
			Warmup:           10,
			Weight:           0.85,
			IsAutoEnforce:    false,
		},
		minCooldownFrames: detect.GetInt(params, "min_cooldown_frames", 150),
		minViolations:     detect.GetInt(params, "min_violations", 2),
		sigmoidSteepness:  detect.GetFloat(params, "sigmoid_steepness", 0.05),
		wasShieldActive:   make(map[string]bool),
		shieldOffFrame:    make(map[string]int),
		violations:        make(map[string]int),
	}
	return d
}

func (d *State005) Reset() {
	d.wasShieldActive = make(map[string]bool)
	d.shieldOffFrame = make(map[string]int)
	d.violations = make(map[string]int)
}

func (d *State005) Configure(params map[string]any) error {
	d.minCooldownFrames = detect.GetInt(params, "min_cooldown_frames", d.minCooldownFrames)
	d.minViolations = detect.GetInt(params, "min_violations", d.minViolations)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *State005) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID
		wasActive := d.wasShieldActive[pid]
		d.wasShieldActive[pid] = ps.ShieldActive

		if wasActive && !ps.ShieldActive {
			// Shield off transition
			d.shieldOffFrame[pid] = frameIdx
		} else if !wasActive && ps.ShieldActive {
			// Shield on transition - check gap
			offFrame, hasOff := d.shieldOffFrame[pid]
			if !hasOff {
				continue
			}

			gapFrames := frameIdx - offFrame
			if gapFrames >= d.minCooldownFrames {
				continue
			}

			d.violations[pid]++

			if d.violations[pid] < d.minViolations {
				continue
			}

			ratio := float64(gapFrames) / float64(d.minCooldownFrames)
			severity := model.SigmoidConfidence(float64(d.minCooldownFrames-gapFrames), 0, d.sigmoidSteepness)
			confidence := model.Clamp01((1.0 - ratio) * 0.9)
			confidence *= model.SigmoidConfidence(float64(d.violations[pid]), float64(d.minViolations), 0.5)

			metrics := map[string]float64{
				"gap_frames":         float64(gapFrames),
				"min_cooldown":       float64(d.minCooldownFrames),
				"cooldown_ratio":     ratio,
				"violation_count":    float64(d.violations[pid]),
				"min_violations":     float64(d.minViolations),
			}

			ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
				severity, confidence,
				model.StateEvidence{
					DetectorSpecific: "cooldown_bypass",
					Metrics:          metrics,
				},
				fmt.Sprintf("shield_cooldown: %d frames (min %d), %d violations", gapFrames, d.minCooldownFrames, d.violations[pid]),
				fmt.Sprintf("shield_cooldown: >=%d frames", d.minCooldownFrames),
				model.CausalKey{
					PlayerID:    pid,
					FrameStart:  offFrame,
					FrameEnd:    frameIdx,
					AnomalyType: "cooldown_bypass",
				},
			)
			events = append(events, ev)
		}
	}

	return events
}
