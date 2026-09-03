package model

import "math"

// PlayerState holds per-player accumulated state within a match.
type PlayerState struct {
	// Identity
	PlayerID string `json:"player_id"`
	Team     string `json:"team"`

	// Current frame state
	Position     Vec3 `json:"position"`
	Rotation     Quat `json:"rotation"`
	LeftHand     Vec3 `json:"left_hand"`
	RightHand    Vec3 `json:"right_hand"`
	LeftHandRot  Quat `json:"left_hand_rot"`
	RightHandRot Quat `json:"right_hand_rot"`

	// Derived kinematics
	Velocity              Vec3    `json:"velocity"`
	Speed                 float64 `json:"speed"`
	Acceleration          Vec3    `json:"acceleration"`
	AccelerationMagnitude float64 `json:"acceleration_magnitude"`

	// Hand kinematics
	// LeftHandVelocity/RightHandVelocity are world-space velocities. The
	// relative variants subtract body translation and describe controller
	// motion relative to the player; biomechanical limits must use those so
	// fast legal body movement is not counted again as impossible arm motion.
	LeftHandVelocity          Vec3    `json:"left_hand_velocity"`
	RightHandVelocity         Vec3    `json:"right_hand_velocity"`
	LeftHandSpeed             float64 `json:"left_hand_speed"`
	RightHandSpeed            float64 `json:"right_hand_speed"`
	LeftHandRelativeVelocity  Vec3    `json:"left_hand_relative_velocity"`
	RightHandRelativeVelocity Vec3    `json:"right_hand_relative_velocity"`
	LeftHandRelativeSpeed     float64 `json:"left_hand_relative_speed"`
	RightHandRelativeSpeed    float64 `json:"right_hand_relative_speed"`
	LeftWristAngularRate      float64 `json:"left_wrist_angular_rate"`
	RightWristAngularRate     float64 `json:"right_wrist_angular_rate"`

	// Game state
	IsStunned            bool    `json:"is_stunned"`
	StunStartFrame       int     `json:"stun_start_frame,omitempty"`
	StunEndTime          float64 `json:"stun_end_time,omitempty"`
	HasDisc              bool    `json:"has_disc"`
	PossessionStartFrame int     `json:"possession_start_frame,omitempty"`
	PossessionStartTime  float64 `json:"possession_start_time,omitempty"`
	IsBoosting           bool    `json:"is_boosting"`
	ShieldActive         bool    `json:"shield_active"`
	ShieldStartFrame     int     `json:"shield_start_frame,omitempty"`
	IsImmune             bool    `json:"is_immune"`
	IsInvulnerable       bool    `json:"is_invulnerable"`

	// Timing
	LastFrameIdx  int     `json:"last_frame_idx"`
	LastTimestamp float64 `json:"last_timestamp"`
	FrameDt       float64 `json:"frame_dt"`
	FrameCount    int     `json:"frame_count"`

	// History buffers (sliding window)
	PositionHistory     []Vec3    `json:"-"`
	VelocityHistory     []Vec3    `json:"-"`
	SpeedHistory        []float64 `json:"-"`
	LeftHandHistory     []Vec3    `json:"-"`
	RightHandHistory    []Vec3    `json:"-"`
	LeftHandRotHistory  []Quat    `json:"-"`
	RightHandRotHistory []Quat    `json:"-"`
	TimestampHistory    []float64 `json:"-"`
	DiscVelocityHistory []Vec3    `json:"-"`

	// Statistics (Welford accumulators)
	SpeedStats          WelfordAccumulator `json:"-"`
	LeftHandSpeedStats  WelfordAccumulator `json:"-"`
	RightHandSpeedStats WelfordAccumulator `json:"-"`

	// Throw tracking
	ThrowCount   int          `json:"throw_count"`
	LastThrow    *ThrowEvent  `json:"-"`
	ThrowHistory []ThrowEvent `json:"-"`

	// Disc state (shared — same for all players in same frame)
	CurrentDisc *DiscState `json:"-"`

	// Latency
	EstimatedPingMs float64 `json:"estimated_ping_ms"`
	IsHighPing      bool    `json:"is_high_ping"`

	// Score tracking (per-match stats)
	PrevBlueScore   int `json:"-"`
	PrevOrangeScore int `json:"-"`
	PrevGoals       int `json:"-"`
	PrevStuns       int `json:"-"`

	// Boost tracking
	BoostTimestamps []float64 `json:"-"`
	LastBoostFrame  int       `json:"-"`

	// Invulnerability tracking
	InvulnerableFrames int `json:"-"`

	// Shield consecutive frames
	ConsecutiveShieldFrames int `json:"-"`

	// Cooldown tracking
	LastShieldOffFrame int `json:"-"`

	// Stun tracking
	WasStunned     bool `json:"-"`
	StunRecoveries int  `json:"-"`
}

const DefaultHistoryWindow = 30

// PushVec3History appends a value and trims to max window size.
// Uses copy-trim to prevent the backing array from growing unboundedly.
func PushVec3History(hist *[]Vec3, v Vec3, maxSize int) {
	*hist = append(*hist, v)
	if len(*hist) > maxSize {
		copy(*hist, (*hist)[len(*hist)-maxSize:])
		*hist = (*hist)[:maxSize]
	}
}

// PushQuatHistory appends and trims quaternion history.
func PushQuatHistory(hist *[]Quat, q Quat, maxSize int) {
	*hist = append(*hist, q)
	if len(*hist) > maxSize {
		copy(*hist, (*hist)[len(*hist)-maxSize:])
		*hist = (*hist)[:maxSize]
	}
}

// PushFloat64History appends and trims float64 history.
func PushFloat64History(hist *[]float64, v float64, maxSize int) {
	*hist = append(*hist, v)
	if len(*hist) > maxSize {
		copy(*hist, (*hist)[len(*hist)-maxSize:])
		*hist = (*hist)[:maxSize]
	}
}

// WelfordAccumulator implements Welford's online algorithm for running mean and variance.
type WelfordAccumulator struct {
	N    int64   `json:"count"`
	Mean float64 `json:"mean"`
	M2   float64 `json:"m2"`
}

// Update adds a new observation.
func (w *WelfordAccumulator) Update(value float64) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	w.N++
	delta := value - w.Mean
	w.Mean += delta / float64(w.N)
	delta2 := value - w.Mean
	w.M2 += delta * delta2
}

// PopulationVariance returns the population variance. Returns 0 if N < 2.
func (w *WelfordAccumulator) PopulationVariance() float64 {
	if w.N < 2 {
		return 0
	}
	return w.M2 / float64(w.N)
}

// StdDev returns the population standard deviation.
func (w *WelfordAccumulator) StdDev() float64 {
	return math.Sqrt(w.PopulationVariance())
}

// Reset clears the accumulator.
func (w *WelfordAccumulator) Reset() {
	w.N = 0
	w.Mean = 0
	w.M2 = 0
}

// ComputePositionVariance computes the positional variance of a set of Vec3 positions.
func ComputePositionVariance(positions []Vec3) float64 {
	if len(positions) < 2 {
		return 0
	}
	meanPos := Vec3{}
	for _, p := range positions {
		meanPos = meanPos.Add(p)
	}
	n := float64(len(positions))
	meanPos = meanPos.Scale(1.0 / n)

	sumSq := 0.0
	for _, p := range positions {
		d := p.Sub(meanPos)
		sumSq += d.MagnitudeSq()
	}
	return sumSq / n
}

// ComputeRotationVariance computes the rotational variance of a set of quaternions.
func ComputeRotationVariance(quats []Quat) float64 {
	if len(quats) < 2 {
		return 0
	}
	meanQ := QuaternionMean(quats)
	sumSq := 0.0
	for _, q := range quats {
		d := q.AngularDistance(meanQ)
		sumSq += d * d
	}
	return sumSq / float64(len(quats))
}
