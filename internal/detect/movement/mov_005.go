package movement

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Mov005 detects boost spam abuse (MOV_005).
//
// STATUS: TELEMETRY_DEPENDENT — requires IsBoosting field (same as MOV_004).
// Also has a known sequence counter design limitation. Disabled by default.
type Mov005 struct {
	detect.BaseDetector
	windowSeconds       float64
	maxBoostsPerWindow  int
	maxConsecutive      int
	rechargePauseFrames int
	minSequences        int
	sigmoidSteepness    float64

	boostTimestamps    map[string][]float64
	consecutiveBoosts  map[string]int
	lastBoostEndFrame  map[string]int
	sequenceCount      map[string]int
}

// NewMov005 creates a new MOV_005 Boost Spam detector.
func NewMov005(params map[string]any) *Mov005 {
	d := &Mov005{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "MOV_005",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Boost Spam",
			DetectorCategory: "movement",
			Inputs:           []string{"boosting", "speed"},
			Warmup:           10,
			Weight:           0.6,
			IsAutoEnforce:    false,
		},
		windowSeconds:       detect.GetFloat(params, "window_seconds", 10.0),
		maxBoostsPerWindow:  detect.GetInt(params, "max_boosts_per_window", 8),
		maxConsecutive:       detect.GetInt(params, "max_consecutive", 5),
		rechargePauseFrames: detect.GetInt(params, "recharge_pause_frames", 10),
		minSequences:         detect.GetInt(params, "min_sequences", 2),
		sigmoidSteepness:    detect.GetFloat(params, "sigmoid_steepness", 0.5),
		boostTimestamps:     make(map[string][]float64),
		consecutiveBoosts:   make(map[string]int),
		lastBoostEndFrame:   make(map[string]int),
		sequenceCount:       make(map[string]int),
	}
	return d
}

func (d *Mov005) Reset() {
	d.boostTimestamps = make(map[string][]float64)
	d.consecutiveBoosts = make(map[string]int)
	d.lastBoostEndFrame = make(map[string]int)
	d.sequenceCount = make(map[string]int)
}

func (d *Mov005) Configure(params map[string]any) error {
	d.windowSeconds = detect.GetFloat(params, "window_seconds", d.windowSeconds)
	d.maxBoostsPerWindow = detect.GetInt(params, "max_boosts_per_window", d.maxBoostsPerWindow)
	d.maxConsecutive = detect.GetInt(params, "max_consecutive", d.maxConsecutive)
	d.rechargePauseFrames = detect.GetInt(params, "recharge_pause_frames", d.rechargePauseFrames)
	d.minSequences = detect.GetInt(params, "min_sequences", d.minSequences)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Mov005) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID

		if ps.IsBoosting {
			// Check if this is a new boost (gap since last)
			lastEnd := d.lastBoostEndFrame[pid]
			if frameIdx-lastEnd > d.rechargePauseFrames || lastEnd == 0 {
				// New boost sequence
				d.consecutiveBoosts[pid]++
				d.boostTimestamps[pid] = append(d.boostTimestamps[pid], ps.LastTimestamp)
			}
		} else {
			if d.consecutiveBoosts[pid] > 0 {
				d.lastBoostEndFrame[pid] = frameIdx
			}
			// Reset consecutive on pause
			if frameIdx-d.lastBoostEndFrame[pid] > d.rechargePauseFrames {
				if d.consecutiveBoosts[pid] > d.maxConsecutive {
					d.sequenceCount[pid]++
				}
				d.consecutiveBoosts[pid] = 0
			}
		}

		// Trim boost timestamps to window
		cutoff := ps.LastTimestamp - d.windowSeconds
		timestamps := d.boostTimestamps[pid]
		start := 0
		for start < len(timestamps) && timestamps[start] < cutoff {
			start++
		}
		if start > 0 {
			d.boostTimestamps[pid] = timestamps[start:]
		}

		boostsInWindow := len(d.boostTimestamps[pid])
		consecutive := d.consecutiveBoosts[pid]
		sequences := d.sequenceCount[pid]

		frequencyViolation := boostsInWindow > d.maxBoostsPerWindow
		consecutiveViolation := consecutive > d.maxConsecutive

		if !frequencyViolation && !consecutiveViolation {
			continue
		}
		if sequences < d.minSequences {
			continue
		}

		severity := 0.0
		if frequencyViolation {
			severity = model.SigmoidConfidence(float64(boostsInWindow), float64(d.maxBoostsPerWindow)*1.5, d.sigmoidSteepness)
		}
		if consecutiveViolation {
			s := model.SigmoidConfidence(float64(consecutive), float64(d.maxConsecutive)*1.5, d.sigmoidSteepness)
			if s > severity {
				severity = s
			}
		}
		confidence := model.Clamp01(severity * 0.8)

		metrics := map[string]float64{
			"boosts_in_window":    float64(boostsInWindow),
			"max_boosts_window":   float64(d.maxBoostsPerWindow),
			"consecutive_boosts":  float64(consecutive),
			"max_consecutive":     float64(d.maxConsecutive),
			"violation_sequences": float64(sequences),
			"window_seconds":      d.windowSeconds,
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.MovementEvidence{
				DetectorSpecific: "boost_spam",
				Metrics:          metrics,
			},
			fmt.Sprintf("boost_spam: %d boosts/%.0fs, %d consecutive, %d sequences", boostsInWindow, d.windowSeconds, consecutive, sequences),
			fmt.Sprintf("boosts: <%d per %.0fs, <%d consecutive", d.maxBoostsPerWindow, d.windowSeconds, d.maxConsecutive),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  frameIdx - 60,
				FrameEnd:    frameIdx,
				AnomalyType: "boost_spam",
			},
		)
		events = append(events, ev)

		// Reset after detection
		d.boostTimestamps[pid] = nil
		d.consecutiveBoosts[pid] = 0
		d.sequenceCount[pid] = 0
	}

	return events
}
