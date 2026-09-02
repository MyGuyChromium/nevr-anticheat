package throw

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Throw003 detects an unnatural release angle (THROW_003): the disc leaving
// in a direction nearly opposite to the throwing hand's motion.
//
// ReleaseAngle is measured in the world frame (hand velocity vs. game-reported
// disc velocity). Body translation contributes to both vectors, and whether
// the disc inherits the carrier's body velocity is not verified on Echo data,
// so a player translating fast with a body-stationary hand can legitimately
// read a large angle. The detector therefore requires the hand to move faster
// than the body and scales confidence by how much of the hand motion cannot
// be explained by body motion. The 177 degree threshold itself is UNVERIFIED.
type Throw003 struct {
	detect.BaseDetector
	maxAngleDev   float64
	minHandSpeed  float64
	minThrowSpeed float64
}

func NewThrow003(params map[string]any) *Throw003 {
	return &Throw003{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_003", DetectorVersion: "1.1.0",
			DetectorName: "Unnatural Release Angle", DetectorCategory: "throw",
			Inputs: []string{"throw_event"}, Warmup: 5, Weight: 0.5,
		},
		maxAngleDev:   detect.GetFloat(params, "max_release_angle_deviation", 177.0),
		minHandSpeed:  detect.GetFloat(params, "min_hand_speed", 3.0),
		minThrowSpeed: detect.GetFloat(params, "min_throw_speed", 5.0),
	}
}

func (d *Throw003) Reset() {}
func (d *Throw003) Configure(params map[string]any) error {
	d.maxAngleDev = detect.GetFloat(params, "max_release_angle_deviation", d.maxAngleDev)
	d.minHandSpeed = detect.GetFloat(params, "min_hand_speed", d.minHandSpeed)
	d.minThrowSpeed = detect.GetFloat(params, "min_throw_speed", d.minThrowSpeed)
	return nil
}

func (d *Throw003) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	for _, pid := range sortedPlayerIDs(players) {
		ps := players[pid]
		t := throwAt(ps, frameIdx)
		if t == nil {
			continue
		}
		if t.HandSpeed < d.minHandSpeed || t.ReleaseSpeed < d.minThrowSpeed {
			continue
		}
		if math.IsNaN(t.ReleaseAngle) || t.ReleaseAngle <= d.maxAngleDev {
			continue
		}
		// Frame-of-reference guard: if the body moved as fast as the hand,
		// the world-frame hand velocity may be pure body translation and the
		// angle carries no information about the wrist motion.
		bodySpeed := t.PlayerVelocity.Magnitude()
		if t.HandSpeed <= 0 || bodySpeed >= t.HandSpeed {
			continue
		}
		bodyFactor := 1.0 - bodySpeed/t.HandSpeed

		severity := model.SigmoidConfidence(t.ReleaseAngle, d.maxAngleDev, 0.1)
		confidence := severity * bodyFactor
		if t.Attribution.Confidence > 0 {
			confidence *= t.Attribution.Confidence
		}
		ev := d.MakeEvent(matchCtx, pid, frameIdx, t.Timestamp, severity, confidence,
			model.ReleaseAngleEvidence{
				ReleaseAngle: t.ReleaseAngle, HandVelocity: t.HandVelocity,
				DiscVelocity: t.ReleaseVelocity, HandSpeed: t.HandSpeed,
				DiscSpeed: t.ReleaseSpeed, WristOrientation: t.WristOrientation,
				ThrowingHand: t.ThrowingHand,
			},
			fmt.Sprintf("release_angle: %.1f deg (body %.1f m/s, hand %.1f m/s)", t.ReleaseAngle, bodySpeed, t.HandSpeed),
			fmt.Sprintf("release_angle: < %.1f deg", d.maxAngleDev),
			model.CausalKey{PlayerID: pid, FrameStart: frameIdx - 3, FrameEnd: frameIdx, AnomalyType: "release_angle"},
		)
		ev.Attribution = &t.Attribution
		events = append(events, ev)
	}
	return events
}
