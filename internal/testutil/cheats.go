package testutil

import (
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// ===================================================================
// 4. Cheat Scenarios (one player, production thresholds)
// ===================================================================

// SpeedHackFrames generates a player sustaining hackSpeed (with the normal
// +-20 % modulation) along Z. With hackSpeed >= 70 the 30-frame median
// exceeds MOV_001's 55 m/s max_legitimate_speed on every full window.
// Hands follow the body, so their player-relative speed stays normal and
// BIO_002 does not duplicate MOV_001's body-movement evidence.
func (fb *FrameBuilder) SpeedHackFrames(nFrames int, hackSpeed float64) []model.PlayerTelemetryFrame {
	return fb.NormalMovingPlayer(nFrames, hackSpeed)
}

// BurstSpeed / BurstFrames describe OscillatingSpeedHack's bursts: 5
// consecutive frames at 70 m/s (4.7 m per frame at 15 fps, under MOV_002's
// 8 m teleport threshold) every 60 frames on an otherwise 5 m/s player.
// The window median stays legitimate, so only MOV_001's burst branch
// (min_burst_frames = 5 over the 55 m/s physics cap) fires, once per burst.
const (
	BurstSpeed  = 70.0
	BurstFrames = 5
	BurstPeriod = 60
)

// OscillatingSpeedHack generates the toggled speed hack described above.
func (fb *FrameBuilder) OscillatingSpeedHack(nFrames int) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	dt := fb.dt()
	z := fb.startPos[2]
	dir := 1.0
	for i := 0; i < nFrames; i++ {
		speed := 5.0
		if i > 0 && i%BurstPeriod < BurstFrames {
			speed = BurstSpeed
		}
		if math.Abs(z+dir*speed*dt) > moveHalfZ {
			dir = -dir
		}
		z += dir * speed * dt
		pos := ClampToArena(model.Vec3{fb.startPos[0], fb.startPos[1], z})
		frames[i] = fb.baseFrame(i, pos, rotationFromVelocity(model.Vec3{0, 0, dir * speed}))
	}
	return frames
}

// TeleportCheat generates a 4 m/s player who jumps `distance` metres along
// Z every `everyFrames` frames (alternating direction so the path stays in
// the arena) while neither immune nor stunned. With 8 <= distance <= 12
// each jump is a MOV_002 candidate (over teleport_threshold, under
// max_displacement, >> 3x the expected displacement); the detector emits
// from the min_incidents-th (5th) jump on.
func (fb *FrameBuilder) TeleportCheat(nFrames, everyFrames int, distance float64) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 4.0)
	if everyFrames < 1 {
		everyFrames = 30
	}
	offset := 0.0
	sign := 1.0
	for i := range frames {
		if i > 0 && i%everyFrames == 0 {
			if frames[i].Position[2]+offset > 0 {
				sign = -1
			} else {
				sign = 1
			}
			offset += sign * distance
		}
		frames[i].Position = ClampToArena(frames[i].Position.Add(model.Vec3{0, 0, offset}))
		fb.humanHands(&frames[i], i)
	}
	return frames
}

// ReversalSpeed is the speed of ZeroInertiaReversals' legs: above
// MOV_003's 12 m/s min_speed on both sides of every flip.
const ReversalSpeed = 20.0

// ZeroInertiaReversals generates straight 45-frame legs at ReversalSpeed
// whose direction flips instantly (180 degrees, no deceleration) and is
// then held: the zero-inertia signature MOV_003 confirms after 3 frames on
// the new heading. Legit players (NormalMovingPlayer) ramp through zero.
func (fb *FrameBuilder) ZeroInertiaReversals(nFrames int) []model.PlayerTelemetryFrame {
	const legFrames = 45
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	dt := fb.dt()
	z := fb.startPos[2] - float64(legFrames)*ReversalSpeed*dt/2
	dir := 1.0
	for i := 0; i < nFrames; i++ {
		if i > 0 && i%legFrames == 0 {
			dir = -dir
		}
		z += dir * ReversalSpeed * dt
		pos := ClampToArena(model.Vec3{fb.startPos[0], fb.startPos[1], z})
		frames[i] = fb.baseFrame(i, pos, rotationFromVelocity(model.Vec3{0, 0, dir * ReversalSpeed}))
	}
	return frames
}

// speedProfile integrates a per-frame (speed, boosting) profile along Z.
func (fb *FrameBuilder) speedProfile(nFrames int, profile func(i int) (speed float64, boosting bool)) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	mover := newZMover(fb.startPos[2], 1.0, fb.dt())
	for i := 0; i < nFrames; i++ {
		speed, boosting := profile(i)
		z, vz := mover.step(speed)
		pos := ClampToArena(model.Vec3{fb.startPos[0], fb.startPos[1], z})
		f := fb.baseFrame(i, pos, rotationFromVelocity(model.Vec3{0, 0, vz}))
		f.IsBoosting = boosting
		frames[i] = f
	}
	return frames
}

// BoostCapGain is the speed BoostCapViolation adds while boosting: 10 m/s,
// over MOV_004's cap + margin (5 + 1.5 m/s).
const BoostCapGain = 10.0

// BoostCapViolation generates a 5 m/s player who, every 90 frames, boosts
// for 8 frames ramping up by BoostCapGain and then decays over 15 frames.
// MOV_004 fires when each boost ends.
func (fb *FrameBuilder) BoostCapViolation(nFrames int) []model.PlayerTelemetryFrame {
	return fb.speedProfile(nFrames, func(i int) (float64, bool) {
		c := i % 90
		switch {
		case c >= 30 && c < 38:
			return 5.0 + BoostCapGain*float64(c-29)/8.0, true
		case c >= 38 && c < 53:
			return 5.0 + BoostCapGain*(1.0-float64(c-37)/15.0), false
		default:
			return 5.0, false
		}
	})
}

// InfiniteBoost generates a 5 m/s player with eight 2-frame boost taps 3
// frames apart followed by a 20-frame pause, every 60 frames. MOV_005
// counts activations: 8 consecutive (> max_consecutive 5) with gaps <=
// recharge_pause_frames (10) close as a violation sequence at each pause;
// from the third cycle on, min_sequences (2) is met and the sixth
// activation of a cycle fires the event (then the counters reset).
func (fb *FrameBuilder) InfiniteBoost(nFrames int) []model.PlayerTelemetryFrame {
	return fb.speedProfile(nFrames, func(i int) (float64, bool) {
		c := i % 60
		return 5.0, c < 40 && c%5 < 2
	})
}

// StunBypass generates a moving player stunned for 5 frames (0.33 s) every
// 40 frames: far under STATE_002's min_stun_frames (20 = 1.33 s at 15 Hz).
// The detector fires from the second incident on.
func (fb *FrameBuilder) StunBypass(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 3.0)
	for i := range frames {
		c := i % 40
		frames[i].IsStunned = c >= 10 && c < 15
	}
	return frames
}

// GodMode generates a moving player who is immune on every frame. STATE_004
// counts active immune frames past max_immune_frames (225) + 5 and re-fires
// every escalation interval (112 frames) while the streak continues; the
// pipeline merges the escalations into one incident.
func (fb *FrameBuilder) GodMode(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 4.0)
	for i := range frames {
		frames[i].IsImmune = true
	}
	return frames
}

// ShieldAbuse generates a moving player whose shield never drops. STATE_003
// escalates at 300 / 375 / 600 consecutive frames.
func (fb *FrameBuilder) ShieldAbuse(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 3.0)
	for i := range frames {
		frames[i].ShieldActive = true
	}
	return frames
}

// CooldownBypass generates shield cycles of 10 frames on / 5 frames off:
// every reactivation comes 0.33 s after the shield dropped, under
// STATE_005's 60-frame (4 s) cooldown. It fires from the 15th violation.
func (fb *FrameBuilder) CooldownBypass(nCycles int) []model.PlayerTelemetryFrame {
	return fb.shieldCycle(nCycles, 10, 5)
}

// ScoreJump generates an idle player whose blue score jumps by delta at
// frame 25. STATE_006 is SUSPENDED (no confirmed impossible invariant) and
// returns nil for any delta; MOV_002 arms its goal cooldown on the change.
func (fb *FrameBuilder) ScoreJump(nFrames, delta int) []model.PlayerTelemetryFrame {
	frames := fb.NormalIdlePlayer(nFrames)
	for i := range frames {
		if i >= 25 {
			frames[i].BlueScore = delta
		}
	}
	return frames
}

// BotJitterAmplitude is BotBehavior's hand jitter: per-axis variance
// amp^2/4 over three axes gives ~1.9e-7 m^2, under BIO_003's 1e-5 threshold
// and above its 1e-10 frozen-data guard.
const BotJitterAmplitude = 0.0005

// BotBehavior generates a 4 m/s player (active for BIO_003's gate) whose
// hands sit on the body with BotJitterAmplitude jitter and normal aim
// wobble. BIO_003 fires after two consecutive 90-frame windows.
func (fb *FrameBuilder) BotBehavior(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 4.0)
	for i := range frames {
		frames[i].LeftHandPosition = frames[i].Position.Add(leftHandOffset).Add(deterministicJitter3(i, 11, BotJitterAmplitude))
		frames[i].RightHandPosition = frames[i].Position.Add(rightHandOffset).Add(deterministicJitter3(i, 17, BotJitterAmplitude))
	}
	return frames
}

// FrozenAimDriftRad is FrozenAim's per-frame rotation drift: over 90 frames
// the angles spread 0.009 rad (variance ~7e-6 rad^2), under BIO_004's 5e-5
// threshold and above the frozen-data guard.
const FrozenAimDriftRad = 0.0001

// FrozenAim generates a 4 m/s player with human hand jitter but hand
// rotations that only creep by FrozenAimDriftRad per frame. BIO_004 fires
// after two consecutive 90-frame windows.
func (fb *FrameBuilder) FrozenAim(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 4.0)
	for i := range frames {
		a := float64(i) * FrozenAimDriftRad
		frames[i].LeftHandRotation = axisAngleQuat(model.Vec3{1, 0, 0}, a)
		frames[i].RightHandRotation = axisAngleQuat(model.Vec3{0, 1, 0}, a)
	}
	return frames
}

// HandJumpMetres is HandSpeedHack's hand offset to either side of the
// body: the hand alternates between +HandJumpMetres and -HandJumpMetres on
// X, so the per-frame displacement is 4.8 m in 67 ms = 72 m/s, over
// BIO_002's 50 m/s limit, while every sample stays within the validator's
// MaxHandBodyDistance (3 m) of the body.
const HandJumpMetres = 2.4

// HandSpeedHack generates a slowly moving player whose right hand flips
// HandJumpMetres to alternate sides of the body on seven consecutive frames
// around the middle of the stream (a hand-position injection), giving six
// consecutive violation frames. BIO_002 emits on the 2nd, 4th and 6th
// consecutive violation frames; the pipeline merges them.
func (fb *FrameBuilder) HandSpeedHack(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 3.0)
	mid := nFrames / 2
	for i := mid; i < mid+7 && i < nFrames; i++ {
		side := HandJumpMetres
		if (i-mid)%2 == 1 {
			side = -HandJumpMetres
		}
		frames[i].RightHandPosition = frames[i].RightHandPosition.Add(model.Vec3{side, 0, 0})
	}
	return frames
}

// WristSpin generates a moving player whose right hand rotates by
// radPerFrame around Z on 8 consecutive frames starting at nFrames/3. The
// wrist rate is radPerFrame / dt, capped by the quaternion distance at
// pi / dt: at 15 fps that is 46.9 rad/s, under BIO_001's 50 rad/s limit,
// so BIO_001 is unreachable; at 60 fps 1.5 rad per frame is 90 rad/s.
func (fb *FrameBuilder) WristSpin(nFrames int, radPerFrame float64) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 3.0)
	start := nFrames / 3
	angle := 0.0
	for i := start; i < start+8 && i < nFrames; i++ {
		angle += radPerFrame
		frames[i].RightHandRotation = axisAngleQuat(model.Vec3{0, 0, 1}, angle).Multiply(humanWobble(i, 5.0)).Normalize()
	}
	return frames
}

// ExtendedReachMetres is ExtendedReach's hand-to-head distance: over
// PAT_005's 1.6 m playspace threshold.
const ExtendedReachMetres = 2.5

// ExtendedReach generates a slowly moving player whose hands are held
// ExtendedReachMetres from the head on every frame. PAT_005 fires after
// min_sustained_frames (30) and re-fires every 30 frames.
func (fb *FrameBuilder) ExtendedReach(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 2.0)
	for i := range frames {
		frames[i].LeftHandPosition = frames[i].Position.Add(model.Vec3{-ExtendedReachMetres, 0.2, 0}).Add(deterministicJitter3(i, 11, humanHandJitter))
		frames[i].RightHandPosition = frames[i].Position.Add(model.Vec3{ExtendedReachMetres, 0.2, 0}).Add(deterministicJitter3(i, 17, humanHandJitter))
	}
	return frames
}

// CompositeCheater generates one player exhibiting three cheat categories
// in sequence: a sustained speed hack (movement, MOV_001), impossible grabs
// (state, STATE_001) and aimbot releases (throw, THROW_001). Each fires
// with confidence >= 0.7, so PAT_004 (min_categories 3) fires as well.
func (fb *FrameBuilder) CompositeCheater() []model.PlayerTelemetryFrame {
	speed := fb.SpeedHackFrames(90, 75)
	grabs := fb.After(speed).ImpossibleGrabs(3)
	aimbot := fb.After(grabs).AimbotThrows(3)
	return Concat(speed, grabs, aimbot)
}
