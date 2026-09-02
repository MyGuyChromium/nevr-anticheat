package state

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// State005 detects cooldown bypass - shield reactivated too quickly (STATE_005).
//
// STATUS: TELEMETRY_DEPENDENT — requires ShieldActive field (same as STATE_003).
// Disabled by default.
//
// The off-to-on gap is measured in SECONDS from frame timestamps when
// available (min_cooldown_seconds, or min_cooldown_frames converted at the
// match tick rate); frame counting is only the fallback for sources
// without timestamps.
type State005 struct {
	detect.BaseDetector
	minCooldownFrames  int
	minCooldownSeconds float64
	minViolations      int
	sigmoidSteepness   float64

	wasShieldActive map[string]bool
	shieldOffFrame  map[string]int
	shieldOffTime   map[string]float64
	violations      map[string]int
}

// NewState005 creates a new STATE_005 Cooldown Bypass detector.
func NewState005(params map[string]any) *State005 {
	d := &State005{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "STATE_005",
			DetectorVersion:  "2.1.0",
			DetectorName:     "Cooldown Bypass",
			DetectorCategory: "state",
			Inputs:           []string{"shield_state"},
			Warmup:           10,
			Weight:           0.85,
			IsAutoEnforce:    false,
		},
		minCooldownFrames:  detect.GetIntAlias(params, 60, "min_cooldown_frames", "cooldown_bypass_threshold_frames"),
		minCooldownSeconds: detect.GetFloat(params, "min_cooldown_seconds", 0),
		minViolations:      detect.GetIntAlias(params, 15, "min_violations", "min_violations_to_surface"),
		sigmoidSteepness:   detect.GetFloat(params, "sigmoid_steepness", 0.05),
	}
	d.Reset()
	return d
}

func (d *State005) Reset() {
	d.wasShieldActive = make(map[string]bool)
	d.shieldOffFrame = make(map[string]int)
	d.shieldOffTime = make(map[string]float64)
	d.violations = make(map[string]int)
}

func (d *State005) Configure(params map[string]any) error {
	d.minCooldownFrames = detect.GetIntAlias(params, d.minCooldownFrames, "min_cooldown_frames", "cooldown_bypass_threshold_frames")
	d.minCooldownSeconds = detect.GetFloat(params, "min_cooldown_seconds", d.minCooldownSeconds)
	d.minViolations = detect.GetIntAlias(params, d.minViolations, "min_violations", "min_violations_to_surface")
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *State005) minimumSeconds(matchCtx *model.MatchContext) float64 {
	if d.minCooldownSeconds > 0 {
		return d.minCooldownSeconds
	}
	return float64(d.minCooldownFrames) / tickRate(matchCtx)
}

func (d *State005) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID
		wasActive := d.wasShieldActive[pid]
		d.wasShieldActive[pid] = ps.ShieldActive

		if wasActive && !ps.ShieldActive {
			// Shield off transition
			d.shieldOffFrame[pid] = frameIdx
			d.shieldOffTime[pid] = ps.LastTimestamp
			continue
		}
		if wasActive || !ps.ShieldActive {
			continue
		}

		// Shield on transition - check gap
		offFrame, hasOff := d.shieldOffFrame[pid]
		if !hasOff {
			continue
		}

		gapFrames := frameIdx - offFrame
		gapSeconds := ps.LastTimestamp - d.shieldOffTime[pid]
		minSeconds := d.minimumSeconds(matchCtx)
		rate := tickRate(matchCtx)

		useSeconds := gapSeconds > 0
		var shortfallFrames, ratio float64
		if useSeconds {
			if gapSeconds >= minSeconds {
				continue
			}
			shortfallFrames = (minSeconds - gapSeconds) * rate
			ratio = gapSeconds / minSeconds
		} else {
			if gapFrames >= d.minCooldownFrames {
				continue
			}
			shortfallFrames = float64(d.minCooldownFrames - gapFrames)
			ratio = float64(gapFrames) / float64(d.minCooldownFrames)
			gapSeconds = float64(gapFrames) / rate
		}

		d.violations[pid]++

		if d.violations[pid] < d.minViolations {
			continue
		}

		severity := model.SigmoidConfidence(shortfallFrames, 0, d.sigmoidSteepness)
		confidence := model.Clamp01((1.0 - ratio) * 0.9)
		confidence *= model.SigmoidConfidence(float64(d.violations[pid]), float64(d.minViolations), 0.5)

		metrics := map[string]float64{
			"gap_frames":                float64(gapFrames),
			"gap_seconds":               gapSeconds,
			"min_cooldown":              float64(d.minCooldownFrames),
			"min_cooldown_seconds":      minSeconds,
			"expected_cooldown_seconds": physicsShieldCooldown(matchCtx),
			"cooldown_ratio":            ratio,
			"violation_count":           float64(d.violations[pid]),
			"min_violations":            float64(d.minViolations),
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.StateEvidence{
				DetectorSpecific: "cooldown_bypass",
				Metrics:          metrics,
			},
			fmt.Sprintf("shield_cooldown: %.2fs / %d frames (min %.2fs), %d violations", gapSeconds, gapFrames, minSeconds, d.violations[pid]),
			fmt.Sprintf("shield_cooldown: >=%.2fs", minSeconds),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  offFrame,
				FrameEnd:    frameIdx,
				AnomalyType: "cooldown_bypass",
			},
		)
		events = append(events, ev)
	}

	return events
}
