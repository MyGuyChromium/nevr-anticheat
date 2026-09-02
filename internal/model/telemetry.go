package model

// PlayerTelemetryFrame represents a single frame of telemetry data for one player.
type PlayerTelemetryFrame struct {
	PlayerID  string  `json:"player_id"`
	// Team is "blue" or "orange" when known, empty otherwise. Producers
	// (adapter.Mapper, cmd/bridge) populate it; ingest uses it to build
	// MatchContext.TeamAssignments for live matches.
	Team string `json:"team,omitempty"`
	FrameIndex int    `json:"frame_index"`
	Timestamp  float64 `json:"timestamp"`
	DeltaTime  float64 `json:"delta_time"`

	Position Vec3 `json:"position"`
	Rotation Quat `json:"rotation"`

	LeftHandPosition  Vec3 `json:"left_hand_position"`
	RightHandPosition Vec3 `json:"right_hand_position"`
	LeftHandRotation  Quat `json:"left_hand_rotation"`
	RightHandRotation Quat `json:"right_hand_rotation"`

	IsStunned     bool `json:"is_stunned"`
	IsBoosting    bool `json:"is_boosting"`
	ShieldActive  bool `json:"shield_active"`
	IsImmune      bool `json:"is_immune"`
	HasPossession bool `json:"has_possession"`

	Disc *DiscState `json:"disc,omitempty"`

	EstimatedPingMs float64 `json:"estimated_ping_ms,omitempty"`

	// Game phase: "playing", "round_start", "round_over", "pre_match", "post_match"
	GamePhase string `json:"game_phase,omitempty"`

	// Team scores for state validation
	BlueScore   int `json:"blue_score,omitempty"`
	OrangeScore int `json:"orange_score,omitempty"`
	Goals       int `json:"goals,omitempty"`
	Stuns       int `json:"stuns,omitempty"`
}

// DiscState represents the state of the disc at a single frame.
type DiscState struct {
	Position Vec3 `json:"position"`
	Velocity Vec3 `json:"velocity"`
	Speed    float64 `json:"speed"`

	PreviousVelocity      *Vec3   `json:"previous_velocity,omitempty"`
	Acceleration          *Vec3   `json:"acceleration,omitempty"`
	AccelerationMagnitude float64 `json:"acceleration_magnitude,omitempty"`

	PossessorID string `json:"possessor_id,omitempty"`
	IsHeld      bool   `json:"is_held"`

	FramesSinceRelease int    `json:"frames_since_release"`
	ReleasePosition    *Vec3  `json:"release_position,omitempty"`
	ReleaseVelocity    *Vec3  `json:"release_velocity,omitempty"`

	TrajectoryAngleChange float64 `json:"trajectory_angle_change,omitempty"`
	DistanceFromThrower   float64 `json:"distance_from_thrower,omitempty"`
	InPenaltyField        bool    `json:"in_penalty_field"`
}

// ControllerState represents derived hand/controller state.
type ControllerState struct {
	Hand                  string  `json:"hand"`
	Position              Vec3    `json:"position"`
	Rotation              Quat    `json:"rotation"`
	Velocity              Vec3    `json:"velocity"`
	Speed                 float64 `json:"speed"`
	Acceleration          Vec3    `json:"acceleration"`
	AccelerationMagnitude float64 `json:"acceleration_magnitude"`
	AngularVelocity       float64 `json:"angular_velocity"`
	Jerk                  Vec3    `json:"jerk,omitempty"`
	JerkMagnitude         float64 `json:"jerk_magnitude,omitempty"`
	DistanceFromBody      float64 `json:"distance_from_body"`
	PositionVariance      float64 `json:"position_variance,omitempty"`
	RotationVariance      float64 `json:"rotation_variance,omitempty"`
}
