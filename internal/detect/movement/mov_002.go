package movement

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Mov002 detects teleportation - instant position changes (MOV_002).
type Mov002 struct {
	detect.BaseDetector
	teleportThreshold      float64
	velocityMismatchFactor float64
	maxFrameGap            int
	sigmoidSteepness       float64

	prevPosition map[string]model.Vec3
	prevVelocity map[string]model.Vec3
	prevFrame    map[string]int
}

// NewMov002 creates a new MOV_002 Teleportation detector.
func NewMov002(params map[string]any) *Mov002 {
	d := &Mov002{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "MOV_002",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Teleportation",
			DetectorCategory: "movement",
			Inputs:           []string{"position", "velocity"},
			Warmup:           2,
			Weight:           1.0,
			IsAutoEnforce:    true,
		},
		teleportThreshold:      detect.GetFloat(params, "teleport_threshold", 5.0),
		velocityMismatchFactor: detect.GetFloat(params, "velocity_mismatch_factor", 3.0),
		maxFrameGap:            detect.GetInt(params, "max_frame_gap", 5),
		sigmoidSteepness:       detect.GetFloat(params, "sigmoid_steepness", 0.5),
		prevPosition:           make(map[string]model.Vec3),
		prevVelocity:           make(map[string]model.Vec3),
		prevFrame:              make(map[string]int),
	}
	return d
}

func (d *Mov002) Reset() {
	d.prevPosition = make(map[string]model.Vec3)
	d.prevVelocity = make(map[string]model.Vec3)
	d.prevFrame = make(map[string]int)
}

func (d *Mov002) Configure(params map[string]any) error {
	d.teleportThreshold = detect.GetFloat(params, "teleport_threshold", d.teleportThreshold)
	d.velocityMismatchFactor = detect.GetFloat(params, "velocity_mismatch_factor", d.velocityMismatchFactor)
	d.maxFrameGap = detect.GetInt(params, "max_frame_gap", d.maxFrameGap)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Mov002) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID

		prevPos, hasPrev := d.prevPosition[pid]
		prevVel := d.prevVelocity[pid]
		prevF := d.prevFrame[pid]

		d.prevPosition[pid] = ps.Position
		d.prevVelocity[pid] = ps.Velocity
		d.prevFrame[pid] = frameIdx

		if !hasPrev {
			continue
		}

		// Skip large frame gaps (respawns, reconnects)
		frameGap := frameIdx - prevF
		if frameGap > d.maxFrameGap {
			continue
		}

		delta := ps.Position.Sub(prevPos)
		actualDist := delta.Magnitude()

		// Expected delta based on previous velocity and frame dt
		dt := ps.FrameDt
		if dt <= 0 {
			dt = 1.0 / 60.0
		}
		expectedDelta := prevVel.Scale(dt * float64(frameGap))
		expectedDist := expectedDelta.Magnitude()

		if actualDist <= d.teleportThreshold {
			continue
		}
		if actualDist <= expectedDist*d.velocityMismatchFactor {
			continue
		}

		severity := model.SigmoidConfidence(actualDist, d.teleportThreshold*2, d.sigmoidSteepness)
		confidence := model.SigmoidConfidence(actualDist/model.MaxF(expectedDist, 0.01), d.velocityMismatchFactor, 0.5)

		if ps.IsHighPing {
			confidence *= 0.5
		}

		metrics := map[string]float64{
			"actual_distance":   actualDist,
			"expected_distance": expectedDist,
			"frame_gap":         float64(frameGap),
			"frame_dt":          dt,
			"mismatch_ratio":    actualDist / model.MaxF(expectedDist, 0.01),
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.MovementEvidence{
				DetectorSpecific: "teleportation",
				Metrics:          metrics,
			},
			fmt.Sprintf("teleport_distance: %.2f m (expected %.2f m)", actualDist, expectedDist),
			fmt.Sprintf("position_delta: <%.1f m or <%.1fx expected", d.teleportThreshold, d.velocityMismatchFactor),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  prevF,
				FrameEnd:    frameIdx,
				AnomalyType: "teleportation",
			},
		)
		events = append(events, ev)
	}

	return events
}
