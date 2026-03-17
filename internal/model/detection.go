package model

import (
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// DetectionEvent represents a single detected anomaly with full provenance and evidence.
type DetectionEvent struct {
	EventID         string  `json:"event_id"`
	DetectorID      string  `json:"detector_id"`
	DetectorVersion string  `json:"detector_version"`
	MatchID         string  `json:"match_id"`
	PlayerID        string  `json:"player_id"`
	FrameIndex      int     `json:"frame_index"`
	FrameRangeStart int     `json:"frame_range_start"`
	FrameRangeEnd   int     `json:"frame_range_end"`
	Timestamp       float64 `json:"timestamp"`
	Severity        float64 `json:"severity"`
	Confidence      float64 `json:"confidence"`

	Evidence Evidence `json:"evidence"`

	ObservedValue      string `json:"observed_value"`
	ExpectedRange      string `json:"expected_range"`
	BaselineComparison string `json:"baseline_comparison,omitempty"`

	CausalKey CausalKey `json:"causal_key"`

	EnforcementWeight float64          `json:"enforcement_weight"`
	AutoEnforce       bool             `json:"auto_enforce"`
	Attribution       *ThrowAttribution `json:"attribution,omitempty"`
	IsShadow          bool             `json:"is_shadow"`

	// StoredAt is the wall-clock time when this event was persisted to the database.
	// Populated only when reading events back from storage. Not serialized to JSON.
	StoredAt time.Time `json:"-"`
}

// NewEventID generates a unique event ID.
func NewEventID() string {
	return uuid.New().String()
}

// Validate checks the DetectionEvent for required fields and valid ranges.
func (e *DetectionEvent) Validate() error {
	if e.EventID == "" {
		return fmt.Errorf("missing event_id")
	}
	if e.DetectorID == "" {
		return fmt.Errorf("missing detector_id")
	}
	if e.PlayerID == "" {
		return fmt.Errorf("missing player_id")
	}
	if math.IsNaN(e.Severity) || e.Severity < 0 || e.Severity > 1 {
		return fmt.Errorf("severity %.4f out of [0,1]", e.Severity)
	}
	if math.IsNaN(e.Confidence) || e.Confidence < 0 || e.Confidence > 1 {
		return fmt.Errorf("confidence %.4f out of [0,1]", e.Confidence)
	}
	if e.ObservedValue == "" {
		return fmt.Errorf("missing observed_value")
	}
	return nil
}

// Evidence is the sealed interface for typed detection evidence.
type Evidence interface {
	EvidenceType() string
}

// CausalKey groups related detection events stemming from the same underlying anomaly.
type CausalKey struct {
	PlayerID    string `json:"player_id"`
	FrameStart  int    `json:"frame_start"`
	FrameEnd    int    `json:"frame_end"`
	AnomalyType string `json:"anomaly_type"`
}

// Overlaps returns true if two causal keys overlap in frame range and have the same anomaly type.
func (c CausalKey) Overlaps(other CausalKey) bool {
	if c.PlayerID != other.PlayerID || c.AnomalyType != other.AnomalyType {
		return false
	}
	return c.FrameStart <= other.FrameEnd && other.FrameStart <= c.FrameEnd
}

// Key returns a string key for deduplication lookup.
func (c CausalKey) Key() string {
	return fmt.Sprintf("%s:%s:%d-%d", c.PlayerID, c.AnomalyType, c.FrameStart, c.FrameEnd)
}
