package model

// ThrowEvent represents a detected throw (disc release from a player).
type ThrowEvent struct {
	ThrowerID   string           `json:"thrower_id"`
	Attribution ThrowAttribution `json:"attribution"`

	FrameIndex int     `json:"frame_index"`
	Timestamp  float64 `json:"timestamp"`

	ReleasePosition Vec3    `json:"release_position"`
	ReleaseVelocity Vec3    `json:"release_velocity"`
	ReleaseSpeed    float64 `json:"release_speed"`

	ThrowingHand string `json:"throwing_hand"` // "left", "right", "unknown"
	HandPosition Vec3   `json:"hand_position"`
	HandVelocity Vec3   `json:"hand_velocity"`
	HandSpeed    float64 `json:"hand_speed"`

	WristOrientation     Quat    `json:"wrist_orientation"`
	WristAngularVelocity float64 `json:"wrist_angular_velocity"`

	PlayerPosition Vec3 `json:"player_position"`
	PlayerVelocity Vec3 `json:"player_velocity"`

	HandToDiscDistance float64 `json:"hand_to_disc_distance"`
	ReleaseAngle       float64 `json:"release_angle"` // degrees

	TargetPosition  *Vec3   `json:"target_position,omitempty"`
	TargetDeviation float64 `json:"target_deviation,omitempty"` // degrees

	PossessionDuration float64              `json:"possession_duration"`
	PreReleaseFrames   []ThrowFrameSnapshot `json:"pre_release_frames,omitempty"`
}

// ThrowAttribution holds the result of thrower identification.
type ThrowAttribution struct {
	PlayerID      string  `json:"player_id"`
	Confidence    float64 `json:"confidence"`
	Method        string  `json:"method"` // "possession_track", "proximity", "velocity_match", "fallback"
	LookbackDepth int     `json:"lookback_depth"`
}

// ThrowFrameSnapshot captures hand and disc state for a single frame near a throw event.
type ThrowFrameSnapshot struct {
	FrameIndex     int     `json:"frame_index"`
	Timestamp      float64 `json:"timestamp"`
	HandPosition   Vec3    `json:"hand_position"`
	HandVelocity   Vec3    `json:"hand_velocity"`
	HandRotation   Quat    `json:"hand_rotation"`
	DiscPosition   Vec3    `json:"disc_position"`
	DiscVelocity   Vec3    `json:"disc_velocity"`
	PlayerPosition Vec3    `json:"player_position"`
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
