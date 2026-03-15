package testutil

import (
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// FrameBuilder constructs deterministic telemetry frame sequences.
type FrameBuilder struct {
	playerID  string
	tickRate  float64 // fps, default 15
	startPos  model.Vec3
	gamePhase string
	pingMs    float64
}

// NewFrameBuilder creates a new FrameBuilder for the given player.
func NewFrameBuilder(playerID string) *FrameBuilder {
	return &FrameBuilder{
		playerID:  playerID,
		tickRate:  15.0,
		startPos:  model.Vec3{5, 0, 0},
		gamePhase: "playing",
		pingMs:    20.0,
	}
}

// WithTickRate sets the tick rate (frames per second).
func (fb *FrameBuilder) WithTickRate(fps float64) *FrameBuilder {
	fb.tickRate = fps
	return fb
}

// WithStartPos sets the starting position.
func (fb *FrameBuilder) WithStartPos(pos model.Vec3) *FrameBuilder {
	fb.startPos = pos
	return fb
}

// WithGamePhase sets the game phase for all generated frames.
func (fb *FrameBuilder) WithGamePhase(phase string) *FrameBuilder {
	fb.gamePhase = phase
	return fb
}

// WithPing sets the estimated ping in milliseconds.
func (fb *FrameBuilder) WithPing(ms float64) *FrameBuilder {
	fb.pingMs = ms
	return fb
}

// dt returns the time delta per frame.
func (fb *FrameBuilder) dt() float64 {
	return 1.0 / fb.tickRate
}

// baseFrame creates a base frame with common fields populated.
func (fb *FrameBuilder) baseFrame(index int, pos model.Vec3, rot model.Quat) model.PlayerTelemetryFrame {
	return model.PlayerTelemetryFrame{
		PlayerID:          fb.playerID,
		FrameIndex:        index,
		Timestamp:         1.0 + float64(index)*fb.dt(),
		DeltaTime:         fb.dt(),
		Position:          pos,
		Rotation:          rot,
		LeftHandPosition:  pos.Add(model.Vec3{-0.3, 0.3, 0.2}),
		RightHandPosition: pos.Add(model.Vec3{0.3, 0.3, -0.2}),
		LeftHandRotation:  rotatedQuat(0.01 * math.Sin(float64(index)*0.7+1.0)),
		RightHandRotation: rotatedQuat(0.01 * math.Cos(float64(index)*0.6+2.0)),
		GamePhase:         fb.gamePhase,
		EstimatedPingMs:   fb.pingMs,
		Disc: &model.DiscState{
			Position: model.Vec3{0, 0, 0},
			Velocity: model.Vec3{0, 0, 0},
		},
	}
}

// ---------- Helper functions ----------

// rotatedQuat creates a slightly-rotated quaternion (small rotation around Y axis).
func rotatedQuat(baseAngle float64) model.Quat {
	s := math.Sin(baseAngle / 2)
	c := math.Cos(baseAngle / 2)
	return model.Quat{0, s, 0, c}
}

// smallQuat creates a small rotation quaternion around the Y axis by angle radians.
func smallQuat(angle float64) model.Quat {
	half := angle / 2.0
	return model.Quat{0, math.Sin(half), 0, math.Cos(half)}.Normalize()
}

// axisAngleQuat creates a quaternion from an axis and angle (radians).
func axisAngleQuat(axis model.Vec3, angle float64) model.Quat {
	axis = axis.Normalized()
	half := angle / 2.0
	s := math.Sin(half)
	return model.Quat{axis[0] * s, axis[1] * s, axis[2] * s, math.Cos(half)}.Normalize()
}

// deterministicJitter returns a deterministic pseudo-jitter value based on frame index and a seed.
// Uses sinusoidal mixing to avoid needing a PRNG.
func deterministicJitter(frame int, seed int, amplitude float64) float64 {
	t := float64(frame)
	s := float64(seed)
	return amplitude * math.Sin(t*1.7+s*3.1) * math.Cos(t*0.3+s*7.9)
}

// deterministicJitter3 returns a 3D jitter vector.
func deterministicJitter3(frame int, seed int, amplitude float64) model.Vec3 {
	return model.Vec3{
		deterministicJitter(frame, seed, amplitude),
		deterministicJitter(frame, seed+1, amplitude),
		deterministicJitter(frame, seed+2, amplitude),
	}
}

// clampToArena ensures positions stay within arena bounds.
func clampToArena(pos model.Vec3) model.Vec3 {
	return model.Vec3{
		model.Clamp(pos[0], -40, 40),
		model.Clamp(pos[1], -15, 15),
		model.Clamp(pos[2], -15, 15),
	}
}

// rotationFromVelocity creates a quaternion that "faces" the velocity direction.
func rotationFromVelocity(vel model.Vec3) model.Quat {
	if vel.Magnitude() < 1e-6 {
		return model.QuatIdentity()
	}
	dir := vel.Normalized()
	// Yaw angle from X axis in XZ plane
	yaw := math.Atan2(dir[2], dir[0])
	return axisAngleQuat(model.Vec3{0, 1, 0}, yaw)
}

// ===================================================================
// 1. Normal Gameplay Scenarios
// ===================================================================

// NormalIdlePlayer generates frames of a player standing still with natural
// micro-jitter on hands (sinusoidal, 0.001-0.003m), slight head wobble.
func (fb *FrameBuilder) NormalIdlePlayer(nFrames int) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	for i := 0; i < nFrames; i++ {
		t := float64(i)
		pos := fb.startPos

		// Slight head wobble (very small)
		headWobble := model.Vec3{
			0.0005 * math.Sin(t*0.5),
			0.0003 * math.Cos(t*0.7),
			0.0004 * math.Sin(t*0.3+1.0),
		}
		pos = clampToArena(pos.Add(headWobble))

		rot := axisAngleQuat(model.Vec3{0, 1, 0}, 0.001*math.Sin(t*0.2))

		f := fb.baseFrame(i, pos, rot)

		// Hand micro-jitter: sinusoidal, 0.001-0.003m
		leftJitter := model.Vec3{
			0.002 * math.Sin(t*2.3),
			0.001 * math.Cos(t*1.7+0.5),
			0.0015 * math.Sin(t*3.1+1.2),
		}
		rightJitter := model.Vec3{
			0.002 * math.Cos(t*2.1+0.3),
			0.0015 * math.Sin(t*1.9+0.8),
			0.001 * math.Cos(t*2.7+2.0),
		}
		f.LeftHandPosition = pos.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(leftJitter)
		f.RightHandPosition = pos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(rightJitter)

		// Tiny quaternion perturbation on hands
		f.LeftHandRotation = axisAngleQuat(model.Vec3{1, 0, 0}, 0.005*math.Sin(t*1.3))
		f.RightHandRotation = axisAngleQuat(model.Vec3{0, 0, 1}, 0.005*math.Cos(t*1.1))

		frames[i] = f
	}
	return frames
}

// NormalMovingPlayer generates frames of a player moving along a smooth sinusoidal
// path at avgSpeed (typical 3-8 m/s) with natural hand lag and jitter.
func (fb *FrameBuilder) NormalMovingPlayer(nFrames int, avgSpeed float64) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	dt := fb.dt()

	// Accumulate position incrementally to ensure smooth frame-to-frame deltas
	posX := fb.startPos[0]

	for i := 0; i < nFrames; i++ {
		// Speed varies ±20% with a sinusoidal pattern
		speedVariation := 1.0 + 0.2*math.Sin(float64(i)*0.3)
		speed := avgSpeed * speedVariation

		// Incremental position update along X; sinusoidal weave on Y/Z
		posX += speed * dt
		pos := model.Vec3{
			posX,
			fb.startPos[1] + 0.5*math.Sin(float64(i)*0.1),
			fb.startPos[2] + 2.0*math.Sin(float64(i)*0.05),
		}
		pos = clampToArena(pos)

		// Velocity direction for rotation
		vel := model.Vec3{
			speed,
			0.5 * 0.1 * math.Cos(float64(i)*0.1),
			2.0 * 0.05 * math.Cos(float64(i)*0.05),
		}
		rot := rotationFromVelocity(vel)

		f := fb.baseFrame(i, pos, rot)

		// Hands track body with natural lag (offset + jitter)
		handLag := 0.01 * math.Sin(float64(i)*0.5)
		leftJitter := model.Vec3{
			0.003 * math.Sin(float64(i)*2.3),
			0.002 * math.Cos(float64(i)*1.7+0.5),
			0.003 * math.Sin(float64(i)*3.1+1.2),
		}
		rightJitter := model.Vec3{
			0.003 * math.Cos(float64(i)*2.1+0.3),
			0.002 * math.Sin(float64(i)*1.9+0.8),
			0.003 * math.Cos(float64(i)*2.7+2.0),
		}
		f.LeftHandPosition = pos.Add(model.Vec3{-0.3 + handLag, 0.3, 0.2}).Add(leftJitter)
		f.RightHandPosition = pos.Add(model.Vec3{0.3 + handLag, 0.3, -0.2}).Add(rightJitter)

		// Rotational perturbation on hands (sinusoidal ±0.01 rad to prevent zero-wobble detection)
		f.LeftHandRotation = rotatedQuat(0.01 * math.Sin(float64(i)*0.7+1.0)).Multiply(
			axisAngleQuat(model.Vec3{1, 0, 0}, 0.01*math.Sin(float64(i)*1.3)))
		f.RightHandRotation = rotatedQuat(0.01 * math.Cos(float64(i)*0.6+2.0)).Multiply(
			axisAngleQuat(model.Vec3{0, 0, 1}, 0.01*math.Cos(float64(i)*1.1)))

		frames[i] = f
	}
	return frames
}

// NormalThrowSequence generates frames containing nThrows throws with legitimate mechanics.
// Each throw: hold 0.5-2.0s, wind-up 5-8 frames, release at 8-16 m/s, disc flies 10-20 frames.
// Throws vary realistically in release speed (±2 m/s), angle (±10 deg), hand speed, and possession duration (±0.5s).
func (fb *FrameBuilder) NormalThrowSequence(nThrows int) []model.PlayerTelemetryFrame {
	var frames []model.PlayerTelemetryFrame
	dt := fb.dt()
	frameIdx := 0

	for throwNum := 0; throwNum < nThrows; throwNum++ {
		// Deterministic hold duration: 0.5 to 2.0s, varying by throw index (±0.5s variation)
		holdDuration := 0.5 + 1.5*(0.5+0.5*math.Sin(float64(throwNum)*2.1)) +
			0.5*math.Sin(float64(throwNum)*3.7)
		if holdDuration < 0.3 {
			holdDuration = 0.3
		}
		holdFrames := int(holdDuration / dt)

		// Wind-up frames: 5-8
		windupFrames := 5 + (throwNum % 4)

		// Release speed: 8-16 m/s with ±2 m/s extra variation per throw
		releaseSpeed := 8.0 + 8.0*(0.5+0.5*math.Sin(float64(throwNum)*1.3)) +
			2.0*math.Sin(float64(throwNum)*4.3+0.7)
		if releaseSpeed < 5.0 {
			releaseSpeed = 5.0
		}
		if releaseSpeed > 18.0 {
			releaseSpeed = 18.0
		}

		// Release angle variation: 5-25 degrees between throws with ±10 degree variation
		releaseAngleDeg := 5.0 + 20.0*(0.5+0.5*math.Cos(float64(throwNum)*1.7)) +
			10.0*math.Sin(float64(throwNum)*2.9+1.3)
		releaseAngle := releaseAngleDeg * math.Pi / 180.0

		// Hand-speed proportion of disc speed varies per throw: 50-70%
		handSpeedRatio := 0.5 + 0.2*math.Sin(float64(throwNum)*3.1+0.5)

		// Disc flight frames: 10-20
		flightFrames := 10 + (throwNum*3)%11

		basePos := model.Vec3{
			fb.startPos[0] + float64(throwNum)*2.0,
			fb.startPos[1],
			fb.startPos[2],
		}
		basePos = clampToArena(basePos)

		// Wrist rotation varies per throw
		wristAngleBase := 0.03 * float64(throwNum+1)

		// Phase 1: Holding disc
		for j := 0; j < holdFrames; j++ {
			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = true
			f.Disc = &model.DiscState{
				Position:    basePos.Add(model.Vec3{0.3, 0.3, -0.2}),
				Velocity:    model.Vec3{0, 0, 0},
				Speed:       0,
				PossessorID: fb.playerID,
				IsHeld:      true,
			}
			// Gentle hand jitter while holding
			f.RightHandPosition = basePos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(deterministicJitter3(frameIdx, 30, 0.002))
			f.LeftHandPosition = basePos.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(deterministicJitter3(frameIdx, 40, 0.002))
			// Non-identity hand rotations with slight variation
			f.RightHandRotation = rotatedQuat(wristAngleBase + 0.01*math.Sin(float64(frameIdx)*0.9))
			f.LeftHandRotation = rotatedQuat(wristAngleBase*0.7 + 0.01*math.Cos(float64(frameIdx)*0.8))
			frames = append(frames, f)
			frameIdx++
		}

		// Phase 2: Wind-up (hand accelerates from 0 to releaseSpeed over windupFrames)
		throwDir := model.Vec3{math.Cos(releaseAngle), 0, math.Sin(releaseAngle)}.Normalized()
		for j := 0; j < windupFrames; j++ {
			progress := float64(j+1) / float64(windupFrames)
			handSpeed := releaseSpeed * progress * handSpeedRatio
			handOffset := throwDir.Scale(0.3 + progress*0.2)

			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = true
			handPos := basePos.Add(model.Vec3{0, 0.3, 0}).Add(handOffset)
			f.RightHandPosition = handPos
			f.LeftHandPosition = basePos.Add(model.Vec3{-0.3, 0.3, 0.2})
			// Wrist rotation increases during wind-up
			wristAngle := wristAngleBase + progress*0.5*float64(throwNum+1)*0.1
			f.RightHandRotation = axisAngleQuat(model.Vec3{0, 0, 1}, wristAngle)
			f.LeftHandRotation = rotatedQuat(wristAngleBase * 0.7)

			discVel := throwDir.Scale(handSpeed)
			f.Disc = &model.DiscState{
				Position:    handPos.Add(throwDir.Scale(0.15)),
				Velocity:    discVel,
				Speed:       handSpeed,
				PossessorID: fb.playerID,
				IsHeld:      true,
			}
			frames = append(frames, f)
			frameIdx++
		}

		// Phase 3: Release frame
		releasePos := basePos.Add(model.Vec3{0, 0.3, 0}).Add(throwDir.Scale(0.5))
		releaseVel := throwDir.Scale(releaseSpeed)
		{
			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = false
			f.RightHandPosition = releasePos.Add(model.Vec3{0, 0, 0})
			handToDisc := 0.1 + 0.2*(0.5+0.5*math.Sin(float64(throwNum)*0.9))
			// Wrist rotation at release varies per throw
			f.RightHandRotation = axisAngleQuat(model.Vec3{0, 0, 1}, wristAngleBase+0.5*float64(throwNum+1)*0.1)
			f.LeftHandRotation = rotatedQuat(wristAngleBase * 0.7)

			discPos := releasePos.Add(throwDir.Scale(handToDisc))
			relVec := releasePos
			relVel := releaseVel
			f.Disc = &model.DiscState{
				Position:            discPos,
				Velocity:            releaseVel,
				Speed:               releaseSpeed,
				PossessorID:         "",
				IsHeld:              false,
				FramesSinceRelease:  0,
				ReleasePosition:     &relVec,
				ReleaseVelocity:     &relVel,
				DistanceFromThrower: discPos.Distance(basePos),
			}
			frames = append(frames, f)
			frameIdx++
		}

		// Phase 4: Disc in flight
		for j := 1; j <= flightFrames; j++ {
			discFlightPos := releasePos.Add(releaseVel.Scale(float64(j) * dt))
			discFlightPos = clampToArena(discFlightPos)

			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = false
			f.RightHandRotation = rotatedQuat(wristAngleBase)
			f.LeftHandRotation = rotatedQuat(wristAngleBase * 0.7)
			prevVel := releaseVel
			f.Disc = &model.DiscState{
				Position:            discFlightPos,
				Velocity:            releaseVel,
				Speed:               releaseSpeed,
				PreviousVelocity:    &prevVel,
				IsHeld:              false,
				FramesSinceRelease:  j,
				ReleasePosition:     &releasePos,
				ReleaseVelocity:     &releaseVel,
				DistanceFromThrower: discFlightPos.Distance(basePos),
			}
			frames = append(frames, f)
			frameIdx++
		}

		// "Caught" frame - possession changes to another player
		{
			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = false
			f.RightHandRotation = rotatedQuat(wristAngleBase)
			f.LeftHandRotation = rotatedQuat(wristAngleBase * 0.7)
			catchPos := releasePos.Add(releaseVel.Scale(float64(flightFrames+1) * dt))
			catchPos = clampToArena(catchPos)
			f.Disc = &model.DiscState{
				Position:    catchPos,
				Velocity:    model.Vec3{0, 0, 0},
				Speed:       0,
				PossessorID: "other_player",
				IsHeld:      true,
			}
			frames = append(frames, f)
			frameIdx++
		}
	}

	return frames
}

// NormalBoostSequence generates frames with nBoosts boosts, each with 3-5s gaps.
// Speed increases by 3-4 m/s during boost, decays over 1-2s after.
func (fb *FrameBuilder) NormalBoostSequence(nBoosts int) []model.PlayerTelemetryFrame {
	var frames []model.PlayerTelemetryFrame
	dt := fb.dt()
	frameIdx := 0
	baseSpeed := 5.0
	pos := fb.startPos

	for boostNum := 0; boostNum < nBoosts; boostNum++ {
		// Gap before boost: 3-5 seconds
		gapDuration := 3.0 + 2.0*(0.5+0.5*math.Sin(float64(boostNum)*1.3))
		gapFrames := int(gapDuration / dt)

		// Boost duration: ~0.5s (about 7-8 frames at 15fps)
		boostFrames := 7 + (boostNum % 2)

		// Boost magnitude: 3-4 m/s
		boostMagnitude := 3.0 + 1.0*(0.5+0.5*math.Cos(float64(boostNum)*2.1))

		// Decay duration: 1-2s
		decayDuration := 1.0 + 1.0*(0.5+0.5*math.Sin(float64(boostNum)*0.7))
		decayFrames := int(decayDuration / dt)

		// Pre-boost gap (normal movement)
		for j := 0; j < gapFrames; j++ {
			speed := baseSpeed + 0.5*math.Sin(float64(frameIdx)*0.2)
			pos = clampToArena(pos.Add(model.Vec3{speed * dt, 0, 0}))
			rot := rotationFromVelocity(model.Vec3{speed, 0, 0})

			f := fb.baseFrame(frameIdx, pos, rot)
			f.IsBoosting = false
			f.LeftHandPosition = pos.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(deterministicJitter3(frameIdx, 50, 0.003))
			f.RightHandPosition = pos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(deterministicJitter3(frameIdx, 60, 0.003))
			frames = append(frames, f)
			frameIdx++
		}

		// Boost phase
		for j := 0; j < boostFrames; j++ {
			speed := baseSpeed + boostMagnitude
			pos = clampToArena(pos.Add(model.Vec3{speed * dt, 0, 0}))
			rot := rotationFromVelocity(model.Vec3{speed, 0, 0})

			f := fb.baseFrame(frameIdx, pos, rot)
			f.IsBoosting = true
			f.LeftHandPosition = pos.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(deterministicJitter3(frameIdx, 50, 0.003))
			f.RightHandPosition = pos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(deterministicJitter3(frameIdx, 60, 0.003))
			frames = append(frames, f)
			frameIdx++
		}

		// Decay phase
		for j := 0; j < decayFrames; j++ {
			progress := float64(j) / float64(decayFrames)
			speed := baseSpeed + boostMagnitude*(1.0-progress)
			pos = clampToArena(pos.Add(model.Vec3{speed * dt, 0, 0}))
			rot := rotationFromVelocity(model.Vec3{speed, 0, 0})

			f := fb.baseFrame(frameIdx, pos, rot)
			f.IsBoosting = false
			f.LeftHandPosition = pos.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(deterministicJitter3(frameIdx, 50, 0.003))
			f.RightHandPosition = pos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(deterministicJitter3(frameIdx, 60, 0.003))
			frames = append(frames, f)
			frameIdx++
		}
	}

	return frames
}

// ===================================================================
// 2. Jitter/Artifact Scenarios
// ===================================================================

// JitteryTelemetry generates normal movement with position jitter of jitterLevel
// meters added per frame, simulating noisy tracking.
func (fb *FrameBuilder) JitteryTelemetry(nFrames int, jitterLevel float64) []model.PlayerTelemetryFrame {
	// Start with normal movement, then add jitter
	frames := fb.NormalMovingPlayer(nFrames, 5.0)
	for i := range frames {
		posJitter := deterministicJitter3(i, 100, jitterLevel)
		frames[i].Position = clampToArena(frames[i].Position.Add(posJitter))

		// Independent hand jitter
		leftJitter := deterministicJitter3(i, 110, jitterLevel*0.8)
		rightJitter := deterministicJitter3(i, 120, jitterLevel*0.8)
		frames[i].LeftHandPosition = frames[i].LeftHandPosition.Add(leftJitter)
		frames[i].RightHandPosition = frames[i].RightHandPosition.Add(rightJitter)
	}
	return frames
}

// PacketLossFrames generates normal movement but with frames dropped at dropRate.
// At drop points, DeltaTime is doubled to simulate missing frames.
func (fb *FrameBuilder) PacketLossFrames(nFrames int, dropRate float64) []model.PlayerTelemetryFrame {
	baseFrames := fb.NormalMovingPlayer(nFrames*2, 5.0)
	var frames []model.PlayerTelemetryFrame
	dt := fb.dt()
	outputIdx := 0

	for i := 0; i < len(baseFrames); i++ {
		// Deterministic "drop" decision based on sinusoidal hash
		dropVal := math.Abs(math.Sin(float64(i)*7.3 + 2.9))
		if dropVal < dropRate && i > 0 && len(frames) > 0 {
			// Skip this frame, but accumulate dt into next frame
			if i+1 < len(baseFrames) {
				baseFrames[i+1].DeltaTime = dt * 2.0
			}
			continue
		}

		f := baseFrames[i]
		f.FrameIndex = outputIdx
		f.Timestamp = 1.0 + float64(outputIdx)*dt
		if f.DeltaTime == 0 {
			f.DeltaTime = dt
		}
		frames = append(frames, f)
		outputIdx++

		if outputIdx >= nFrames {
			break
		}
	}

	return frames
}

// LargeFrameGap generates normal movement with a single large time gap at gapAtFrame.
// Position jumps to where the player would be if moving continuously.
func (fb *FrameBuilder) LargeFrameGap(nFrames int, gapAtFrame int, gapDuration float64) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	dt := fb.dt()
	speed := 5.0
	accumulatedTime := 0.0
	accumulatedDist := 0.0

	for i := 0; i < nFrames; i++ {
		var frameDt float64
		if i == gapAtFrame {
			frameDt = gapDuration
		} else {
			frameDt = dt
		}
		accumulatedTime += frameDt
		accumulatedDist += speed * frameDt

		pos := model.Vec3{
			fb.startPos[0] + accumulatedDist,
			fb.startPos[1] + 0.5*math.Sin(accumulatedTime*0.5),
			fb.startPos[2],
		}
		pos = clampToArena(pos)

		f := fb.baseFrame(i, pos, rotationFromVelocity(model.Vec3{speed, 0, 0}))
		f.Timestamp = 1.0 + accumulatedTime
		f.DeltaTime = frameDt
		f.LeftHandPosition = pos.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(deterministicJitter3(i, 130, 0.003))
		f.RightHandPosition = pos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(deterministicJitter3(i, 140, 0.003))
		f.LeftHandRotation = rotatedQuat(0.01 * math.Sin(float64(i)*0.7+1.0))
		f.RightHandRotation = rotatedQuat(0.01 * math.Cos(float64(i)*0.6+2.0))

		frames[i] = f
	}

	return frames
}

// InterpolationArtifact generates normal movement with 3-5 frames in the middle
// where position oscillates back and forth, creating apparent 200+ m/s speed spikes.
func (fb *FrameBuilder) InterpolationArtifact(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 5.0)
	dt := fb.dt()

	// Insert oscillation artifact in the middle (5 frames)
	midFrame := nFrames / 2
	artifactFrames := 5
	oscillationDistance := 15.0 // Large jump to create 200+ m/s apparent speed

	for j := 0; j < artifactFrames && (midFrame+j) < nFrames; j++ {
		idx := midFrame + j
		basePos := frames[idx].Position
		// Oscillate: even frames jump forward, odd frames jump back
		if j%2 == 0 {
			frames[idx].Position = clampToArena(basePos.Add(model.Vec3{oscillationDistance, 0, 0}))
		} else {
			frames[idx].Position = clampToArena(basePos.Add(model.Vec3{-oscillationDistance, 0, 0}))
		}
		// Speed spike: oscillationDistance / dt >> 200 m/s
		_ = dt
		// Update hand positions to follow body
		frames[idx].LeftHandPosition = frames[idx].Position.Add(model.Vec3{-0.3, 0.3, 0.2})
		frames[idx].RightHandPosition = frames[idx].Position.Add(model.Vec3{0.3, 0.3, -0.2})
	}

	return frames
}

// ===================================================================
// 3. High-Ping Scenarios
// ===================================================================

// HighPingPlayer generates normal gameplay with EstimatedPingMs set to avgPingMs
// (100-300ms). Positions have extra jitter proportional to ping, frame timing is
// slightly irregular (±10ms variation).
func (fb *FrameBuilder) HighPingPlayer(nFrames int, avgPingMs float64) []model.PlayerTelemetryFrame {
	// Use the builder's ping setting for base, but override with avgPingMs
	saved := fb.pingMs
	fb.pingMs = avgPingMs
	frames := fb.NormalMovingPlayer(nFrames, 5.0)
	fb.pingMs = saved

	dt := fb.dt()
	pingJitterScale := avgPingMs / 1000.0 * 0.5 // Higher ping = more position jitter

	for i := range frames {
		frames[i].EstimatedPingMs = avgPingMs

		// Extra position jitter proportional to ping
		pingJitter := deterministicJitter3(i, 200, pingJitterScale)
		frames[i].Position = clampToArena(frames[i].Position.Add(pingJitter))

		// Slightly irregular frame timing (±10ms)
		timeJitter := 0.01 * math.Sin(float64(i)*4.7+1.3)
		frames[i].DeltaTime = dt + timeJitter
		if frames[i].DeltaTime < 0.01 {
			frames[i].DeltaTime = 0.01
		}

		// Update hand positions with extra jitter
		frames[i].LeftHandPosition = frames[i].Position.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(deterministicJitter3(i, 210, pingJitterScale*0.5))
		frames[i].RightHandPosition = frames[i].Position.Add(model.Vec3{0.3, 0.3, -0.2}).Add(deterministicJitter3(i, 220, pingJitterScale*0.5))
	}

	return frames
}

// PingSpikeSequence generates normal gameplay with a sudden ping spike
// (50ms -> 400ms) at spikeAtFrame lasting 20 frames.
func (fb *FrameBuilder) PingSpikeSequence(nFrames int, spikeAtFrame int) []model.PlayerTelemetryFrame {
	saved := fb.pingMs
	fb.pingMs = 50.0
	frames := fb.NormalMovingPlayer(nFrames, 5.0)
	fb.pingMs = saved

	for i := range frames {
		if i >= spikeAtFrame && i < spikeAtFrame+20 {
			// Spike: 400ms ping
			frames[i].EstimatedPingMs = 400.0

			// During spike, positions lag behind actual movement
			lagFrames := 6 // ~400ms at 15fps
			sourceIdx := i - lagFrames
			if sourceIdx < 0 {
				sourceIdx = 0
			}
			frames[i].Position = frames[sourceIdx].Position
			frames[i].LeftHandPosition = frames[sourceIdx].LeftHandPosition
			frames[i].RightHandPosition = frames[sourceIdx].RightHandPosition

			// Add extra jitter during spike
			spikeJitter := deterministicJitter3(i, 300, 0.1)
			frames[i].Position = clampToArena(frames[i].Position.Add(spikeJitter))
		} else {
			frames[i].EstimatedPingMs = 50.0
		}
	}

	return frames
}

// ===================================================================
// 4. Extreme-But-Legit Scenarios
// ===================================================================

// EliteThrowSequence generates top-tier player throws at 15-17 m/s consistently
// (near cap but under). Release angles tight (8-15 degree spread).
// Target deviation 3-5 degrees. Should NOT trigger any detector.
// Elite players have fast hands matching their fast throws (hand speed >= releaseSpeed/2.5).
func (fb *FrameBuilder) EliteThrowSequence(nThrows int) []model.PlayerTelemetryFrame {
	var frames []model.PlayerTelemetryFrame
	dt := fb.dt()
	frameIdx := 0

	for throwNum := 0; throwNum < nThrows; throwNum++ {
		// Hold duration varies per throw: 0.8-1.5s
		holdDuration := 0.8 + 0.7*(0.5+0.5*math.Sin(float64(throwNum)*2.3))
		holdFrames := int(holdDuration / dt)
		windupFrames := 7

		// Elite speed: 15-17 m/s with some variation
		releaseSpeed := 15.0 + 2.0*(0.5+0.5*math.Sin(float64(throwNum)*1.1))

		// Tight release angles: 8-15 degrees with variation per throw
		releaseAngleDeg := 8.0 + 7.0*(0.5+0.5*math.Cos(float64(throwNum)*0.9)) +
			3.0*math.Sin(float64(throwNum)*3.7)
		releaseAngle := releaseAngleDeg * math.Pi / 180.0

		// Target deviation: 3-5 degrees
		targetDevDeg := 3.0 + 2.0*(0.5+0.5*math.Sin(float64(throwNum)*1.7))

		flightFrames := 15

		basePos := model.Vec3{
			fb.startPos[0] + float64(throwNum)*1.5,
			fb.startPos[1],
			fb.startPos[2],
		}
		basePos = clampToArena(basePos)

		throwDir := model.Vec3{math.Cos(releaseAngle), 0, math.Sin(releaseAngle)}.Normalized()

		// Goal position (for target deviation calculation)
		goalPos := model.Vec3{30, 0, 0}
		toGoal := goalPos.Sub(basePos).Normalized()
		_ = targetDevDeg

		// Elite hand speed: at least releaseSpeed/2.5 to keep ratio below 3.0
		handSpeedAtRelease := releaseSpeed / 2.0 // ratio = 2.0, well under 3.0

		// Wrist rotation varies per throw for signature variance
		wristAngleBase := 0.05 * float64(throwNum+1)

		// Hold phase
		for j := 0; j < holdFrames; j++ {
			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = true
			f.LeftHandPosition = basePos.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(deterministicJitter3(frameIdx, 400, 0.002))
			f.RightHandPosition = basePos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(deterministicJitter3(frameIdx, 410, 0.002))
			f.RightHandRotation = rotatedQuat(wristAngleBase + 0.01*math.Sin(float64(frameIdx)*0.7))
			f.LeftHandRotation = rotatedQuat(wristAngleBase*0.5 + 0.01*math.Cos(float64(frameIdx)*0.6))
			f.Disc = &model.DiscState{
				Position:    f.RightHandPosition.Add(model.Vec3{0.1, 0, 0}),
				Velocity:    model.Vec3{0, 0, 0},
				Speed:       0,
				PossessorID: fb.playerID,
				IsHeld:      true,
			}
			frames = append(frames, f)
			frameIdx++
		}

		// Wind-up phase: hand accelerates to handSpeedAtRelease
		// Hand moves along toGoal direction; we need the position delta per frame
		// to produce the correct measured hand speed at the pipeline level.
		// handSpeed = positionDelta / dt, so positionDelta = handSpeed * dt
		for j := 0; j < windupFrames; j++ {
			progress := float64(j+1) / float64(windupFrames)
			handSpeed := handSpeedAtRelease * progress
			// Position the hand so that the delta between consecutive frames = handSpeed * dt
			// cumulative distance along toGoal from the hold hand position
			cumDist := 0.0
			for k := 1; k <= j+1; k++ {
				p := float64(k) / float64(windupFrames)
				cumDist += handSpeedAtRelease * p * dt
			}
			handOffset := toGoal.Scale(cumDist)

			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = true
			// Hand starts from (0.3, 0.3, -0.2) relative to basePos (the hold position)
			handPos := basePos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(handOffset)
			f.RightHandPosition = handPos
			f.LeftHandPosition = basePos.Add(model.Vec3{-0.3, 0.3, 0.2})
			// Wrist rotation increases during wind-up
			wristAngle := wristAngleBase + progress*0.3
			f.RightHandRotation = axisAngleQuat(model.Vec3{0, 0, 1}, wristAngle)
			f.LeftHandRotation = rotatedQuat(wristAngleBase * 0.5)
			f.Disc = &model.DiscState{
				Position:    handPos.Add(throwDir.Scale(0.1)),
				Velocity:    throwDir.Scale(handSpeed),
				Speed:       handSpeed,
				PossessorID: fb.playerID,
				IsHeld:      true,
			}
			frames = append(frames, f)
			frameIdx++
		}

		// Release frame: hand continues moving at handSpeedAtRelease
		// Position the hand one more step forward from last wind-up position
		{
			cumDist := 0.0
			for k := 1; k <= windupFrames; k++ {
				p := float64(k) / float64(windupFrames)
				cumDist += handSpeedAtRelease * p * dt
			}
			cumDist += handSpeedAtRelease * dt // one more frame at full hand speed
			handOffset := toGoal.Scale(cumDist)
			releaseHandPos := basePos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(handOffset)

			releaseVel := throwDir.Scale(releaseSpeed)
			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = false
			f.RightHandPosition = releaseHandPos
			f.RightHandRotation = axisAngleQuat(model.Vec3{0, 0, 1}, wristAngleBase+0.3)
			f.LeftHandRotation = rotatedQuat(wristAngleBase * 0.5)
			handToDisc := 0.15 + 0.05*math.Sin(float64(throwNum)*1.9)
			discPos := releaseHandPos.Add(throwDir.Scale(handToDisc))
			rp := releaseHandPos
			rv := releaseVel
			f.Disc = &model.DiscState{
				Position:            discPos,
				Velocity:            releaseVel,
				Speed:               releaseSpeed,
				IsHeld:              false,
				FramesSinceRelease:  0,
				ReleasePosition:     &rp,
				ReleaseVelocity:     &rv,
				DistanceFromThrower: discPos.Distance(basePos),
			}
			frames = append(frames, f)
			frameIdx++
		}

		// Flight phase
		cumDist := 0.0
		for k := 1; k <= windupFrames; k++ {
			p := float64(k) / float64(windupFrames)
			cumDist += handSpeedAtRelease * p * dt
		}
		cumDist += handSpeedAtRelease * dt
		releaseHandPos := basePos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(toGoal.Scale(cumDist))
		releaseVel := throwDir.Scale(releaseSpeed)
		for j := 1; j <= flightFrames; j++ {
			discFlightPos := releaseHandPos.Add(releaseVel.Scale(float64(j) * dt))
			discFlightPos = clampToArena(discFlightPos)
			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = false
			f.RightHandRotation = rotatedQuat(wristAngleBase)
			f.LeftHandRotation = rotatedQuat(wristAngleBase * 0.5)
			pv := releaseVel
			rp := releaseHandPos
			rv := releaseVel
			f.Disc = &model.DiscState{
				Position:            discFlightPos,
				Velocity:            releaseVel,
				Speed:               releaseSpeed,
				PreviousVelocity:    &pv,
				IsHeld:              false,
				FramesSinceRelease:  j,
				ReleasePosition:     &rp,
				ReleaseVelocity:     &rv,
				DistanceFromThrower: discFlightPos.Distance(basePos),
			}
			frames = append(frames, f)
			frameIdx++
		}

		// Caught frame
		{
			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = false
			f.RightHandRotation = rotatedQuat(wristAngleBase)
			f.LeftHandRotation = rotatedQuat(wristAngleBase * 0.5)
			f.Disc = &model.DiscState{
				Position:    releaseHandPos.Add(releaseVel.Scale(float64(flightFrames+1) * dt)),
				Velocity:    model.Vec3{0, 0, 0},
				Speed:       0,
				PossessorID: "other_player",
				IsHeld:      true,
			}
			frames = append(frames, f)
			frameIdx++
		}
	}

	return frames
}

// RegrabStackingBurst generates frames where the player reaches 45-50 m/s for 3-5 frames
// via regrab stacking, then decelerates. Speed median over 30 frames stays below 55.
// Must NOT trigger MOV_001.
func (fb *FrameBuilder) RegrabStackingBurst(nFrames int) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	dt := fb.dt()
	pos := fb.startPos

	burstStart := nFrames / 3
	burstFrames := 4 // 3-5 frames at high speed
	burstEnd := burstStart + burstFrames

	for i := 0; i < nFrames; i++ {
		var speed float64

		if i >= burstStart && i < burstEnd {
			// Burst: 45-50 m/s
			burstProgress := float64(i-burstStart) / float64(burstFrames)
			speed = 45.0 + 5.0*math.Sin(burstProgress*math.Pi)
		} else if i >= burstEnd && i < burstEnd+30 {
			// Deceleration
			decayProgress := float64(i-burstEnd) / 30.0
			speed = 45.0 * math.Exp(-3.0*decayProgress)
			if speed < 5.0 {
				speed = 5.0
			}
		} else {
			// Normal speed
			speed = 5.0 + 2.0*math.Sin(float64(i)*0.3)
		}

		pos = clampToArena(pos.Add(model.Vec3{speed * dt, 0, 0}))
		rot := rotationFromVelocity(model.Vec3{speed, 0, 0})

		f := fb.baseFrame(i, pos, rot)
		f.LeftHandPosition = pos.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(deterministicJitter3(i, 500, 0.004))
		f.RightHandPosition = pos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(deterministicJitter3(i, 510, 0.004))

		// Mark boosting during burst
		if i >= burstStart && i < burstEnd {
			f.IsBoosting = true
		}

		frames[i] = f
	}

	return frames
}

// FastWristFlick generates frames with a wrist flick throw where wrist angular rate
// hits 22-25 rad/s for 1-2 frames (below 30 rad/s threshold). Must NOT trigger BIO_001.
func (fb *FrameBuilder) FastWristFlick(nFrames int) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	dt := fb.dt()
	pos := fb.startPos

	flickFrame := nFrames / 2

	for i := 0; i < nFrames; i++ {
		f := fb.baseFrame(i, pos, model.QuatIdentity())
		f.LeftHandPosition = pos.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(deterministicJitter3(i, 600, 0.002))
		f.RightHandPosition = pos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(deterministicJitter3(i, 610, 0.002))

		// Normal hand rotation most of the time
		baseAngle := 0.02 * math.Sin(float64(i)*0.5)
		f.RightHandRotation = axisAngleQuat(model.Vec3{0, 0, 1}, baseAngle)
		f.LeftHandRotation = axisAngleQuat(model.Vec3{1, 0, 0}, baseAngle*0.5)

		// Wrist flick: 22-25 rad/s for 1-2 frames
		if i == flickFrame || i == flickFrame+1 {
			// Angular rate = angle_change / dt
			// Want 23 rad/s -> angle_change = 23 * dt
			targetAngularRate := 23.0 + 2.0*math.Sin(float64(i)*1.1)
			angleChange := targetAngularRate * dt
			f.RightHandRotation = axisAngleQuat(model.Vec3{0, 0, 1}, baseAngle+angleChange)
		}

		frames[i] = f
	}

	_ = dt
	return frames
}

// SteadyHandPlayer generates frames with unusually steady hands. Position variance
// 0.0001 (above 0.00001 threshold). Must NOT trigger BIO_003.
func (fb *FrameBuilder) SteadyHandPlayer(nFrames int) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	pos := fb.startPos

	// Very small but nonzero jitter to keep variance at ~0.0001
	// Variance = mean(sum of squared deviations)
	// For sinusoidal jitter of amplitude A: variance ~ A^2 / 2
	// We want ~0.0001 -> A ~ sqrt(0.0002) ~ 0.014
	jitterAmp := 0.014

	for i := 0; i < nFrames; i++ {
		t := float64(i)

		f := fb.baseFrame(i, pos, model.QuatIdentity())

		// Steady but not perfectly still hands
		leftJitter := model.Vec3{
			jitterAmp * math.Sin(t*0.7),
			jitterAmp * 0.5 * math.Cos(t*0.5),
			jitterAmp * 0.3 * math.Sin(t*0.9+1.0),
		}
		rightJitter := model.Vec3{
			jitterAmp * math.Cos(t*0.6+0.3),
			jitterAmp * 0.5 * math.Sin(t*0.4+0.8),
			jitterAmp * 0.3 * math.Cos(t*0.8+2.0),
		}

		f.LeftHandPosition = pos.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(leftJitter)
		f.RightHandPosition = pos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(rightJitter)

		// Small rotation variance
		f.LeftHandRotation = axisAngleQuat(model.Vec3{1, 0, 0}, 0.01*math.Sin(t*0.3))
		f.RightHandRotation = axisAngleQuat(model.Vec3{0, 0, 1}, 0.01*math.Cos(t*0.4))

		frames[i] = f
	}

	return frames
}

// ===================================================================
// 5. Cheat Scenarios
// ===================================================================

// SpeedHackFrames generates frames with the player sustained at hackSpeed (60-100 m/s)
// for all frames. Clear MOV_001 trigger.
func (fb *FrameBuilder) SpeedHackFrames(nFrames int, hackSpeed float64) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	dt := fb.dt()
	pos := fb.startPos

	for i := 0; i < nFrames; i++ {
		pos = clampToArena(pos.Add(model.Vec3{hackSpeed * dt, 0, 0}))
		rot := rotationFromVelocity(model.Vec3{hackSpeed, 0, 0})

		f := fb.baseFrame(i, pos, rot)
		f.LeftHandPosition = pos.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(deterministicJitter3(i, 700, 0.003))
		f.RightHandPosition = pos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(deterministicJitter3(i, 710, 0.003))
		frames[i] = f
	}

	return frames
}

// TeleportCheat generates normal movement then an instant position jump of distance
// meters at teleportAtFrame with no velocity change. Clear MOV_002 trigger.
func (fb *FrameBuilder) TeleportCheat(nFrames int, teleportAtFrame int, distance float64) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 5.0)

	if teleportAtFrame > 0 && teleportAtFrame < nFrames {
		// Instant position jump
		jumpVec := model.Vec3{distance, 0, 0}
		for i := teleportAtFrame; i < nFrames; i++ {
			frames[i].Position = clampToArena(frames[i].Position.Add(jumpVec))
			frames[i].LeftHandPosition = frames[i].Position.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(deterministicJitter3(i, 720, 0.003))
			frames[i].RightHandPosition = frames[i].Position.Add(model.Vec3{0.3, 0.3, -0.2}).Add(deterministicJitter3(i, 730, 0.003))
		}
	}

	return frames
}

// AimbotThrows generates throws with clear cheat signatures:
// - Release speed 22+ m/s (over cap) -> THROW_001
// - Target deviation < 1 degree consistently -> THROW_005
// - Release angle > 60 degrees -> THROW_003
// - Near-zero variance across throws -> THROW_004
func (fb *FrameBuilder) AimbotThrows(nThrows int) []model.PlayerTelemetryFrame {
	var frames []model.PlayerTelemetryFrame
	dt := fb.dt()
	frameIdx := 0

	goalPos := model.Vec3{30, 0, 0}

	for throwNum := 0; throwNum < nThrows; throwNum++ {
		holdFrames := int(1.0 / dt)
		windupFrames := 6

		// Cheat: over cap speed, consistent across throws (near-zero variance)
		releaseSpeed := 22.0 + 0.01*math.Sin(float64(throwNum)*0.1) // tiny variance

		basePos := model.Vec3{
			fb.startPos[0] + float64(throwNum)*1.5,
			fb.startPos[1],
			fb.startPos[2],
		}
		basePos = clampToArena(basePos)

		// Target direction (towards goal, < 1 degree deviation)
		toGoal := goalPos.Sub(basePos).Normalized()

		// Cheat: release angle > 60 degrees (hand going one way, disc another)
		handDir := model.Vec3{
			math.Cos(70.0 * math.Pi / 180.0),
			0,
			math.Sin(70.0 * math.Pi / 180.0),
		}.Normalized()

		// Hold phase
		for j := 0; j < holdFrames; j++ {
			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = true
			f.RightHandPosition = basePos.Add(model.Vec3{0.3, 0.3, -0.2})
			f.LeftHandPosition = basePos.Add(model.Vec3{-0.3, 0.3, 0.2})
			f.Disc = &model.DiscState{
				Position:    f.RightHandPosition.Add(model.Vec3{0.1, 0, 0}),
				Velocity:    model.Vec3{0, 0, 0},
				Speed:       0,
				PossessorID: fb.playerID,
				IsHeld:      true,
			}
			frames = append(frames, f)
			frameIdx++
		}

		// Wind-up (hand moves in handDir, disc will go toGoal -- mismatch)
		for j := 0; j < windupFrames; j++ {
			progress := float64(j+1) / float64(windupFrames)
			handOffset := handDir.Scale(0.3 + progress*0.3)

			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = true
			handPos := basePos.Add(model.Vec3{0, 0.3, 0}).Add(handOffset)
			f.RightHandPosition = handPos
			f.LeftHandPosition = basePos.Add(model.Vec3{-0.3, 0.3, 0.2})
			f.Disc = &model.DiscState{
				Position:    handPos.Add(handDir.Scale(0.1)),
				Velocity:    handDir.Scale(releaseSpeed * progress * 0.5),
				Speed:       releaseSpeed * progress * 0.5,
				PossessorID: fb.playerID,
				IsHeld:      true,
			}
			frames = append(frames, f)
			frameIdx++
		}

		// Release frame -- disc goes toward goal (not hand direction)
		{
			releasePos := basePos.Add(model.Vec3{0, 0.3, 0}).Add(handDir.Scale(0.6))
			releaseVel := toGoal.Scale(releaseSpeed) // Disc goes toward goal
			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = false
			f.RightHandPosition = releasePos
			discPos := releasePos.Add(toGoal.Scale(0.15))
			rp := releasePos
			rv := releaseVel
			f.Disc = &model.DiscState{
				Position:            discPos,
				Velocity:            releaseVel,
				Speed:               releaseSpeed,
				IsHeld:              false,
				FramesSinceRelease:  0,
				ReleasePosition:     &rp,
				ReleaseVelocity:     &rv,
				DistanceFromThrower: discPos.Distance(basePos),
			}
			frames = append(frames, f)
			frameIdx++
		}

		// Flight (disc moves perfectly toward goal with minimal deviation)
		releasePos := basePos.Add(model.Vec3{0, 0.3, 0}).Add(handDir.Scale(0.6))
		releaseVel := toGoal.Scale(releaseSpeed)
		flightFrames := 15
		for j := 1; j <= flightFrames; j++ {
			discFlightPos := releasePos.Add(releaseVel.Scale(float64(j) * dt))
			discFlightPos = clampToArena(discFlightPos)
			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = false
			pv := releaseVel
			rp := releasePos
			rv := releaseVel
			f.Disc = &model.DiscState{
				Position:                    discFlightPos,
				Velocity:                    releaseVel,
				Speed:                       releaseSpeed,
				PreviousVelocity:            &pv,
				IsHeld:                      false,
				FramesSinceRelease:          j,
				ReleasePosition:             &rp,
				ReleaseVelocity:             &rv,
				DistanceFromThrower:         discFlightPos.Distance(basePos),
				TrajectoryAngleChange:       0.0, // Perfect straight line
			}
			frames = append(frames, f)
			frameIdx++
		}
	}

	return frames
}

// MagnetismCheat generates throws where the disc trajectory bends 5+ degrees per frame
// for 5+ frames post-release toward a target. Clear THROW_006 trigger.
func (fb *FrameBuilder) MagnetismCheat(nThrows int) []model.PlayerTelemetryFrame {
	var frames []model.PlayerTelemetryFrame
	dt := fb.dt()
	frameIdx := 0

	goalPos := model.Vec3{30, 0, 0}

	for throwNum := 0; throwNum < nThrows; throwNum++ {
		holdFrames := int(0.8 / dt)
		releaseSpeed := 14.0

		basePos := model.Vec3{
			fb.startPos[0] + float64(throwNum)*2.0,
			fb.startPos[1],
			fb.startPos[2],
		}
		basePos = clampToArena(basePos)

		// Throw direction: off-target initially (30 degrees off from goal)
		offAngle := 30.0 * math.Pi / 180.0
		throwDir := model.Vec3{math.Cos(offAngle), 0, math.Sin(offAngle)}.Normalized()
		toGoal := goalPos.Sub(basePos).Normalized()

		// Hold
		for j := 0; j < holdFrames; j++ {
			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = true
			f.RightHandPosition = basePos.Add(model.Vec3{0.3, 0.3, -0.2})
			f.LeftHandPosition = basePos.Add(model.Vec3{-0.3, 0.3, 0.2})
			f.Disc = &model.DiscState{
				Position:    f.RightHandPosition.Add(model.Vec3{0.1, 0, 0}),
				Velocity:    model.Vec3{0, 0, 0},
				Speed:       0,
				PossessorID: fb.playerID,
				IsHeld:      true,
			}
			frames = append(frames, f)
			frameIdx++
		}

		// Release frame
		releasePos := basePos.Add(model.Vec3{0, 0.3, 0}).Add(throwDir.Scale(0.5))
		releaseVel := throwDir.Scale(releaseSpeed)
		{
			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = false
			f.RightHandPosition = releasePos
			discPos := releasePos.Add(throwDir.Scale(0.15))
			rp := releasePos
			rv := releaseVel
			f.Disc = &model.DiscState{
				Position:            discPos,
				Velocity:            releaseVel,
				Speed:               releaseSpeed,
				IsHeld:              false,
				FramesSinceRelease:  0,
				ReleasePosition:     &rp,
				ReleaseVelocity:     &rv,
				DistanceFromThrower: discPos.Distance(basePos),
			}
			frames = append(frames, f)
			frameIdx++
		}

		// Flight with magnetism: disc bends toward goal at 5+ degrees per frame
		flightFrames := 20
		currentDir := throwDir
		discPos := releasePos.Add(throwDir.Scale(0.15))

		for j := 1; j <= flightFrames; j++ {
			// Bend direction toward goal: 6 degrees per frame for first 8 frames
			if j <= 8 {
				bendAngle := 6.0 * math.Pi / 180.0 // 6 degrees per frame
				// Rotate currentDir toward toGoal
				cross := currentDir.Cross(toGoal)
				if cross.Magnitude() > 1e-6 {
					rotAxis := cross.Normalized()
					currentDir = rotateVec3(currentDir, rotAxis, bendAngle)
				}
			}

			prevVel := currentDir.Scale(releaseSpeed)
			discPos = discPos.Add(currentDir.Scale(releaseSpeed * dt))
			discPos = clampToArena(discPos)
			newVel := currentDir.Scale(releaseSpeed)

			// Compute trajectory angle change
			angleChange := 0.0
			if j <= 8 {
				angleChange = 6.0 // degrees
			}

			f := fb.baseFrame(frameIdx, basePos, model.QuatIdentity())
			f.HasPossession = false
			pv := prevVel
			rp := releasePos
			rv := releaseVel
			f.Disc = &model.DiscState{
				Position:              discPos,
				Velocity:              newVel,
				Speed:                 releaseSpeed,
				PreviousVelocity:      &pv,
				IsHeld:                false,
				FramesSinceRelease:    j,
				ReleasePosition:       &rp,
				ReleaseVelocity:       &rv,
				TrajectoryAngleChange: angleChange,
				DistanceFromThrower:   discPos.Distance(basePos),
			}
			frames = append(frames, f)
			frameIdx++
		}
	}

	return frames
}

// rotateVec3 rotates a vector around an axis by angle radians.
func rotateVec3(v model.Vec3, axis model.Vec3, angle float64) model.Vec3 {
	q := axisAngleQuat(axis, angle)
	// Rotate v by quaternion: q * v * q^-1
	vq := model.Quat{v[0], v[1], v[2], 0}
	rotated := q.Multiply(vq).Multiply(q.Conjugate())
	return model.Vec3{rotated[0], rotated[1], rotated[2]}
}

// StunBypass generates frames where the player is stunned at frame 50 and recovers
// at frame 55 (5 frames = ~0.33s, well under the 20-frame minimum). Clear STATE_002 trigger.
func (fb *FrameBuilder) StunBypass(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 5.0)

	for i := range frames {
		if i >= 50 && i < 55 {
			frames[i].IsStunned = true
		} else {
			frames[i].IsStunned = false
		}
	}

	return frames
}

// GodMode generates frames where the player has IsImmune=true for 500 continuous frames
// during active play. Clear STATE_004 trigger.
func (fb *FrameBuilder) GodMode(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 5.0)

	// Set immune for 500 frames starting at frame 10
	immuneStart := 10
	immuneEnd := immuneStart + 500
	if immuneEnd > nFrames {
		immuneEnd = nFrames
	}

	for i := immuneStart; i < immuneEnd; i++ {
		frames[i].IsImmune = true
	}

	return frames
}

// ScoreManipulation generates frames with an impossible score delta of 7.
// Clear STATE_006 trigger.
func (fb *FrameBuilder) ScoreManipulation(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 5.0)

	// Normal scores for first half
	for i := 0; i < nFrames; i++ {
		if i < nFrames/2 {
			frames[i].BlueScore = 3
			frames[i].OrangeScore = 2
			frames[i].Goals = 1
		} else {
			// Sudden impossible score jump: +7 in one frame
			frames[i].BlueScore = 10
			frames[i].OrangeScore = 2
			frames[i].Goals = 8
		}
	}

	return frames
}

// InfiniteBoost generates many boost activations with short recharge gaps,
// producing multiple violation sequences. Clear MOV_005 trigger.
// Pattern: groups of 7 consecutive boosts separated by 12-frame pauses, repeated many times.
func (fb *FrameBuilder) InfiniteBoost(nFrames int) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	dt := fb.dt()
	pos := fb.startPos

	// Each cycle: 7 boost frames + 12 pause frames = 19 frames per cycle
	// maxConsecutive=5, so 7 consecutive exceeds it.
	// rechargePauseFrames=10, so 12-frame pause resets consecutive counter and counts sequence.
	// minSequences=2, so after 2+ completed cycles the detector should fire.
	boostFramesPerCycle := 7
	pauseFramesPerCycle := 12
	cycleLen := boostFramesPerCycle + pauseFramesPerCycle

	boostFrameSet := make(map[int]bool)
	for i := 0; i < nFrames; i++ {
		posInCycle := i % cycleLen
		if posInCycle < boostFramesPerCycle {
			boostFrameSet[i] = true
		}
	}

	for i := 0; i < nFrames; i++ {
		speed := 8.0
		if boostFrameSet[i] {
			speed = 12.0
		}
		pos = clampToArena(pos.Add(model.Vec3{speed * dt, 0, 0}))
		rot := rotationFromVelocity(model.Vec3{speed, 0, 0})

		f := fb.baseFrame(i, pos, rot)
		f.IsBoosting = boostFrameSet[i]
		f.LeftHandPosition = pos.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(deterministicJitter3(i, 800, 0.003))
		f.RightHandPosition = pos.Add(model.Vec3{0.3, 0.3, -0.2}).Add(deterministicJitter3(i, 810, 0.003))
		frames[i] = f
	}

	return frames
}

// BotBehavior generates frames with zero hand jitter (exact same position every frame),
// zero aim wobble (exact same rotation), and frame-perfect throw timing (exactly every
// 60 frames). Should trigger BIO_003, BIO_004, PAT_001.
func (fb *FrameBuilder) BotBehavior(nFrames int) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	dt := fb.dt()
	pos := fb.startPos

	// Fixed hand positions - zero jitter
	leftHandPos := pos.Add(model.Vec3{-0.3, 0.3, 0.2})
	rightHandPos := pos.Add(model.Vec3{0.3, 0.3, -0.2})

	// Fixed rotations - zero wobble
	fixedRot := model.QuatIdentity()
	fixedHandRot := model.QuatIdentity()

	throwDir := model.Vec3{1, 0, 0}
	throwSpeed := 14.0

	for i := 0; i < nFrames; i++ {
		f := fb.baseFrame(i, pos, fixedRot)
		f.LeftHandPosition = leftHandPos
		f.RightHandPosition = rightHandPos
		f.LeftHandRotation = fixedHandRot
		f.RightHandRotation = fixedHandRot

		// Frame-perfect throw timing: exactly every 60 frames
		if i > 0 && i%60 == 0 {
			// Release frame
			f.HasPossession = false
			releaseVel := throwDir.Scale(throwSpeed)
			discPos := rightHandPos.Add(throwDir.Scale(0.15))
			rp := rightHandPos
			rv := releaseVel
			f.Disc = &model.DiscState{
				Position:            discPos,
				Velocity:            releaseVel,
				Speed:               throwSpeed,
				IsHeld:              false,
				FramesSinceRelease:  0,
				ReleasePosition:     &rp,
				ReleaseVelocity:     &rv,
				DistanceFromThrower: discPos.Distance(pos),
			}
		} else if i%60 >= 1 && i%60 < 20 {
			// Disc in flight
			framesSinceRelease := i%60
			discFlightPos := rightHandPos.Add(throwDir.Scale(0.15 + throwSpeed*float64(framesSinceRelease)*dt))
			discFlightPos = clampToArena(discFlightPos)
			releaseVel := throwDir.Scale(throwSpeed)
			pv := releaseVel
			rp := rightHandPos
			rv := releaseVel
			f.HasPossession = false
			f.Disc = &model.DiscState{
				Position:            discFlightPos,
				Velocity:            releaseVel,
				Speed:               throwSpeed,
				PreviousVelocity:    &pv,
				IsHeld:              false,
				FramesSinceRelease:  framesSinceRelease,
				ReleasePosition:     &rp,
				ReleaseVelocity:     &rv,
				DistanceFromThrower: discFlightPos.Distance(pos),
			}
		} else {
			// Holding disc
			f.HasPossession = true
			f.Disc = &model.DiscState{
				Position:    rightHandPos.Add(model.Vec3{0.1, 0, 0}),
				Velocity:    model.Vec3{0, 0, 0},
				Speed:       0,
				PossessorID: fb.playerID,
				IsHeld:      true,
			}
		}

		frames[i] = f
	}

	return frames
}

// ExtendedReach generates frames with hand-to-head distance sustained at 1.8m for
// 60 frames. Clear PAT_005 trigger.
func (fb *FrameBuilder) ExtendedReach(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 5.0)

	// Starting at frame 20, sustain 1.8m reach for 60 frames
	reachStart := 20
	reachEnd := reachStart + 60
	if reachEnd > nFrames {
		reachEnd = nFrames
	}

	for i := reachStart; i < reachEnd; i++ {
		pos := frames[i].Position
		// Place hands 1.8m from head position
		frames[i].LeftHandPosition = pos.Add(model.Vec3{-1.8, 0, 0})
		frames[i].RightHandPosition = pos.Add(model.Vec3{1.8, 0, 0})
	}

	return frames
}
