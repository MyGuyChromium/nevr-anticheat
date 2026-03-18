package state

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// State004 detects damage immunity during active play (STATE_004).
//
// STATUS: TELEMETRY_DEPENDENT — requires IsImmune field, which is not
// confirmed in standard Echo VR API or .echoreplay format. Disabled by default.
type State004 struct {
	detect.BaseDetector
	maxImmuneFrames  int
	sigmoidSteepness float64

	immuneFrames map[string]int
	fired        map[string]bool
}

// NewState004 creates a new STATE_004 Damage Immunity detector.
func NewState004(params map[string]any) *State004 {
	d := &State004{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "STATE_004",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Damage Immunity Exploit",
			DetectorCategory: "state",
			Inputs:           []string{"immune_state", "invulnerable_state"},
			Warmup:           10,
			Weight:           0.9,
			IsAutoEnforce:    false,
		},
		maxImmuneFrames:  detect.GetInt(params, "max_immune_frames",
			detect.GetInt(params, "immunity_threshold_frames", 225)),
		sigmoidSteepness: detect.GetFloat(params, "sigmoid_steepness", 0.05),
		immuneFrames:     make(map[string]int),
		fired:            make(map[string]bool),
	}
	return d
}

func (d *State004) Reset() {
	d.immuneFrames = make(map[string]int)
	d.fired = make(map[string]bool)
}

func (d *State004) Configure(params map[string]any) error {
	d.maxImmuneFrames = detect.GetInt(params, "max_immune_frames", d.maxImmuneFrames)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *State004) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID

		// Track invulnerable/immune frames during active play
		isImmune := ps.IsInvulnerable || ps.IsImmune
		isActive := !ps.IsStunned && ps.Speed > 0.1

		if isImmune && isActive {
			d.immuneFrames[pid]++
		} else if !isImmune {
			// Reset when immunity drops
			d.immuneFrames[pid] = 0
			d.fired[pid] = false
			continue
		}

		total := d.immuneFrames[pid]
		if total <= d.maxImmuneFrames {
			continue
		}

		if d.fired[pid] {
			continue
		}
		d.fired[pid] = true

		excess := total - d.maxImmuneFrames
		severity := model.SigmoidConfidence(float64(total), float64(d.maxImmuneFrames)*1.5, d.sigmoidSteepness)
		confidence := model.SigmoidConfidence(float64(excess), 0, 0.1)
		confidence = model.Clamp01(confidence * 0.9)

		metrics := map[string]float64{
			"immune_frames":     float64(total),
			"max_immune_frames": float64(d.maxImmuneFrames),
			"excess_frames":     float64(excess),
			"player_speed":      ps.Speed,
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.StateEvidence{
				DetectorSpecific: "damage_immunity",
				Metrics:          metrics,
			},
			fmt.Sprintf("immune_frames: %d (active play)", total),
			fmt.Sprintf("immune_frames: <%d during active play", d.maxImmuneFrames),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  frameIdx - total,
				FrameEnd:    frameIdx,
				AnomalyType: "damage_immunity",
			},
		)
		events = append(events, ev)
	}

	return events
}
