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
//
// Severity is a sigmoid on the ratio of the immune streak to
// max_immune_frames (0.5 at 1.25x, ~0.9 at 1.5x, ~1.0 at 2x) and the
// detector re-fires every escalation_interval_frames (default: half of
// max_immune_frames) while the streak continues, so a 60 s god-mode streak
// produces escalating evidence instead of one score-inert event at
// severity 0.005.
type State004 struct {
	detect.BaseDetector
	maxImmuneFrames          int
	escalationIntervalCfg    int // raw escalation_interval_frames; 0 = derive from max_immune_frames
	escalationIntervalFrames int // effective interval, recomputed by sanitize()
	sigmoidSteepness         float64

	immuneFrames map[string]int
	nextFireAt   map[string]int
}

// NewState004 creates a new STATE_004 Damage Immunity detector.
func NewState004(params map[string]any) *State004 {
	d := &State004{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "STATE_004",
			DetectorVersion:  "2.1.0",
			DetectorName:     "Damage Immunity Exploit",
			DetectorCategory: "state",
			Inputs:           []string{"immune_state", "invulnerable_state"},
			Warmup:           10,
			Weight:           0.9,
			IsAutoEnforce:    false,
		},
		maxImmuneFrames:       detect.GetIntAlias(params, 225, "max_immune_frames", "immunity_threshold_frames"),
		escalationIntervalCfg: detect.GetInt(params, "escalation_interval_frames", 0),
		sigmoidSteepness:      detect.GetFloat(params, "sigmoid_steepness", 8.0),
	}
	d.Reset()
	d.sanitize()
	return d
}

// sanitize derives the effective escalation interval from the RAW
// configured value every time, so a later Configure({max_immune_frames})
// re-derives the "0 = max_immune_frames/2" default instead of keeping the
// interval computed from the previous limit.
func (d *State004) sanitize() {
	if d.maxImmuneFrames < 1 {
		d.maxImmuneFrames = 1
	}
	d.escalationIntervalFrames = d.escalationIntervalCfg
	if d.escalationIntervalFrames <= 0 {
		d.escalationIntervalFrames = d.maxImmuneFrames / 2
	}
	if d.escalationIntervalFrames < 1 {
		d.escalationIntervalFrames = 1
	}
}

func (d *State004) Reset() {
	d.immuneFrames = make(map[string]int)
	d.nextFireAt = make(map[string]int)
}

func (d *State004) Configure(params map[string]any) error {
	d.maxImmuneFrames = detect.GetIntAlias(params, d.maxImmuneFrames, "max_immune_frames", "immunity_threshold_frames")
	d.escalationIntervalCfg = detect.GetInt(params, "escalation_interval_frames", d.escalationIntervalCfg)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	d.sanitize()
	return nil
}

func (d *State004) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID

		// Track invulnerable/immune frames during active play
		isImmune := ps.IsInvulnerable || ps.IsImmune
		isActive := !ps.IsStunned && ps.Speed > 0.1

		if isImmune && isActive {
			d.immuneFrames[pid]++
		} else if !isImmune {
			// Reset when immunity drops
			d.immuneFrames[pid] = 0
			d.nextFireAt[pid] = 0
			continue
		}

		total := d.immuneFrames[pid]
		// Require at least 5 excess frames before firing. Single-frame
		// excesses are timing artifacts from variable frame rates and
		// interpolation boundaries, not cheating.
		const toleranceFrames = 5
		if total <= d.maxImmuneFrames+toleranceFrames {
			continue
		}

		if total < d.nextFireAt[pid] {
			continue
		}
		d.nextFireAt[pid] = total + d.escalationIntervalFrames

		excess := total - d.maxImmuneFrames
		ratio := float64(total) / float64(d.maxImmuneFrames)
		severity := model.SigmoidConfidence(ratio, 1.25, d.sigmoidSteepness)
		confidence := model.SigmoidConfidence(float64(excess), 0, 0.1)
		confidence = model.Clamp01(confidence * 0.9)

		metrics := map[string]float64{
			"immune_frames":     float64(total),
			"max_immune_frames": float64(d.maxImmuneFrames),
			"excess_frames":     float64(excess),
			"streak_ratio":      ratio,
			"player_speed":      ps.Speed,
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.StateEvidence{
				DetectorSpecific: "damage_immunity",
				Metrics:          metrics,
			},
			fmt.Sprintf("immune_frames: %d (active play, %.2fx limit)", total, ratio),
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
