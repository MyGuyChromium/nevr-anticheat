package pattern

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Pat005 detects playspace abuse - hands too far from head for sustained frames (PAT_005).
type Pat005 struct {
	detect.BaseDetector
	handToHeadThreshold float64
	minSustainedFrames  int
	sigmoidSteepness    float64

	consecutiveFrames map[string]int
}

// NewPat005 creates a new PAT_005 Playspace Abuse detector.
func NewPat005(params map[string]any) *Pat005 {
	d := &Pat005{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "PAT_005",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Playspace Abuse",
			DetectorCategory: "pattern",
			Inputs:           []string{"hand_tracking", "head_position"},
			Warmup:           10,
			Weight:           0.6,
			IsAutoEnforce:    false,
		},
		handToHeadThreshold: detect.GetFloat(params, "hand_to_head_threshold", 2.0),
		minSustainedFrames:  detect.GetInt(params, "min_sustained_frames", 30),
		sigmoidSteepness:    detect.GetFloat(params, "sigmoid_steepness", 0.1),
		consecutiveFrames:   make(map[string]int),
	}
	return d
}

func (d *Pat005) Reset() {
	d.consecutiveFrames = make(map[string]int)
}

func (d *Pat005) Configure(params map[string]any) error {
	d.handToHeadThreshold = detect.GetFloat(params, "hand_to_head_threshold", d.handToHeadThreshold)
	d.minSustainedFrames = detect.GetInt(params, "min_sustained_frames", d.minSustainedFrames)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Pat005) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID

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
			continue
		}

		consecutive := d.consecutiveFrames[pid]
		if consecutive < d.minSustainedFrames {
			continue
		}

		severity := model.SigmoidConfidence(float64(consecutive), float64(d.minSustainedFrames)*2, d.sigmoidSteepness)
		confidence := model.SigmoidConfidence(maxDist, d.handToHeadThreshold*1.5, 1.0)
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
					"threshold":             d.handToHeadThreshold,
					"min_sustained":         float64(d.minSustainedFrames),
				},
			},
			fmt.Sprintf("playspace: hand-to-head %.2f m for %d frames", maxDist, consecutive),
			fmt.Sprintf("hand_to_head: <%.1f m or <%d consecutive frames", d.handToHeadThreshold, d.minSustainedFrames),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  frameIdx - consecutive,
				FrameEnd:    frameIdx,
				AnomalyType: "playspace_abuse",
			},
		)
		events = append(events, ev)

		// Reset after firing
		d.consecutiveFrames[pid] = 0
	}

	return events
}
