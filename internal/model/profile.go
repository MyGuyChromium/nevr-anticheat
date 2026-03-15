package model

import (
	"math"
	"time"
)

// Baseline holds statistical summary data for a metric.
type Baseline struct {
	Metric      string  `json:"metric"`
	Mean        float64 `json:"mean"`
	StdDev      float64 `json:"stddev"`
	P50         float64 `json:"p50"`
	P90         float64 `json:"p90"`
	P95         float64 `json:"p95"`
	P99         float64 `json:"p99"`
	P999        float64 `json:"p999"`
	Min         float64 `json:"min"`
	Max         float64 `json:"max"`
	SampleCount int64   `json:"sample_count"`
}

// PlayerBehavioralProfile captures long-term behavioral patterns.
type PlayerBehavioralProfile struct {
	PlayerID    string    `json:"player_id"`
	MatchCount  int       `json:"match_count"`
	LastUpdated time.Time `json:"last_updated"`

	ThrowProfile        ThrowBehavioralProfile        `json:"throw_profile"`
	MovementProfile     MovementBehavioralProfile     `json:"movement_profile"`
	BiomechanicsProfile BiomechanicsBehavioralProfile `json:"biomechanics_profile"`
	GeneralProfile      GeneralBehavioralProfile      `json:"general_profile"`
}

// ThrowBehavioralProfile captures throwing patterns.
type ThrowBehavioralProfile struct {
	ThrowSpeedDistribution        Histogram          `json:"throw_speed_distribution"`
	ReleaseAngleDistribution      Histogram          `json:"release_angle_distribution"`
	PossessionDurationDistribution Histogram          `json:"possession_duration_distribution"`
	ThrowsPerMatch                WelfordAccumulator `json:"throws_per_match"`
	PreferredHand                 string             `json:"preferred_hand"`
	HandPreferenceRatio           float64            `json:"hand_preference_ratio"`
}

// MovementBehavioralProfile captures movement patterns.
type MovementBehavioralProfile struct {
	SpeedDistribution        Histogram          `json:"speed_distribution"`
	AccelerationDistribution Histogram          `json:"acceleration_distribution"`
	BoostUsagePerMatch       WelfordAccumulator `json:"boost_usage_per_match"`
}

// BiomechanicsBehavioralProfile captures hand/wrist patterns.
type BiomechanicsBehavioralProfile struct {
	HandSpeedDistribution            Histogram          `json:"hand_speed_distribution"`
	WristAngularVelocityDistribution Histogram          `json:"wrist_angular_velocity_distribution"`
	HandJitterBaseline               WelfordAccumulator `json:"hand_jitter_baseline"`
	AimWobbleBaseline                WelfordAccumulator `json:"aim_wobble_baseline"`
	ReachEnvelope                    WelfordAccumulator `json:"reach_envelope"`
}

// GeneralBehavioralProfile captures general play patterns.
type GeneralBehavioralProfile struct {
	MatchDuration         WelfordAccumulator `json:"match_duration"`
	GoalsPerMatch         WelfordAccumulator `json:"goals_per_match"`
	StealsPerMatch        WelfordAccumulator `json:"steals_per_match"`
	SavesPerMatch         WelfordAccumulator `json:"saves_per_match"`
	AveragePingMs         WelfordAccumulator `json:"average_ping_ms"`
	DetectionRatePerMatch WelfordAccumulator `json:"detection_rate_per_match"`
}

// Histogram is a fixed-width histogram for distribution storage.
type Histogram struct {
	BucketMin   float64 `json:"bucket_min"`
	BucketMax   float64 `json:"bucket_max"`
	BucketCount int     `json:"bucket_count"`
	Counts      []int64 `json:"counts"`
	TotalCount  int64   `json:"total_count"`
	Underflow   int64   `json:"underflow"`
	Overflow    int64   `json:"overflow"`
}

// NewHistogram creates a histogram with the specified range and bucket count.
func NewHistogram(min, max float64, buckets int) Histogram {
	return Histogram{
		BucketMin:   min,
		BucketMax:   max,
		BucketCount: buckets,
		Counts:      make([]int64, buckets),
	}
}

// Add adds an observation to the histogram.
func (h *Histogram) Add(value float64) {
	if h.BucketCount <= 0 || h.BucketMax <= h.BucketMin {
		return
	}
	h.TotalCount++
	if value < h.BucketMin {
		h.Underflow++
		return
	}
	if value >= h.BucketMax {
		h.Overflow++
		return
	}
	bucketWidth := (h.BucketMax - h.BucketMin) / float64(h.BucketCount)
	idx := int((value - h.BucketMin) / bucketWidth)
	if idx >= h.BucketCount {
		idx = h.BucketCount - 1
	}
	h.Counts[idx]++
}

// BhattacharyyaCoefficient computes the Bhattacharyya coefficient between two histograms.
// Returns a value in [0, 1] where 1 = identical distributions.
func (h *Histogram) BhattacharyyaCoefficient(other *Histogram) float64 {
	if h.BucketCount != other.BucketCount || h.TotalCount == 0 || other.TotalCount == 0 {
		return 0
	}
	// Normalize by bucket counts only (excluding underflow/overflow)
	hBucketTotal := h.TotalCount - h.Underflow - h.Overflow
	oBucketTotal := other.TotalCount - other.Underflow - other.Overflow
	if hBucketTotal <= 0 || oBucketTotal <= 0 {
		return 0
	}
	bc := 0.0
	for i := 0; i < h.BucketCount; i++ {
		p := float64(h.Counts[i]) / float64(hBucketTotal)
		q := float64(other.Counts[i]) / float64(oBucketTotal)
		bc += math.Sqrt(p * q)
	}
	return bc
}
