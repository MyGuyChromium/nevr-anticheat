package movement

import (
	"fmt"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Mov001 detects impossible player speed sustained over a window (MOV_001).
//
// Two branches share one sliding window of per-frame speed samples:
//
//   - sustained: the window MEDIAN exceeds max_legitimate_speed. Robust to
//     single-frame position glitches because a median needs half the
//     window to be wrong.
//   - burst: at least min_burst_frames samples in the window exceed the
//     game's hard MaxPlayerSpeed (from MatchContext.Physics) while the
//     median is still legitimate — an oscillating / toggled speed hack.
//     The threshold is the physics cap, never a fraction of it, and the
//     window is cleared after a burst event so one burst yields one event
//     rather than one per frame while it drains out of the window.
type Mov001 struct {
	detect.BaseDetector
	maxLegitimateSpeed   float64
	sustainedSpeedWindow int
	sigmoidSteepness     float64
	minBurstFrames       int

	speedWindow map[string][]float64
	sortBuf     []float64 // reusable buffer for sorting (avoids per-frame alloc)
}

// NewMov001 creates a new MOV_001 Impossible Player Speed detector.
func NewMov001(params map[string]any) *Mov001 {
	d := &Mov001{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "MOV_001",
			DetectorVersion:  "2.1.0",
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
		minBurstFrames:       detect.GetInt(params, "min_burst_frames", 5),
		speedWindow:          make(map[string][]float64),
	}
	d.sanitize()
	return d
}

func (d *Mov001) sanitize() {
	if d.sustainedSpeedWindow < 2 {
		d.sustainedSpeedWindow = 2
	}
	if d.minBurstFrames < 1 {
		d.minBurstFrames = 1
	}
	if d.minBurstFrames > d.sustainedSpeedWindow {
		d.minBurstFrames = d.sustainedSpeedWindow
	}
}

func (d *Mov001) Reset() {
	d.speedWindow = make(map[string][]float64)
}

func (d *Mov001) Configure(params map[string]any) error {
	d.maxLegitimateSpeed = detect.GetFloat(params, "max_legitimate_speed", d.maxLegitimateSpeed)
	d.sustainedSpeedWindow = detect.GetInt(params, "sustained_speed_window", d.sustainedSpeedWindow)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	d.minBurstFrames = detect.GetInt(params, "min_burst_frames", d.minBurstFrames)
	d.sanitize()
	return nil
}

// burstThreshold is the hard physics cap: the larger of the configured
// legitimate speed and MatchContext.Physics.MaxPlayerSpeed.
func (d *Mov001) burstThreshold(matchCtx *model.MatchContext) float64 {
	t := d.maxLegitimateSpeed
	if matchCtx != nil && matchCtx.Physics.MaxPlayerSpeed > t {
		t = matchCtx.Physics.MaxPlayerSpeed
	}
	return t
}

func (d *Mov001) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	burstThreshold := d.burstThreshold(matchCtx)

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
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

		if medianSpeed <= d.maxLegitimateSpeed {
			// Burst branch: count frames over the hard physics cap.
			burstFrames := 0
			for _, s := range window {
				if s > burstThreshold {
					burstFrames++
				}
			}
			if burstFrames < d.minBurstFrames {
				continue
			}

			severity := model.SigmoidConfidence(p90/burstThreshold, 1.2, 8.5)
			confidence := model.SigmoidConfidence(float64(burstFrames), float64(d.minBurstFrames), 1.0) * 0.6
			if ps.IsHighPing {
				confidence *= 0.6
			}
			metrics := map[string]float64{
				"p90_speed":              p90,
				"median_speed":           medianSpeed,
				"burst_threshold":        burstThreshold,
				"frames_above_threshold": float64(burstFrames),
				"min_burst_frames":       float64(d.minBurstFrames),
				"max_legitimate_speed":   d.maxLegitimateSpeed,
				"current_speed":          ps.Speed,
				"window_frames":          float64(d.sustainedSpeedWindow),
				"max_speed_in_window":    model.MaxFloat(window),
				"min_speed_in_window":    model.MinFloat(window),
			}
			ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
				severity, confidence,
				model.MovementEvidence{
					DetectorSpecific: "oscillating_speed_hack",
					Metrics:          metrics,
				},
				fmt.Sprintf("burst_speed: %d of %d frames above %.1f m/s (p90 %.1f, median %.1f)",
					burstFrames, d.sustainedSpeedWindow, burstThreshold, p90, medianSpeed),
				fmt.Sprintf("speed: 0-%.1f m/s (physics cap)", burstThreshold),
				model.CausalKey{
					PlayerID:    pid,
					FrameStart:  frameIdx - d.sustainedSpeedWindow + 1,
					FrameEnd:    frameIdx,
					AnomalyType: "oscillating_speed",
				},
			)
			events = append(events, ev)
			// One burst, one event: start a fresh window.
			d.speedWindow[pid] = nil
			continue
		}

		severity := model.SigmoidConfidence(medianSpeed, d.maxLegitimateSpeed*1.2, d.sigmoidSteepness)
		confidence := model.SigmoidConfidence(medianSpeed, d.maxLegitimateSpeed, d.sigmoidSteepness)

		if ps.IsHighPing {
			confidence *= 0.6
		}

		metrics := map[string]float64{
			"median_speed":         medianSpeed,
			"p90_speed":            p90,
			"max_legitimate_speed": d.maxLegitimateSpeed,
			"current_speed":        ps.Speed,
			"window_frames":        float64(d.sustainedSpeedWindow),
			"max_speed_in_window":  model.MaxFloat(window),
			"min_speed_in_window":  model.MinFloat(window),
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
				FrameStart:  frameIdx - d.sustainedSpeedWindow + 1,
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
