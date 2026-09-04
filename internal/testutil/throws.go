package testutil

import (
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// ThrowSpec describes one throw of a ThrowSequence: a hold, a release and
// a tracked flight, generated with the same disc/hand conventions the
// adapter produces (shared disc state, possession boolean, game-reported
// disc velocity). Every field maps onto a detector input:
//
//	ReleaseSpeed      THROW_001 (18.9 m/s cap), THROW_002 (delta from rest)
//	DeviationDeg      THROW_005 (goal-directed when < 30 deg; precision when mean < 2 deg)
//	HandSpeed         THROW_003 (min 3 m/s), THROW_001 speed ratio, THROW_004/PAT_002 signature
//	HandReverse       THROW_003 (release angle ~180 deg > 177)
//	BendDegPerFrame   THROW_006 (8-15 deg/frame sustained, alignment improvement)
//	SpeedGainPerFrame THROW_008 (> 5 m/s per frame in free flight)
//	GrabDistance      STATE_001 (hand-to-disc on possession gain > 3 m)
//	HoldFrames/Gap    PAT_001 (release interval CoV), possession duration signature
//	ScriptedHand      PAT_002/THROW_004 (no human jitter on the throwing hand)
type ThrowSpec struct {
	HoldFrames        int
	FlightFrames      int
	GapFrames         int
	ReleaseSpeed      float64
	DeviationDeg      float64
	DeviationSeed     float64 // rotation of the deviation axis around the goal direction (radians)
	HandSpeed         float64
	HandReverse       bool
	BendDegPerFrame   float64
	SpeedGainPerFrame float64
	GrabDistance      float64
	ScriptedHand      bool
	WindupBack        float64
	// ReleaseOffset displaces this throw's hand path (body-relative), the
	// centimetre-scale release-point variation of a human thrower.
	ReleaseOffset model.Vec3
	// HandDeviationDeg is the angle between the hand's motion on the
	// release frame and the disc's direction (a human release is never
	// perfectly along the disc's path).
	HandDeviationDeg float64
}

const (
	windupFrames    = 5    // hold frames over which the hand pulls back
	discInHand      = 0.10 // m, disc offset from the hand while held
	discReleaseLead = 0.10 // m, disc offset from the hand on the release frame
	offHandTuck     = 0.35 // m, the off hand drops back during the throw so the throwing hand is unambiguous
	handRecover     = 5    // flight frames over which the hand returns to rest
	minFlightSpeed  = 0.5  // m/s, floor for decelerating discs
)

// frac returns the fractional part of x (a cheap deterministic "random").
func frac(x float64) float64 { return x - math.Floor(x) }

// goalFor returns the goal a static player at pos naturally attacks: the
// far one on the Z axis (both when Z == 0).
func goalFor(pos model.Vec3) model.Vec3 {
	gz := model.DefaultPhysics().GoalZ
	if pos[2] > 0 {
		gz = -gz
	}
	return model.Vec3{0, 0, gz}
}

// deviate rotates dir away from itself by dev degrees around an axis
// perpendicular to dir whose orientation is chosen by seed, so the angle
// between the result and dir is exactly dev.
func deviate(dir model.Vec3, devDeg, seed float64) model.Vec3 {
	if devDeg == 0 {
		return dir
	}
	up := model.Vec3{0, 1, 0}
	p1 := dir.Cross(up)
	if p1.Magnitude() < 1e-6 {
		p1 = dir.Cross(model.Vec3{1, 0, 0})
	}
	p1 = p1.Normalized()
	p2 := dir.Cross(p1).Normalized()
	axis := p1.Scale(math.Cos(seed)).Add(p2.Scale(math.Sin(seed)))
	return rotateVec3(dir, axis, model.DegToRad(devDeg)).Normalized()
}

// bendToward rotates vel toward target by at most maxDeg degrees.
func bendToward(vel, target model.Vec3, maxDeg float64) model.Vec3 {
	ang := model.RadToDeg(vel.AngleBetween(target))
	if math.IsNaN(ang) || ang < 1e-6 || maxDeg <= 0 {
		return vel
	}
	axis := vel.Cross(target)
	if axis.Magnitude() < 1e-9 {
		return vel
	}
	step := math.Min(maxDeg, ang)
	return rotateVec3(vel, axis, model.DegToRad(step))
}

// ThrowSequence generates one frame stream for a static player performing
// the given throws in order. The disc state is the tick-shared state the
// adapter emits (IsHeld/PossessorID while held, game-reported velocity in
// flight); the body never moves so BIO_003/BIO_004 stay inactive and MOV_*
// stay silent, isolating the throw and grab detectors.
func (fb *FrameBuilder) ThrowSequence(specs []ThrowSpec) []model.PlayerTelemetryFrame {
	var frames []model.PlayerTelemetryFrame
	dt := fb.dt()
	body := ClampToArena(fb.startPos)
	goal := goalFor(body)
	facing := rotationFromVelocity(goal.Sub(body))
	restL := body.Add(leftHandOffset)
	restR := body.Add(rightHandOffset)
	fi := 0

	// tuck is the off-hand pull-back during a throw (set per throw).
	tuck := model.Vec3{}
	emit := func(right model.Vec3, scripted bool, disc model.DiscState, has bool) {
		f := fb.baseFrame(fi, body, facing)
		f.HasPossession = has
		f.RightHandPosition = right
		if !scripted {
			f.RightHandPosition = right.Add(deterministicJitter3(fi, 17, humanHandJitter))
		} else {
			f.RightHandRotation = model.QuatIdentity()
		}
		f.LeftHandPosition = restL.Add(tuck).Add(deterministicJitter3(fi, 11, humanHandJitter))
		d := disc
		f.Disc = &d
		frames = append(frames, f)
		fi++
	}

	for _, s := range specs {
		hold := s.HoldFrames
		if hold < 2 {
			hold = 2
		}
		flight := s.FlightFrames
		if flight < 1 {
			flight = 1
		}
		back := s.WindupBack
		if back == 0 {
			back = 0.3
		}
		handSpeed := s.HandSpeed
		if handSpeed <= 0 {
			handSpeed = 5.0
		}

		rest := restR.Add(s.ReleaseOffset)

		// Release geometry: iterate once so the deviation is measured from
		// the actual release position, not the resting hand.
		dirGoal := goal.Sub(rest).Normalized()
		dir := deviate(dirGoal, s.DeviationDeg, s.DeviationSeed)
		handDir := dir
		for iter := 0; iter < 2; iter++ {
			handDir = deviate(dir, s.HandDeviationDeg, s.DeviationSeed+1.0)
			handMove := handDir.Scale(handSpeed * dt)
			if s.HandReverse {
				handMove = handMove.Scale(-1)
			}
			handRel := rest.Sub(dir.Scale(back)).Add(handMove)
			releasePos := handRel.Add(dir.Scale(discReleaseLead))
			dirGoal = goal.Sub(releasePos).Normalized()
			dir = deviate(dirGoal, s.DeviationDeg, s.DeviationSeed)
		}
		handDir = deviate(dir, s.HandDeviationDeg, s.DeviationSeed+1.0)

		// Hold: hand at rest, pulling back over the last windupFrames while
		// the off hand drops back (so the throwing hand is the one nearest
		// the release point on the frame before release).
		var hand model.Vec3
		tuck = model.Vec3{}
		for j := 0; j < hold; j++ {
			pull := 0.0
			if j >= hold-windupFrames {
				pull = back * float64(j-(hold-windupFrames)+1) / windupFrames
				tuck = dir.Scale(-offHandTuck)
			}
			hand = rest.Sub(dir.Scale(pull))
			disc := model.DiscState{
				Position:    hand.Add(dir.Scale(discInHand)),
				IsHeld:      true,
				PossessorID: fb.playerID,
			}
			if j == 0 && s.GrabDistance > 0 {
				disc.Position = hand.Add(model.Vec3{0, 0, s.GrabDistance})
			}
			emit(hand, s.ScriptedHand, disc, true)
		}

		// Release: the hand snaps forward (or backward) and the disc leaves
		// with the game-reported velocity.
		handMove := handDir.Scale(handSpeed * dt)
		if s.HandReverse {
			handMove = handMove.Scale(-1)
		}
		handRelease := hand.Add(handMove)
		vel := dir.Scale(s.ReleaseSpeed)
		pos := handRelease.Add(dir.Scale(discReleaseLead))
		emit(handRelease, s.ScriptedHand, model.DiscState{Position: pos, Velocity: vel, Speed: vel.Magnitude()}, false)

		// Flight: straight line unless bending/accelerating; the hand
		// returns to rest.
		for k := 1; k <= flight; k++ {
			if s.BendDegPerFrame > 0 {
				vel = bendToward(vel, goal.Sub(pos), s.BendDegPerFrame)
			}
			if s.SpeedGainPerFrame != 0 {
				spd := math.Max(minFlightSpeed, vel.Magnitude()+s.SpeedGainPerFrame)
				vel = vel.Normalized().Scale(spd)
			}
			pos = pos.Add(vel.Scale(dt))
			h := rest
			if k <= handRecover {
				h = handRelease.Lerp(rest, float64(k)/handRecover)
			}
			emit(h, s.ScriptedHand, model.DiscState{Position: pos, Velocity: vel, Speed: vel.Magnitude(), FramesSinceRelease: k}, false)
		}
		lastDisc := model.DiscState{Position: pos}
		tuck = model.Vec3{}
		for g := 0; g < s.GapFrames; g++ {
			emit(restR, s.ScriptedHand, lastDisc, false)
		}
	}
	return frames
}

// humanRelease returns the per-throw release-point offset (a few cm) and
// hand-direction deviation (5-25 degrees) of a human thrower.
func humanRelease(t int) (model.Vec3, float64) {
	ft := float64(t)
	return model.Vec3{0.05 * math.Sin(ft*1.9), 0.04 * math.Cos(ft*2.7), 0.03 * math.Sin(ft*0.8+1)},
		5.0 + 20.0*frac(ft*0.47+0.15)
}

// NormalThrowSequence generates nThrows legitimate throws: 8-16 m/s, 3-12
// degrees off the goal, hand speeds 4-9 m/s, holds of 8-30 frames, a
// centimetre-scale release-point spread, gentle drag in flight. No
// production-enabled throw detector may fire at production thresholds.
func (fb *FrameBuilder) NormalThrowSequence(nThrows int) []model.PlayerTelemetryFrame {
	specs := make([]ThrowSpec, nThrows)
	for t := range specs {
		ft := float64(t)
		off, handDev := humanRelease(t)
		specs[t] = ThrowSpec{
			HoldFrames:        8 + (t*7)%23,
			FlightFrames:      15,
			GapFrames:         8 + t%5,
			ReleaseSpeed:      8.0 + 8.0*frac(ft*0.37+0.1),
			DeviationDeg:      3.0 + 9.0*frac(ft*0.61+0.2),
			DeviationSeed:     ft * 2.399,
			HandSpeed:         4.0 + 5.0*frac(ft*0.53+0.3),
			WindupBack:        0.2 + 0.2*frac(ft*0.71),
			SpeedGainPerFrame: -0.05,
			ReleaseOffset:     off,
			HandDeviationDeg:  handDev,
		}
	}
	return fb.ThrowSequence(specs)
}

// EliteThrowSequence generates top-tier throws at 15-17 m/s (under the
// 18.9 m/s cap), 2.5-4 degrees off the goal with fast hands. Must stay clean
// on the production-enabled set.
func (fb *FrameBuilder) EliteThrowSequence(nThrows int) []model.PlayerTelemetryFrame {
	specs := make([]ThrowSpec, nThrows)
	for t := range specs {
		ft := float64(t)
		off, handDev := humanRelease(t)
		specs[t] = ThrowSpec{
			HoldFrames:        10 + (t*5)%17,
			FlightFrames:      15,
			GapFrames:         6 + t%4,
			ReleaseSpeed:      15.0 + 2.0*frac(ft*0.43+0.2),
			DeviationDeg:      2.5 + 1.5*frac(ft*0.77+0.4),
			DeviationSeed:     ft * 1.7,
			HandSpeed:         7.0 + 2.0*frac(ft*0.31),
			WindupBack:        0.25 + 0.1*frac(ft*0.71),
			SpeedGainPerFrame: -0.05,
			ReleaseOffset:     off,
			HandDeviationDeg:  handDev,
		}
	}
	return fb.ThrowSequence(specs)
}

// AimbotSpeed is the release speed of AimbotThrows: over the effective
// THROW_001 cap (18.9 m/s by default) and under the 2x-cap artifact
// band, so each release is an over-cap "disc_speed" event.
const AimbotSpeed = 35.0

// AimbotThrows generates nThrows releases at AimbotSpeed from a hand that
// barely moves (1 m/s), aimed 1 degree off the goal. THROW_001 fires on
// every release; THROW_002 (when enabled) sees a 35 m/s jump from rest.
func (fb *FrameBuilder) AimbotThrows(nThrows int) []model.PlayerTelemetryFrame {
	specs := make([]ThrowSpec, nThrows)
	for t := range specs {
		specs[t] = ThrowSpec{
			HoldFrames: 10, FlightFrames: 15, GapFrames: 6,
			ReleaseSpeed: AimbotSpeed, DeviationDeg: 1.0, DeviationSeed: float64(t),
			HandSpeed: 1.0, WindupBack: 0.05,
		}
	}
	return fb.ThrowSequence(specs)
}

// ArtifactThrows generates releases at 60 m/s (> 2x the physics cap), which
// THROW_001 reports as suspected telemetry artifacts at low severity.
func (fb *FrameBuilder) ArtifactThrows(nThrows int) []model.PlayerTelemetryFrame {
	frames := fb.AimbotThrows(nThrows)
	for i := range frames {
		if d := frames[i].Disc; d != nil && d.Speed > 30 {
			d.Velocity = d.Velocity.Normalized().Scale(60)
			d.Speed = 60
		}
	}
	return frames
}

// NearCapThrows generates legal sub-cap releases pinned at 17.9-18.7 m/s.
// It is a regression fixture: consistency close to the cap is player skill,
// not evidence of a modified client.
func (fb *FrameBuilder) NearCapThrows(nThrows int) []model.PlayerTelemetryFrame {
	specs := make([]ThrowSpec, nThrows)
	for t := range specs {
		ft := float64(t)
		specs[t] = ThrowSpec{
			HoldFrames: 10 + t%3, FlightFrames: 15, GapFrames: 6,
			ReleaseSpeed: 17.9 + 0.8*frac(ft*0.37+0.2), DeviationDeg: 3.0 + 5.0*frac(ft*0.61),
			DeviationSeed: ft * 2.1, HandSpeed: 8.0, WindupBack: 0.3, SpeedGainPerFrame: -0.05,
		}
	}
	return fb.ThrowSequence(specs)
}

// PrecisionAimbot generates sub-cap throws that land 0.3-0.5 degrees off
// the goal every time. THROW_005 fires on the 8th goal-directed throw
// (mean < 2 deg, stddev < 1.5 deg).
func (fb *FrameBuilder) PrecisionAimbot(nThrows int) []model.PlayerTelemetryFrame {
	specs := make([]ThrowSpec, nThrows)
	for t := range specs {
		ft := float64(t)
		specs[t] = ThrowSpec{
			HoldFrames: 10 + (t*3)%9, FlightFrames: 15, GapFrames: 6,
			ReleaseSpeed: 10.0 + 4.0*frac(ft*0.37+0.2), DeviationDeg: 0.3 + 0.2*frac(ft*0.53),
			DeviationSeed: ft * 2.399, HandSpeed: 6.0, WindupBack: 0.3, SpeedGainPerFrame: -0.05,
		}
	}
	return fb.ThrowSequence(specs)
}

// MagnetismBendDegPerFrame is the homing bend of MagnetismCheat: above
// THROW_006's 8 deg/frame violation threshold and below its 15 deg/frame
// elastic-bounce filter.
const MagnetismBendDegPerFrame = 10.0

// MagnetismCheat generates throws released 120 degrees away from the goal
// whose disc homes onto the goal at MagnetismBendDegPerFrame with constant
// speed: a magnetism cheat. Over the 15 tracked frames the disc bends ~100
// degrees in >= 5 violation frames and its goal alignment improves from ~0
// to 1, which passes THROW_006's alignment gate at production thresholds.
func (fb *FrameBuilder) MagnetismCheat(nThrows int) []model.PlayerTelemetryFrame {
	specs := make([]ThrowSpec, nThrows)
	for t := range specs {
		specs[t] = ThrowSpec{
			HoldFrames: 10, FlightFrames: 15, GapFrames: 6,
			ReleaseSpeed: 12.0, DeviationDeg: 120.0, DeviationSeed: float64(t) * 1.3,
			HandSpeed: 6.0, WindupBack: 0.3, BendDegPerFrame: MagnetismBendDegPerFrame,
		}
	}
	return fb.ThrowSequence(specs)
}

// AcceleratingDisc generates throws whose disc GAINS 6 m/s on every
// free-flight frame (10 -> 70 m/s over 10 frames, under the validator's
// 4 x DiscSpeedCap = 75.6 m/s disc sanity bound) without changing
// direction. THROW_008 counts >= 4 speed increases above its 5 m/s
// tolerance and fires when the track finalizes.
func (fb *FrameBuilder) AcceleratingDisc(nThrows int) []model.PlayerTelemetryFrame {
	specs := make([]ThrowSpec, nThrows)
	for t := range specs {
		specs[t] = ThrowSpec{
			HoldFrames: 10, FlightFrames: 10, GapFrames: 6,
			ReleaseSpeed: 10.0, DeviationDeg: 4.0, DeviationSeed: float64(t),
			HandSpeed: 6.0, WindupBack: 0.3, SpeedGainPerFrame: 6.0,
		}
	}
	return fb.ThrowSequence(specs)
}

// ReverseHandThrows generates throws where the hand moves AGAINST the disc
// on the release frame at 5 m/s (release angle ~180 degrees) from a
// stationary body. THROW_003 fires above its 177 degree threshold.
func (fb *FrameBuilder) ReverseHandThrows(nThrows int) []model.PlayerTelemetryFrame {
	specs := make([]ThrowSpec, nThrows)
	for t := range specs {
		specs[t] = ThrowSpec{
			HoldFrames: 10 + t%4, FlightFrames: 15, GapFrames: 6,
			ReleaseSpeed: 12.0, DeviationDeg: 5.0, DeviationSeed: float64(t) * 2.1,
			HandSpeed: 5.0, HandReverse: true, WindupBack: 0.3, SpeedGainPerFrame: -0.05,
		}
	}
	return fb.ThrowSequence(specs)
}

// MacroThrows generates scripted throws: no hand jitter, a fixed wind-up
// and release point, releases 36-37 frames apart, speed and hand speed
// varying by +-0.03 m/s. PAT_001 (interval CoV ~0.01), PAT_002 (release
// spread ~0 m) and THROW_004 (generalized variance far below threshold)
// fire once 12 throws have been seen; the throws themselves are legal.
func (fb *FrameBuilder) MacroThrows(nThrows int) []model.PlayerTelemetryFrame {
	specs := make([]ThrowSpec, nThrows)
	for t := range specs {
		ft := float64(t)
		specs[t] = ThrowSpec{
			HoldFrames: 12 + t%2, FlightFrames: 15, GapFrames: 8,
			ReleaseSpeed: 12.0 + 0.03*math.Sin(ft*1.7), DeviationDeg: 2.0, DeviationSeed: 0,
			HandSpeed: 6.0 + 0.03*math.Cos(ft*2.3), WindupBack: 0.3, ScriptedHand: true,
			SpeedGainPerFrame: -0.05,
		}
	}
	return fb.ThrowSequence(specs)
}

// ImpossibleGrabDistance is the hand-to-disc distance ImpossibleGrabs uses
// on the possession-gain frame: over STATE_001's 3 m threshold and under
// its 6 m desync guard (threshold + zero closing credit + 3 m margin).
const ImpossibleGrabDistance = 4.5

// ImpossibleGrabs generates normal throws whose every grab happens with the
// disc ImpossibleGrabDistance metres from the nearest hand. STATE_001
// fires on each possession gain.
func (fb *FrameBuilder) ImpossibleGrabs(nThrows int) []model.PlayerTelemetryFrame {
	specs := make([]ThrowSpec, nThrows)
	for t := range specs {
		ft := float64(t)
		specs[t] = ThrowSpec{
			HoldFrames: 10 + (t*5)%11, FlightFrames: 15, GapFrames: 8,
			ReleaseSpeed: 9.0 + 5.0*frac(ft*0.37), DeviationDeg: 4.0 + 6.0*frac(ft*0.61),
			DeviationSeed: ft * 2.399, HandSpeed: 5.0 + 3.0*frac(ft*0.53), WindupBack: 0.3,
			GrabDistance: ImpossibleGrabDistance, SpeedGainPerFrame: -0.05,
		}
	}
	return fb.ThrowSequence(specs)
}
