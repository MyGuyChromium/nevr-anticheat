package model

import "time"

// ReviewCase represents a player case in the moderator review queue.
type ReviewCase struct {
	CaseID         string    `json:"case_id"`
	PlayerID       string    `json:"player_id"`
	MatchID        string    `json:"match_id"`
	TimestampStart time.Time `json:"timestamp_start"`
	TimestampEnd   time.Time `json:"timestamp_end"`

	DetectorsTriggered []TriggeredDetector `json:"detectors_triggered"`

	Severity           string  `json:"severity"` // "critical", "high", "medium", "low"
	SuspicionScore     float64 `json:"suspicion_score"`
	RecommendedAction  string  `json:"recommended_action"` // "ban", "temp_restrict", "enhanced_monitoring", "review_only"
	Explanation        string  `json:"explanation"`

	Status    string    `json:"status"` // "pending", "assigned", "in_review", "decided", "appealed", "closed"
	AssignedTo string   `json:"assigned_to,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`

	ThresholdVersion string `json:"threshold_version,omitempty"`
}

// TriggeredDetector records a single detector firing within a case.
type TriggeredDetector struct {
	DetectorID   string  `json:"detector_id"`
	DetectorName string  `json:"detector_name"`
	Severity     string  `json:"severity"`
	Confidence   float64 `json:"confidence"`
	MetricName   string  `json:"metric_name"`
	MetricValue  float64 `json:"metric_value"`
	Threshold    float64 `json:"threshold"`
	Details      string  `json:"details"`
}

// ModeratorDecision records a moderator's verdict on a ReviewCase.
type ModeratorDecision struct {
	DecisionID         string    `json:"decision_id"`
	CaseID             string    `json:"case_id"`
	ModeratorID        string    `json:"moderator_id"`
	Verdict            string    `json:"verdict"` // "confirmed_cheat", "false_positive", "inconclusive", "needs_more_data"
	ActionTaken        string    `json:"action_taken"`
	Notes              string    `json:"notes"`
	DecidedAt          time.Time `json:"decided_at"`
	ConfidenceOverride *float64  `json:"confidence_override,omitempty"`
	ReviewDurationSec  int       `json:"review_duration_sec"`
	DetectorFeedback   []DetectorVerdict `json:"detector_feedback,omitempty"`
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
