package model

import (
	"fmt"
	"time"
)

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

// LevelTable is the single source of truth for what a suspicion score means.
// Each field is the inclusive lower bound of the named tier; anything below
// Informational is LevelClean. Every consumer of score semantics (Level(),
// the scorer's ExceedsReview flag, enforcement, evidence, cross-match
// aggregation and CLI output) must derive its bands from a LevelTable rather
// than from literals.
type LevelTable struct {
	Informational float64 `json:"informational"`
	Suspicious    float64 `json:"suspicious"`
	HighRisk      float64 `json:"high_risk"`
	Critical      float64 `json:"critical"`
	ActionWorthy  float64 `json:"action_worthy"`
}

// DefaultLevelTable returns the documented tier boundaries
// (README "Scoring": 20/40/60/80/95).
func DefaultLevelTable() LevelTable {
	return LevelTable{
		Informational: 20,
		Suspicious:    40,
		HighRisk:      60,
		Critical:      80,
		ActionWorthy:  95,
	}
}

// IsZero reports whether the table has never been populated.
func (t LevelTable) IsZero() bool {
	return t == LevelTable{}
}

// Validate returns an error unless the boundaries are strictly positive and
// non-decreasing.
func (t LevelTable) Validate() error {
	bounds := []float64{t.Informational, t.Suspicious, t.HighRisk, t.Critical, t.ActionWorthy}
	names := []string{"informational", "suspicious", "high_risk", "critical", "action_worthy"}
	for i, b := range bounds {
		if b <= 0 {
			return fmt.Errorf("level table: %s boundary must be > 0, got %.2f", names[i], b)
		}
		if i > 0 && b < bounds[i-1] {
			return fmt.Errorf("level table: %s (%.2f) must be >= %s (%.2f)", names[i], b, names[i-1], bounds[i-1])
		}
	}
	return nil
}

// WithReviewThreshold maps a configured review threshold onto the HighRisk
// boundary (the tier at which a player enters the moderator review queue).
// Neighbouring tiers are clamped so the table stays monotonic: lower tiers
// never exceed HighRisk and upper tiers never fall below it. A threshold <= 0
// leaves the table unchanged.
func (t LevelTable) WithReviewThreshold(threshold float64) LevelTable {
	if threshold <= 0 {
		return t
	}
	out := t
	out.HighRisk = threshold
	if out.Suspicious > threshold {
		out.Suspicious = threshold
	}
	if out.Informational > out.Suspicious {
		out.Informational = out.Suspicious
	}
	if out.Critical < threshold {
		out.Critical = threshold
	}
	if out.ActionWorthy < out.Critical {
		out.ActionWorthy = out.Critical
	}
	return out
}

// LevelFor returns the tier a score falls into. A table that does not pass
// Validate (zero-valued, partially populated or non-monotonic) behaves like
// DefaultLevelTable, so scores built outside the scorer, or decoded from a
// snapshot with a half-filled "levels" block, still classify sensibly: with
// a zero ActionWorthy boundary every score, including 0, would otherwise be
// action_worthy.
func (t LevelTable) LevelFor(score float64) ScoringLevel {
	if t.Validate() != nil {
		t = DefaultLevelTable()
	}
	switch {
	case score >= t.ActionWorthy:
		return LevelActionWorthy
	case score >= t.Critical:
		return LevelCritical
	case score >= t.HighRisk:
		return LevelHighRisk
	case score >= t.Suspicious:
		return LevelSuspicious
	case score >= t.Informational:
		return LevelInformational
	default:
		return LevelClean
	}
}

// Rank returns an ordinal for a level (clean=0 .. action_worthy=5) so callers
// can compare tiers without string comparisons.
func (l ScoringLevel) Rank() int {
	switch l {
	case LevelInformational:
		return 1
	case LevelSuspicious:
		return 2
	case LevelHighRisk:
		return 3
	case LevelCritical:
		return 4
	case LevelActionWorthy:
		return 5
	default:
		return 0
	}
}

// AtLeast reports whether l is the same tier as other or a higher one.
func (l ScoringLevel) AtLeast(other ScoringLevel) bool {
	return l.Rank() >= other.Rank()
}

// SuspicionScore holds per-player scoring state.
//
// TotalScore is always min(100, BaseScore + CorrelationBonus). BaseScore is
// the accumulated (capped, diminished) event contribution; CorrelationBonus
// is recomputed from the distinct evidence categories on every call to the
// scorer's ApplyCorrelationBonus and is never accumulated.
//
// Persistence note: sqlite.StoreSuspicionScore currently writes only
// PlayerID, TotalScore, ScoreByDetector, ScoreByCategory, EventCount and
// MatchCount. A score loaded via GetPlayerScore is therefore DISPLAY-ONLY and
// must not be fed back into a SuspicionScorer or ApplyDecay. The full state
// (including the JSON-tagged tracking maps below) round-trips through
// encoding/json, which is the intended path for a complete snapshot.
type SuspicionScore struct {
	PlayerID   string  `json:"player_id"`
	TotalScore float64 `json:"total_score"`

	// BaseScore is the sum of capped event contributions before any bonus.
	BaseScore float64 `json:"base_score"`
	// CorrelationBonus is the current multi-category bonus (idempotent).
	CorrelationBonus float64 `json:"correlation_bonus"`

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

	// Levels is the tier table this score was produced under. Zero means
	// DefaultLevelTable.
	Levels LevelTable `json:"levels"`

	ReviewThreshold      float64 `json:"review_threshold"`
	AutoEnforceThreshold float64 `json:"auto_enforce_threshold"`
	ExceedsReview        bool    `json:"exceeds_review"`
	ExceedsAutoEnforce   bool    `json:"exceeds_auto_enforce"`

	SnapshotTime time.Time `json:"snapshot_time"`

	// Internal tracking for accumulation rules. All of these are part of the
	// JSON snapshot so a serialized score can be restored losslessly.
	DetectorCounts  map[string]int  `json:"detector_counts,omitempty"`
	CategoryCounts  map[string]int  `json:"category_counts,omitempty"`
	LastEventFrames map[string]int  `json:"last_event_frames,omitempty"`
	MatchIDs        map[string]bool `json:"match_ids,omitempty"`
}

// Level returns the scoring level based on TotalScore and the score's own
// LevelTable (DefaultLevelTable when unset).
func (s *SuspicionScore) Level() ScoringLevel {
	return s.Levels.LevelFor(s.TotalScore)
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

// IsRestorable reports whether the score carries enough state to be resumed
// by a scorer (as opposed to a lossy storage snapshot). A score with events
// but no tracking maps or event times came from a partial persistence path.
func (s *SuspicionScore) IsRestorable() bool {
	if s.EventCount == 0 {
		return true
	}
	return s.DetectorCounts != nil && s.CategoryCounts != nil &&
		s.LastEventFrames != nil && s.MatchIDs != nil && !s.LastEventTime.IsZero()
}

// Clone returns a deep copy that shares no maps with the receiver. Every
// value handed out of a SuspicionScorer is a Clone so callers can range over
// or JSON-encode it while ingestion continues on another goroutine.
func (s *SuspicionScore) Clone() SuspicionScore {
	if s == nil {
		return SuspicionScore{}
	}
	out := *s
	out.ScoreByDetector = cloneFloatMap(s.ScoreByDetector)
	out.ScoreByCategory = cloneFloatMap(s.ScoreByCategory)
	out.DetectorCounts = cloneIntMap(s.DetectorCounts)
	out.CategoryCounts = cloneIntMap(s.CategoryCounts)
	out.LastEventFrames = cloneIntMap(s.LastEventFrames)
	if s.MatchIDs != nil {
		out.MatchIDs = make(map[string]bool, len(s.MatchIDs))
		for k, v := range s.MatchIDs {
			out.MatchIDs[k] = v
		}
	}
	return out
}

func cloneFloatMap(m map[string]float64) map[string]float64 {
	if m == nil {
		return nil
	}
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneIntMap(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Enforcement action types. Everything the enforcement engine or policy can
// emit is listed here; consumers switching on ActionType must handle all of
// them. "flag", "kick" and "review_queue" are RECOMMENDATIONS surfaced to
// moderators, never live actions.
const (
	ActionWarn        = "warn"
	ActionTempBan     = "temp_ban"
	ActionPermBan     = "perm_ban"
	ActionDismiss     = "dismiss"
	ActionEscalate    = "escalate"
	ActionRestrict    = "restrict"
	ActionReviewQueue = "review_queue"
	ActionFlag        = "flag"
	ActionKick        = "kick"
)

// EnforcementAction represents an action taken (or recommended) against a player.
type EnforcementAction struct {
	ActionID   string `json:"action_id"`
	PlayerID   string `json:"player_id"`
	ActionType string `json:"action_type"` // one of the Action* constants
	Reason     string `json:"reason"`
	// Duration is non-zero only for time-limited actions (temp_ban, restrict).
	Duration    time.Duration `json:"duration,omitempty"`
	IssuedBy    string        `json:"issued_by"` // moderator ID or "auto"
	IssuedAt    time.Time     `json:"issued_at"`
	EvidenceIDs []string      `json:"evidence_ids"`
	MatchIDs    []string      `json:"match_ids"`
	ScoreAtTime float64       `json:"score_at_time"`
	Notes       string        `json:"notes,omitempty"`
}

// DurationSeconds returns Duration as whole seconds for persistence in
// enforcement_actions.duration_seconds.
func (a EnforcementAction) DurationSeconds() float64 {
	return a.Duration.Seconds()
}
