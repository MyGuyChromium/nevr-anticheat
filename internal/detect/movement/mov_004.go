package movement

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Mov004 detects boost speed cap violations (MOV_004).
//
// STATUS: TELEMETRY_DEPENDENT — requires IsBoosting field, which is absent
// from the standard Echo VR API and unconfirmed in .echoreplay format.
// Disabled by default.
//
// The boost cap (Physics.BoostSpeedCap, 5 m/s) bounds the speed a boost
// ADDS, not the player's total speed (legitimate players reach 55 m/s), so
// the detector compares the gain over the pre-boost speed against
// cap + boost_cap_margin.
type Mov004 struct {
	detect.BaseDetector
	boostCapMargin   float64
	sigmoidSteepness float64

	wasBoosting     map[string]bool
	peakSpeed       map[string]float64
	preBoostSpeed   map[string]float64 // speed on the last non-boosting frame
	boostStartFrame map[string]int
	baselineSpeed   map[string]float64 // pre-boost speed captured at the boost start
}

// NewMov004 creates a new MOV_004 Boost Speed Cap detector.
func NewMov004(params map[string]any) *Mov004 {
	d := &Mov004{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "MOV_004",
			DetectorVersion:  "2.1.0",
			DetectorName:     "Boost Speed Cap Violation",
			DetectorCategory: "movement",
			Inputs:           []string{"speed", "boosting"},
			Warmup:           5,
			Weight:           0.7,
			IsAutoEnforce:    false,
		},
		boostCapMargin:   detect.GetFloatAlias(params, 1.5, "boost_cap_margin", "boost_margin"),
		sigmoidSteepness: detect.GetFloat(params, "sigmoid_steepness", 0.8),
	}
	d.Reset()
	return d
}

func (d *Mov004) Reset() {
	d.wasBoosting = make(map[string]bool)
	d.peakSpeed = make(map[string]float64)
	d.preBoostSpeed = make(map[string]float64)
	d.boostStartFrame = make(map[string]int)
	d.baselineSpeed = make(map[string]float64)
}

func (d *Mov004) Configure(params map[string]any) error {
	d.boostCapMargin = detect.GetFloatAlias(params, d.boostCapMargin, "boost_cap_margin", "boost_margin")
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Mov004) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	boostCap := 5.0
	if matchCtx != nil && matchCtx.Physics.BoostSpeedCap > 0 {
		boostCap = matchCtx.Physics.BoostSpeedCap
	}
	threshold := boostCap + d.boostCapMargin

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID

		if ps.IsBoosting {
			if !d.wasBoosting[pid] {
				d.boostStartFrame[pid] = frameIdx
				base, ok := d.preBoostSpeed[pid]
				if !ok {
					base = ps.Speed
				}
				d.baselineSpeed[pid] = base
				d.peakSpeed[pid] = 0
			}
			// Track peak speed during boost
			if ps.Speed > d.peakSpeed[pid] {
				d.peakSpeed[pid] = ps.Speed
			}
			d.wasBoosting[pid] = true
			continue
		}

		// Not boosting: remember the speed for the next boost's baseline.
		d.preBoostSpeed[pid] = ps.Speed
		if !d.wasBoosting[pid] {
			continue
		}

		// Boost just ended, check the gain over the pre-boost speed.
		peak := d.peakSpeed[pid]
		baseline := d.baselineSpeed[pid]
		startFrame := d.boostStartFrame[pid]
		d.wasBoosting[pid] = false
		d.peakSpeed[pid] = 0

		gain := peak - baseline
		if gain <= threshold {
			continue
		}

		excess := gain - threshold
		severity := model.SigmoidConfidence(gain, threshold*1.2, d.sigmoidSteepness)
		confidence := model.SigmoidConfidence(excess, 0, 1.0)
		confidence = model.Clamp01(confidence * 0.85)

		metrics := map[string]float64{
			"peak_speed":      peak,
			"pre_boost_speed": baseline,
			"boost_gain":      gain,
			"boost_cap":       boostCap,
			"cap_margin":      d.boostCapMargin,
			"threshold":       threshold,
			"excess":          excess,
			"boost_frames":    float64(frameIdx - startFrame),
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.MovementEvidence{
				DetectorSpecific: "boost_speed_cap",
				Metrics:          metrics,
			},
			fmt.Sprintf("boost_gain: %.1f m/s (%.1f -> %.1f m/s)", gain, baseline, peak),
			fmt.Sprintf("boost_gain: 0-%.1f m/s (cap %.1f + margin %.1f)", threshold, boostCap, d.boostCapMargin),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  startFrame,
				FrameEnd:    frameIdx,
				AnomalyType: "boost_speed_cap",
			},
		)
		events = append(events, ev)
	}

	return events
}
