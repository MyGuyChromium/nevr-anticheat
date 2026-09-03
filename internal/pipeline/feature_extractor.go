package pipeline

import (
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	// MinFrameDt and MaxFrameDt bound the time step used for kinematics.
	// A known dt is clamped into this range; a dt <= 0 (first frame,
	// duplicate or out-of-order timestamp) is unknown and no kinematics are
	// derived for that frame. Nothing here assumes a fixed tick rate.
	MinFrameDt = 0.005
	MaxFrameDt = 0.5

	// DefaultHighPingThresholdMs is the ping above which PlayerState.IsHighPing
	// is set. Override with SetHighPingThreshold (config high_ping_threshold_ms).
	DefaultHighPingThresholdMs = 150.0

	// preReleaseSnapshotCount is how many frames before a release are captured.
	preReleaseSnapshotCount = 5

	// goalDirectedMaxDeviationDeg: a throw deviating more than this from the
	// chosen goal is not goal-directed (ThrowEvent.TargetPosition stays nil).
	goalDirectedMaxDeviationDeg = 30.0

	// maxTrackedDiscHistories bounds the extractor-side disc history map.
	maxTrackedDiscHistories = 4096

	// Goal selection labels recorded in ThrowEvent.GoalSelection (defined
	// on the model so detectors can read them without importing pipeline).
	GoalSelectionTeam    = model.GoalSelectionTeam
	GoalSelectionAngular = model.GoalSelectionAngular
	GoalSelectionNearest = model.GoalSelectionNearest
)

// discSample is one frame of disc position for the pre-release snapshots.
// PlayerState has no disc position history, so the extractor keeps one here,
// pushed in lockstep with PlayerState.DiscVelocityHistory.
type discSample struct {
	pos     model.Vec3
	missing bool
}

// goalSides records which goal the blue team scores into for one match.
type goalSides struct {
	matchID   string
	blueGoalZ float64
	known     bool
}

// FeatureExtractor computes derived features from raw telemetry frames.
type FeatureExtractor struct {
	historyWindow       int
	highPingThresholdMs float64

	// configuredBlueSign: +1 blue attacks +Z, -1 blue attacks -Z, 0 unknown.
	// When unknown the side is learned from score increments (see
	// learnGoalSides); until then goals are chosen by release direction.
	configuredBlueSign int
	learned            goalSides

	discHistory map[string][]discSample
}

// NewFeatureExtractor creates a new feature extractor.
func NewFeatureExtractor(historyWindow int) *FeatureExtractor {
	if historyWindow < 5 {
		historyWindow = 30
	}
	return &FeatureExtractor{
		historyWindow:       historyWindow,
		highPingThresholdMs: DefaultHighPingThresholdMs,
		discHistory:         make(map[string][]discSample),
	}
}

// SetHighPingThreshold sets the ping (ms) above which IsHighPing is set.
// Non-positive values restore the default.
func (fe *FeatureExtractor) SetHighPingThreshold(ms float64) {
	if ms <= 0 || math.IsNaN(ms) {
		ms = DefaultHighPingThresholdMs
	}
	fe.highPingThresholdMs = ms
}

// HighPingThreshold returns the current high-ping threshold in ms.
func (fe *FeatureExtractor) HighPingThreshold() float64 { return fe.highPingThresholdMs }

// SetBlueGoalSide configures which goal the blue team attacks: sign > 0 means
// blue scores into the goal at +GoalZ, sign < 0 into the goal at -GoalZ, and
// 0 (the default) means unknown, in which case the side is learned from the
// first scored goal of each match and throws before that use the goal the
// release velocity points at.
func (fe *FeatureExtractor) SetBlueGoalSide(sign int) {
	switch {
	case sign > 0:
		fe.configuredBlueSign = 1
	case sign < 0:
		fe.configuredBlueSign = -1
	default:
		fe.configuredBlueSign = 0
	}
}

// TeamGoalZ returns the Z coordinate of the goal the given team attacks in
// the given match, and whether it is known (configured or learned).
func (fe *FeatureExtractor) TeamGoalZ(matchCtx *model.MatchContext, team string) (float64, bool) {
	goalZ := matchCtx.Physics.GoalZ
	if goalZ <= 0 {
		return 0, false
	}
	var blueGoalZ float64
	switch {
	case fe.configuredBlueSign != 0:
		blueGoalZ = float64(fe.configuredBlueSign) * goalZ
	case fe.learned.known && fe.learned.matchID == matchCtx.MatchID:
		blueGoalZ = fe.learned.blueGoalZ
	default:
		return 0, false
	}
	switch team {
	case "blue":
		return blueGoalZ, true
	case "orange":
		return -blueGoalZ, true
	}
	return 0, false
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
	prevBlueScore := ps.PrevBlueScore
	prevOrangeScore := ps.PrevOrangeScore
	wasStunned := ps.IsStunned
	wasShieldActive := ps.ShieldActive
	wasBoosting := ps.IsBoosting
	firstFrame := ps.FrameCount == 0

	if firstFrame {
		// Fresh PlayerState (new match or new player): drop any side history
		// left over from an earlier state with the same ID.
		delete(fe.discHistory, ps.PlayerID)
	}

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
	ps.IsHighPing = frame.EstimatedPingMs > fe.highPingThresholdMs
	ps.LastFrameIdx = frame.FrameIndex
	ps.LastTimestamp = frame.Timestamp
	if ps.Team == "" && frame.Team != "" {
		ps.Team = frame.Team
	}

	// Score/stat tracking for state detectors
	ps.PrevBlueScore = frame.BlueScore
	ps.PrevOrangeScore = frame.OrangeScore
	ps.PrevGoals = frame.Goals
	ps.PrevStuns = frame.Stuns

	// Time step. dt <= 0 (first frame, duplicate or out-of-order timestamp)
	// is unknown: FrameDt is 0 and no kinematics are derived. A gap longer
	// than MaxFrameDt is too long for finite differences; the frame still
	// updates raw state but kinematics are skipped.
	rawDt := frame.Timestamp - prevTimestamp
	dtKnown := !firstFrame && rawDt > 0 && !math.IsNaN(rawDt) && !math.IsInf(rawDt, 0)
	largeGap := dtKnown && rawDt > MaxFrameDt
	dt := 0.0
	if dtKnown {
		dt = model.Clamp(rawDt, MinFrameDt, MaxFrameDt)
	}
	ps.FrameDt = dt

	// Skip kinematics when a player with post-respawn immunity jumped
	// (respawn teleport). Continuous immune movement (god mode) still computes
	// kinematics so STATE_004 can see active play during immunity.
	immuneRespawnJump := false
	if dtKnown && frame.IsImmune && !prevPos.IsZero() {
		immuneRespawnJump = frame.Position.Sub(prevPos).Magnitude()/dt > matchCtx.Physics.MaxPlayerSpeed*2.0
	}

	kinematicsValid := dtKnown && !prevPos.IsZero() && !largeGap && !immuneRespawnJump
	if kinematicsValid {
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
	} else {
		// No valid finite difference for this frame: clear derived kinematics
		// so detectors and histories never consume values from before a gap,
		// a respawn teleport or a tracking loss as if they were current.
		ps.Velocity = model.Vec3{}
		ps.Speed = 0
		ps.Acceleration = model.Vec3{}
		ps.AccelerationMagnitude = 0
		ps.LeftHandVelocity = model.Vec3{}
		ps.RightHandVelocity = model.Vec3{}
		ps.LeftHandSpeed = 0
		ps.RightHandSpeed = 0
		ps.LeftWristAngularRate = 0
		ps.RightWristAngularRate = 0
	}

	// Learn which goal each team attacks from score increments (needs the
	// disc position at the moment a score changed).
	if !firstFrame {
		fe.learnGoalSides(frame, matchCtx, prevBlueScore, prevOrangeScore)
	}

	// --- State machine tracking ---

	// Throw detection: possession transition held -> free, evaluated BEFORE
	// this frame is pushed into the histories so pre-release snapshots hold
	// only frames strictly before the release.
	// Require at least 2 frames of possession to filter out possession flicker.
	// A 1-frame possession glitch (API reporting error) would otherwise attribute
	// the current disc flight velocity as a "throw" to this player — a false positive.
	// Possession released outside active play (round-end disc reset) is not a throw.
	if prevHasDisc && !ps.HasDisc && !firstFrame && matchCtx.IsActivePhase(frame.GamePhase) {
		possessionFrames := frame.FrameIndex - ps.PossessionStartFrame
		if possessionFrames >= 2 {
			fe.detectThrow(ps, frame, matchCtx, prevLeftHand, prevRightHand, prevLeftHandRot, prevRightHandRot)
		}
	}

	// Push to history buffers (all buffers advance together so indices align).
	model.PushVec3History(&ps.PositionHistory, ps.Position, fe.historyWindow)
	model.PushVec3History(&ps.VelocityHistory, ps.Velocity, fe.historyWindow)
	model.PushFloat64History(&ps.SpeedHistory, ps.Speed, fe.historyWindow)
	model.PushVec3History(&ps.LeftHandHistory, ps.LeftHand, fe.historyWindow)
	model.PushVec3History(&ps.RightHandHistory, ps.RightHand, fe.historyWindow)
	model.PushQuatHistory(&ps.LeftHandRotHistory, ps.LeftHandRot, fe.historyWindow)
	model.PushQuatHistory(&ps.RightHandRotHistory, ps.RightHandRot, fe.historyWindow)
	model.PushFloat64History(&ps.TimestampHistory, frame.Timestamp, fe.historyWindow)
	fe.pushDiscHistory(ps, frame.Disc)

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

// pushDiscHistory advances the disc velocity history on PlayerState and the
// extractor-side disc position history. A frame without disc state pushes a
// zero placeholder (flagged missing) so both stay index-aligned with
// PositionHistory.
func (fe *FeatureExtractor) pushDiscHistory(ps *model.PlayerState, disc *model.DiscState) {
	sample := discSample{missing: true}
	vel := model.Vec3{}
	if disc != nil {
		sample = discSample{pos: disc.Position}
		vel = disc.Velocity
	}
	model.PushVec3History(&ps.DiscVelocityHistory, vel, fe.historyWindow)

	if fe.discHistory == nil {
		fe.discHistory = make(map[string][]discSample)
	}
	if _, ok := fe.discHistory[ps.PlayerID]; !ok && len(fe.discHistory) >= maxTrackedDiscHistories {
		// Evidence-only data: drop everything rather than grow without bound
		// across many matches.
		fe.discHistory = make(map[string][]discSample)
	}
	hist := append(fe.discHistory[ps.PlayerID], sample)
	if len(hist) > fe.historyWindow {
		copy(hist, hist[len(hist)-fe.historyWindow:])
		hist = hist[:fe.historyWindow]
	}
	fe.discHistory[ps.PlayerID] = hist
}

// learnGoalSides infers which goal the blue team scores into from the first
// score increment seen with the disc near a goal. Only used when no side has
// been configured with SetBlueGoalSide.
func (fe *FeatureExtractor) learnGoalSides(frame *model.PlayerTelemetryFrame, matchCtx *model.MatchContext, prevBlue, prevOrange int) {
	if fe.configuredBlueSign != 0 {
		return
	}
	if fe.learned.matchID != matchCtx.MatchID {
		fe.learned = goalSides{matchID: matchCtx.MatchID}
	}
	if fe.learned.known || frame.Disc == nil {
		return
	}
	goalZ := matchCtx.Physics.GoalZ
	if goalZ <= 0 {
		return
	}
	dz := frame.Disc.Position.Z()
	if math.Abs(dz) < goalZ*0.5 {
		// Disc already reset to centre (or nowhere near a goal): no information.
		return
	}
	blueDelta := frame.BlueScore - prevBlue
	orangeDelta := frame.OrangeScore - prevOrange
	// Echo Arena goals are worth 2 or 3 points; accept 1-3 to be safe and
	// require exactly one team to have scored.
	switch {
	case blueDelta >= 1 && blueDelta <= 3 && orangeDelta == 0:
		fe.learned.blueGoalZ = math.Copysign(goalZ, dz)
		fe.learned.known = true
	case orangeDelta >= 1 && orangeDelta <= 3 && blueDelta == 0:
		fe.learned.blueGoalZ = -math.Copysign(goalZ, dz)
		fe.learned.known = true
	}
}

// chooseGoal picks the goal a throw is measured against. When the thrower's
// attacking side is known the attacked goal is used; otherwise the goal the
// release velocity points at most closely (ties and near-zero velocity fall
// back to the nearest goal by distance). ok is false when no goal geometry
// is available.
func (fe *FeatureExtractor) chooseGoal(ps *model.PlayerState, matchCtx *model.MatchContext, releasePos, releaseVel model.Vec3) (goal model.Vec3, selection string, ok bool) {
	goalZ := matchCtx.Physics.GoalZ
	if goalZ <= 0 {
		return model.Vec3{}, "", false
	}
	team := ps.Team
	if team == "" && matchCtx.TeamAssignments != nil {
		team = matchCtx.TeamAssignments[ps.PlayerID]
	}
	if z, known := fe.TeamGoalZ(matchCtx, team); known {
		return model.Vec3{0, 0, z}, GoalSelectionTeam, true
	}

	goalPos := model.Vec3{0, 0, goalZ}
	goalNeg := model.Vec3{0, 0, -goalZ}
	if releaseVel.Magnitude() > 0.1 {
		devPos := releaseVel.AngleBetween(goalPos.Sub(releasePos))
		devNeg := releaseVel.AngleBetween(goalNeg.Sub(releasePos))
		if devPos < devNeg {
			return goalPos, GoalSelectionAngular, true
		}
		if devNeg < devPos {
			return goalNeg, GoalSelectionAngular, true
		}
	}
	if releasePos.Distance(goalNeg) < releasePos.Distance(goalPos) {
		return goalNeg, GoalSelectionNearest, true
	}
	return goalPos, GoalSelectionNearest, true
}

// tailIndex maps index i of a history of length refLen onto a history of
// length otherLen, aligning both from the most recent entry. Returns -1 when
// the other history has no entry for that frame.
func tailIndex(otherLen, refLen, i int) int {
	j := otherLen - (refLen - i)
	if j < 0 || j >= otherLen {
		return -1
	}
	return j
}

// buildPreReleaseSnapshots captures the last frames strictly before the
// release frame. It must be called before the release frame is pushed into
// the histories.
func (fe *FeatureExtractor) buildPreReleaseSnapshots(ps *model.PlayerState, frame *model.PlayerTelemetryFrame, throwingHand string) []model.ThrowFrameSnapshot {
	histLen := len(ps.PositionHistory)
	n := preReleaseSnapshotCount
	if histLen < n {
		n = histLen
	}
	if n <= 0 {
		return nil
	}
	handHist := ps.RightHandHistory
	rotHist := ps.RightHandRotHistory
	if throwingHand == "left" {
		handHist = ps.LeftHandHistory
		rotHist = ps.LeftHandRotHistory
	}
	discHist := fe.discHistory[ps.PlayerID]

	snaps := make([]model.ThrowFrameSnapshot, 0, n)
	for i := histLen - n; i < histLen; i++ {
		snap := model.ThrowFrameSnapshot{
			// Histories hold one entry per frame seen for this player; with
			// consecutive frames entry histLen-1 is frame N-1.
			FrameIndex:     frame.FrameIndex - (histLen - i),
			PlayerPosition: ps.PositionHistory[i],
		}
		ti := tailIndex(len(ps.TimestampHistory), histLen, i)
		if ti >= 0 {
			snap.Timestamp = ps.TimestampHistory[ti]
		}
		if hi := tailIndex(len(handHist), histLen, i); hi >= 0 {
			snap.HandPosition = handHist[hi]
			if hi > 0 && ti > 0 && !handHist[hi].IsZero() && !handHist[hi-1].IsZero() {
				hdt := ps.TimestampHistory[ti] - ps.TimestampHistory[ti-1]
				if hdt > 0 {
					snap.HandVelocity = handHist[hi].Sub(handHist[hi-1]).Scale(1.0 / model.Clamp(hdt, MinFrameDt, MaxFrameDt))
				}
			}
		}
		if ri := tailIndex(len(rotHist), histLen, i); ri >= 0 {
			snap.HandRotation = rotHist[ri]
		}
		if vi := tailIndex(len(ps.DiscVelocityHistory), histLen, i); vi >= 0 {
			snap.DiscVelocity = ps.DiscVelocityHistory[vi]
		} else {
			snap.DiscMissing = true
		}
		if di := tailIndex(len(discHist), histLen, i); di >= 0 {
			snap.DiscPosition = discHist[di].pos
			snap.DiscMissing = snap.DiscMissing || discHist[di].missing
		} else {
			snap.DiscMissing = true
		}
		snaps = append(snaps, snap)
	}
	return snaps
}

// detectThrow fires when a player releases the disc.
func (fe *FeatureExtractor) detectThrow(
	ps *model.PlayerState,
	frame *model.PlayerTelemetryFrame,
	matchCtx *model.MatchContext,
	prevLeftHand, prevRightHand model.Vec3,
	prevLeftHandRot, prevRightHandRot model.Quat,
) {
	disc := frame.Disc
	if disc == nil {
		return
	}

	// Release speed comes from the game-reported disc velocity, never from
	// position deltas, so it does not depend on the sampling interval.
	releaseVel := disc.Velocity
	releaseSpeed := disc.Speed
	if releaseSpeed <= 0 {
		releaseSpeed = releaseVel.Magnitude()
	}
	releasePos := disc.Position

	// Guard against NaN/Inf
	if math.IsNaN(releaseSpeed) || math.IsInf(releaseSpeed, 0) || releaseSpeed <= 0 || releaseVel.HasNaN() || releaseVel.HasInf() {
		return
	}

	// Determine throwing hand: which hand was closer to the disc at release
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

	// Release angle: world-frame angle between hand velocity and disc velocity
	releaseAngle := 0.0
	if handSpeed > 0.1 && releaseSpeed > 0.1 {
		releaseAngle = model.RadToDeg(handVel.AngleBetween(releaseVel))
	}

	// Possession duration
	possessionDuration := frame.Timestamp - ps.PossessionStartTime
	if possessionDuration < 0 {
		possessionDuration = 0
	}

	preRelease := fe.buildPreReleaseSnapshots(ps, frame, throwingHand)

	// Goal geometry: deviation of the release direction from the chosen goal.
	var targetPos *model.Vec3
	targetDev := 0.0
	goalPos, goalSelection, goalOK := fe.chooseGoal(ps, matchCtx, releasePos, releaseVel)
	if goalOK {
		toGoal := goalPos.Sub(releasePos)
		if releaseSpeed > 0.1 && toGoal.Magnitude() > 0.1 {
			targetDev = model.RadToDeg(releaseVel.AngleBetween(toGoal))
			if targetDev < goalDirectedMaxDeviationDeg {
				tp := goalPos
				targetPos = &tp
			}
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
		HandToDiscDistance:   handToDiscDist,
		ReleaseAngle:         releaseAngle,
		GoalPosition:         goalPos,
		GoalSelection:        goalSelection,
		TargetPosition:       targetPos,
		TargetDeviation:      targetDev,
		PossessionDuration:   possessionDuration,
		PreReleaseFrames:     preRelease,
	}

	ps.LastThrow = &throw
	ps.ThrowHistory = append(ps.ThrowHistory, throw)
	ps.ThrowCount++
}
