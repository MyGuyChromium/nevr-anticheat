package bio

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Bio003 detects zero hand jitter, indicating non-human input (BIO_003).
type Bio003 struct {
	detect.BaseDetector
	jitterWindowFrames int
	maxJitterVariance  float64
	minActiveFrames    int
	sigmoidSteepness   float64

	// Per-player hand position history relative to body
	leftHandRelHistory  map[string][]model.Vec3
	rightHandRelHistory map[string][]model.Vec3
	activeFrames        map[string]int
}

// NewBio003 creates a new BIO_003 Zero Hand Jitter detector.
func NewBio003(params map[string]any) *Bio003 {
	d := &Bio003{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "BIO_003",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Zero Hand Jitter",
			DetectorCategory: "bio",
			Inputs:           []string{"hand_tracking", "position"},
			Warmup:           30,
			Weight:           0.7,
			IsAutoEnforce:    false,
		},
		jitterWindowFrames:  detect.GetInt(params, "jitter_window_frames", 30),
		maxJitterVariance:   detect.GetFloat(params, "max_jitter_variance", 0.0001),
		minActiveFrames:     detect.GetInt(params, "min_active_frames", 20),
		sigmoidSteepness:    detect.GetFloat(params, "sigmoid_steepness", 50000.0),
		leftHandRelHistory:  make(map[string][]model.Vec3),
		rightHandRelHistory: make(map[string][]model.Vec3),
		activeFrames:        make(map[string]int),
	}
	return d
}

func (d *Bio003) Reset() {
	d.leftHandRelHistory = make(map[string][]model.Vec3)
	d.rightHandRelHistory = make(map[string][]model.Vec3)
	d.activeFrames = make(map[string]int)
}

func (d *Bio003) Configure(params map[string]any) error {
	d.jitterWindowFrames = detect.GetInt(params, "jitter_window_frames", d.jitterWindowFrames)
	d.maxJitterVariance = detect.GetFloat(params, "max_jitter_variance", d.maxJitterVariance)
	d.minActiveFrames = detect.GetInt(params, "min_active_frames", d.minActiveFrames)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Bio003) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID

		// Compute hand positions relative to body (player position)
		leftRel := ps.LeftHand.Sub(ps.Position)
		rightRel := ps.RightHand.Sub(ps.Position)

		leftHist := d.leftHandRelHistory[pid]
		model.PushVec3History(&leftHist, leftRel, d.jitterWindowFrames)
		d.leftHandRelHistory[pid] = leftHist

		rightHist := d.rightHandRelHistory[pid]
		model.PushVec3History(&rightHist, rightRel, d.jitterWindowFrames)
		d.rightHandRelHistory[pid] = rightHist

		if !ps.IsStunned && ps.Speed > 0.5 {
			d.activeFrames[pid]++
		}

		if d.activeFrames[pid] < d.minActiveFrames {
			continue
		}

		hands := []struct {
			name    string
			history []model.Vec3
		}{
			{"left", d.leftHandRelHistory[pid]},
			{"right", d.rightHandRelHistory[pid]},
		}

		for _, h := range hands {
			if len(h.history) < d.jitterWindowFrames {
				continue
			}

			variance := model.ComputePositionVariance(h.history)

			if variance < d.maxJitterVariance {
				// Lower variance = more suspicious, so invert for sigmoid
				severity := model.SigmoidConfidence(d.maxJitterVariance-variance, 0, d.sigmoidSteepness)
				confidence := model.Clamp01(severity * 0.85)

				ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
					severity, confidence,
					model.ZeroJitterEvidence{
						Hand:             h.name,
						PositionVariance: variance,
						WindowFrames:     d.jitterWindowFrames,
						ActiveFrames:     d.activeFrames[pid],
						Threshold:        d.maxJitterVariance,
						PlayerSpeed:      ps.Speed,
					},
					fmt.Sprintf("hand_jitter_variance: %.8f (%s)", variance, h.name),
					fmt.Sprintf("hand_jitter_variance: >%.6f", d.maxJitterVariance),
					model.CausalKey{
						PlayerID:    pid,
						FrameStart:  frameIdx - d.jitterWindowFrames,
						FrameEnd:    frameIdx,
						AnomalyType: "zero_jitter",
					},
				)
				events = append(events, ev)

				// Reset history after firing
				if h.name == "left" {
					d.leftHandRelHistory[pid] = nil
				} else {
					d.rightHandRelHistory[pid] = nil
				}
				d.activeFrames[pid] = 0
			}
		}
	}

	return events
}
