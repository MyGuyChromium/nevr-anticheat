package model

// Goal selection labels recorded in ThrowEvent.GoalSelection. Only
// GoalSelectionTeam means the attacked goal is actually known; the other two
// are guesses from the release geometry, and detectors that judge a flight
// against a goal (THROW_006) treat them as "side unknown".
const (
	GoalSelectionTeam    = "team"    // goal the thrower's team attacks (configured or learned)
	GoalSelectionAngular = "angular" // goal the release velocity points at most closely
	GoalSelectionNearest = "nearest" // nearest goal by distance (release velocity too small to aim)
)

// ThrowEvent represents a detected throw (disc release from a player).
type ThrowEvent struct {
	ThrowerID   string           `json:"thrower_id"`
	Attribution ThrowAttribution `json:"attribution"`

	FrameIndex int     `json:"frame_index"`
	Timestamp  float64 `json:"timestamp"`

	ReleasePosition Vec3    `json:"release_position"`
	ReleaseVelocity Vec3    `json:"release_velocity"`
	ReleaseSpeed    float64 `json:"release_speed"`
	// SampledDiscSpeed is the magnitude reported on the disc snapshot at
	// release. ReleaseSpeed uses the higher of this sample and the engine's
	// last_throw.total_speed; both are retained so reviewers can compare them.
	SampledDiscSpeed float64 `json:"sampled_disc_speed,omitempty"`
	// GameLastThrow is the engine-authored component breakdown when this is a
	// local-client throw and the source supplies Echo VR's last_throw object.
	GameLastThrow *GameThrowDetails `json:"game_last_throw,omitempty"`

	ThrowingHand string  `json:"throwing_hand"` // "left", "right", "unknown"
	HandPosition Vec3    `json:"hand_position"`
	HandVelocity Vec3    `json:"hand_velocity"`
	HandSpeed    float64 `json:"hand_speed"`
	// HandRelativeVelocity removes player translation from HandVelocity.
	// HandAttributionConfidence describes only the left/right hand choice;
	// Attribution.Confidence independently describes the thrower identity.
	HandRelativeVelocity      Vec3    `json:"hand_relative_velocity"`
	HandRelativeSpeed         float64 `json:"hand_relative_speed"`
	HandTracked               bool    `json:"hand_tracked"`
	HandKinematicsValid       bool    `json:"hand_kinematics_valid"`
	HandAttributionConfidence float64 `json:"hand_attribution_confidence"`
	HandAttributionAnchor     string  `json:"hand_attribution_anchor,omitempty"`

	WristOrientation     Quat    `json:"wrist_orientation"`
	WristAngularVelocity float64 `json:"wrist_angular_velocity"`

	PlayerPosition Vec3 `json:"player_position"`
	PlayerVelocity Vec3 `json:"player_velocity"`

	// HandToDiscDistance is measured against HandAttributionAnchor. Normally
	// that is the held disc position from the frame immediately before release;
	// the current free-disc position is only a lower-quality fallback.
	HandToDiscDistance float64 `json:"hand_to_disc_distance"`
	// ReleaseAngle is the world-frame angle (degrees) between the throwing
	// hand's velocity and the disc's release velocity. Because both vectors
	// are measured in the world frame, body translation contributes to it;
	// detectors must account for PlayerVelocity before treating a large angle
	// as physically impossible.
	ReleaseAngle float64 `json:"release_angle"` // degrees

	// GoalPosition is the goal this throw is measured against. It is the goal
	// the thrower's team attacks when the team side is known, otherwise the
	// goal the release velocity points at most closely. Zero when no goal
	// geometry is available. GoalSelection records how it was chosen
	// ("team", "angular" or "nearest").
	GoalPosition  Vec3   `json:"goal_position"`
	GoalSelection string `json:"goal_selection,omitempty"`

	// TargetPosition is GoalPosition when the throw is goal-directed
	// (TargetDeviation below the goal-directed threshold), nil otherwise.
	TargetPosition  *Vec3   `json:"target_position,omitempty"`
	TargetDeviation float64 `json:"target_deviation,omitempty"` // degrees

	PossessionDuration float64 `json:"possession_duration"`
	// PreReleaseFrames are snapshots of the frames immediately BEFORE the
	// release frame (oldest first). The release frame itself is never included.
	PreReleaseFrames []ThrowFrameSnapshot `json:"pre_release_frames,omitempty"`
}

// ThrowAttribution holds the result of thrower identification.
type ThrowAttribution struct {
	PlayerID      string  `json:"player_id"`
	Confidence    float64 `json:"confidence"`
	Method        string  `json:"method"` // "possession_track", "proximity", "velocity_match", "fallback"
	LookbackDepth int     `json:"lookback_depth"`
}

// ThrowFrameSnapshot captures hand and disc state for a single frame near a throw event.
// Hand fields refer to the throwing hand.
type ThrowFrameSnapshot struct {
	FrameIndex     int     `json:"frame_index"`
	Timestamp      float64 `json:"timestamp"`
	HandPosition   Vec3    `json:"hand_position"`
	HandVelocity   Vec3    `json:"hand_velocity"`
	HandRotation   Quat    `json:"hand_rotation"`
	DiscPosition   Vec3    `json:"disc_position"`
	DiscVelocity   Vec3    `json:"disc_velocity"`
	PlayerPosition Vec3    `json:"player_position"`
	// DiscMissing is true when the source frame carried no disc state; the
	// disc fields are then zero placeholders, not observations.
	DiscMissing bool `json:"disc_missing,omitempty"`
}

// PossessionEvent represents a change in disc possession.
type PossessionEvent struct {
	EventType           string  `json:"event_type"` // "pickup", "steal", "catch", "drop", "goal_reset"
	PlayerID            string  `json:"player_id"`
	PreviousPossessorID string  `json:"previous_possessor_id,omitempty"`
	FrameIndex          int     `json:"frame_index"`
	Timestamp           float64 `json:"timestamp"`
	Position            Vec3    `json:"position"`
	DiscSpeed           float64 `json:"disc_speed"`
	PlayerSpeed         float64 `json:"player_speed"`
	GrabDistance        float64 `json:"grab_distance,omitempty"`
}

// MovementSnapshot captures a player's movement state at a point in time.
type MovementSnapshot struct {
	PlayerID              string  `json:"player_id"`
	FrameIndex            int     `json:"frame_index"`
	Timestamp             float64 `json:"timestamp"`
	Position              Vec3    `json:"position"`
	Velocity              Vec3    `json:"velocity"`
	Speed                 float64 `json:"speed"`
	Acceleration          Vec3    `json:"acceleration"`
	AccelerationMagnitude float64 `json:"acceleration_magnitude"`
	IsBoosting            bool    `json:"is_boosting"`
	IsStunned             bool    `json:"is_stunned"`
}
