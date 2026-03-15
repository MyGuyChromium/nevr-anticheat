package pipeline

import (
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// FeatureExtractor computes derived features from raw telemetry frames.
type FeatureExtractor struct {
	historyWindow int
}

// NewFeatureExtractor creates a new feature extractor.
func NewFeatureExtractor(historyWindow int) *FeatureExtractor {
	if historyWindow < 5 {
		historyWindow = 30
	}
	return &FeatureExtractor{historyWindow: historyWindow}
}

// UpdatePlayerState updates a player's derived state from a new telemetry frame.
func (fe *FeatureExtractor) UpdatePlayerState(
	ps *model.PlayerState,
	frame *model.PlayerTelemetryFrame,
	matchCtx *model.MatchContext,
) {
	// Capture PREVIOUS state before overwriting — critical for delta computations.
	prevPos := ps.Position
	prevVel := ps.Velocity
	prevLeftHand := ps.LeftHand
	prevRightHand := ps.RightHand
	prevLeftHandRot := ps.LeftHandRot
	prevRightHandRot := ps.RightHandRot
	prevTimestamp := ps.LastTimestamp
	prevHasDisc := ps.HasDisc
	wasStunned := ps.IsStunned
	wasShieldActive := ps.ShieldActive
	wasBoosting := ps.IsBoosting

	// Update raw state from frame
	ps.Position = frame.Position
	ps.Rotation = frame.Rotation
	ps.LeftHand = frame.LeftHandPosition
	ps.RightHand = frame.RightHandPosition
	ps.LeftHandRot = frame.LeftHandRotation
	ps.RightHandRot = frame.RightHandRotation
	ps.CurrentDisc = frame.Disc
	ps.IsStunned = frame.IsStunned
	ps.IsBoosting = frame.IsBoosting
	ps.ShieldActive = frame.ShieldActive
	ps.IsImmune = frame.IsImmune
	ps.HasDisc = frame.HasPossession
	ps.EstimatedPingMs = frame.EstimatedPingMs
	ps.IsHighPing = frame.EstimatedPingMs > 150
	ps.LastFrameIdx = frame.FrameIndex
	ps.LastTimestamp = frame.Timestamp

	// Score/stat tracking for state detectors
	ps.PrevBlueScore = frame.BlueScore
	ps.PrevOrangeScore = frame.OrangeScore
	ps.PrevGoals = frame.Goals
	ps.PrevStuns = frame.Stuns

	// Compute dt with clamping
	dt := frame.Timestamp - prevTimestamp
	if ps.FrameCount == 0 || dt <= 0 {
		dt = 0.067 // default ~15fps for first frame
	}
	if dt < 0.01 {
		dt = 0.01
	}
	largeGap := dt > 0.5
	if dt > 0.2 {
		dt = 0.2
	}
	ps.FrameDt = dt

	// Compute kinematics (only after first frame with valid previous position)
	if ps.FrameCount > 0 && !prevPos.IsZero() && !largeGap {
		// Body velocity and acceleration
		ps.Velocity = frame.Position.Sub(prevPos).Scale(1.0 / dt)
		ps.Speed = ps.Velocity.Magnitude()
		ps.Acceleration = ps.Velocity.Sub(prevVel).Scale(1.0 / dt)
		ps.AccelerationMagnitude = ps.Acceleration.Magnitude()

		// Hand velocities — use PREVIOUS hand positions (captured before overwrite).
		// Guard BOTH previous AND current against zero vectors (tracking loss).
		// A zero→real or real→zero transition produces false velocity spikes.
		if !prevLeftHand.IsZero() && !frame.LeftHandPosition.IsZero() {
			ps.LeftHandVelocity = frame.LeftHandPosition.Sub(prevLeftHand).Scale(1.0 / dt)
			ps.LeftHandSpeed = ps.LeftHandVelocity.Magnitude()
		} else {
			ps.LeftHandVelocity = model.Vec3{}
			ps.LeftHandSpeed = 0
		}
		if !prevRightHand.IsZero() && !frame.RightHandPosition.IsZero() {
			ps.RightHandVelocity = frame.RightHandPosition.Sub(prevRightHand).Scale(1.0 / dt)
			ps.RightHandSpeed = ps.RightHandVelocity.Magnitude()
		} else {
			ps.RightHandVelocity = model.Vec3{}
			ps.RightHandSpeed = 0
		}

		// Wrist angular rates — quaternion angular distance / dt
		if prevLeftHandRot.IsUnit() && frame.LeftHandRotation.IsUnit() {
			ps.LeftWristAngularRate = prevLeftHandRot.AngularDistance(frame.LeftHandRotation) / dt
		} else {
			ps.LeftWristAngularRate = 0
		}
		if prevRightHandRot.IsUnit() && frame.RightHandRotation.IsUnit() {
			ps.RightWristAngularRate = prevRightHandRot.AngularDistance(frame.RightHandRotation) / dt
		} else {
			ps.RightWristAngularRate = 0
		}

		// Update Welford accumulators
		ps.SpeedStats.Update(ps.Speed)
		ps.LeftHandSpeedStats.Update(ps.LeftHandSpeed)
		ps.RightHandSpeedStats.Update(ps.RightHandSpeed)
	}

	// Push to history buffers
	model.PushVec3History(&ps.PositionHistory, ps.Position, fe.historyWindow)
	model.PushVec3History(&ps.VelocityHistory, ps.Velocity, fe.historyWindow)
	model.PushFloat64History(&ps.SpeedHistory, ps.Speed, fe.historyWindow)
	model.PushVec3History(&ps.LeftHandHistory, ps.LeftHand, fe.historyWindow)
	model.PushVec3History(&ps.RightHandHistory, ps.RightHand, fe.historyWindow)
	model.PushQuatHistory(&ps.LeftHandRotHistory, ps.LeftHandRot, fe.historyWindow)
	model.PushQuatHistory(&ps.RightHandRotHistory, ps.RightHandRot, fe.historyWindow)
	model.PushFloat64History(&ps.TimestampHistory, frame.Timestamp, fe.historyWindow)

	// Push disc velocity to history for pre-release snapshot accuracy
	if frame.Disc != nil {
		model.PushVec3History(&ps.DiscVelocityHistory, frame.Disc.Velocity, fe.historyWindow)
	}

	// --- State machine tracking ---

	// Throw detection: possession transition held -> free.
	// Require at least 2 frames of possession to filter out possession flicker.
	// A 1-frame possession glitch (API reporting error) would otherwise attribute
	// the current disc flight velocity as a "throw" to this player — a false positive.
	if prevHasDisc && !ps.HasDisc && ps.FrameCount > 0 {
		possessionFrames := frame.FrameIndex - ps.PossessionStartFrame
		if possessionFrames >= 2 {
			fe.detectThrow(ps, frame, matchCtx, dt, prevLeftHand, prevRightHand, prevLeftHandRot, prevRightHandRot)
		}
	}

	// Possession start tracking
	if !prevHasDisc && ps.HasDisc {
		ps.PossessionStartFrame = frame.FrameIndex
		ps.PossessionStartTime = frame.Timestamp
	}

	// Stun transition tracking
	if !wasStunned && ps.IsStunned {
		ps.StunStartFrame = frame.FrameIndex
		ps.WasStunned = true
	}
	if wasStunned && !ps.IsStunned && ps.WasStunned {
		// Stun ended — recovery time can be checked by STATE_002
		ps.StunRecoveries++
	}

	// Shield tracking
	if ps.ShieldActive {
		ps.ConsecutiveShieldFrames++
		if !wasShieldActive {
			ps.ShieldStartFrame = frame.FrameIndex
		}
	} else {
		if wasShieldActive {
			ps.LastShieldOffFrame = frame.FrameIndex
		}
		ps.ConsecutiveShieldFrames = 0
	}

	// Invulnerability tracking during active play
	if matchCtx.IsActivePhase(frame.GamePhase) && ps.IsImmune {
		ps.InvulnerableFrames++
	}

	// Boost tracking
	if ps.IsBoosting && !wasBoosting {
		model.PushFloat64History(&ps.BoostTimestamps, frame.Timestamp, 100)
		ps.LastBoostFrame = frame.FrameIndex
	}

	ps.FrameCount++
}

// detectThrow fires when a player releases the disc.
func (fe *FeatureExtractor) detectThrow(
	ps *model.PlayerState,
	frame *model.PlayerTelemetryFrame,
	matchCtx *model.MatchContext,
	dt float64,
	prevLeftHand, prevRightHand model.Vec3,
	prevLeftHandRot, prevRightHandRot model.Quat,
) {
	disc := frame.Disc
	if disc == nil {
		return
	}

	releaseSpeed := disc.Speed
	releaseVel := disc.Velocity
	releasePos := disc.Position

	// Guard against NaN/Inf
	if math.IsNaN(releaseSpeed) || math.IsInf(releaseSpeed, 0) || releaseSpeed <= 0 {
		return
	}

	// Determine throwing hand: which hand was closer AND velocity-aligned
	leftDist := prevLeftHand.Distance(releasePos)
	rightDist := prevRightHand.Distance(releasePos)

	throwingHand := "right"
	handPos := prevRightHand
	handVel := ps.RightHandVelocity
	handSpeed := ps.RightHandSpeed
	wristRot := prevRightHandRot
	wristAngVel := ps.RightWristAngularRate

	if leftDist < rightDist {
		throwingHand = "left"
		handPos = prevLeftHand
		handVel = ps.LeftHandVelocity
		handSpeed = ps.LeftHandSpeed
		wristRot = prevLeftHandRot
		wristAngVel = ps.LeftWristAngularRate
	}

	handToDiscDist := handPos.Distance(releasePos)

	// Release angle: angle between hand velocity and disc velocity
	releaseAngle := 0.0
	if handSpeed > 0.1 && releaseSpeed > 0.1 {
		releaseAngle = model.RadToDeg(handVel.AngleBetween(releaseVel))
	}

	// Possession duration
	possessionDuration := frame.Timestamp - ps.PossessionStartTime
	if possessionDuration < 0 {
		possessionDuration = 0
	}

	// Build pre-release frame snapshots from history
	var preRelease []model.ThrowFrameSnapshot
	histLen := len(ps.PositionHistory)
	snapshotCount := 5
	if histLen < snapshotCount {
		snapshotCount = histLen
	}
	for i := histLen - snapshotCount; i < histLen; i++ {
		if i < 0 {
			continue
		}
		ts := 0.0
		if i < len(ps.TimestampHistory) {
			ts = ps.TimestampHistory[i]
		}
		snap := model.ThrowFrameSnapshot{
			FrameIndex:     frame.FrameIndex - (histLen - i),
			Timestamp:      ts,
			PlayerPosition: ps.PositionHistory[i],
		}
		if i < len(ps.LeftHandHistory) {
			snap.HandPosition = ps.LeftHandHistory[i]
		}
		// Use historical disc velocity instead of current frame's release velocity
		discHistIdx := len(ps.DiscVelocityHistory) - snapshotCount + (i - (histLen - snapshotCount))
		if discHistIdx >= 0 && discHistIdx < len(ps.DiscVelocityHistory) {
			snap.DiscVelocity = ps.DiscVelocityHistory[discHistIdx]
		}
		if disc != nil {
			snap.DiscPosition = disc.Position
		}
		preRelease = append(preRelease, snap)
	}

	// Estimate target (nearest goal)
	var targetPos *model.Vec3
	targetDev := 0.0
	goalBlue := model.Vec3{-matchCtx.Physics.ArenaLength / 2, 0, 0}
	goalOrange := model.Vec3{matchCtx.Physics.ArenaLength / 2, 0, 0}
	distBlue := releasePos.Distance(goalBlue)
	distOrange := releasePos.Distance(goalOrange)
	nearestGoal := goalBlue
	if distOrange < distBlue {
		nearestGoal = goalOrange
	}
	toGoal := nearestGoal.Sub(releasePos)
	if releaseSpeed > 0.1 && toGoal.Magnitude() > 0.1 {
		targetDev = model.RadToDeg(releaseVel.AngleBetween(toGoal))
		if targetDev < 30 {
			tp := nearestGoal
			targetPos = &tp
		}
	}

	throw := model.ThrowEvent{
		ThrowerID: ps.PlayerID,
		Attribution: model.ThrowAttribution{
			PlayerID: ps.PlayerID, Confidence: 0.9,
			Method: "possession_track", LookbackDepth: 1,
		},
		FrameIndex:           frame.FrameIndex,
		Timestamp:            frame.Timestamp,
		ReleasePosition:      releasePos,
		ReleaseVelocity:      releaseVel,
		ReleaseSpeed:         releaseSpeed,
		ThrowingHand:         throwingHand,
		HandPosition:         handPos,
		HandVelocity:         handVel,
		HandSpeed:            handSpeed,
		WristOrientation:     wristRot,
		WristAngularVelocity: wristAngVel,
		PlayerPosition:       ps.Position,
		PlayerVelocity:       ps.Velocity,
		HandToDiscDistance:    handToDiscDist,
		ReleaseAngle:         releaseAngle,
		TargetPosition:       targetPos,
		TargetDeviation:      targetDev,
		PossessionDuration:   possessionDuration,
		PreReleaseFrames:     preRelease,
	}

	ps.LastThrow = &throw
	ps.ThrowHistory = append(ps.ThrowHistory, throw)
	ps.ThrowCount++
}
