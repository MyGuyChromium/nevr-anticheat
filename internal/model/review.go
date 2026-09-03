package model

import (
	"fmt"
	"time"
)

// Review case status lifecycle: pending -> assigned -> in_review -> decided
// (-> appealed -> closed). Transitions are validated by review.Queue.
const (
	CaseStatusPending  = "pending"
	CaseStatusAssigned = "assigned"
	CaseStatusInReview = "in_review"
	CaseStatusDecided  = "decided"
	CaseStatusAppealed = "appealed"
	CaseStatusClosed   = "closed"
)

// Moderator verdict vocabulary.
const (
	VerdictConfirmedCheat = "confirmed_cheat"
	VerdictFalsePositive  = "false_positive"
	VerdictInconclusive   = "inconclusive"
	VerdictNeedsMoreData  = "needs_more_data"
)

// Per-detector feedback vocabulary (DetectorVerdict.Correct).
const (
	DetectorVerdictYes       = "yes"
	DetectorVerdictNo        = "no"
	DetectorVerdictUncertain = "uncertain"
)

// ReviewCase represents a player case in the moderator review queue.
type ReviewCase struct {
	CaseID         string    `json:"case_id"`
	PlayerID       string    `json:"player_id"`
	MatchID        string    `json:"match_id"`
	TimestampStart time.Time `json:"timestamp_start"`
	TimestampEnd   time.Time `json:"timestamp_end"`

	DetectorsTriggered []TriggeredDetector `json:"detectors_triggered"`

	Severity          string  `json:"severity"` // "critical", "high", "medium", "low"
	SuspicionScore    float64 `json:"suspicion_score"`
	Level             string  `json:"level,omitempty"`    // ScoringLevel the score fell into when the case was built
	RecommendedAction string  `json:"recommended_action"` // "ban", "temp_restrict", "enhanced_monitoring", "review_only"
	Explanation       string  `json:"explanation"`

	Status     string    `json:"status"` // one of the CaseStatus* constants
	AssignedTo string    `json:"assigned_to,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
	// CloseReason is set when the system (not a moderator) closed the case:
	// a re-analysis of the match no longer flags the player. Such a case is
	// reopened automatically if a later analysis flags the player again.
	// Empty for moderator closures.
	CloseReason string `json:"close_reason,omitempty"`

	// ThresholdVersion identifies the level table and detector versions the
	// case was built under, so cases from different threshold sets can be
	// told apart during calibration.
	ThresholdVersion string `json:"threshold_version,omitempty"`
}

// TriggeredDetector records a single detector firing within a case. It
// carries everything a moderator needs to locate the moment in the replay.
type TriggeredDetector struct {
	DetectorID      string  `json:"detector_id"`
	DetectorName    string  `json:"detector_name"`
	DetectorVersion string  `json:"detector_version,omitempty"`
	EventID         string  `json:"event_id,omitempty"`
	Severity        string  `json:"severity"`
	SeverityValue   float64 `json:"severity_value,omitempty"`
	Confidence      float64 `json:"confidence"`
	FrameIndex      int     `json:"frame_index"`
	FrameRangeStart int     `json:"frame_range_start"`
	FrameRangeEnd   int     `json:"frame_range_end"`
	// Timestamp is match-relative seconds (same base as DetectionEvent.Timestamp).
	Timestamp     float64 `json:"timestamp"`
	MetricName    string  `json:"metric_name"`
	MetricValue   float64 `json:"metric_value"`
	Threshold     float64 `json:"threshold"`
	ExpectedRange string  `json:"expected_range,omitempty"`
	Details       string  `json:"details"`
}

// ModeratorDecision records a moderator's verdict on a ReviewCase.
type ModeratorDecision struct {
	DecisionID         string            `json:"decision_id"`
	CaseID             string            `json:"case_id"`
	ModeratorID        string            `json:"moderator_id"`
	Verdict            string            `json:"verdict"` // one of the Verdict* constants
	ActionTaken        string            `json:"action_taken"`
	Notes              string            `json:"notes"`
	DecidedAt          time.Time         `json:"decided_at"`
	ConfidenceOverride *float64          `json:"confidence_override,omitempty"`
	ReviewDurationSec  int               `json:"review_duration_sec"`
	DetectorFeedback   []DetectorVerdict `json:"detector_feedback,omitempty"`
}

// Validate checks required fields and vocabulary.
func (d *ModeratorDecision) Validate() error {
	if d.CaseID == "" {
		return fmt.Errorf("moderator decision: missing case_id")
	}
	if d.ModeratorID == "" {
		return fmt.Errorf("moderator decision: missing moderator_id")
	}
	switch d.Verdict {
	case VerdictConfirmedCheat, VerdictFalsePositive, VerdictInconclusive, VerdictNeedsMoreData:
	default:
		return fmt.Errorf("moderator decision: unknown verdict %q", d.Verdict)
	}
	for _, fb := range d.DetectorFeedback {
		if fb.DetectorID == "" {
			return fmt.Errorf("moderator decision: detector feedback missing detector_id")
		}
		switch fb.Correct {
		case DetectorVerdictYes, DetectorVerdictNo, DetectorVerdictUncertain:
		default:
			return fmt.Errorf("moderator decision: detector %s has unknown verdict %q", fb.DetectorID, fb.Correct)
		}
	}
	return nil
}

// DetectorVerdict records whether a specific detector's firing was correct.
type DetectorVerdict struct {
	DetectorID string `json:"detector_id"`
	Correct    string `json:"correct"` // "yes", "no", "uncertain"
	Comment    string `json:"comment,omitempty"`
}

// AppealRecord tracks a player's appeal.
type AppealRecord struct {
	AppealID           string     `json:"appeal_id"`
	CaseID             string     `json:"case_id"`
	OriginalDecisionID string     `json:"original_decision_id"`
	PlayerID           string     `json:"player_id"`
	AppealText         string     `json:"appeal_text"`
	SubmittedAt        time.Time  `json:"submitted_at"`
	OriginalVerdict    string     `json:"original_verdict"`
	OriginalAction     string     `json:"original_action"`
	ReviewVerdict      string     `json:"review_verdict"` // "upheld", "overturned", "modified", "pending"
	ReviewAction       string     `json:"review_action"`
	ReviewedBy         string     `json:"reviewed_by"`
	ReviewedAt         *time.Time `json:"reviewed_at,omitempty"`
	ReviewNotes        string     `json:"review_notes"`
	Status             string     `json:"status"` // "submitted", "in_review", "resolved"
}

// RecommendedActionForLevel maps a scoring tier to the moderator-facing
// recommendation vocabulary used by ReviewCase.RecommendedAction.
func RecommendedActionForLevel(l ScoringLevel) string {
	switch {
	case l.AtLeast(LevelCritical):
		return "temp_restrict"
	case l.AtLeast(LevelHighRisk):
		return "review_only"
	case l.AtLeast(LevelSuspicious):
		return "enhanced_monitoring"
	default:
		return "none"
	}
}

// CaseSeverityForLevel maps a scoring tier to ReviewCase.Severity.
func CaseSeverityForLevel(l ScoringLevel) string {
	switch {
	case l.AtLeast(LevelCritical):
		return "critical"
	case l.AtLeast(LevelHighRisk):
		return "high"
	case l.AtLeast(LevelSuspicious):
		return "medium"
	default:
		return "low"
	}
}

// EventSeverityLabel maps a DetectionEvent.Severity in [0,1] to the
// TriggeredDetector.Severity word.
func EventSeverityLabel(severity float64) string {
	switch {
	case severity >= 0.8:
		return "critical"
	case severity >= 0.6:
		return "high"
	case severity >= 0.3:
		return "medium"
	default:
		return "low"
	}
}
