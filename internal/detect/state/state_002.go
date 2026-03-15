package state

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// State002 detects stun recovery exploit - recovering from stun too fast (STATE_002).
type State002 struct {
	detect.BaseDetector
	minStunFrames    int
	minIncidents     int
	sigmoidSteepness float64

	wasStunned     map[string]bool
	stunStartFrame map[string]int
	incidents      map[string]int
}

// NewState002 creates a new STATE_002 Stun Recovery Exploit detector.
func NewState002(params map[string]any) *State002 {
	d := &State002{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "STATE_002",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Stun Recovery Exploit",
			DetectorCategory: "state",
			Inputs:           []string{"stun_state"},
			Warmup:           10,
			Weight:           0.9,
			IsAutoEnforce:    false,
		},
		minStunFrames:    detect.GetInt(params, "min_stun_frames", 30),
		minIncidents:     detect.GetInt(params, "min_incidents", 2),
		sigmoidSteepness: detect.GetFloat(params, "sigmoid_steepness", 0.2),
		wasStunned:       make(map[string]bool),
		stunStartFrame:   make(map[string]int),
		incidents:        make(map[string]int),
	}
	return d
}

func (d *State002) Reset() {
	d.wasStunned = make(map[string]bool)
	d.stunStartFrame = make(map[string]int)
	d.incidents = make(map[string]int)
}

func (d *State002) Configure(params map[string]any) error {
	d.minStunFrames = detect.GetInt(params, "min_stun_frames", d.minStunFrames)
	d.minIncidents = detect.GetInt(params, "min_incidents", d.minIncidents)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *State002) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID

		if ps.IsStunned && !d.wasStunned[pid] {
			// Stun start
			d.stunStartFrame[pid] = frameIdx
			d.wasStunned[pid] = true
		} else if !ps.IsStunned && d.wasStunned[pid] {
			// Stun end - compute recovery frames
			d.wasStunned[pid] = false
			startFrame := d.stunStartFrame[pid]
			recoveryFrames := frameIdx - startFrame

			if recoveryFrames >= d.minStunFrames {
				continue
			}

			d.incidents[pid]++

			if d.incidents[pid] < d.minIncidents {
				continue
			}

			ratio := float64(recoveryFrames) / float64(d.minStunFrames)
			severity := model.SigmoidConfidence(float64(d.minStunFrames-recoveryFrames), 0, d.sigmoidSteepness)
			confidence := model.Clamp01(1.0 - ratio)
			confidence *= model.SigmoidConfidence(float64(d.incidents[pid]), float64(d.minIncidents), 0.5)

			metrics := map[string]float64{
				"recovery_frames":  float64(recoveryFrames),
				"min_stun_frames":  float64(d.minStunFrames),
				"stun_ratio":       ratio,
				"incident_count":   float64(d.incidents[pid]),
				"min_incidents":    float64(d.minIncidents),
			}

			ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
				severity, confidence,
				model.StateEvidence{
					DetectorSpecific: "stun_recovery_exploit",
					Metrics:          metrics,
				},
				fmt.Sprintf("stun_recovery: %d frames (min %d), %d incidents", recoveryFrames, d.minStunFrames, d.incidents[pid]),
				fmt.Sprintf("stun_duration: >=%d frames", d.minStunFrames),
				model.CausalKey{
					PlayerID:    pid,
					FrameStart:  startFrame,
					FrameEnd:    frameIdx,
					AnomalyType: "stun_recovery",
				},
			)
			events = append(events, ev)
		}
	}

	return events
}
