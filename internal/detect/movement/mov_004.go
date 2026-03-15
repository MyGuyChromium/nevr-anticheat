package movement

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Mov004 detects boost speed cap violations (MOV_004).
type Mov004 struct {
	detect.BaseDetector
	boostCapMargin   float64
	sigmoidSteepness float64

	wasBoosting map[string]bool
	peakSpeed   map[string]float64
}

// NewMov004 creates a new MOV_004 Boost Speed Cap detector.
func NewMov004(params map[string]any) *Mov004 {
	d := &Mov004{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "MOV_004",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Boost Speed Cap Violation",
			DetectorCategory: "movement",
			Inputs:           []string{"speed", "boosting"},
			Warmup:           5,
			Weight:           0.7,
			IsAutoEnforce:    false,
		},
		boostCapMargin:   detect.GetFloat(params, "boost_cap_margin", 1.5),
		sigmoidSteepness: detect.GetFloat(params, "sigmoid_steepness", 0.8),
		wasBoosting:      make(map[string]bool),
		peakSpeed:        make(map[string]float64),
	}
	return d
}

func (d *Mov004) Reset() {
	d.wasBoosting = make(map[string]bool)
	d.peakSpeed = make(map[string]float64)
}

func (d *Mov004) Configure(params map[string]any) error {
	d.boostCapMargin = detect.GetFloat(params, "boost_cap_margin", d.boostCapMargin)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Mov004) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	boostCap := matchCtx.Physics.BoostSpeedCap
	if boostCap <= 0 {
		boostCap = 5.0
	}
	threshold := boostCap + d.boostCapMargin

	for _, ps := range players {
		pid := ps.PlayerID

		if ps.IsBoosting {
			// Track peak speed during boost
			if ps.Speed > d.peakSpeed[pid] {
				d.peakSpeed[pid] = ps.Speed
			}
			d.wasBoosting[pid] = true
		} else if d.wasBoosting[pid] {
			// Boost just ended, check peak
			peak := d.peakSpeed[pid]
			d.wasBoosting[pid] = false
			d.peakSpeed[pid] = 0

			if peak <= threshold {
				continue
			}

			excess := peak - threshold
			severity := model.SigmoidConfidence(peak, threshold*1.2, d.sigmoidSteepness)
			confidence := model.SigmoidConfidence(excess, 0, 1.0)
			confidence = model.Clamp01(confidence * 0.85)

			metrics := map[string]float64{
				"peak_speed":     peak,
				"boost_cap":      boostCap,
				"cap_margin":     d.boostCapMargin,
				"threshold":      threshold,
				"excess":         excess,
			}

			ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
				severity, confidence,
				model.MovementEvidence{
					DetectorSpecific: "boost_speed_cap",
					Metrics:          metrics,
				},
				fmt.Sprintf("boost_peak_speed: %.1f m/s", peak),
				fmt.Sprintf("boost_speed: 0-%.1f m/s (cap %.1f + margin %.1f)", threshold, boostCap, d.boostCapMargin),
				model.CausalKey{
					PlayerID:    pid,
					FrameStart:  frameIdx - 10,
					FrameEnd:    frameIdx,
					AnomalyType: "boost_speed_cap",
				},
			)
			events = append(events, ev)
		}
	}

	return events
}
