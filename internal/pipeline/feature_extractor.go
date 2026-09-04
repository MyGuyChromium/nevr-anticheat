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
	//
	// MaxFrameDt is also the extractor's GAP threshold: a dt above it is too
	// long for finite differences, so kinematics and the throw state machine
	// skip that frame. It is the default for SetMaxFrameDt (config key
	// pipeline.max_frame_dt); the validator's hard rejection bound is the
	// separate MaxProducerDt.
	MinFrameDt = 0.005
	MaxFrameDt = 0.5

	// DefaultHighPingThresholdMs is the ping above which PlayerState.IsHighPing
	// is set. Override with SetHighPingThreshold (config high_ping_threshold_ms).
	DefaultHighPingThresholdMs = 150.0

	// playspaceMaxDt mirrors EchoTools' reconstruction guard. A wider gap is
	// too stale to distinguish physical movement from a recorder/network jump.
	playspaceMaxDt = 0.1
	// playspaceRecenteringMPS slowly pulls the predicted anchor toward the
	// tracked head so long-term drift does not accumulate forever.
	playspaceRecenteringMPS = 0.05

	// preReleaseSnapshotCount is how many frames before a release are captured.
	preReleaseSnapshotCount = 5

	// A sampled release can contain a headbutt that happened between the last
	// held-disc tick and the first free-disc tick. When the disc is within this
	// combined head/disc/tracking envelope and materially closer to the head
	// than either controller, its velocity cannot safely be treated as the
	// throwing hand's release vector.
	headContactDistanceM    = 0.42
	headContactCloserMargin = 0.08

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
	pos      model.Vec3
	missing  bool
	frameIdx int // frame index of the sample (histories are per frame SEEN)
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
	maxFrameDt          float64 // gap threshold and dt clamp upper bound (s)

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
		maxFrameDt:          MaxFrameDt,
		discHistory:         make(map[string][]discSample),
	}
}

// SetMaxFrameDt sets the gap threshold in seconds (config key
// pipeline.max_frame_dt): a frame whose real spacing from the player's
// previous frame exceeds it updates raw state but derives no kinematics and
// is never a throw release; known spacings are clamped to at most this
// value. Values that are not finite or not above MinFrameDt restore the
// default MaxFrameDt.
func (fe *FeatureExtractor) SetMaxFrameDt(seconds float64) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= MinFrameDt {
		seconds = MaxFrameDt
	}
	fe.maxFrameDt = seconds
}

// MaxFrameDtSeconds returns the current gap threshold in seconds.
func (fe *FeatureExtractor) MaxFrameDtSeconds() float64 {
	if fe.maxFrameDt <= 0 {
		return MaxFrameDt
	}
	return fe.maxFrameDt
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
	prevPlayspaceAnchor := ps.PlayspaceAnchor
	prevReportedVelocity := ps.ReportedVelocity
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
	ps.HasReportedVelocity = frame.ReportedVelocity != nil
	if frame.ReportedVelocity != nil {
		ps.ReportedVelocity = *frame.ReportedVelocity
	} else {
		ps.ReportedVelocity = model.Vec3{}
	}
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
	// than the configured max frame dt is too long for finite differences;
	// the frame still updates raw state but kinematics are skipped.
	maxDt := fe.MaxFrameDtSeconds()
	rawDt := frame.Timestamp - prevTimestamp
	dtKnown := !firstFrame && rawDt > 0 && !math.IsNaN(rawDt) && !math.IsInf(rawDt, 0)
	largeGap := dtKnown && rawDt > maxDt
	dt := 0.0
	if dtKnown {
		dt = model.Clamp(rawDt, MinFrameDt, maxDt)
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
			ps.LeftHandRelativeVelocity = ps.LeftHandVelocity.Sub(ps.Velocity)
			ps.LeftHandRelativeSpeed = ps.LeftHandRelativeVelocity.Magnitude()
		} else {
			ps.LeftHandVelocity = model.Vec3{}
			ps.LeftHandSpeed = 0
			ps.LeftHandRelativeVelocity = model.Vec3{}
			ps.LeftHandRelativeSpeed = 0
		}
		if !prevRightHand.IsZero() && !frame.RightHandPosition.IsZero() {
			ps.RightHandVelocity = frame.RightHandPosition.Sub(prevRightHand).Scale(1.0 / dt)
			ps.RightHandSpeed = ps.RightHandVelocity.Magnitude()
			ps.RightHandRelativeVelocity = ps.RightHandVelocity.Sub(ps.Velocity)
			ps.RightHandRelativeSpeed = ps.RightHandRelativeVelocity.Magnitude()
		} else {
			ps.RightHandVelocity = model.Vec3{}
			ps.RightHandSpeed = 0
			ps.RightHandRelativeVelocity = model.Vec3{}
			ps.RightHandRelativeSpeed = 0
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
		ps.LeftHandSpeedStats.Update(ps.LeftHandRelativeSpeed)
		ps.RightHandSpeedStats.Update(ps.RightHandRelativeSpeed)
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
		ps.LeftHandRelativeVelocity = model.Vec3{}
		ps.RightHandRelativeVelocity = model.Vec3{}
		ps.LeftHandRelativeSpeed = 0
		ps.RightHandRelativeSpeed = 0
		ps.LeftWristAngularRate = 0
		ps.RightWristAngularRate = 0
	}

	// EchoTools/Spark reconstructs physical playspace motion by advancing an
	// arena-space anchor with the game's reported velocity, then comparing the
	// actual tracked pose with that prediction. Stacking and ordinary game
	// movement are present in ReportedVelocity and subtract away; physical
	// room-scale steps remain in the residual.
	playspaceValid := kinematicsValid && ps.HasReportedVelocity && rawDt < playspaceMaxDt &&
		matchCtx.IsActivePhase(frame.GamePhase)
	if !playspaceValid {
		ps.PlayspaceAnchor = frame.Position
		ps.PlayspaceOffset = model.Vec3{}
		ps.PlayspaceDistance = 0
		ps.PlayspaceVelocity = model.Vec3{}
		ps.PlayspaceSpeed = 0
		ps.PlayspaceRigCoherence = 0
		ps.PlayspaceTrackedHands = 0
		ps.PlayspaceValid = false
		ps.MovementOrigin = ""
	} else {
		anchor := prevPlayspaceAnchor.Add(ps.ReportedVelocity.Scale(dt))
		offset := frame.Position.Sub(anchor)
		if distance := offset.Magnitude(); distance > 0 {
			step := math.Min(distance, playspaceRecenteringMPS*dt)
			anchor = anchor.Add(offset.Normalized().Scale(step))
		}
		ps.PlayspaceAnchor = anchor
		ps.PlayspaceOffset = frame.Position.Sub(anchor)
		ps.PlayspaceDistance = ps.PlayspaceOffset.Magnitude()
		ps.PlayspaceVelocity = ps.Velocity.Sub(ps.ReportedVelocity)
		ps.PlayspaceSpeed = ps.PlayspaceVelocity.Magnitude()
		ps.PlayspaceRigCoherence, ps.PlayspaceTrackedHands = playspaceRigCoherence(
			frame.Position.Sub(prevPos).Sub(ps.ReportedVelocity.Scale(dt)),
			prevLeftHand, frame.LeftHandPosition,
			prevRightHand, frame.RightHandPosition,
			ps.ReportedVelocity.Scale(dt),
		)
		ps.PlayspaceValid = true
		const movementFloor = 0.25
		physical, game := ps.PlayspaceSpeed >= movementFloor, ps.ReportedVelocity.Magnitude() >= movementFloor
		switch {
		case physical && game:
			ps.MovementOrigin = "mixed"
		case physical:
			ps.MovementOrigin = "playspace_step"
		case game:
			ps.MovementOrigin = "game_velocity"
		default:
			ps.MovementOrigin = "stationary"
		}
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
	// A release first seen after a gap (dt unknown, or longer than the max
	// frame dt) is not a measured release either: the disc velocity on that
	// frame is mid-flight state, the hand kinematics were cleared and the
	// pre-release snapshots predate the gap, so every consumer (release
	// speed, signature, spread, trajectory anchor) would be fed the wrong
	// moment. Such a release is skipped, consistent with "gap = no
	// kinematics"; the next possession starts a fresh track.
	if prevHasDisc && !ps.HasDisc && !firstFrame && dtKnown && !largeGap && matchCtx.IsActivePhase(frame.GamePhase) {
		possessionFrames := frame.FrameIndex - ps.PossessionStartFrame
		if possessionFrames >= 2 {
			fe.detectThrow(ps, frame, matchCtx, prevPos, prevLeftHand, prevRightHand, prevLeftHandRot, prevRightHandRot)
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
	fe.pushDiscHistory(ps, frame.Disc, frame.FrameIndex)

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

	ps.LegalContext = buildLegalMotionContext(ps, frame, prevReportedVelocity, dtKnown, rawDt)

	ps.FrameCount++
}

// buildLegalMotionContext centralizes legitimate-motion interpretation for
// every detector. The source cannot identify the object or player involved in
// a collision, so contact flags are intentionally phrased as candidates.
func buildLegalMotionContext(ps *model.PlayerState, frame *model.PlayerTelemetryFrame, previousReported model.Vec3, dtKnown bool, dt float64) model.LegalMotionContext {
	ctx := model.LegalMotionContext{
		Boosting:       ps.IsBoosting,
		GameLocomotion: ps.HasReportedVelocity && ps.ReportedVelocity.Magnitude() >= 0.25,
		TrackingLimited: frame.LeftHandPosition.IsZero() || frame.RightHandPosition.IsZero() ||
			!frame.LeftHandRotation.IsUnit() || !frame.RightHandRotation.IsUnit(),
		Confidence: 1,
	}
	if ps.IsHighPing {
		ctx.Confidence *= 0.75
	}
	if ctx.TrackingLimited {
		ctx.Confidence *= 0.65
	}
	if ps.PlayspaceValid && ps.PlayspaceRigCoherence >= 0.4 {
		ctx.Leaning = ps.PlayspaceDistance >= 0.08 && ps.PlayspaceDistance <= 0.65 && ps.PlayspaceSpeed < 0.35
		ctx.PlayspaceStep = !ps.IsBoosting && ps.PlayspaceSpeed >= 0.35 && ps.PlayspaceSpeed <= 2.2
	}
	if dtKnown && dt > 0 && !ps.IsBoosting && ps.HasReportedVelocity {
		reportedAcceleration := ps.ReportedVelocity.Sub(previousReported).Magnitude() / dt
		handBurst := math.Max(ps.LeftHandRelativeSpeed, ps.RightHandRelativeSpeed)
		ctx.PossibleSlapOrPush = reportedAcceleration >= 6 && handBurst >= 1.2
	}
	if ps.LastThrow != nil && ps.LastThrow.FrameIndex == frame.FrameIndex {
		ctx.PossibleHeadContact = ps.LastThrow.PossibleHeadContact
	}
	ctx.CannotDistinguishContact = ctx.PossibleSlapOrPush || (ctx.GameLocomotion && !ctx.Boosting)
	switch {
	case ctx.PossibleHeadContact:
		ctx.PrimaryExplanation = "possible legal head contact at disc release"
	case ctx.Boosting:
		ctx.PrimaryExplanation = "game-reported boost"
	case ctx.PossibleSlapOrPush:
		ctx.PrimaryExplanation = "possible legal wall/block slap or player push"
	case ctx.PlayspaceStep:
		ctx.PrimaryExplanation = "coherent physical playspace step"
	case ctx.Leaning:
		ctx.PrimaryExplanation = "coherent lean inside the tracked playspace"
	case ctx.GameLocomotion:
		ctx.PrimaryExplanation = "game-authored locomotion (stack, grab, push, or ordinary movement may contribute)"
	default:
		ctx.PrimaryExplanation = "no special legal-motion context identified"
	}
	return ctx
}

// playspaceRigCoherence measures whether tracked hands translated with the
// head after game velocity was removed. A physical step normally moves the
// whole tracked rig; independent controller swings do not. The returned
// score is [0,1], averaged across available hands, plus the hand count.
func playspaceRigCoherence(bodyResidual, prevLeft, left, prevRight, right, expectedGameDelta model.Vec3) (float64, int) {
	var sum float64
	tracked := 0
	add := func(prev, current model.Vec3) {
		if prev.IsZero() || current.IsZero() {
			return
		}
		handResidual := current.Sub(prev).Sub(expectedGameDelta)
		difference := handResidual.Sub(bodyResidual).Magnitude()
		// Twelve centimetres covers controller jitter and ordinary small arm
		// motion. Scale with the body residual so a large coherent step is not
		// rejected for a proportionally larger tracking discrepancy.
		tolerance := math.Max(0.12, bodyResidual.Magnitude()*0.75)
		sum += model.Clamp(1.0-difference/(2.0*tolerance), 0, 1)
		tracked++
	}
	add(prevLeft, left)
	add(prevRight, right)
	if tracked == 0 {
		return 0, 0
	}
	return sum / float64(tracked), tracked
}

// pushDiscHistory advances the disc velocity history on PlayerState and the
// extractor-side disc position history (which also records the frame index
// of every sample). A frame without disc state pushes a zero placeholder
// (flagged missing) so both stay index-aligned with PositionHistory.
func (fe *FeatureExtractor) pushDiscHistory(ps *model.PlayerState, disc *model.DiscState, frameIdx int) {
	sample := discSample{missing: true, frameIdx: frameIdx}
	vel := model.Vec3{}
	if disc != nil {
		sample = discSample{pos: disc.Position, frameIdx: frameIdx}
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
	var handHist []model.Vec3
	var rotHist []model.Quat
	switch throwingHand {
	case "right":
		handHist = ps.RightHandHistory
		rotHist = ps.RightHandRotHistory
	case "left":
		handHist = ps.LeftHandHistory
		rotHist = ps.LeftHandRotHistory
	}
	discHist := fe.discHistory[ps.PlayerID]

	maxDt := fe.MaxFrameDtSeconds()
	snaps := make([]model.ThrowFrameSnapshot, 0, n)
	for i := histLen - n; i < histLen; i++ {
		snap := model.ThrowFrameSnapshot{
			// Histories hold one entry per frame SEEN for this player (a
			// rejected frame or a missed poll leaves a hole), so the label
			// comes from the recorded frame index of the sample; the
			// consecutive-frames arithmetic is only the fallback when the
			// extractor-side history was evicted.
			FrameIndex:     frame.FrameIndex - (histLen - i),
			PlayerPosition: ps.PositionHistory[i],
		}
		if di := tailIndex(len(discHist), histLen, i); di >= 0 {
			snap.FrameIndex = discHist[di].frameIdx
		}
		ti := tailIndex(len(ps.TimestampHistory), histLen, i)
		if ti >= 0 {
			snap.Timestamp = ps.TimestampHistory[ti]
		}
		if hi := tailIndex(len(handHist), histLen, i); hi >= 0 {
			snap.HandPosition = handHist[hi]
			if hi > 0 && ti > 0 && !handHist[hi].IsZero() && !handHist[hi-1].IsZero() {
				hdt := ps.TimestampHistory[ti] - ps.TimestampHistory[ti-1]
				if hdt > 0 && hdt <= maxDt {
					snap.HandVelocity = handHist[hi].Sub(handHist[hi-1]).Scale(1.0 / model.Clamp(hdt, MinFrameDt, maxDt))
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

// throwHandAnchor returns the best observed disc position for choosing the
// throwing hand. Possession transitions are first observed after the disc has
// become free, so the current disc may already be more than a metre from the
// releasing hand. The prior held-frame position is the correctly aligned
// anchor when available.
func (fe *FeatureExtractor) throwHandAnchor(playerID string, releasePos model.Vec3) (model.Vec3, string, float64) {
	hist := fe.discHistory[playerID]
	if len(hist) > 0 {
		last := hist[len(hist)-1]
		if !last.missing {
			return last.pos, "previous_held_disc", 1.0
		}
	}
	return releasePos, "current_free_disc", 0.5
}

// selectThrowingHand chooses only among tracked hands. Confidence is a
// transparent geometry-quality score: 1 means one hand is at the anchor and
// the other is far away, 0 means the two tracked hands are equally close.
// With only one tracked hand there is no safe way to know whether it or the
// missing hand released the disc, so the result stays unknown.
func selectThrowingHand(left, right, anchor model.Vec3, anchorQuality float64) (string, model.Vec3, float64, float64) {
	leftTracked := !left.IsZero()
	rightTracked := !right.IsZero()
	switch {
	case leftTracked && rightTracked:
		leftDist := left.Distance(anchor)
		rightDist := right.Distance(anchor)
		maxDist := math.Max(leftDist, rightDist)
		confidence := 0.0
		if maxDist > 0 {
			confidence = math.Abs(leftDist-rightDist) / maxDist
		}
		confidence = model.Clamp01(confidence * anchorQuality)
		if leftDist < rightDist {
			return "left", left, leftDist, confidence
		}
		return "right", right, rightDist, confidence
	default:
		return "unknown", model.Vec3{}, 0, 0
	}
}

// detectThrow fires when a player releases the disc.
func (fe *FeatureExtractor) detectThrow(
	ps *model.PlayerState,
	frame *model.PlayerTelemetryFrame,
	matchCtx *model.MatchContext,
	prevHead model.Vec3,
	prevLeftHand, prevRightHand model.Vec3,
	prevLeftHandRot, prevRightHandRot model.Quat,
) {
	disc := frame.Disc
	if disc == nil {
		return
	}

	// Release speed comes from game telemetry, never from position deltas, so
	// it does not depend on the sampling interval. For local-client throws the
	// engine's last_throw.total_speed is the authoritative value; the sampled
	// disc magnitude is retained separately for corroboration.
	releaseVel := disc.Velocity
	sampledDiscSpeed := disc.Speed
	if sampledDiscSpeed <= 0 {
		sampledDiscSpeed = releaseVel.Magnitude()
	}
	releaseSpeed := sampledDiscSpeed
	var gameLastThrow *model.GameThrowDetails
	if frame.GameLastThrow != nil && frame.GameLastThrow.Valid() {
		details := *frame.GameLastThrow
		gameLastThrow = &details
		releaseSpeed = math.Max(releaseSpeed, details.TotalSpeed)
	}
	releasePos := disc.Position

	// Guard against NaN/Inf
	if math.IsNaN(releaseSpeed) || math.IsInf(releaseSpeed, 0) || releaseSpeed <= 0 || releaseVel.HasNaN() || releaseVel.HasInf() {
		return
	}

	// Choose the hand against the prior held-disc position when possible.
	// Never treat the zero-vector tracking-loss sentinel as a real hand.
	handAnchor, handAnchorName, anchorQuality := fe.throwHandAnchor(ps.PlayerID, releasePos)
	throwingHand, handPos, handToDiscDist, handAttributionConfidence :=
		selectThrowingHand(prevLeftHand, prevRightHand, handAnchor, anchorQuality)
	releaseHandDist := nearestTrackedHandDistance(releasePos, frame.LeftHandPosition, frame.RightHandPosition)
	headDist := releasePos.Distance(ps.Position)
	if !prevHead.IsZero() {
		headDist = math.Min(headDist, releasePos.Distance(prevHead))
	}
	possibleHeadContact := headDist <= headContactDistanceM && releaseHandDist >= 0 &&
		headDist+headContactCloserMargin < releaseHandDist

	var handVel, handRelativeVel model.Vec3
	var handSpeed, handRelativeSpeed, wristAngVel float64
	var wristRot model.Quat
	handKinematicsValid := false
	switch throwingHand {
	case "left":
		handVel = ps.LeftHandVelocity
		handSpeed = ps.LeftHandSpeed
		handRelativeVel = ps.LeftHandRelativeVelocity
		handRelativeSpeed = ps.LeftHandRelativeSpeed
		wristRot = prevLeftHandRot
		wristAngVel = ps.LeftWristAngularRate
		handKinematicsValid = !frame.LeftHandPosition.IsZero()
	case "right":
		handVel = ps.RightHandVelocity
		handSpeed = ps.RightHandSpeed
		handRelativeVel = ps.RightHandRelativeVelocity
		handRelativeSpeed = ps.RightHandRelativeSpeed
		wristRot = prevRightHandRot
		wristAngVel = ps.RightWristAngularRate
		handKinematicsValid = !frame.RightHandPosition.IsZero()
	default:
		handAnchorName = "none"
	}

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

	attribution := model.ThrowAttribution{
		PlayerID: ps.PlayerID, Confidence: 0.9,
		Method: "possession_track", LookbackDepth: 1,
	}
	if gameLastThrow != nil {
		attribution.Confidence = 1
		attribution.Method = "game_last_throw"
		attribution.LookbackDepth = 0
	}

	throw := model.ThrowEvent{
		ThrowerID:                 ps.PlayerID,
		Attribution:               attribution,
		FrameIndex:                frame.FrameIndex,
		Timestamp:                 frame.Timestamp,
		ReleasePosition:           releasePos,
		ReleaseVelocity:           releaseVel,
		ReleaseSpeed:              releaseSpeed,
		SampledDiscSpeed:          sampledDiscSpeed,
		GameLastThrow:             gameLastThrow,
		ThrowingHand:              throwingHand,
		HandPosition:              handPos,
		HandVelocity:              handVel,
		HandSpeed:                 handSpeed,
		HandRelativeVelocity:      handRelativeVel,
		HandRelativeSpeed:         handRelativeSpeed,
		HandTracked:               throwingHand != "unknown",
		HandKinematicsValid:       handKinematicsValid,
		HandAttributionConfidence: handAttributionConfidence,
		HandAttributionAnchor:     handAnchorName,
		WristOrientation:          wristRot,
		WristAngularVelocity:      wristAngVel,
		PlayerPosition:            ps.Position,
		PlayerVelocity:            ps.Velocity,
		HandToDiscDistance:        handToDiscDist,
		ReleaseHandToDiscDistance: releaseHandDist,
		HeadToDiscDistance:        headDist,
		PossibleHeadContact:       possibleHeadContact,
		ReleaseAngle:              releaseAngle,
		GoalPosition:              goalPos,
		GoalSelection:             goalSelection,
		TargetPosition:            targetPos,
		TargetDeviation:           targetDev,
		PossessionDuration:        possessionDuration,
		PreReleaseFrames:          preRelease,
	}

	ps.LastThrow = &throw
	ps.ThrowHistory = append(ps.ThrowHistory, throw)
	ps.ThrowCount++
}

// nearestTrackedHandDistance returns -1 when neither controller is tracked.
// A zero position is the telemetry contract's tracking-loss sentinel.
func nearestTrackedHandDistance(point, left, right model.Vec3) float64 {
	distance := -1.0
	if !left.IsZero() {
		distance = point.Distance(left)
	}
	if !right.IsZero() {
		rightDistance := point.Distance(right)
		if distance < 0 || rightDistance < distance {
			distance = rightDistance
		}
	}
	return distance
}
