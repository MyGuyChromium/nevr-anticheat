package model

import (
	"fmt"
	"math"
)

const (
	MaxCatchReviewRecords = 128
	MaxCatchReviewMetrics = 24
)

type CatchReviewOutcome string

const (
	CatchReviewObservation      CatchReviewOutcome = "observation"
	CatchReviewExcluded         CatchReviewOutcome = "excluded"
	CatchReviewInsufficientData CatchReviewOutcome = "insufficient_data"
	CatchReviewUnconfirmed      CatchReviewOutcome = "unconfirmed"
)

// CatchReviewRecord is one finalized diagnostic for an explicitly observed
// free-to-held transition. Identity/time refer to the first held sample, not
// the later confirmation. It is not a DetectionEvent or proof of automation.
type CatchReviewRecord struct {
	FrameIndex        int                `json:"frame_index"`
	Timestamp         float64            `json:"timestamp"`
	StartFrame        int                `json:"start_frame,omitempty"`
	LastFreeFrame     int                `json:"last_free_frame,omitempty"`
	Confirmed         bool               `json:"confirmed"`
	Outcome           CatchReviewOutcome `json:"outcome"`
	Reason            string             `json:"reason"`
	ReasonDescription string             `json:"reason_description,omitempty"`
	Metrics           map[string]float64 `json:"metrics,omitempty"`
}

func catchReviewKey(key string) bool {
	if len(key) == 0 || len(key) > 64 {
		return false
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func (r CatchReviewRecord) Validate() error {
	if r.FrameIndex < 0 || r.StartFrame < 0 || r.LastFreeFrame < 0 || r.StartFrame > r.FrameIndex || r.LastFreeFrame > r.FrameIndex ||
		math.IsNaN(r.Timestamp) || math.IsInf(r.Timestamp, 0) || r.Timestamp < 0 {
		return fmt.Errorf("invalid catch diagnostic frame or timestamp")
	}
	switch r.Outcome {
	case CatchReviewObservation:
		if !r.Confirmed {
			return fmt.Errorf("catch observation requires a confirmed transition")
		}
	case CatchReviewUnconfirmed:
		if r.Confirmed {
			return fmt.Errorf("unconfirmed catch diagnostic cannot be confirmed")
		}
	case CatchReviewExcluded, CatchReviewInsufficientData:
	default:
		return fmt.Errorf("invalid catch diagnostic outcome")
	}
	if !catchReviewKey(r.Reason) || len(r.ReasonDescription) > 512 || len(r.Metrics) > MaxCatchReviewMetrics {
		return fmt.Errorf("invalid catch diagnostic reason or metric count")
	}
	for key, value := range r.Metrics {
		if !catchReviewKey(key) || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("invalid catch diagnostic metric")
		}
	}
	return nil
}

func (r CatchReviewRecord) Clone() CatchReviewRecord {
	if len(r.Metrics) == 0 {
		r.Metrics = nil // canonical form survives omitempty JSON round trips
	} else {
		metrics := make(map[string]float64, len(r.Metrics))
		for key, value := range r.Metrics {
			metrics[key] = value
		}
		r.Metrics = metrics
	}
	return r
}

// CatchReviewLog counts only explicit, finalized transitions observed by this
// instrumentation, never all catches or independent calibration opportunities.
// Outcome counts include valid records omitted by the cap. Invalid counts
// malformed diagnostic payloads separately, not player actions.
type CatchReviewLog struct {
	Version          int                 `json:"version"`
	Records          []CatchReviewRecord `json:"records"`
	Total            int                 `json:"total"`
	Dropped          int                 `json:"dropped"`
	Invalid          int                 `json:"invalid"`
	Observation      int                 `json:"observation"`
	Excluded         int                 `json:"excluded"`
	InsufficientData int                 `json:"insufficient_data"`
	Unconfirmed      int                 `json:"unconfirmed"`
	LastFrameIndex   int                 `json:"last_frame_index"`
}

func NewCatchReviewLog() *CatchReviewLog {
	return &CatchReviewLog{Version: 1, Records: []CatchReviewRecord{}, LastFrameIndex: -1}
}

// Add accepts chronological first-held identities exactly once. A duplicate
// or stale callback is ignored even after Records is full; no per-match
// unbounded identity map is needed. The producer finalizes pending records
// before proceeding to later transitions for the same player.
func (l *CatchReviewLog) Add(record CatchReviewRecord) {
	if err := record.Validate(); err != nil {
		l.Invalid++
		return
	}
	if l.Total > 0 && record.FrameIndex <= l.LastFrameIndex {
		return
	}
	l.Version = 1
	l.Total++
	l.LastFrameIndex = record.FrameIndex
	switch record.Outcome {
	case CatchReviewObservation:
		l.Observation++
	case CatchReviewExcluded:
		l.Excluded++
	case CatchReviewInsufficientData:
		l.InsufficientData++
	case CatchReviewUnconfirmed:
		l.Unconfirmed++
	}
	if len(l.Records) >= MaxCatchReviewRecords {
		l.Dropped++
		return
	}
	l.Records = append(l.Records, record.Clone())
}

func (l *CatchReviewLog) Clone() *CatchReviewLog {
	if l == nil {
		return nil
	}
	out := *l
	out.Records = make([]CatchReviewRecord, len(l.Records))
	for i, record := range l.Records {
		out.Records[i] = record.Clone()
	}
	return &out
}

// Validate checks summaries as well as individual payloads before live JSON
// is merged. Corrupt stored diagnostics must not manufacture coverage counts.
func (l *CatchReviewLog) Validate() error {
	if l == nil {
		return nil
	}
	if l.Version != 1 || len(l.Records) > MaxCatchReviewRecords || l.Total < 0 || l.Dropped < 0 || l.Invalid < 0 ||
		l.Observation < 0 || l.Excluded < 0 || l.InsufficientData < 0 || l.Unconfirmed < 0 ||
		l.Total != len(l.Records)+l.Dropped || l.Total != l.Observation+l.Excluded+l.InsufficientData+l.Unconfirmed ||
		(l.Dropped > 0 && len(l.Records) != MaxCatchReviewRecords) ||
		(l.Total == 0 && l.LastFrameIndex != -1) {
		return fmt.Errorf("invalid catch diagnostic summary")
	}
	retained := NewCatchReviewLog()
	for _, record := range l.Records {
		if err := record.Validate(); err != nil {
			return err
		}
		if record.FrameIndex <= retained.LastFrameIndex || record.FrameIndex > l.LastFrameIndex {
			return fmt.Errorf("invalid catch diagnostic ordering")
		}
		retained.Add(record)
	}
	if retained.Observation > l.Observation || retained.Excluded > l.Excluded || retained.InsufficientData > l.InsufficientData || retained.Unconfirmed > l.Unconfirmed ||
		(l.Total > 0 && len(l.Records) == 0) || (l.Dropped == 0 && retained.LastFrameIndex != l.LastFrameIndex) {
		return fmt.Errorf("inconsistent catch diagnostic summary")
	}
	return nil
}

// Merge adds a later, disjoint live chunk. Logs keep the first 128 records;
// overflow outcome counts are retained without retaining more telemetry.
// An already-applied or stale chunk is ignored by its high-water identity.
func (l *CatchReviewLog) Merge(next *CatchReviewLog) {
	if next == nil || next.Validate() != nil {
		return
	}
	if next.Total > 0 && l.Total > 0 && next.LastFrameIndex <= l.LastFrameIndex {
		return
	}
	if l.Total > 0 && next.Dropped > 0 && len(next.Records) > 0 && l.LastFrameIndex >= next.Records[len(next.Records)-1].FrameIndex {
		// A partial overlap into an unretained tail cannot be deduplicated
		// from counts alone. Fail closed rather than inflate the denominator.
		return
	}
	l.Invalid += next.Invalid
	for _, record := range next.Records {
		l.Add(record)
	}
	if next.Dropped > 0 {
		// Only a disjoint, chronologically later chunk may contribute its
		// unretained tail; callers never replay partial overlapping chunks.
		retained := NewCatchReviewLog()
		for _, record := range next.Records {
			retained.Add(record)
		}
		l.Total += next.Dropped
		l.Dropped += next.Dropped
		l.Observation += next.Observation - retained.Observation
		l.Excluded += next.Excluded - retained.Excluded
		l.InsufficientData += next.InsufficientData - retained.InsufficientData
		l.Unconfirmed += next.Unconfirmed - retained.Unconfirmed
		l.LastFrameIndex = next.LastFrameIndex
	}
}
