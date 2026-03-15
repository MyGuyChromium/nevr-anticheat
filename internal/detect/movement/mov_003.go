package movement

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Mov003 detects zero-inertia direction changes (MOV_003).
type Mov003 struct {
	detect.BaseDetector
	minAngleDeg      float64
	minSpeed         float64
	stunCooldown     int
	sigmoidSteepness float64

	prevVelocity    map[string]model.Vec3
	lastStunFrame   map[string]int
}

// NewMov003 creates a new MOV_003 Zero-Inertia Direction Change detector.
func NewMov003(params map[string]any) *Mov003 {
	d := &Mov003{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "MOV_003",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Zero-Inertia Direction Change",
			DetectorCategory: "movement",
			Inputs:           []string{"velocity", "speed"},
			Warmup:           5,
			Weight:           0.8,
			IsAutoEnforce:    false,
		},
		minAngleDeg:      detect.GetFloat(params, "min_angle_deg", 175.0),
		minSpeed:          detect.GetFloat(params, "min_speed", 12.0),
		stunCooldown:      detect.GetInt(params, "stun_cooldown_frames", 30),
		sigmoidSteepness:  detect.GetFloat(params, "sigmoid_steepness", 0.5),
		prevVelocity:      make(map[string]model.Vec3),
		lastStunFrame:     make(map[string]int),
	}
	return d
}

func (d *Mov003) Reset() {
	d.prevVelocity = make(map[string]model.Vec3)
	d.lastStunFrame = make(map[string]int)
}

func (d *Mov003) Configure(params map[string]any) error {
	d.minAngleDeg = detect.GetFloat(params, "min_angle_deg", d.minAngleDeg)
	d.minSpeed = detect.GetFloat(params, "min_speed", d.minSpeed)
	d.stunCooldown = detect.GetInt(params, "stun_cooldown_frames", d.stunCooldown)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Mov003) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID

		// Track stun history
		if ps.IsStunned {
			d.lastStunFrame[pid] = frameIdx
		}

		prevVel, hasPrev := d.prevVelocity[pid]
		d.prevVelocity[pid] = ps.Velocity

		if !hasPrev {
			continue
		}

		// Skip if recently stunned
		if lastStun, ok := d.lastStunFrame[pid]; ok {
			if frameIdx-lastStun < d.stunCooldown {
				continue
			}
		}

		prevSpeed := prevVel.Magnitude()
		currSpeed := ps.Velocity.Magnitude()

		// Both speeds must exceed minimum
		if prevSpeed < d.minSpeed || currSpeed < d.minSpeed {
			continue
		}

		// Compute angle between consecutive velocity vectors
		angleDeg := prevVel.AngleBetweenDeg(ps.Velocity)

		if angleDeg < d.minAngleDeg {
			continue
		}

		avgSpeed := (prevSpeed + currSpeed) / 2.0
		severity := model.SigmoidConfidence(angleDeg, 178.0, 1.0)
		confidence := model.SigmoidConfidence(avgSpeed, d.minSpeed*1.5, d.sigmoidSteepness)
		confidence = model.Clamp01(confidence * 0.85)

		metrics := map[string]float64{
			"angle_deg":   angleDeg,
			"prev_speed":  prevSpeed,
			"curr_speed":  currSpeed,
			"avg_speed":   avgSpeed,
			"min_angle":   d.minAngleDeg,
			"min_speed":   d.minSpeed,
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.MovementEvidence{
				DetectorSpecific: "zero_inertia_direction_change",
				Metrics:          metrics,
			},
			fmt.Sprintf("direction_change: %.1f° at %.1f m/s", angleDeg, avgSpeed),
			fmt.Sprintf("direction_change: <%.0f° at >%.0f m/s", d.minAngleDeg, d.minSpeed),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  frameIdx - 1,
				FrameEnd:    frameIdx,
				AnomalyType: "zero_inertia",
			},
		)
		events = append(events, ev)
	}

	return events
}
