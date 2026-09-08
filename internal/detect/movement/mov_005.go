package movement

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Mov005 detects boost spam abuse (MOV_005).
//
// STATUS: TELEMETRY_DEPENDENT — requires IsBoosting field (same as MOV_004).
// Disabled by default.
//
// Counting is done on boost ACTIVATIONS (IsBoosting rising edges), never on
// boosting frames: one 12-frame boost is one boost. A run of activations
// whose gaps are all <= recharge_pause_frames is one "consecutive"
// sequence; when a longer pause finally arrives the sequence closes, and
// closes as a violation if it held more than max_consecutive activations.
// Events require min_sequences such violation sequences plus either the
// per-window frequency or the consecutive limit being exceeded.
//
// Config keys: max_boosts_per_10s and min_sequences_to_surface from
// configs/default.toml are accepted as aliases (same semantics).
// max_consecutive_boosts is NOT: default.toml documents it as consecutive
// boost FRAMES, which is the frame-counting defect this version removes,
// so its value (8) would be meaningless as an activation limit.
type Mov005 struct {
	detect.BaseDetector
	windowSeconds       float64
	maxBoostsPerWindow  int
	maxConsecutive      int
	rechargePauseFrames int
	minSequences        int
	sigmoidSteepness    float64

	boostTimestamps   map[string][]float64
	wasBoosting       map[string]bool
	consecutiveBoosts map[string]int
	lastBoostEndFrame map[string]int // -1 until the first boost ends
	sequenceCount     map[string]int
	observations      map[string]*boostSample
}

// NewMov005 creates a new MOV_005 Boost Spam detector.
func NewMov005(params map[string]any) *Mov005 {
	d := &Mov005{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "MOV_005",
			DetectorVersion:  "2.2.0",
			DetectorName:     "Boost Spam",
			DetectorCategory: "movement",
			Inputs:           []string{"boosting", "speed"},
			Warmup:           10,
			Weight:           0.6,
			IsAutoEnforce:    false,
			TraceBranches:    true,
		},
		windowSeconds:       detect.GetFloat(params, "window_seconds", 10.0),
		maxBoostsPerWindow:  detect.GetIntAlias(params, 25, "max_boosts_per_window", "max_boosts_per_10s"),
		maxConsecutive:      detect.GetInt(params, "max_consecutive", 5),
		rechargePauseFrames: detect.GetInt(params, "recharge_pause_frames", 10),
		minSequences:        detect.GetIntAlias(params, 2, "min_sequences", "min_sequences_to_surface"),
		sigmoidSteepness:    detect.GetFloat(params, "sigmoid_steepness", 0.5),
	}
	d.Reset()
	return d
}

func (d *Mov005) Reset() {
	d.boostTimestamps = make(map[string][]float64)
	d.wasBoosting = make(map[string]bool)
	d.consecutiveBoosts = make(map[string]int)
	d.lastBoostEndFrame = make(map[string]int)
	d.sequenceCount = make(map[string]int)
	d.observations = make(map[string]*boostSample)
}

func (d *Mov005) clearBoost(pid string) {
	delete(d.boostTimestamps, pid)
	delete(d.wasBoosting, pid)
	delete(d.consecutiveBoosts, pid)
	delete(d.lastBoostEndFrame, pid)
	delete(d.sequenceCount, pid)
	delete(d.observations, pid)
}

func (d *Mov005) Configure(params map[string]any) error {
	d.windowSeconds = detect.GetFloat(params, "window_seconds", d.windowSeconds)
	d.maxBoostsPerWindow = detect.GetIntAlias(params, d.maxBoostsPerWindow, "max_boosts_per_window", "max_boosts_per_10s")
	d.maxConsecutive = detect.GetInt(params, "max_consecutive", d.maxConsecutive)
	d.rechargePauseFrames = detect.GetInt(params, "recharge_pause_frames", d.rechargePauseFrames)
	d.minSequences = detect.GetIntAlias(params, d.minSequences, "min_sequences", "min_sequences_to_surface")
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Mov005) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID
		current := readBoostSample(ps, frameIdx)
		if current == nil {
			d.clearBoost(pid)
			d.TraceDecision(pid, frameIdx, "boost_input_unavailable")
			continue
		}
		if !continuousBoostSample(d.observations[pid], current, ps.FrameDt) {
			d.clearBoost(pid)
			d.observations[pid] = current
			d.wasBoosting[pid] = ps.IsBoosting
			d.TraceDecision(pid, frameIdx, "boost_baseline_unavailable")
			continue
		}
		d.observations[pid] = current
		wasBoosting := d.wasBoosting[pid]
		d.wasBoosting[pid] = ps.IsBoosting
		lastEnd, hasEnd := d.lastBoostEndFrame[pid]

		if ps.IsBoosting {
			if !wasBoosting {
				// Activation edge: one boost.
				d.consecutiveBoosts[pid]++
				d.boostTimestamps[pid] = append(d.boostTimestamps[pid], ps.LastTimestamp)
			}
		} else {
			if wasBoosting && d.consecutiveBoosts[pid] > 0 {
				// Falling edge: the boost ended on this frame.
				d.lastBoostEndFrame[pid] = frameIdx
				lastEnd, hasEnd = frameIdx, true
			}
			// A pause longer than the recharge window closes the sequence.
			if hasEnd && d.consecutiveBoosts[pid] > 0 && frameIdx-lastEnd > d.rechargePauseFrames {
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
