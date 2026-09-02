package state

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// State002 detects stun recovery exploit - recovering from stun too fast (STATE_002).
//
// Stun duration is measured in SECONDS from frame timestamps whenever they
// are available, so a poll stall that packs a legitimate 3 s stun into few
// frames is not mistaken for an early recovery. The minimum is
// min_stun_seconds when set, otherwise min_stun_frames converted at the
// match tick rate (default 15 Hz). Frame counting is only the fallback for
// sources without timestamps.
type State002 struct {
	detect.BaseDetector
	minStunFrames    int
	minStunSeconds   float64
	minIncidents     int
	sigmoidSteepness float64

	wasStunned     map[string]bool
	stunStartFrame map[string]int
	stunStartTime  map[string]float64
	incidents      map[string]int
}

// NewState002 creates a new STATE_002 Stun Recovery Exploit detector.
func NewState002(params map[string]any) *State002 {
	d := &State002{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "STATE_002",
			DetectorVersion:  "2.1.0",
			DetectorName:     "Stun Recovery Exploit",
			DetectorCategory: "state",
			Inputs:           []string{"stun_state"},
			Warmup:           10,
			Weight:           0.9,
			IsAutoEnforce:    false,
		},
		minStunFrames:    detect.GetInt(params, "min_stun_frames", 30),
		minStunSeconds:   detect.GetFloat(params, "min_stun_seconds", 0),
		minIncidents:     detect.GetIntAlias(params, 2, "min_incidents", "min_incidents_to_surface"),
		sigmoidSteepness: detect.GetFloat(params, "sigmoid_steepness", 0.2),
	}
	d.Reset()
	return d
}

func (d *State002) Reset() {
	d.wasStunned = make(map[string]bool)
	d.stunStartFrame = make(map[string]int)
	d.stunStartTime = make(map[string]float64)
	d.incidents = make(map[string]int)
}

func (d *State002) Configure(params map[string]any) error {
	d.minStunFrames = detect.GetInt(params, "min_stun_frames", d.minStunFrames)
	d.minStunSeconds = detect.GetFloat(params, "min_stun_seconds", d.minStunSeconds)
	d.minIncidents = detect.GetIntAlias(params, d.minIncidents, "min_incidents", "min_incidents_to_surface")
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

// minimumSeconds is the shortest legitimate stun in seconds.
func (d *State002) minimumSeconds(matchCtx *model.MatchContext) float64 {
	if d.minStunSeconds > 0 {
		return d.minStunSeconds
	}
	return float64(d.minStunFrames) / tickRate(matchCtx)
}

func (d *State002) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID

		if ps.IsStunned && !d.wasStunned[pid] {
			// Stun start
			d.stunStartFrame[pid] = frameIdx
			d.stunStartTime[pid] = ps.LastTimestamp
			d.wasStunned[pid] = true
			continue
		}
		if ps.IsStunned || !d.wasStunned[pid] {
			continue
		}

		// Stun end - measure the stun duration
		d.wasStunned[pid] = false
		startFrame := d.stunStartFrame[pid]
		recoveryFrames := frameIdx - startFrame
		recoverySeconds := ps.LastTimestamp - d.stunStartTime[pid]
		minSeconds := d.minimumSeconds(matchCtx)
		rate := tickRate(matchCtx)

		useSeconds := recoverySeconds > 0
		var shortfallFrames, ratio float64
		if useSeconds {
			if recoverySeconds >= minSeconds {
				continue
			}
			shortfallFrames = (minSeconds - recoverySeconds) * rate
			ratio = recoverySeconds / minSeconds
		} else {
			if recoveryFrames >= d.minStunFrames {
				continue
			}
			shortfallFrames = float64(d.minStunFrames - recoveryFrames)
			ratio = float64(recoveryFrames) / float64(d.minStunFrames)
			recoverySeconds = float64(recoveryFrames) / rate
		}

		d.incidents[pid]++

		if d.incidents[pid] < d.minIncidents {
			continue
		}

		severity := model.SigmoidConfidence(shortfallFrames, 0, d.sigmoidSteepness)
		confidence := model.Clamp01(1.0 - ratio)
		confidence *= model.SigmoidConfidence(float64(d.incidents[pid]), float64(d.minIncidents), 0.5)

		metrics := map[string]float64{
			"recovery_frames":       float64(recoveryFrames),
			"recovery_seconds":      recoverySeconds,
			"min_stun_frames":       float64(d.minStunFrames),
			"min_stun_seconds":      minSeconds,
			"expected_stun_seconds": physicsStunDuration(matchCtx),
			"stun_ratio":            ratio,
			"incident_count":        float64(d.incidents[pid]),
			"min_incidents":         float64(d.minIncidents),
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.StateEvidence{
				DetectorSpecific: "stun_recovery_exploit",
				Metrics:          metrics,
			},
			fmt.Sprintf("stun_recovery: %.2fs / %d frames (min %.2fs), %d incidents", recoverySeconds, recoveryFrames, minSeconds, d.incidents[pid]),
			fmt.Sprintf("stun_duration: >=%.2fs", minSeconds),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  startFrame,
				FrameEnd:    frameIdx,
				AnomalyType: "stun_recovery",
			},
		)
		events = append(events, ev)
	}

	return events
}
