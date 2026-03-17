package throw

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

type Throw003 struct {
	detect.BaseDetector
	maxAngleDev    float64
	minHandSpeed   float64
	minThrowSpeed  float64
	minAngleStdDev float64
	consistMinThrows int
	angleHistory   map[string][]float64
}

func NewThrow003(params map[string]any) *Throw003 {
	return &Throw003{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_003", DetectorVersion: "1.0.0",
			DetectorName: "Unnatural Release Angle", DetectorCategory: "throw",
			Inputs: []string{"throw_event"}, Warmup: 5, Weight: 0.5,
		},
		maxAngleDev:      detect.GetFloat(params, "max_release_angle_deviation", 177.0),
		minHandSpeed:     detect.GetFloat(params, "min_hand_speed", 3.0),
		minThrowSpeed:    detect.GetFloat(params, "min_throw_speed", 5.0),
		minAngleStdDev:   detect.GetFloat(params, "min_angle_stddev", 1.0),
		consistMinThrows: detect.GetInt(params, "consistency_min_throws", 5),
		angleHistory:     make(map[string][]float64),
	}
}

func (d *Throw003) Reset() { d.angleHistory = make(map[string][]float64) }
func (d *Throw003) Configure(params map[string]any) error {
	d.maxAngleDev = detect.GetFloat(params, "max_release_angle_deviation", d.maxAngleDev)
	d.minHandSpeed = detect.GetFloat(params, "min_hand_speed", d.minHandSpeed)
	d.minThrowSpeed = detect.GetFloat(params, "min_throw_speed", d.minThrowSpeed)
	return nil
}

func (d *Throw003) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	for _, ps := range players {
		if ps.LastThrow == nil || ps.LastThrow.FrameIndex != frameIdx {
			continue
		}
		t := ps.LastThrow
		if t.HandSpeed < d.minHandSpeed || t.ReleaseSpeed < d.minThrowSpeed {
			continue
		}
		d.angleHistory[ps.PlayerID] = append(d.angleHistory[ps.PlayerID], t.ReleaseAngle)
		if len(d.angleHistory[ps.PlayerID]) > 50 {
			d.angleHistory[ps.PlayerID] = d.angleHistory[ps.PlayerID][len(d.angleHistory[ps.PlayerID])-50:]
		}
		if math.IsNaN(t.ReleaseAngle) || t.ReleaseAngle <= d.maxAngleDev {
			continue
		}
		severity := model.SigmoidConfidence(t.ReleaseAngle, d.maxAngleDev, 0.1)
		confidence := severity * math.Min(1.0, t.HandSpeed/3.0)
		if t.Attribution.Confidence > 0 {
			confidence *= t.Attribution.Confidence
		}
		ev := d.MakeEvent(matchCtx, ps.PlayerID, frameIdx, t.Timestamp, severity, confidence,
			model.ReleaseAngleEvidence{
				ReleaseAngle: t.ReleaseAngle, HandVelocity: t.HandVelocity,
				DiscVelocity: t.ReleaseVelocity, HandSpeed: t.HandSpeed,
				DiscSpeed: t.ReleaseSpeed, WristOrientation: t.WristOrientation,
				ThrowingHand: t.ThrowingHand,
			},
			fmt.Sprintf("release_angle: %.1f deg", t.ReleaseAngle),
			fmt.Sprintf("release_angle: < %.1f deg", d.maxAngleDev),
			model.CausalKey{PlayerID: ps.PlayerID, FrameStart: frameIdx - 3, FrameEnd: frameIdx, AnomalyType: "release_angle"},
		)
		ev.Attribution = &t.Attribution
		events = append(events, ev)
	}
	return events
}
