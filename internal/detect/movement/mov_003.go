package movement

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Mov003 detects zero-inertia direction changes (MOV_003).
//
// STATUS: UNSAFE — wall bounces and player-to-player collisions produce
// legitimate 180° reversals. Disabled by default until calibrated.
//
// Detection model: a genuine zero-inertia hack is ONE reversal frame (the
// velocity flips by >= min_angle_deg at >= min_speed) followed by the
// player continuing on the new heading. The reversal is therefore only
// confirmed when the next confirm_frames frames stay within
// heading_tolerance_deg of the reversed heading at >= min_speed. A
// (v, -v, v, -v) flip-flop — position jitter / interpolation — never
// confirms because the frame after the reversal flips back. Reversals
// that happen within collision_radius of another player are skipped as
// probable collisions.
type Mov003 struct {
	detect.BaseDetector
	minAngleDeg         float64
	minSpeed            float64
	stunCooldown        int
	sigmoidSteepness    float64
	confirmFrames       int
	headingToleranceDeg float64
	collisionRadius     float64

	prevVelocity  map[string]model.Vec3
	prevFrame     map[string]int // frame at which prevVelocity was observed
	lastStunFrame map[string]int
	pending       map[string]*pendingReversal
}

type pendingReversal struct {
	frame     int
	lastFrame int // last frame that extended the confirmation run
	angleDeg  float64
	prevSpeed float64
	revSpeed  float64
	heading   model.Vec3 // unit vector of the reversed velocity
	confirmed int
	speeds    []float64
}

// NewMov003 creates a new MOV_003 Zero-Inertia Direction Change detector.
func NewMov003(params map[string]any) *Mov003 {
	d := &Mov003{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "MOV_003",
			DetectorVersion:  "2.1.0",
			DetectorName:     "Zero-Inertia Direction Change",
			DetectorCategory: "movement",
			Inputs:           []string{"velocity", "speed"},
			Warmup:           5,
			Weight:           0.8,
			IsAutoEnforce:    false,
		},
		minAngleDeg:         detect.GetFloatAlias(params, 175.0, "min_angle_deg", "min_reversal_angle"),
		minSpeed:            detect.GetFloat(params, "min_speed", 12.0),
		stunCooldown:        detect.GetInt(params, "stun_cooldown_frames", 30),
		sigmoidSteepness:    detect.GetFloat(params, "sigmoid_steepness", 0.5),
		confirmFrames:       detect.GetInt(params, "confirm_frames", 3),
		headingToleranceDeg: detect.GetFloat(params, "heading_tolerance_deg", 20.0),
		collisionRadius:     detect.GetFloat(params, "collision_radius", 2.0),
	}
	d.Reset()
	d.sanitize()
	return d
}

func (d *Mov003) sanitize() {
	if d.confirmFrames < 1 {
		d.confirmFrames = 1
	}
}

func (d *Mov003) Reset() {
	d.prevVelocity = make(map[string]model.Vec3)
	d.prevFrame = make(map[string]int)
	d.lastStunFrame = make(map[string]int)
	d.pending = make(map[string]*pendingReversal)
}

func (d *Mov003) Configure(params map[string]any) error {
	d.minAngleDeg = detect.GetFloatAlias(params, d.minAngleDeg, "min_angle_deg", "min_reversal_angle")
	d.minSpeed = detect.GetFloat(params, "min_speed", d.minSpeed)
	d.stunCooldown = detect.GetInt(params, "stun_cooldown_frames", d.stunCooldown)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	d.confirmFrames = detect.GetInt(params, "confirm_frames", d.confirmFrames)
	d.headingToleranceDeg = detect.GetFloat(params, "heading_tolerance_deg", d.headingToleranceDeg)
	d.collisionRadius = detect.GetFloat(params, "collision_radius", d.collisionRadius)
	d.sanitize()
	return nil
}

// nearAnotherPlayer reports whether any other player is within
// collision_radius of ps (a probable body collision, not a hack).
func (d *Mov003) nearAnotherPlayer(ps *model.PlayerState, all []*model.PlayerState) bool {
	if d.collisionRadius <= 0 {
		return false
	}
	for _, other := range all {
		if other.PlayerID == ps.PlayerID {
			continue
		}
		if other.Position.IsZero() {
			continue
		}
		if other.Position.Distance(ps.Position) <= d.collisionRadius {
			return true
		}
	}
	return false
}

func (d *Mov003) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	all := detect.SortedPlayers(players)

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID

		// Track stun history
		if ps.IsStunned {
			d.lastStunFrame[pid] = frameIdx
		}

		prevVel, hasPrev := d.prevVelocity[pid]
		prevF := d.prevFrame[pid]
		d.prevVelocity[pid] = ps.Velocity
		d.prevFrame[pid] = frameIdx

		if !hasPrev {
			continue
		}
		// The reversal test compares CONSECUTIVE frames. After a gap
		// (validator rejection, missed poll, reconnect) the previous
		// velocity is arbitrarily old and a normal turn-around executed in
		// between would read as an instantaneous flip; just refresh and
		// wait for the next contiguous pair, mirroring the confirmation
		// run's own contiguity check.
		if frameIdx != prevF+1 {
			delete(d.pending, pid)
			continue
		}

		// Skip if recently stunned
		if lastStun, ok := d.lastStunFrame[pid]; ok {
			if frameIdx-lastStun < d.stunCooldown {
				delete(d.pending, pid)
				continue
			}
		}

		prevSpeed := prevVel.Magnitude()
		currSpeed := ps.Velocity.Magnitude()

		// Confirmation phase: the player must keep the reversed heading.
		if p, ok := d.pending[pid]; ok {
			contiguous := frameIdx == p.lastFrame+1
			if contiguous && currSpeed >= d.minSpeed && p.heading.AngleBetweenDeg(ps.Velocity) <= d.headingToleranceDeg {
				p.confirmed++
				p.lastFrame = frameIdx
				p.speeds = append(p.speeds, currSpeed)
				if p.confirmed >= d.confirmFrames {
					events = append(events, d.makeEvent(matchCtx, ps, p, frameIdx))
					delete(d.pending, pid)
				}
				continue
			}
			// Heading not held (flip-flop, bounce-back, slowdown or a frame
			// gap that hides what happened in between): discard.
			delete(d.pending, pid)
		}

		// Both speeds must exceed minimum
		if prevSpeed < d.minSpeed || currSpeed < d.minSpeed {
			continue
		}

		// Compute angle between consecutive velocity vectors
		angleDeg := prevVel.AngleBetweenDeg(ps.Velocity)
		if angleDeg < d.minAngleDeg {
			continue
		}

		// Reversal next to another player is a probable collision.
		if d.nearAnotherPlayer(ps, all) {
			continue
		}

		d.pending[pid] = &pendingReversal{
			frame:     frameIdx,
			lastFrame: frameIdx,
			angleDeg:  angleDeg,
			prevSpeed: prevSpeed,
			revSpeed:  currSpeed,
			heading:   ps.Velocity.Normalized(),
		}
	}

	return events
}

func (d *Mov003) makeEvent(matchCtx *model.MatchContext, ps *model.PlayerState, p *pendingReversal, frameIdx int) model.DetectionEvent {
	avgSpeed := (p.prevSpeed + p.revSpeed) / 2.0
	severity := model.SigmoidConfidence(p.angleDeg, 178.0, 1.0)
	confidence := model.SigmoidConfidence(avgSpeed, d.minSpeed*1.5, d.sigmoidSteepness)
	confidence = model.Clamp01(confidence * 0.85)

	metrics := map[string]float64{
		"angle_deg":        p.angleDeg,
		"prev_speed":       p.prevSpeed,
		"curr_speed":       p.revSpeed,
		"avg_speed":        avgSpeed,
		"sustained_speed":  model.Mean(p.speeds),
		"confirmed_frames": float64(p.confirmed),
		"min_angle":        d.minAngleDeg,
		"min_speed":        d.minSpeed,
	}

	return d.MakeEvent(matchCtx, ps.PlayerID, frameIdx, ps.LastTimestamp,
		severity, confidence,
		model.MovementEvidence{
			DetectorSpecific: "zero_inertia_direction_change",
			Metrics:          metrics,
		},
		fmt.Sprintf("direction_change: %.1f° at %.1f m/s, heading held %d frames", p.angleDeg, avgSpeed, p.confirmed),
		fmt.Sprintf("direction_change: <%.0f° at >%.0f m/s", d.minAngleDeg, d.minSpeed),
		model.CausalKey{
			PlayerID:    ps.PlayerID,
			FrameStart:  p.frame - 1,
			FrameEnd:    frameIdx,
			AnomalyType: "zero_inertia",
		},
	)
}
