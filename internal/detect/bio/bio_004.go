package bio

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Bio004 detects zero aim wobble, indicating non-human aiming (BIO_004).
type Bio004 struct {
	detect.BaseDetector
	wobbleWindowFrames int
	maxWobbleVariance  float64
	minActiveFrames    int
	sigmoidSteepness   float64

	leftHandRotHistory  map[string][]model.Quat
	rightHandRotHistory map[string][]model.Quat
	activeFrames        map[string]int
}

// NewBio004 creates a new BIO_004 Zero Aim Wobble detector.
func NewBio004(params map[string]any) *Bio004 {
	d := &Bio004{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "BIO_004",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Zero Aim Wobble",
			DetectorCategory: "bio",
			Inputs:           []string{"hand_tracking", "hand_rotation"},
			Warmup:           30,
			Weight:           0.7,
			IsAutoEnforce:    false,
		},
		wobbleWindowFrames:  detect.GetInt(params, "wobble_window_frames", 30),
		maxWobbleVariance:   detect.GetFloat(params, "max_wobble_variance", 0.0001),
		minActiveFrames:     detect.GetInt(params, "min_active_frames", 20),
		sigmoidSteepness:    detect.GetFloat(params, "sigmoid_steepness", 50000.0),
		leftHandRotHistory:  make(map[string][]model.Quat),
		rightHandRotHistory: make(map[string][]model.Quat),
		activeFrames:        make(map[string]int),
	}
	return d
}

func (d *Bio004) Reset() {
	d.leftHandRotHistory = make(map[string][]model.Quat)
	d.rightHandRotHistory = make(map[string][]model.Quat)
	d.activeFrames = make(map[string]int)
}

func (d *Bio004) Configure(params map[string]any) error {
	d.wobbleWindowFrames = detect.GetInt(params, "wobble_window_frames", d.wobbleWindowFrames)
	d.maxWobbleVariance = detect.GetFloat(params, "max_wobble_variance", d.maxWobbleVariance)
	d.minActiveFrames = detect.GetInt(params, "min_active_frames", d.minActiveFrames)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Bio004) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID

		leftHist := d.leftHandRotHistory[pid]
		model.PushQuatHistory(&leftHist, ps.LeftHandRot, d.wobbleWindowFrames)
		d.leftHandRotHistory[pid] = leftHist

		rightHist := d.rightHandRotHistory[pid]
		model.PushQuatHistory(&rightHist, ps.RightHandRot, d.wobbleWindowFrames)
		d.rightHandRotHistory[pid] = rightHist

		if !ps.IsStunned && ps.Speed > 0.5 {
			d.activeFrames[pid]++
		}

		if d.activeFrames[pid] < d.minActiveFrames {
			continue
		}

		hands := []struct {
			name    string
			history []model.Quat
		}{
			{"left", d.leftHandRotHistory[pid]},
			{"right", d.rightHandRotHistory[pid]},
		}

		for _, h := range hands {
			if len(h.history) < d.wobbleWindowFrames {
				continue
			}

			variance := model.ComputeRotationVariance(h.history)

			if variance < d.maxWobbleVariance {
				severity := model.SigmoidConfidence(d.maxWobbleVariance-variance, 0, d.sigmoidSteepness)
				confidence := model.Clamp01(severity * 0.85)

				stddevDeg := model.RadToDeg(math.Sqrt(variance))

				ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
					severity, confidence,
					model.ZeroWobbleEvidence{
						Hand:             h.name,
						RotationVariance: variance,
						WindowFrames:     d.wobbleWindowFrames,
						Threshold:        d.maxWobbleVariance,
						PlayerSpeed:      ps.Speed,
						StdDevDegrees:    stddevDeg,
					},
					fmt.Sprintf("aim_wobble_variance: %.8f (%s)", variance, h.name),
					fmt.Sprintf("aim_wobble_variance: >%.6f", d.maxWobbleVariance),
					model.CausalKey{
						PlayerID:    pid,
						FrameStart:  frameIdx - d.wobbleWindowFrames,
						FrameEnd:    frameIdx,
						AnomalyType: "zero_wobble",
					},
				)
				events = append(events, ev)

				// Reset after firing
				if h.name == "left" {
					d.leftHandRotHistory[pid] = nil
				} else {
					d.rightHandRotHistory[pid] = nil
				}
				d.activeFrames[pid] = 0
			}
		}
	}

	return events
}
