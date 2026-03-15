package model

// ThrowEvidence contains evidence from throw-speed related detectors.
type ThrowEvidence struct {
	ReleaseVelocity Vec3    `json:"release_velocity"`
	ReleaseSpeed    float64 `json:"release_speed"`
	ReleasePosition Vec3    `json:"release_position"`
	HandVelocity    Vec3    `json:"hand_velocity"`
	HandSpeed       float64 `json:"hand_speed"`
	SpeedRatio      float64 `json:"speed_ratio"`
	EffectiveCap    float64 `json:"effective_cap"`
	PingMs          float64 `json:"ping_ms"`
}

func (ThrowEvidence) EvidenceType() string { return "throw" }

// DiscAccelerationEvidence for THROW_002.
type DiscAccelerationEvidence struct {
	ReleaseAcceleration    float64              `json:"release_acceleration"`
	AveragePreAcceleration float64              `json:"average_pre_acceleration"`
	AccelerationRatio      float64              `json:"acceleration_ratio"`
	ReleaseSpeed           float64              `json:"release_speed"`
	PreReleaseSpeed        float64              `json:"pre_release_speed"`
	PreReleaseFrames       []ThrowFrameSnapshot `json:"pre_release_frames,omitempty"`
}

func (DiscAccelerationEvidence) EvidenceType() string { return "disc_acceleration" }

// ReleaseAngleEvidence for THROW_003.
type ReleaseAngleEvidence struct {
	ReleaseAngle     float64 `json:"release_angle"`
	HandVelocity     Vec3    `json:"hand_velocity"`
	DiscVelocity     Vec3    `json:"disc_velocity"`
	HandSpeed        float64 `json:"hand_speed"`
	DiscSpeed        float64 `json:"disc_speed"`
	WristOrientation Quat    `json:"wrist_orientation"`
	ThrowingHand     string  `json:"throwing_hand"`
}

func (ReleaseAngleEvidence) EvidenceType() string { return "release_angle" }

// SignatureRepeatEvidence for THROW_004.
type SignatureRepeatEvidence struct {
	ThrowCount          int       `json:"throw_count"`
	GeneralizedVariance float64   `json:"generalized_variance"`
	DimensionVariances  []float64 `json:"dimension_variances"`
	DimensionNames      []string  `json:"dimension_names"`
	BhattacharyyaCoeff  float64   `json:"bhattacharyya_coeff"`
}

func (SignatureRepeatEvidence) EvidenceType() string { return "signature_repeat" }

// PrecisionEvidence for THROW_005.
type PrecisionEvidence struct {
	GoalDirectedThrows  int       `json:"goal_directed_throws"`
	MeanDeviation       float64   `json:"mean_deviation"`
	StddevDeviation     float64   `json:"stddev_deviation"`
	MinDeviation        float64   `json:"min_deviation"`
	MaxDeviation        float64   `json:"max_deviation"`
	DeviationHistory    []float64 `json:"deviation_history,omitempty"`
	PopulationMeanDev   float64   `json:"population_mean_dev"`
	PopulationStddevDev float64   `json:"population_stddev_dev"`
}

func (PrecisionEvidence) EvidenceType() string { return "precision" }

// TrajectoryEvidence for THROW_006.
type TrajectoryEvidence struct {
	CumulativeAngleChange float64 `json:"cumulative_angle_change"`
	MaxSingleFrameChange  float64 `json:"max_single_frame_change"`
	ViolationFrameCount   int     `json:"violation_frame_count"`
	TotalTrackedFrames    int     `json:"total_tracked_frames"`
	DistanceTraveled      float64 `json:"distance_traveled"`
	ReleaseSpeed          float64 `json:"release_speed"`
	FinalSpeed            float64 `json:"final_speed"`
	CorrectionTarget      string  `json:"correction_target"`
	CorrectionConfidence  float64 `json:"correction_confidence"`
	TrajectoryPoints      []Vec3  `json:"trajectory_points,omitempty"`
	VelocityPoints        []Vec3  `json:"velocity_points,omitempty"`
}

func (TrajectoryEvidence) EvidenceType() string { return "trajectory" }

// PenaltyFieldEvidence for THROW_007.
type PenaltyFieldEvidence struct {
	EntrySpeed        float64 `json:"entry_speed"`
	ExitSpeed         float64 `json:"exit_speed"`
	ActualLossRatio   float64 `json:"actual_loss_ratio"`
	ExpectedLossRatio float64 `json:"expected_loss_ratio"`
	LossDeficit       float64 `json:"loss_deficit"`
	FramesInField     int     `json:"frames_in_field"`
}

func (PenaltyFieldEvidence) EvidenceType() string { return "penalty_field" }

// SpeedDistanceEvidence for THROW_008.
type SpeedDistanceEvidence struct {
	SpeedIncreaseCount  int          `json:"speed_increase_count"`
	CurveViolationCount int          `json:"curve_violation_count"`
	MaxSpeedIncrease    float64      `json:"max_speed_increase"`
	MaxCurveExcess      float64      `json:"max_curve_excess"`
	SpeedDistanceSamples [][2]float64 `json:"speed_distance_samples,omitempty"`
}

func (SpeedDistanceEvidence) EvidenceType() string { return "speed_distance" }

// WristRotationEvidence for BIO_001.
type WristRotationEvidence struct {
	Hand              string  `json:"hand"`
	AngularVelocity   float64 `json:"angular_velocity"`
	ConsecutiveFrames int     `json:"consecutive_frames"`
	RunningMean       float64 `json:"running_mean"`
	RunningStdDev     float64 `json:"running_stddev"`
	MaxObserved       float64 `json:"max_observed"`
	FrameDt           float64 `json:"frame_dt"`
	PhysicalLimit     float64 `json:"physical_limit"`
}

func (WristRotationEvidence) EvidenceType() string { return "wrist_rotation" }

// HandSpeedEvidence for BIO_002.
type HandSpeedEvidence struct {
	Hand              string  `json:"hand"`
	Speed             float64 `json:"speed"`
	ConsecutiveFrames int     `json:"consecutive_frames"`
	FrameDt           float64 `json:"frame_dt"`
	PhysicalLimit     float64 `json:"physical_limit"`
	PlayerSpeed       float64 `json:"player_speed"`
	SpeedRatio        float64 `json:"speed_ratio"`
}

func (HandSpeedEvidence) EvidenceType() string { return "hand_speed" }

// ZeroJitterEvidence for BIO_003.
type ZeroJitterEvidence struct {
	Hand             string  `json:"hand"`
	PositionVariance float64 `json:"position_variance"`
	WindowFrames     int     `json:"window_frames"`
	ActiveFrames     int     `json:"active_frames"`
	Threshold        float64 `json:"threshold"`
	PlayerSpeed      float64 `json:"player_speed"`
}

func (ZeroJitterEvidence) EvidenceType() string { return "zero_jitter" }

// ZeroWobbleEvidence for BIO_004.
type ZeroWobbleEvidence struct {
	Hand             string  `json:"hand"`
	RotationVariance float64 `json:"rotation_variance"`
	WindowFrames     int     `json:"window_frames"`
	Threshold        float64 `json:"threshold"`
	PlayerSpeed      float64 `json:"player_speed"`
	StdDevDegrees    float64 `json:"stddev_degrees"`
}

func (ZeroWobbleEvidence) EvidenceType() string { return "zero_wobble" }

// MovementEvidence is generic evidence for movement detectors.
type MovementEvidence struct {
	DetectorSpecific string             `json:"detector_specific"`
	Metrics          map[string]float64 `json:"metrics"`
	Snapshots        []MovementSnapshot `json:"snapshots,omitempty"`
}

func (MovementEvidence) EvidenceType() string { return "movement" }

// StateEvidence is generic evidence for state/interaction detectors.
type StateEvidence struct {
	DetectorSpecific string             `json:"detector_specific"`
	Metrics          map[string]float64 `json:"metrics"`
}

func (StateEvidence) EvidenceType() string { return "state" }

// PatternEvidence is generic evidence for pattern detectors.
type PatternEvidence struct {
	DetectorSpecific string             `json:"detector_specific"`
	Metrics          map[string]float64 `json:"metrics"`
	History          []float64          `json:"history,omitempty"`
}

func (PatternEvidence) EvidenceType() string { return "pattern" }
