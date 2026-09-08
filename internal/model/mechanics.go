package model

import (
	"fmt"
	"math"
)

// ProjectRules are owner-supplied rules, not claims that a particular engine
// build implements them. Geometry, timing and source validation live in the
// evaluator's audited knowledge, never in telemetry or a config toggle.
type ProjectRules struct {
	Version             string  `json:"version" toml:"version"`
	DiscGrabLimitM      float64 `json:"disc_grab_limit_m" toml:"disc_grab_limit_m"`
	FastThrowSpeedMPS   float64 `json:"fast_throw_speed_mps" toml:"fast_throw_speed_mps"`
	RequiredMovementMPS float64 `json:"required_movement_mps" toml:"required_movement_mps"`
}

func DefaultProjectRules() ProjectRules {
	return ProjectRules{Version: "owner-2026-09-08-v1", DiscGrabLimitM: .25, FastThrowSpeedMPS: 19, RequiredMovementMPS: 4.7}
}

const (
	MechanicsConsistent         = "consistent"
	MechanicsInconclusive       = "inconclusive"
	MechanicsAnomaly            = "anomaly"
	MechanicsValidatedViolation = "validated_violation"
	MechanicsGrabGeometry       = "grab_geometry_violation"
	MechanicsShotTargeting      = "shot_targeting_anomaly"
	MechanicsThrowPhysics       = "throw_physics_violation"
	MechanicsSettingsIntegrity  = "settings_integrity_violation"
	MaxMechanicsMetrics         = 24
	MaxMechanicsRawSamples      = 128
	MaxMechanicsLimitations     = 8
)

// MechanicsRawSample retains nullable observed inputs. Nil is unavailable,
// not zero. Hand positions describe tracking origins, not verified grab points;
// DiscPosition describes its sampled center, not a verified surface boundary.
type MechanicsRawSample struct {
	// Supporting releases and inspected flight samples are distinguished from
	// the latest event's context. A goal reference is inferred, not verified
	// pocket geometry. SampledSpeedMPS preserves the scalar actually used by
	// descriptive statistics even when it differs from |DiscVelocity|.
	SampleRole            string   `json:"sample_role,omitempty"`
	EventID               string   `json:"event_id,omitempty"`
	SampledSpeedMPS       *float64 `json:"sampled_speed_mps,omitempty"`
	InferredReferenceGoal *Vec3    `json:"inferred_reference_goal,omitempty"`
	BounceCount           *int     `json:"bounce_count,omitempty"`
	FrameIndex            int      `json:"frame_index"`
	Timestamp             float64  `json:"timestamp"`
	DiscPosition          *Vec3    `json:"disc_position,omitempty"`
	DiscVelocity          *Vec3    `json:"disc_velocity,omitempty"`
	LeftHand              *Vec3    `json:"left_hand,omitempty"`
	RightHand             *Vec3    `json:"right_hand,omitempty"`
	PlayerPosition        *Vec3    `json:"player_position,omitempty"`
	PlayerVelocity        *Vec3    `json:"player_velocity,omitempty"`
	ReportedVelocity      *Vec3    `json:"reported_velocity,omitempty"`
	Attachment            string   `json:"attachment,omitempty"`
	LeftHolding           string   `json:"left_holding,omitempty"`
	RightHolding          string   `json:"right_holding,omitempty"`
}

// MechanicsAssessment is a diagnostic, not a DetectionEvent or score. Source,
// Authority and CoordinateSpace preserve descriptions; none grants trust.
type MechanicsAssessment struct {
	Kind                string               `json:"kind"`
	Result              string               `json:"result"`
	Reason              string               `json:"reason"`
	ReasonDescription   string               `json:"reason_description,omitempty"`
	RuleVersion         string               `json:"rule_version"`
	VerifiedEngineBuild string               `json:"verified_engine_build,omitempty"`
	ValidationReference string               `json:"validation_reference,omitempty"`
	GeometryDefinition  string               `json:"geometry_definition,omitempty"`
	FrameIndex          int                  `json:"frame_index"`
	Timestamp           float64              `json:"timestamp"`
	EventID             string               `json:"event_id"`
	PlayerID            string               `json:"player_id,omitempty"`
	SessionID           string               `json:"session_id,omitempty"`
	TimeBasis           string               `json:"time_basis,omitempty"`
	IntervalStart       float64              `json:"interval_start"`
	IntervalEnd         float64              `json:"interval_end"`
	Hand                string               `json:"hand,omitempty"`
	Object              string               `json:"object,omitempty"`
	Source              string               `json:"source,omitempty"`
	Authority           string               `json:"authority,omitempty"`
	CoordinateSpace     string               `json:"coordinate_space,omitempty"`
	Metrics             map[string]float64   `json:"metrics,omitempty"`
	Limitations         []string             `json:"limitations,omitempty"`
	RawSamples          []MechanicsRawSample `json:"raw_samples,omitempty"`
}

func (r MechanicsAssessment) Validate() error {
	switch r.Kind {
	case MechanicsGrabGeometry, MechanicsShotTargeting, MechanicsThrowPhysics, MechanicsSettingsIntegrity:
	default:
		return fmt.Errorf("invalid mechanics kind")
	}
	switch r.Result {
	case MechanicsConsistent, MechanicsInconclusive, MechanicsAnomaly, MechanicsValidatedViolation:
	default:
		return fmt.Errorf("invalid mechanics result")
	}
	if r.FrameIndex < 0 || !mechanicsTime(r.Timestamp) || !mechanicsTime(r.IntervalStart) || !mechanicsTime(r.IntervalEnd) || r.IntervalStart > r.IntervalEnd {
		return fmt.Errorf("invalid mechanics frame or interval")
	}
	if !catchReviewKey(r.Reason) || len(r.ReasonDescription) > 512 || len(r.RuleVersion) == 0 || len(r.RuleVersion) > 128 || len(r.EventID) == 0 || len(r.EventID) > 256 {
		return fmt.Errorf("invalid mechanics identity or reason")
	}
	for _, field := range []string{r.PlayerID, r.SessionID, r.TimeBasis, r.Hand, r.Object, r.Source, r.Authority, r.CoordinateSpace, r.VerifiedEngineBuild, r.ValidationReference, r.GeometryDefinition} {
		if len(field) > 256 {
			return fmt.Errorf("oversized mechanics descriptor")
		}
	}
	if len(r.Metrics) > MaxMechanicsMetrics || len(r.Limitations) > MaxMechanicsLimitations || len(r.RawSamples) > MaxMechanicsRawSamples {
		return fmt.Errorf("oversized mechanics evidence")
	}
	for key, value := range r.Metrics {
		if !catchReviewKey(key) || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("invalid mechanics metric")
		}
	}
	for _, limitation := range r.Limitations {
		if len(limitation) == 0 || len(limitation) > 256 {
			return fmt.Errorf("invalid mechanics limitation")
		}
	}
	for _, sample := range r.RawSamples {
		if (sample.SampleRole != "" && sample.SampleRole != "supporting_release" && sample.SampleRole != "flight_sample") || len(sample.EventID) > 256 {
			return fmt.Errorf("invalid mechanics raw sample role or identity")
		}
		if sample.SampledSpeedMPS != nil && !mechanicsTime(*sample.SampledSpeedMPS) {
			return fmt.Errorf("invalid mechanics sampled speed")
		}
		if sample.BounceCount != nil && *sample.BounceCount < 0 {
			return fmt.Errorf("invalid mechanics bounce count")
		}
		if sample.FrameIndex < 0 || !mechanicsTime(sample.Timestamp) || len(sample.Attachment) > 256 || len(sample.LeftHolding) > 256 || len(sample.RightHolding) > 256 {
			return fmt.Errorf("invalid mechanics raw sample")
		}
		for _, value := range []*Vec3{sample.DiscPosition, sample.DiscVelocity, sample.LeftHand, sample.RightHand, sample.PlayerPosition, sample.PlayerVelocity, sample.ReportedVelocity, sample.InferredReferenceGoal} {
			if value != nil && (value.HasNaN() || value.HasInf()) {
				return fmt.Errorf("non-finite mechanics raw vector")
			}
		}
	}
	return nil
}

func mechanicsTime(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func (r MechanicsAssessment) Clone() MechanicsAssessment {
	if len(r.Metrics) > 0 {
		metrics := make(map[string]float64, len(r.Metrics))
		for key, value := range r.Metrics {
			metrics[key] = value
		}
		r.Metrics = metrics
	} else {
		r.Metrics = nil
	}
	r.Limitations = append([]string(nil), r.Limitations...)
	r.RawSamples = append([]MechanicsRawSample(nil), r.RawSamples...)
	for i := range r.RawSamples {
		s := &r.RawSamples[i]
		if s.SampledSpeedMPS != nil {
			value := *s.SampledSpeedMPS
			s.SampledSpeedMPS = &value
		}
		if s.BounceCount != nil {
			value := *s.BounceCount
			s.BounceCount = &value
		}
		for _, value := range []**Vec3{&s.DiscPosition, &s.DiscVelocity, &s.LeftHand, &s.RightHand, &s.PlayerPosition, &s.PlayerVelocity, &s.ReportedVelocity, &s.InferredReferenceGoal} {
			if *value != nil {
				copy := **value
				*value = &copy
			}
		}
	}
	return r
}
