package pattern

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Pat005 detects playspace abuse - hands too far from head for sustained frames (PAT_005).
//
// A zero hand vector is VR tracking loss (the adapter passes absent hand
// positions through as (0,0,0), which is the arena centre, not a hand); a
// frame with either hand untracked is skipped and the streak reset, so a
// 2 s occlusion can never read as a 30 m reach. Severity comes from how far
// the reach exceeds the threshold; confidence from how long it has been
// sustained. The streak counter keeps growing while the reach persists and
// the detector re-fires every min_sustained_frames, so a 40 s streak is
// distinguishable from a 2 s one.
type Pat005 struct {
	detect.BaseDetector
	handToHeadThreshold float64
	minSustainedFrames  int
	sigmoidSteepness    float64 // per-metre steepness of the severity sigmoid

	consecutiveFrames map[string]int
	lastEmitAt        map[string]int
}

// NewPat005 creates a new PAT_005 Playspace Abuse detector.
func NewPat005(params map[string]any) *Pat005 {
	d := &Pat005{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "PAT_005",
			DetectorVersion:  "2.1.0",
			DetectorName:     "Playspace Abuse",
			DetectorCategory: "pattern",
			Inputs:           []string{"hand_tracking", "head_position"},
			Warmup:           10,
			Weight:           0.6,
			IsAutoEnforce:    false,
		},
		handToHeadThreshold: detect.GetFloatAlias(params, 1.6, "hand_to_head_threshold", "max_hand_to_head_distance"),
		minSustainedFrames:  detect.GetInt(params, "min_sustained_frames", 30),
		sigmoidSteepness:    detect.GetFloat(params, "sigmoid_steepness", 2.0),
	}
	d.Reset()
	d.sanitize()
	return d
}

func (d *Pat005) sanitize() {
	if d.minSustainedFrames < 1 {
		d.minSustainedFrames = 1
	}
}

func (d *Pat005) Reset() {
	d.consecutiveFrames = make(map[string]int)
	d.lastEmitAt = make(map[string]int)
}

func (d *Pat005) Configure(params map[string]any) error {
	d.handToHeadThreshold = detect.GetFloatAlias(params, d.handToHeadThreshold, "hand_to_head_threshold", "max_hand_to_head_distance")
	d.minSustainedFrames = detect.GetInt(params, "min_sustained_frames", d.minSustainedFrames)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	d.sanitize()
	return nil
}

func (d *Pat005) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID

		// Tracking loss on either hand: not measurable this frame.
		if ps.LeftHand.IsZero() || ps.RightHand.IsZero() || ps.Position.IsZero() {
			d.consecutiveFrames[pid] = 0
			d.lastEmitAt[pid] = 0
			continue
		}

		// Compute max hand-to-head distance (use position as head proxy)
		leftDist := ps.LeftHand.Distance(ps.Position)
		rightDist := ps.RightHand.Distance(ps.Position)
		maxDist := leftDist
		if rightDist > maxDist {
			maxDist = rightDist
		}

		if maxDist > d.handToHeadThreshold {
			d.consecutiveFrames[pid]++
		} else {
			d.consecutiveFrames[pid] = 0
			d.lastEmitAt[pid] = 0
			continue
		}

		consecutive := d.consecutiveFrames[pid]
		if consecutive < d.minSustainedFrames {
			continue
		}
		// Emit at the minimum and then once per min_sustained_frames while sustained.
		if last := d.lastEmitAt[pid]; last > 0 && consecutive-last < d.minSustainedFrames {
			continue
		}
		d.lastEmitAt[pid] = consecutive

		severity := model.SigmoidConfidence(maxDist, d.handToHeadThreshold*1.5, d.sigmoidSteepness)
		confidence := model.SigmoidConfidence(float64(consecutive), float64(d.minSustainedFrames), 0.1)
		confidence = model.Clamp01(confidence * 0.8)

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.PatternEvidence{
				DetectorSpecific: "playspace_abuse",
				Metrics: map[string]float64{
					"max_hand_to_head_dist": maxDist,
					"left_dist":             leftDist,
					"right_dist":            rightDist,
					"consecutive_frames":    float64(consecutive),
					"sustained_seconds":     float64(consecutive) * ps.FrameDt,
					"threshold":             d.handToHeadThreshold,
					"min_sustained":         float64(d.minSustainedFrames),
				},
			},
			fmt.Sprintf("playspace: hand-to-head %.2f m for %d frames", maxDist, consecutive),
			fmt.Sprintf("hand_to_head: <%.1f m or <%d consecutive frames", d.handToHeadThreshold, d.minSustainedFrames),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  frameIdx - consecutive + 1,
				FrameEnd:    frameIdx,
				AnomalyType: "playspace_abuse",
			},
		)
		events = append(events, ev)
	}

	return events
}
