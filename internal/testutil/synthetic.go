package testutil

import (
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// DefaultTickRate is the bridge's nominal /session poll rate (frames per second).
const DefaultTickRate = 15.0

// Arena geometry used by every generator. The validator accepts
// |X| <= ArenaWidth/2+5 = 12.5, |Y| <= 12.5 and |Z| <= ArenaLength/2+5 = 82
// (see pipeline/validator.go); real players stay within X +-5, Y -4..7 and
// Z +-77 (model.DefaultPhysics). Generators keep well inside both.
const (
	arenaHalfX = 6.0
	arenaHalfY = 6.0
	arenaHalfZ = 75.0
	// moveHalfZ bounds the long-axis path of moving players.
	moveHalfZ = 60.0
	// headHeight is the resting body/head height above the floor.
	headHeight = 1.6
)

// Hand geometry: a resting hand sits ~0.47 m from the head (below the
// PAT_005 1.6 m playspace limit) with human-scale positional jitter and
// aim wobble so BIO_003 (variance >= 1e-5 m^2) and BIO_004 (variance >=
// 5e-5 rad^2) see a human. Jitter is a product of two sinusoids
// (deterministicJitter), whose variance per axis is amp^2/4.
var (
	leftHandOffset  = model.Vec3{-0.3, 0.3, 0.2}
	rightHandOffset = model.Vec3{0.3, 0.3, -0.2}
)

const (
	humanHandJitter  = 0.01 // m; 3 axes -> total variance ~7.5e-5 m^2
	humanWobbleAngle = 0.03 // rad; rotation variance ~4.5e-4 rad^2
)

// FrameBuilder constructs deterministic telemetry frame sequences for ONE
// player. Every scenario documents the production-threshold behaviour it is
// built for; the assertions live in tests/.
type FrameBuilder struct {
	playerID  string
	team      string
	tickRate  float64 // fps, default 15
	startPos  model.Vec3
	gamePhase string
	pingMs    float64
}

// NewFrameBuilder creates a new FrameBuilder for the given player.
func NewFrameBuilder(playerID string) *FrameBuilder {
	return &FrameBuilder{
		playerID:  playerID,
		tickRate:  DefaultTickRate,
		startPos:  model.Vec3{2, headHeight, 0},
		gamePhase: "playing",
		pingMs:    20.0,
	}
}

// WithTickRate sets the tick rate (frames per second).
func (fb *FrameBuilder) WithTickRate(fps float64) *FrameBuilder {
	if fps > 0 {
		fb.tickRate = fps
	}
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

// WithTeam stamps PlayerTelemetryFrame.Team ("blue"/"orange") on every frame,
// as the adapter and bridge do.
func (fb *FrameBuilder) WithTeam(team string) *FrameBuilder {
	fb.team = team
	return fb
}

// After returns a copy of the builder whose start position is where the
// given frames ended, so a segment built from it continues the previous
// one without a jump (see Concat).
func (fb *FrameBuilder) After(prev []model.PlayerTelemetryFrame) *FrameBuilder {
	c := *fb
	if len(prev) > 0 {
		c.startPos = prev[len(prev)-1].Position
	}
	return &c
}

// PlayerID returns the builder's player id.
func (fb *FrameBuilder) PlayerID() string { return fb.playerID }

// TickRate returns the builder's tick rate.
func (fb *FrameBuilder) TickRate() float64 { return fb.tickRate }

// dt returns the time delta per frame.
func (fb *FrameBuilder) dt() float64 {
	return 1.0 / fb.tickRate
}

// baseFrame creates a valid frame at index with human hands, an idle disc and
// the producer conventions: Timestamp = index/tickRate (seconds since the
// first sample) and DeltaTime = 0 on the first frame (unknown), else the
// real spacing.
func (fb *FrameBuilder) baseFrame(index int, pos model.Vec3, rot model.Quat) model.PlayerTelemetryFrame {
	// Synthetic profiles deliberately specify both boosting and non-boosting
	// states; raw Echo inputs do not supply this presence claim.
	boostObserved := true
	dt := fb.dt()
	deltaTime := dt
	if index == 0 {
		deltaTime = 0
	}
	f := model.PlayerTelemetryFrame{
		// This generator deliberately models one described observational feed,
		// not an attested engine. Missing-source tests explicitly clear this.
		Observation:     &model.ObservationContext{Source: "synthetic", SourceID: "frame-builder", Authority: "client_reported", TimeBasis: "fixture", SessionID: "synthetic-session", FrameIndex: index, Timestamp: float64(index) * dt},
		PlayerID:        fb.playerID,
		IsBoostingKnown: &boostObserved,
		Team:            fb.team,
		FrameIndex:      index,
		Timestamp:       float64(index) * dt,
		DeltaTime:       deltaTime,
		Position:        pos,
		Rotation:        rot,
		GamePhase:       fb.gamePhase,
		Disc: &model.DiscState{
			Position:   model.Vec3{0, 2, 0},
			Velocity:   model.Vec3{0, 0, 0},
			Attachment: &model.DiscAttachment{State: "free"},
		},
		EstimatedPingMs: fb.pingMs,
	}
	fb.humanHands(&f, index)
	return f
}

// humanHands places both hands at their resting offsets with human jitter
// and aim wobble.
func (fb *FrameBuilder) humanHands(f *model.PlayerTelemetryFrame, index int) {
	f.LeftHandPosition = f.Position.Add(leftHandOffset).Add(deterministicJitter3(index, 11, humanHandJitter))
	f.RightHandPosition = f.Position.Add(rightHandOffset).Add(deterministicJitter3(index, 17, humanHandJitter))
	f.LeftHandRotation = humanWobble(index, 1.0)
	f.RightHandRotation = humanWobble(index, 5.0)
	leftObserved, rightObserved := true, true
	f.LeftHandRotationValid, f.RightHandRotationValid = &leftObserved, &rightObserved
}

// ---------- Helper functions ----------

// humanWobble returns a small, frame-varying hand rotation around two axes.
func humanWobble(index int, seed float64) model.Quat {
	t := float64(index)
	ax := humanWobbleAngle * math.Sin(t*0.73+seed)
	az := humanWobbleAngle * 0.7 * math.Cos(t*1.13+seed*2)
	return axisAngleQuat(model.Vec3{1, 0, 0}, ax).Multiply(axisAngleQuat(model.Vec3{0, 0, 1}, az)).Normalize()
}

// axisAngleQuat creates a quaternion from an axis and angle (radians).
func axisAngleQuat(axis model.Vec3, angle float64) model.Quat {
	axis = axis.Normalized()
	if axis.IsZero() {
		return model.QuatIdentity()
	}
	half := angle / 2.0
	s := math.Sin(half)
	return model.Quat{axis[0] * s, axis[1] * s, axis[2] * s, math.Cos(half)}.Normalize()
}

// deterministicJitter returns a deterministic pseudo-jitter value based on
// frame index and a seed (product of two sinusoids: variance amp^2/4).
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

// ClampToArena keeps a position inside the generators' arena envelope
// (|X|,|Y| <= 6, |Z| <= 75), which is inside the validator bounds.
func ClampToArena(pos model.Vec3) model.Vec3 {
	return model.Vec3{
		model.Clamp(pos[0], -arenaHalfX, arenaHalfX),
		model.Clamp(pos[1], -arenaHalfY, arenaHalfY),
		model.Clamp(pos[2], -arenaHalfZ, arenaHalfZ),
	}
}

// rotationFromVelocity creates a quaternion that "faces" the velocity direction.
func rotationFromVelocity(vel model.Vec3) model.Quat {
	if vel.Magnitude() < 1e-6 {
		return model.QuatIdentity()
	}
	dir := vel.Normalized()
	yaw := math.Atan2(dir[0], dir[2]) // yaw from +Z toward +X
	return axisAngleQuat(model.Vec3{0, 1, 0}, yaw)
}

// rotateVec3 rotates a vector around an axis by angle radians.
func rotateVec3(v model.Vec3, axis model.Vec3, angle float64) model.Vec3 {
	if axis.Magnitude() < 1e-9 {
		return v
	}
	return v.Rotate(axisAngleQuat(axis, angle))
}

// zMover integrates a player's long-axis (Z) motion at a nominal speed,
// bouncing between -moveHalfZ and +moveHalfZ. Reversals are ramped over
// turnFrames frames THROUGH zero speed so a legitimate bounce never looks
// like an instantaneous reversal (MOV_003 needs >= min_speed on both sides
// of the flip) and per-frame displacement stays continuous (MOV_002).
type zMover struct {
	z, dir     float64
	speed      float64
	dt         float64
	turnFrames int
	turnIdx    int // -1 when not turning
}

func newZMover(z0, speed, dt float64) *zMover {
	return &zMover{z: z0, dir: 1, speed: speed, dt: dt, turnFrames: 9, turnIdx: -1}
}

// step advances one frame at speed*scale and returns the new Z and the
// signed Z velocity used for this frame.
func (m *zMover) step(scale float64) (z, vz float64) {
	v := m.speed * scale * m.dir
	if m.turnIdx < 0 && ((m.dir > 0 && m.z+v*m.dt > moveHalfZ) || (m.dir < 0 && m.z+v*m.dt < -moveHalfZ)) {
		m.turnIdx = 0
	}
	if m.turnIdx >= 0 {
		// Linear ramp from +v to -v over turnFrames (odd, so the middle
		// frame is exactly zero).
		frac := 1.0 - 2.0*float64(m.turnIdx)/float64(m.turnFrames-1)
		v = m.speed * scale * m.dir * frac
		m.turnIdx++
		if m.turnIdx >= m.turnFrames {
			m.turnIdx = -1
			m.dir = -m.dir
		}
	}
	m.z += v * m.dt
	return m.z, v
}

// ===================================================================
// 1. Normal Gameplay Scenarios
// ===================================================================

// NormalIdlePlayer generates frames of a player floating still with natural
// hand jitter and aim wobble. Speed 0 keeps BIO_003/BIO_004 inactive.
func (fb *FrameBuilder) NormalIdlePlayer(nFrames int) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	for i := 0; i < nFrames; i++ {
		t := float64(i)
		headWobble := model.Vec3{
			0.0005 * math.Sin(t*0.5),
			0.0003 * math.Cos(t*0.7),
			0.0004 * math.Sin(t*0.3+1.0),
		}
		pos := ClampToArena(fb.startPos.Add(headWobble))
		rot := axisAngleQuat(model.Vec3{0, 1, 0}, 0.001*math.Sin(t*0.2))
		frames[i] = fb.baseFrame(i, pos, rot)
	}
	return frames
}

// NormalMovingPlayer generates a player flying along the long (Z) axis at
// avgSpeed (+-20 % modulation), bouncing smoothly between the ends, with a
// gentle X/Y weave. Hands follow the body with human jitter. Valid for any
// length: the path never leaves the arena.
func (fb *FrameBuilder) NormalMovingPlayer(nFrames int, avgSpeed float64) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	mover := newZMover(fb.startPos[2], avgSpeed, fb.dt())
	for i := 0; i < nFrames; i++ {
		scale := 1.0 + 0.2*math.Sin(float64(i)*0.3)
		z, vz := mover.step(scale)
		pos := ClampToArena(model.Vec3{
			fb.startPos[0] + 0.5*math.Sin(float64(i)*0.1),
			fb.startPos[1] + 0.3*math.Sin(float64(i)*0.07),
			z,
		})
		vel := model.Vec3{0.05 * math.Cos(float64(i)*0.1), 0, vz}
		frames[i] = fb.baseFrame(i, pos, rotationFromVelocity(vel))
	}
	return frames
}

// NormalBoostSequence generates nBoosts boosts 3-5 s apart. Each boost adds
// 3-4 m/s (below the MOV_004 cap+margin of 6.5 m/s) for 7-8 frames and
// decays over 1-2 s; one activation per boost keeps MOV_005 silent.
func (fb *FrameBuilder) NormalBoostSequence(nBoosts int) []model.PlayerTelemetryFrame {
	var frames []model.PlayerTelemetryFrame
	dt := fb.dt()
	frameIdx := 0
	baseSpeed := 5.0
	mover := newZMover(fb.startPos[2], 1.0, dt)

	emit := func(speed float64, boosting bool) {
		z, vz := mover.step(speed)
		pos := ClampToArena(model.Vec3{fb.startPos[0], fb.startPos[1], z})
		f := fb.baseFrame(frameIdx, pos, rotationFromVelocity(model.Vec3{0, 0, vz}))
		f.IsBoosting = boosting
		frames = append(frames, f)
		frameIdx++
	}

	for boostNum := 0; boostNum < nBoosts; boostNum++ {
		gapFrames := int((3.0 + 2.0*(0.5+0.5*math.Sin(float64(boostNum)*1.3))) / dt)
		boostFrames := 7 + (boostNum % 2)
		boostMagnitude := 3.0 + 1.0*(0.5+0.5*math.Cos(float64(boostNum)*2.1))
		decayFrames := int((1.0 + 1.0*(0.5+0.5*math.Sin(float64(boostNum)*0.7))) / dt)

		for j := 0; j < gapFrames; j++ {
			emit(baseSpeed+0.5*math.Sin(float64(frameIdx)*0.2), false)
		}
		for j := 0; j < boostFrames; j++ {
			emit(baseSpeed+boostMagnitude, true)
		}
		for j := 0; j < decayFrames; j++ {
			progress := float64(j) / float64(decayFrames)
			emit(baseSpeed+boostMagnitude*(1.0-progress), false)
		}
	}
	return frames
}

// NormalStunCycle generates a moving player who is stunned for stunFrames
// (default 45 = 3 s, the game's stun duration) once every 150 frames. Legit
// stuns last >= STATE_002's 20-frame minimum, so it must stay silent.
func (fb *FrameBuilder) NormalStunCycle(nFrames, stunFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 4.0)
	for i := range frames {
		cyclePos := i % 150
		frames[i].IsStunned = cyclePos >= 30 && cyclePos < 30+stunFrames
	}
	return frames
}

// NormalShieldCycle generates shield use of 20 frames on / 90 frames off
// (6 s > the 5 s cooldown, >= STATE_005's 60-frame minimum) repeated
// nCycles times. STATE_003 (300+ frames) and STATE_005 must stay silent.
func (fb *FrameBuilder) NormalShieldCycle(nCycles int) []model.PlayerTelemetryFrame {
	return fb.shieldCycle(nCycles, 20, 90)
}

// shieldCycle emits nCycles of onFrames shield-on followed by offFrames off,
// after a 20-frame warmup, on a slowly moving player.
func (fb *FrameBuilder) shieldCycle(nCycles, onFrames, offFrames int) []model.PlayerTelemetryFrame {
	total := 20 + nCycles*(onFrames+offFrames)
	frames := fb.NormalMovingPlayer(total, 3.0)
	for c := 0; c < nCycles; c++ {
		start := 20 + c*(onFrames+offFrames)
		for i := start; i < start+onFrames && i < total; i++ {
			frames[i].ShieldActive = true
		}
	}
	return frames
}

// RespawnImmunity generates a moving player who respawns at spawnFrame: the
// position jumps to a spawn point 30 m away and IsImmune stays true for the
// game's 1.5 s immunity window (22 frames at 15 fps). MOV_002 skips immune
// players and STATE_004 (limit 225 frames) must stay silent.
func (fb *FrameBuilder) RespawnImmunity(nFrames, spawnFrame int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 4.0)
	immuneFrames := int(math.Round(model.DefaultPhysics().ImmunityWindow * fb.tickRate))
	jump := model.Vec3{0, 0, 30}
	if frames[spawnFrame].Position[2] > 0 {
		jump[2] = -30
	}
	for i := spawnFrame; i < nFrames; i++ {
		frames[i].Position = ClampToArena(frames[i].Position.Add(jump))
		fb.humanHands(&frames[i], i)
		if i < spawnFrame+immuneFrames {
			frames[i].IsImmune = true
		}
	}
	return frames
}

// ===================================================================
// 2. Jitter/Artifact Scenarios
// ===================================================================

// JitteryTelemetry generates normal movement with jitterLevel metres of
// position noise on the body (hands follow the body plus their own noise),
// simulating a noisy tracker.
func (fb *FrameBuilder) JitteryTelemetry(nFrames int, jitterLevel float64) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 5.0)
	for i := range frames {
		posJitter := deterministicJitter3(i, 100, jitterLevel)
		frames[i].Position = ClampToArena(frames[i].Position.Add(posJitter))
		fb.humanHands(&frames[i], i)
		frames[i].LeftHandPosition = frames[i].LeftHandPosition.Add(deterministicJitter3(i, 110, jitterLevel*0.8))
		frames[i].RightHandPosition = frames[i].RightHandPosition.Add(deterministicJitter3(i, 120, jitterLevel*0.8))
	}
	return frames
}

// PacketLossFrames generates normal movement and then DROPS frames at
// dropRate: surviving frames keep their original FrameIndex and Timestamp
// (so both the index sequence and the time base have real gaps) and their
// DeltaTime is the real spacing to the previous surviving frame. The result
// has fewer than nFrames frames; the first frame is always kept.
func (fb *FrameBuilder) PacketLossFrames(nFrames int, dropRate float64) []model.PlayerTelemetryFrame {
	base := fb.NormalMovingPlayer(nFrames, 5.0)
	var frames []model.PlayerTelemetryFrame
	lastTS := 0.0
	for i := 0; i < len(base); i++ {
		// Deterministic pseudo-uniform hash in [0, 1): drops ~dropRate of the frames.
		dropVal := frac(math.Sin(float64(i)*12.9898+78.233) * 43758.5453)
		if i > 0 && dropVal < dropRate {
			continue
		}
		f := base[i]
		if len(frames) > 0 {
			f.DeltaTime = f.Timestamp - lastTS
		}
		lastTS = f.Timestamp
		frames = append(frames, f)
	}
	return frames
}

// LargeFrameGap generates a moving player whose feed stalls for gapSeconds
// at gapAtFrame (a reconnect): the frame index and timestamp both jump by
// the stalled interval and the player is where continuous motion at 5 m/s
// would have put them. The gap frame carries the real DeltaTime.
// Production behaviour: the validator accepts the frame (a stall is a
// sample gap, not a corrupt frame), the feature extractor derives no
// kinematics across it, and MOV_002 skips it because the index gap exceeds
// max_frame_gap.
func (fb *FrameBuilder) LargeFrameGap(nFrames int, gapAtFrame int, gapSeconds float64) []model.PlayerTelemetryFrame {
	dt := fb.dt()
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	mover := newZMover(fb.startPos[2], 5.0, dt)
	accumulatedTime := 0.0
	indexOffset := 0
	for i := 0; i < nFrames; i++ {
		frameDt := dt
		if i == gapAtFrame {
			frameDt = gapSeconds + dt
			skipped := int(math.Round(gapSeconds / dt))
			indexOffset += skipped
			// Advance the path by the skipped frames.
			for k := 0; k < skipped; k++ {
				mover.step(1.0)
			}
		}
		if i > 0 {
			accumulatedTime += frameDt
		}
		z, vz := mover.step(1.0)
		pos := ClampToArena(model.Vec3{fb.startPos[0], fb.startPos[1] + 0.3*math.Sin(accumulatedTime*0.5), z})
		f := fb.baseFrame(i, pos, rotationFromVelocity(model.Vec3{0, 0, vz}))
		f.FrameIndex = i + indexOffset
		f.Timestamp = accumulatedTime
		if i > 0 {
			f.DeltaTime = frameDt
		}
		fb.humanHands(&f, i)
		frames[i] = f
	}
	return frames
}

// InterpolationArtifact generates normal movement with nGlitches
// single-frame position glitches: on the glitch frame the body (and hands)
// are displaced by glitchMetres and snap back on the next frame, the
// classic interpolation/extrapolation hiccup. With the default 4 m the
// apparent speed is ~60 m/s for two frames: above the physics cap, so the
// glitch is visible to MOV_001, but fewer than min_burst_frames (5) frames
// and below MOV_002's 8 m teleport threshold, so neither may fire.
func (fb *FrameBuilder) InterpolationArtifact(nFrames int) []model.PlayerTelemetryFrame {
	return fb.InterpolationArtifactN(nFrames, 3, 4.0)
}

// InterpolationArtifactN is InterpolationArtifact with an explicit glitch
// count and displacement.
func (fb *FrameBuilder) InterpolationArtifactN(nFrames, nGlitches int, glitchMetres float64) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 5.0)
	if nGlitches <= 0 || nFrames < 40 {
		return frames
	}
	spacing := nFrames / (nGlitches + 1)
	for g := 1; g <= nGlitches; g++ {
		idx := g * spacing
		if idx >= nFrames {
			break
		}
		dir := model.Vec3{0, 0, glitchMetres}
		if frames[idx].Position[2] > 0 {
			dir[2] = -glitchMetres
		}
		frames[idx].Position = ClampToArena(frames[idx].Position.Add(dir))
		fb.humanHands(&frames[idx], idx)
	}
	return frames
}

// GlitchDisplacement is the body displacement InterpolationArtifact applies.
const GlitchDisplacement = 4.0

// HighPingPlayer generates normal gameplay with EstimatedPingMs = avgPingMs,
// position jitter proportional to ping, and irregular sample timing: the
// Timestamp of each frame is jittered by +-10 ms and DeltaTime is the real
// spacing, so the kinematics (which use timestamp deltas) see the
// irregularity.
func (fb *FrameBuilder) HighPingPlayer(nFrames int, avgPingMs float64) []model.PlayerTelemetryFrame {
	saved := fb.pingMs
	fb.pingMs = avgPingMs
	frames := fb.NormalMovingPlayer(nFrames, 5.0)
	fb.pingMs = saved

	pingJitterScale := avgPingMs / 1000.0 * 0.5
	prevTS := 0.0
	for i := range frames {
		frames[i].EstimatedPingMs = avgPingMs
		frames[i].Position = ClampToArena(frames[i].Position.Add(deterministicJitter3(i, 200, pingJitterScale)))
		fb.humanHands(&frames[i], i)
		frames[i].LeftHandPosition = frames[i].LeftHandPosition.Add(deterministicJitter3(i, 210, pingJitterScale*0.5))
		frames[i].RightHandPosition = frames[i].RightHandPosition.Add(deterministicJitter3(i, 220, pingJitterScale*0.5))
		if i > 0 {
			frames[i].Timestamp += 0.01 * math.Sin(float64(i)*4.7+1.3)
			frames[i].DeltaTime = frames[i].Timestamp - prevTS
		}
		prevTS = frames[i].Timestamp
	}
	return frames
}

// PingSpikeSequence generates normal gameplay with a ping spike (50 ms ->
// spikePingMs) at spikeAtFrame lasting 20 frames, during which positions lag
// six frames behind and carry extra jitter; the catch-up after the spike is
// a ~2 m jump, below the teleport threshold.
func (fb *FrameBuilder) PingSpikeSequence(nFrames int, spikeAtFrame int, spikePingMs float64) []model.PlayerTelemetryFrame {
	saved := fb.pingMs
	fb.pingMs = 50.0
	frames := fb.NormalMovingPlayer(nFrames, 5.0)
	fb.pingMs = saved

	for i := range frames {
		if i >= spikeAtFrame && i < spikeAtFrame+20 {
			frames[i].EstimatedPingMs = spikePingMs
			sourceIdx := i - 6
			if sourceIdx < 0 {
				sourceIdx = 0
			}
			frames[i].Position = ClampToArena(frames[sourceIdx].Position.Add(deterministicJitter3(i, 300, 0.1)))
			fb.humanHands(&frames[i], i)
		} else {
			frames[i].EstimatedPingMs = 50.0
		}
	}
	return frames
}

// ===================================================================
// 3. Extreme-But-Legit Scenarios
// ===================================================================

// RegrabStackingBurst generates a player who reaches 45-48 m/s for four
// frames via regrab stacking (no boost flag: regrabs are not boosts), then
// decelerates. Below the 55 m/s physics cap on every frame, so neither
// MOV_001 branch may fire; hands follow the body so BIO_002 (50 m/s) stays
// silent too.
func (fb *FrameBuilder) RegrabStackingBurst(nFrames int) []model.PlayerTelemetryFrame {
	frames := make([]model.PlayerTelemetryFrame, nFrames)
	dt := fb.dt()
	z := fb.startPos[2] - 20
	burstStart := nFrames / 3
	burstEnd := burstStart + 4
	for i := 0; i < nFrames; i++ {
		var speed float64
		switch {
		case i >= burstStart && i < burstEnd:
			speed = 45.0 + 3.0*math.Sin(float64(i-burstStart)/4.0*math.Pi)
		case i >= burstEnd && i < burstEnd+30:
			speed = math.Max(5.0, 45.0*math.Exp(-3.0*float64(i-burstEnd)/30.0))
		default:
			speed = 5.0 + 2.0*math.Sin(float64(i)*0.3)
		}
		z += speed * dt
		if z > moveHalfZ {
			z = -moveHalfZ + (z - moveHalfZ)
		}
		pos := ClampToArena(model.Vec3{fb.startPos[0], fb.startPos[1], z})
		frames[i] = fb.baseFrame(i, pos, rotationFromVelocity(model.Vec3{0, 0, speed}))
	}
	return frames
}

// FastWristFlick generates a two-frame wrist flick at 23-25 rad/s on a
// moving player. Below BIO_001's 50 rad/s limit (and below the 46.9 rad/s
// the metric can even express at 15 fps), so BIO_001 must stay silent.
func (fb *FrameBuilder) FastWristFlick(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 3.0)
	dt := fb.dt()
	flickFrame := nFrames / 2
	baseAngle := 0.0
	for i := range frames {
		if i == flickFrame || i == flickFrame+1 {
			rate := 23.0 + 2.0*math.Sin(float64(i)*1.1)
			baseAngle += rate * dt
		}
		if baseAngle > 0 {
			frames[i].RightHandRotation = axisAngleQuat(model.Vec3{0, 0, 1}, baseAngle).Multiply(humanWobble(i, 5.0)).Normalize()
		}
	}
	return frames
}

// SteadyHandPlayer generates a moving player with unusually steady hands:
// jitter amplitude 0.014 m (variance ~1.5e-4 m^2, above BIO_003's 1e-5
// threshold). Must NOT trigger BIO_003.
func (fb *FrameBuilder) SteadyHandPlayer(nFrames int) []model.PlayerTelemetryFrame {
	frames := fb.NormalMovingPlayer(nFrames, 3.0)
	for i := range frames {
		t := float64(i)
		frames[i].LeftHandPosition = frames[i].Position.Add(leftHandOffset).Add(model.Vec3{
			0.014 * math.Sin(t*0.7), 0.007 * math.Cos(t*0.5), 0.005 * math.Sin(t*0.9+1.0)})
		frames[i].RightHandPosition = frames[i].Position.Add(rightHandOffset).Add(model.Vec3{
			0.014 * math.Cos(t*0.6+0.3), 0.007 * math.Sin(t*0.4+0.8), 0.005 * math.Cos(t*0.8+2.0)})
	}
	return frames
}
