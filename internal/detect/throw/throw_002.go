package throw

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

type Throw002 struct {
	detect.BaseDetector
	maxReleaseAccel float64
	maxAccelRatio   float64
	releaseWindow   int
}

func NewThrow002(params map[string]any) *Throw002 {
	return &Throw002{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_002", DetectorVersion: "1.0.0",
			DetectorName: "Impossible Disc Acceleration", DetectorCategory: "throw",
			Inputs: []string{"throw_event"}, Warmup: 5, Weight: 0.7,
		},
		maxReleaseAccel: detect.GetFloat(params, "max_release_acceleration", 500.0),
		maxAccelRatio:   detect.GetFloat(params, "max_accel_ratio", 5.0),
		releaseWindow:   detect.GetInt(params, "release_window_frames", 3),
	}
}

func (d *Throw002) Reset()                          {}
func (d *Throw002) Configure(params map[string]any) error {
	d.maxReleaseAccel = detect.GetFloat(params, "max_release_acceleration", d.maxReleaseAccel)
	d.maxAccelRatio = detect.GetFloat(params, "max_accel_ratio", d.maxAccelRatio)
	return nil
}

func (d *Throw002) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	for _, ps := range players {
		if ps.LastThrow == nil || ps.LastThrow.FrameIndex != frameIdx {
			continue
		}
		t := ps.LastThrow
		if len(t.PreReleaseFrames) < 3 {
			continue
		}
		preAccels := make([]float64, 0, len(t.PreReleaseFrames)-1)
		for i := 1; i < len(t.PreReleaseFrames); i++ {
			prev := t.PreReleaseFrames[i-1]
			curr := t.PreReleaseFrames[i]
			dt := curr.Timestamp - prev.Timestamp
			if dt < 0.005 {
				continue
			}
			prevSpd := prev.DiscVelocity.Magnitude()
			currSpd := curr.DiscVelocity.Magnitude()
			preAccels = append(preAccels, math.Abs(currSpd-prevSpd)/dt)
		}
		if len(preAccels) == 0 {
			continue
		}
		avgPreAccel := model.Mean(preAccels)
		lastPre := t.PreReleaseFrames[len(t.PreReleaseFrames)-1]
		relDt := t.Timestamp - lastPre.Timestamp
		if relDt < 0.005 {
			continue
		}
		lastPreSpd := lastPre.DiscVelocity.Magnitude()
		releaseAccel := math.Abs(t.ReleaseSpeed-lastPreSpd) / relDt
		accelRatio := releaseAccel / math.Max(avgPreAccel, 1.0)

		if releaseAccel <= d.maxReleaseAccel && accelRatio <= d.maxAccelRatio {
			continue
		}
		severity := 0.0
		if releaseAccel > d.maxReleaseAccel {
			severity = model.SigmoidConfidence(releaseAccel, d.maxReleaseAccel, 0.01)
		}
		if accelRatio > d.maxAccelRatio {
			severity = math.Max(severity, model.SigmoidConfidence(accelRatio, d.maxAccelRatio, 0.5))
		}
		confidence := severity
		if t.Attribution.Confidence > 0 {
			confidence *= t.Attribution.Confidence
		}
		if relDt < 0.02 {
			confidence *= 0.5
		}

		ev := d.MakeEvent(matchCtx, ps.PlayerID, frameIdx, t.Timestamp, severity, confidence,
			model.DiscAccelerationEvidence{
				ReleaseAcceleration: releaseAccel, AveragePreAcceleration: avgPreAccel,
				AccelerationRatio: accelRatio, ReleaseSpeed: t.ReleaseSpeed,
				PreReleaseSpeed: lastPreSpd, PreReleaseFrames: t.PreReleaseFrames,
			},
			fmt.Sprintf("release_accel: %.0f m/s^2 (ratio: %.1fx)", releaseAccel, accelRatio),
			fmt.Sprintf("release_accel: < %.0f m/s^2 (ratio < %.1f)", d.maxReleaseAccel, d.maxAccelRatio),
			model.CausalKey{PlayerID: ps.PlayerID, FrameStart: t.PreReleaseFrames[0].FrameIndex, FrameEnd: frameIdx + 2, AnomalyType: "disc_acceleration"},
		)
		ev.Attribution = &t.Attribution
		events = append(events, ev)
	}
	return events
}
