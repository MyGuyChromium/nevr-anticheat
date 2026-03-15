package model

import "time"

// ScoringLevel represents suspicion tiers.
type ScoringLevel string

const (
	LevelClean         ScoringLevel = "clean"
	LevelInformational ScoringLevel = "informational"
	LevelSuspicious    ScoringLevel = "suspicious"
	LevelHighRisk      ScoringLevel = "high_risk"
	LevelCritical      ScoringLevel = "critical"
	LevelActionWorthy  ScoringLevel = "action_worthy"
)

// SuspicionScore holds per-player scoring state.
type SuspicionScore struct {
	PlayerID string  `json:"player_id"`
	TotalScore float64 `json:"total_score"`

	ScoreByDetector map[string]float64 `json:"score_by_detector"`
	ScoreByCategory map[string]float64 `json:"score_by_category"`

	EventCount int `json:"event_count"`
	MatchCount int `json:"match_count"`

	FirstEventTime time.Time `json:"first_event_time"`
	LastEventTime  time.Time `json:"last_event_time"`
	LastDecayTime  time.Time `json:"last_decay_time"`

	DecayHalfLifeHours float64 `json:"decay_half_life_hours"`

	HighestSingleEvent    float64 `json:"highest_single_event"`
	HighestSingleDetector string  `json:"highest_single_detector"`

	ReviewThreshold      float64 `json:"review_threshold"`
	AutoEnforceThreshold float64 `json:"auto_enforce_threshold"`
	ExceedsReview        bool    `json:"exceeds_review"`
	ExceedsAutoEnforce   bool    `json:"exceeds_auto_enforce"`

	SnapshotTime time.Time `json:"snapshot_time"`

	// Internal tracking for accumulation rules
	DetectorCounts   map[string]int    `json:"detector_counts,omitempty"`
	CategoryCounts   map[string]int    `json:"category_counts,omitempty"`
	LastEventFrames  map[string]int    `json:"-"`
	MatchIDs         map[string]bool   `json:"-"`
}

// Level returns the scoring level based on TotalScore.
func (s *SuspicionScore) Level() ScoringLevel {
	switch {
	case s.TotalScore >= 95:
		return LevelActionWorthy
	case s.TotalScore >= 80:
		return LevelCritical
	case s.TotalScore >= 60:
		return LevelHighRisk
	case s.TotalScore >= 40:
		return LevelSuspicious
	case s.TotalScore >= 20:
		return LevelInformational
	default:
		return LevelClean
	}
}

// Init initializes internal maps if nil.
func (s *SuspicionScore) Init() {
	if s.ScoreByDetector == nil {
		s.ScoreByDetector = make(map[string]float64)
	}
	if s.ScoreByCategory == nil {
		s.ScoreByCategory = make(map[string]float64)
	}
	if s.DetectorCounts == nil {
		s.DetectorCounts = make(map[string]int)
	}
	if s.CategoryCounts == nil {
		s.CategoryCounts = make(map[string]int)
	}
	if s.LastEventFrames == nil {
		s.LastEventFrames = make(map[string]int)
	}
	if s.MatchIDs == nil {
		s.MatchIDs = make(map[string]bool)
	}
}

// EnforcementAction represents an action taken against a player.
type EnforcementAction struct {
	ActionID    string        `json:"action_id"`
	PlayerID    string        `json:"player_id"`
	ActionType  string        `json:"action_type"` // "warn", "temp_ban", "perm_ban", "dismiss", "escalate", "restrict", "review_queue"
	Reason      string        `json:"reason"`
	Duration    time.Duration `json:"duration,omitempty"`
	IssuedBy    string        `json:"issued_by"` // moderator ID or "auto"
	IssuedAt    time.Time     `json:"issued_at"`
	EvidenceIDs []string      `json:"evidence_ids"`
	MatchIDs    []string      `json:"match_ids"`
	ScoreAtTime float64       `json:"score_at_time"`
	Notes       string        `json:"notes,omitempty"`
}
