package movement

import (
	"fmt"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Mov001 detects impossible player speed sustained over a window (MOV_001).
type Mov001 struct {
	detect.BaseDetector
	maxLegitimateSpeed    float64
	sustainedSpeedWindow  int
	sigmoidSteepness      float64

	speedWindow map[string][]float64
	sortBuf     []float64 // reusable buffer for sorting (avoids per-frame alloc)
}

// NewMov001 creates a new MOV_001 Impossible Player Speed detector.
func NewMov001(params map[string]any) *Mov001 {
	d := &Mov001{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "MOV_001",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Impossible Player Speed",
			DetectorCategory: "movement",
			Inputs:           []string{"position", "velocity", "speed"},
			Warmup:           10,
			Weight:           0.9,
			IsAutoEnforce:    false,
		},
		maxLegitimateSpeed:   detect.GetFloat(params, "max_legitimate_speed", 12.0),
		sustainedSpeedWindow: detect.GetInt(params, "sustained_speed_window", 10),
		sigmoidSteepness:     detect.GetFloat(params, "sigmoid_steepness", 0.5),
		speedWindow:          make(map[string][]float64),
	}
	return d
}

func (d *Mov001) Reset() {
	d.speedWindow = make(map[string][]float64)
}

func (d *Mov001) Configure(params map[string]any) error {
	d.maxLegitimateSpeed = detect.GetFloat(params, "max_legitimate_speed", d.maxLegitimateSpeed)
	d.sustainedSpeedWindow = detect.GetInt(params, "sustained_speed_window", d.sustainedSpeedWindow)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Mov001) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID

		sw := d.speedWindow[pid]
		model.PushFloat64History(&sw, ps.Speed, d.sustainedSpeedWindow)
		d.speedWindow[pid] = sw

		window := d.speedWindow[pid]
		if len(window) < d.sustainedSpeedWindow {
			continue
		}

		// Single sort for both median and p90. Reuse sort buffer to avoid alloc.
		if cap(d.sortBuf) < len(window) {
			d.sortBuf = make([]float64, len(window))
		}
		sorted := d.sortBuf[:len(window)]
		copy(sorted, window)
		sort.Float64s(sorted)
		n := len(sorted)
		medianSpeed := sorted[n/2]
		if n%2 == 0 && n > 1 {
			medianSpeed = (sorted[n/2-1] + sorted[n/2]) / 2.0
		}
		p90 := sorted[int(float64(n)*0.9)]
		p90Threshold := d.maxLegitimateSpeed * 0.9
		if p90 > p90Threshold && medianSpeed <= d.maxLegitimateSpeed {
			p90Severity := model.SigmoidConfidence(p90, p90Threshold, 0.2)
			p90Confidence := p90Severity * 0.6
			if ps.IsHighPing {
				p90Confidence *= 0.6
			}
			p90Metrics := map[string]float64{
				"p90_speed":            p90,
				"median_speed":         medianSpeed,
				"max_legitimate_speed": d.maxLegitimateSpeed,
				"current_speed":        ps.Speed,
				"window_frames":        float64(d.sustainedSpeedWindow),
				"max_speed_in_window":  model.MaxFloat(window),
				"min_speed_in_window":  model.MinFloat(window),
			}
			ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
				p90Severity, p90Confidence,
				model.MovementEvidence{
					DetectorSpecific: "oscillating_speed_hack",
					Metrics:          p90Metrics,
				},
				fmt.Sprintf("p90_speed: %.1f m/s over %d frames (median: %.1f)", p90, d.sustainedSpeedWindow, medianSpeed),
				fmt.Sprintf("p90_speed: 0-%.1f m/s", p90Threshold),
				model.CausalKey{
					PlayerID:    pid,
					FrameStart:  frameIdx - d.sustainedSpeedWindow,
					FrameEnd:    frameIdx,
					AnomalyType: "oscillating_speed",
				},
			)
			events = append(events, ev)
		}

		if medianSpeed <= d.maxLegitimateSpeed {
			continue
		}

		severity := model.SigmoidConfidence(medianSpeed, d.maxLegitimateSpeed*1.2, d.sigmoidSteepness)
		confidence := model.SigmoidConfidence(medianSpeed, d.maxLegitimateSpeed, d.sigmoidSteepness)

		if ps.IsHighPing {
			confidence *= 0.6
		}

		metrics := map[string]float64{
			"median_speed":          medianSpeed,
			"max_legitimate_speed":  d.maxLegitimateSpeed,
			"current_speed":         ps.Speed,
			"window_frames":         float64(d.sustainedSpeedWindow),
			"max_speed_in_window":   model.MaxFloat(window),
			"min_speed_in_window":   model.MinFloat(window),
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.MovementEvidence{
				DetectorSpecific: "impossible_player_speed",
				Metrics:          metrics,
			},
			fmt.Sprintf("median_speed: %.1f m/s over %d frames", medianSpeed, d.sustainedSpeedWindow),
			fmt.Sprintf("median_speed: 0-%.1f m/s", d.maxLegitimateSpeed),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  frameIdx - d.sustainedSpeedWindow,
				FrameEnd:    frameIdx,
				AnomalyType: "impossible_speed",
			},
		)
		events = append(events, ev)

		// Reset window after detection
		d.speedWindow[pid] = nil
	}

	return events
}
