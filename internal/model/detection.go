package model

import (
	"encoding/json"
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

	// Evidence is serialized with a "type" discriminator (see MarshalJSON) so a
	// stored or exported event can be decoded back into its concrete type.
	Evidence Evidence `json:"evidence"`

	ObservedValue      string `json:"observed_value"`
	ExpectedRange      string `json:"expected_range"`
	BaselineComparison string `json:"baseline_comparison,omitempty"`

	CausalKey CausalKey `json:"causal_key"`

	EnforcementWeight float64           `json:"enforcement_weight"`
	AutoEnforce       bool              `json:"auto_enforce"`
	Attribution       *ThrowAttribution `json:"attribution,omitempty"`
	IsShadow          bool              `json:"is_shadow"`

	// MergedCount is the number of raw detector emissions folded into this
	// event by the pipeline deduplicator (1 for an unmerged event). It is
	// informational: a sustained anomaly is reported as one incident whose
	// frame range spans every merged emission.
	MergedCount int `json:"merged_count,omitempty"`

	// StoredAt is the wall-clock time when this event was persisted to the database.
	// Populated only when reading events back from storage. Not serialized to JSON.
	StoredAt time.Time `json:"-"`
}

// NewEventID generates a unique event ID.
func NewEventID() string {
	return uuid.New().String()
}

// Validate checks the DetectionEvent for required fields and valid ranges.
// The pipeline calls this on every detector emission and drops (and logs)
// events that fail, so a detector that bypasses BaseDetector.MakeEvent cannot
// push NaN or out-of-range values into the scorer.
func (e *DetectionEvent) Validate() error {
	if e.EventID == "" {
		return fmt.Errorf("missing event_id")
	}
	if e.DetectorID == "" {
		return fmt.Errorf("missing detector_id")
	}
	if e.DetectorVersion == "" {
		return fmt.Errorf("missing detector_version")
	}
	if e.MatchID == "" {
		return fmt.Errorf("missing match_id")
	}
	if e.PlayerID == "" {
		return fmt.Errorf("missing player_id")
	}
	if e.FrameIndex < 0 {
		return fmt.Errorf("negative frame_index %d", e.FrameIndex)
	}
	if e.FrameRangeEnd < e.FrameRangeStart {
		return fmt.Errorf("frame range end %d before start %d", e.FrameRangeEnd, e.FrameRangeStart)
	}
	if math.IsNaN(e.Timestamp) || math.IsInf(e.Timestamp, 0) {
		return fmt.Errorf("invalid timestamp")
	}
	if math.IsNaN(e.Severity) || math.IsInf(e.Severity, 0) || e.Severity < 0 || e.Severity > 1 {
		return fmt.Errorf("severity %.4f out of [0,1]", e.Severity)
	}
	if math.IsNaN(e.Confidence) || math.IsInf(e.Confidence, 0) || e.Confidence < 0 || e.Confidence > 1 {
		return fmt.Errorf("confidence %.4f out of [0,1]", e.Confidence)
	}
	if math.IsNaN(e.EnforcementWeight) || math.IsInf(e.EnforcementWeight, 0) ||
		e.EnforcementWeight < 0 || e.EnforcementWeight > 1 {
		return fmt.Errorf("enforcement_weight %.4f out of [0,1]", e.EnforcementWeight)
	}
	if e.ObservedValue == "" {
		return fmt.Errorf("missing observed_value")
	}
	if e.CausalKey.PlayerID == "" || e.CausalKey.AnomalyType == "" {
		return fmt.Errorf("incomplete causal_key")
	}
	return nil
}

// Evidence is the sealed interface for typed detection evidence.
type Evidence interface {
	EvidenceType() string
}

// evidenceRegistry maps EvidenceType() names to constructors of the concrete
// evidence structs. Used by DetectionEvent.UnmarshalJSON.
var evidenceRegistry = map[string]func() Evidence{
	"throw":             func() Evidence { return &ThrowEvidence{} },
	"disc_acceleration": func() Evidence { return &DiscAccelerationEvidence{} },
	"release_angle":     func() Evidence { return &ReleaseAngleEvidence{} },
	"signature_repeat":  func() Evidence { return &SignatureRepeatEvidence{} },
	"precision":         func() Evidence { return &PrecisionEvidence{} },
	"trajectory":        func() Evidence { return &TrajectoryEvidence{} },
	"penalty_field":     func() Evidence { return &PenaltyFieldEvidence{} },
	"speed_distance":    func() Evidence { return &SpeedDistanceEvidence{} },
	"wrist_rotation":    func() Evidence { return &WristRotationEvidence{} },
	"hand_speed":        func() Evidence { return &HandSpeedEvidence{} },
	"zero_jitter":       func() Evidence { return &ZeroJitterEvidence{} },
	"zero_wobble":       func() Evidence { return &ZeroWobbleEvidence{} },
	"movement":          func() Evidence { return &MovementEvidence{} },
	"state":             func() Evidence { return &StateEvidence{} },
	"pattern":           func() Evidence { return &PatternEvidence{} },
}

// EvidenceTypeKey is the JSON key that carries the evidence discriminator.
const EvidenceTypeKey = "type"

// MarshalEvidence encodes evidence as its concrete fields plus a "type"
// discriminator so it can be decoded again with DecodeEvidence. A nil
// evidence encodes as JSON null.
func MarshalEvidence(ev Evidence) ([]byte, error) {
	if ev == nil {
		return []byte("null"), nil
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		// Non-object evidence (should not happen for the sealed types); wrap it.
		return json.Marshal(map[string]any{EvidenceTypeKey: ev.EvidenceType(), "data": json.RawMessage(raw)})
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage, 1)
	}
	typeJSON, _ := json.Marshal(ev.EvidenceType())
	fields[EvidenceTypeKey] = typeJSON
	return json.Marshal(fields)
}

// DecodeEvidence decodes evidence JSON produced by MarshalEvidence. Unknown or
// missing types return nil evidence and no error so old rows remain readable.
func DecodeEvidence(raw []byte) (Evidence, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("decode evidence envelope: %w", err)
	}
	ctor, ok := evidenceRegistry[probe.Type]
	if !ok {
		return nil, nil
	}
	ev := ctor()
	if err := json.Unmarshal(raw, ev); err != nil {
		return nil, fmt.Errorf("decode %s evidence: %w", probe.Type, err)
	}
	return ev, nil
}

// detectionEventJSON mirrors DetectionEvent with Evidence as raw JSON so the
// custom (un)marshalers can reuse the default field encoding.
type detectionEventJSON struct {
	EventID            string            `json:"event_id"`
	DetectorID         string            `json:"detector_id"`
	DetectorVersion    string            `json:"detector_version"`
	MatchID            string            `json:"match_id"`
	PlayerID           string            `json:"player_id"`
	FrameIndex         int               `json:"frame_index"`
	FrameRangeStart    int               `json:"frame_range_start"`
	FrameRangeEnd      int               `json:"frame_range_end"`
	Timestamp          float64           `json:"timestamp"`
	Severity           float64           `json:"severity"`
	Confidence         float64           `json:"confidence"`
	Evidence           json.RawMessage   `json:"evidence"`
	ObservedValue      string            `json:"observed_value"`
	ExpectedRange      string            `json:"expected_range"`
	BaselineComparison string            `json:"baseline_comparison,omitempty"`
	CausalKey          CausalKey         `json:"causal_key"`
	EnforcementWeight  float64           `json:"enforcement_weight"`
	AutoEnforce        bool              `json:"auto_enforce"`
	Attribution        *ThrowAttribution `json:"attribution,omitempty"`
	IsShadow           bool              `json:"is_shadow"`
	MergedCount        int               `json:"merged_count,omitempty"`
}

// MarshalJSON encodes the event with a typed evidence envelope.
func (e DetectionEvent) MarshalJSON() ([]byte, error) {
	evJSON, err := MarshalEvidence(e.Evidence)
	if err != nil {
		return nil, err
	}
	return json.Marshal(detectionEventJSON{
		EventID: e.EventID, DetectorID: e.DetectorID, DetectorVersion: e.DetectorVersion,
		MatchID: e.MatchID, PlayerID: e.PlayerID, FrameIndex: e.FrameIndex,
		FrameRangeStart: e.FrameRangeStart, FrameRangeEnd: e.FrameRangeEnd,
		Timestamp: e.Timestamp, Severity: e.Severity, Confidence: e.Confidence,
		Evidence: evJSON, ObservedValue: e.ObservedValue, ExpectedRange: e.ExpectedRange,
		BaselineComparison: e.BaselineComparison, CausalKey: e.CausalKey,
		EnforcementWeight: e.EnforcementWeight, AutoEnforce: e.AutoEnforce,
		Attribution: e.Attribution, IsShadow: e.IsShadow, MergedCount: e.MergedCount,
	})
}

// UnmarshalJSON decodes the event, restoring typed evidence via the registry.
func (e *DetectionEvent) UnmarshalJSON(data []byte) error {
	var raw detectionEventJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	ev, err := DecodeEvidence(raw.Evidence)
	if err != nil {
		return err
	}
	*e = DetectionEvent{
		EventID: raw.EventID, DetectorID: raw.DetectorID, DetectorVersion: raw.DetectorVersion,
		MatchID: raw.MatchID, PlayerID: raw.PlayerID, FrameIndex: raw.FrameIndex,
		FrameRangeStart: raw.FrameRangeStart, FrameRangeEnd: raw.FrameRangeEnd,
		Timestamp: raw.Timestamp, Severity: raw.Severity, Confidence: raw.Confidence,
		Evidence: ev, ObservedValue: raw.ObservedValue, ExpectedRange: raw.ExpectedRange,
		BaselineComparison: raw.BaselineComparison, CausalKey: raw.CausalKey,
		EnforcementWeight: raw.EnforcementWeight, AutoEnforce: raw.AutoEnforce,
		Attribution: raw.Attribution, IsShadow: raw.IsShadow, MergedCount: raw.MergedCount,
	}
	return nil
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

// Key returns the identity used for deduplication: player and anomaly type.
// The frame range is deliberately excluded so that sliding-window detectors
// (which shift their range every frame) collapse into one incident; the
// Deduplicator uses Overlaps and its merge window to decide whether two
// emissions with the same Key belong to the same incident.
func (c CausalKey) Key() string {
	return c.PlayerID + ":" + c.AnomalyType
}
