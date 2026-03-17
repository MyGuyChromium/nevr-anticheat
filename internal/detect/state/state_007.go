package state

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// State007 detects impossible punch range (STATE_007).
type State007 struct {
	detect.BaseDetector
	punchRangeThreshold float64
	velocityAdjustScale float64
	minIncidents        int
	sigmoidSteepness    float64

	prevStuns map[string]int
	incidents map[string]int
}

// NewState007 creates a new STATE_007 Punch Range detector.
func NewState007(params map[string]any) *State007 {
	d := &State007{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "STATE_007",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Impossible Punch Range",
			DetectorCategory: "state",
			Inputs:           []string{"stun_state", "hand_tracking", "position"},
			Warmup:           10,
			Weight:           0.8,
			IsAutoEnforce:    false,
		},
		punchRangeThreshold: detect.GetFloat(params, "punch_range_threshold", 10.0),
		velocityAdjustScale: detect.GetFloat(params, "velocity_adjust_scale", 0.15),
		minIncidents:        detect.GetInt(params, "min_incidents",
			detect.GetInt(params, "min_incidents_to_surface", 3)),
		sigmoidSteepness:    detect.GetFloat(params, "sigmoid_steepness", 1.0),
		prevStuns:           make(map[string]int),
		incidents:           make(map[string]int),
	}
	return d
}

func (d *State007) Reset() {
	d.prevStuns = make(map[string]int)
	d.incidents = make(map[string]int)
}

func (d *State007) Configure(params map[string]any) error {
	d.punchRangeThreshold = detect.GetFloat(params, "punch_range_threshold", d.punchRangeThreshold)
	d.velocityAdjustScale = detect.GetFloat(params, "velocity_adjust_scale", d.velocityAdjustScale)
	d.minIncidents = detect.GetInt(params, "min_incidents", d.minIncidents)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *State007) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	// Detect stun increments: find players whose stun count increased
	for _, ps := range players {
		pid := ps.PlayerID
		prevStuns := d.prevStuns[pid]
		d.prevStuns[pid] = ps.PrevStuns

		if ps.PrevStuns <= prevStuns {
			continue
		}

		// This player just stunned someone. Find the nearest opponent who became stunned.
		team := ps.Team
		var nearestDist float64 = math.MaxFloat64
		var nearestVictim string

		for _, other := range players {
			if other.PlayerID == pid {
				continue
			}
			if other.Team == team {
				continue
			}
			if !other.IsStunned {
				continue
			}

			// Measure hand-to-head distance (punch)
			leftDist := ps.LeftHand.Distance(other.Position)
			rightDist := ps.RightHand.Distance(other.Position)
			handDist := math.Min(leftDist, rightDist)

			if handDist < nearestDist {
				nearestDist = handDist
				nearestVictim = other.PlayerID
			}
		}

		if nearestVictim == "" {
			continue
		}

		// Adjust for closing velocity
		closingSpeed := ps.Speed
		adjustedDist := nearestDist - (closingSpeed * d.velocityAdjustScale)
		if adjustedDist < 0 {
			adjustedDist = 0
		}

		if adjustedDist <= d.punchRangeThreshold {
			continue
		}

		// Sanity cap: distances > 25m are data timing artifacts, not real punches
		if adjustedDist > 25.0 {
			continue
		}

		d.incidents[pid]++
		if d.incidents[pid] < d.minIncidents {
			continue
		}

		excess := adjustedDist - d.punchRangeThreshold
		severity := model.SigmoidConfidence(adjustedDist, d.punchRangeThreshold*1.5, d.sigmoidSteepness)
		confidence := model.SigmoidConfidence(excess, 0, d.sigmoidSteepness)
		confidence *= model.SigmoidConfidence(float64(d.incidents[pid]), float64(d.minIncidents), 0.5)
		confidence = model.Clamp01(confidence)

		metrics := map[string]float64{
			"hand_to_head_distance": nearestDist,
			"adjusted_distance":     adjustedDist,
			"punch_range_threshold": d.punchRangeThreshold,
			"closing_speed":         closingSpeed,
			"incident_count":        float64(d.incidents[pid]),
			"excess":                excess,
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.StateEvidence{
				DetectorSpecific: "punch_range",
				Metrics:          metrics,
			},
			fmt.Sprintf("punch_range: %.2f m (adjusted: %.2f m) victim=%s", nearestDist, adjustedDist, nearestVictim),
			fmt.Sprintf("punch_range: 0-%.2f m", d.punchRangeThreshold),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  frameIdx - 2,
				FrameEnd:    frameIdx,
				AnomalyType: "punch_range",
			},
		)
		events = append(events, ev)
	}

	return events
}
