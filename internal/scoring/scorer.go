// Package scoring implements the suspicion scoring engine.
//
// # Score composition
//
// A player's TotalScore is min(100, BaseScore + CorrelationBonus).
//
//   - BaseScore accumulates event contributions
//     (severity * confidence * enforcement_weight * 100), each capped at
//     MaxSingleContribution, at most MaxContribPerDetectorPerMatch events per
//     detector, subject to a per-detector frame cooldown, and diminished
//     geometrically for repeated events in the same category.
//   - CorrelationBonus is (independentCategories - 1) * 5, capped at
//     CorrelationBonusCap, where independentCategories counts categories with
//     at least one non-meta detector firing. Meta-detectors (PAT_003,
//     PAT_004) are derived from other detectors' events and never add a
//     category. The bonus is RECOMPUTED on every ApplyCorrelationBonus call,
//     so calling it once per batch on the live path is safe.
//
// Shadow events never contribute; they only create the player's entry. An
// event whose contribution is zero (enforcement_weight 0, or a zero severity
// or confidence) is likewise not counted: it adds no category, does not
// diminish later events in its category and does not count toward the
// per-detector cap, so a detector an operator runs at weight 0 to observe it
// never moves a player's tier.
//
// # Time
//
// Event times default to the wall clock at ingest. Callers that know the
// match start (offline replays of historical matches) should call
// SetMatchStart so FirstEventTime/LastEventTime are derived from the match
// start plus the event's match-relative Timestamp. SetClock exists for tests.
//
// # Decay
//
// ApplyDecay implements the documented half-life decay on an in-memory
// scorer, but the single-match pipeline does NOT call it: a scorer lives for
// one match (offline) or one live match, and the per-match snapshot stored in
// suspicion_scores is the undecayed end-of-match value. The decay that
// moderators see is applied by cross-match aggregation
// (sqlite.ComputePlayerCrossMatchSummary), which recomputes from stored
// events. ApplyDecay is kept for long-lived scorers (e.g. a future resident
// per-player scorer) and is covered by tests.
package scoring

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// ScorerConfig holds scoring parameters.
type ScorerConfig struct {
	MaxSingleContribution         float64
	MaxContribPerDetectorPerMatch int
	SameCategoryDiminishing       float64
	// ReviewThreshold is mapped onto the HighRisk boundary of the level table
	// (see model.LevelTable.WithReviewThreshold). ExceedsReview is true when
	// Level() >= high_risk.
	ReviewThreshold      float64
	AutoEnforceThreshold float64
	DecayHalfLifeHours   float64
	CooldownFrames       int
	CorrelationBonusCap  float64
	// Levels optionally overrides the base tier table. When zero,
	// model.DefaultLevelTable() is used. ReviewThreshold (if > 0) is applied
	// on top of it in either case.
	Levels model.LevelTable
}

// EffectiveLevels returns the tier table the scorer operates under.
func (c ScorerConfig) EffectiveLevels() model.LevelTable {
	t := c.Levels
	if t.IsZero() {
		t = model.DefaultLevelTable()
	}
	return t.WithReviewThreshold(c.ReviewThreshold)
}

// metaDetectors are derived from other detectors' events and therefore do not
// constitute independent evidence categories for the correlation bonus.
var metaDetectors = map[string]bool{
	"PAT_003": true,
	"PAT_004": true,
}

// IsMetaDetector reports whether detectorID is a meta-detector (PAT_003,
// PAT_004): one whose events are derived from other detectors' events and so
// never count as an independent evidence category. Enforcement gates that
// count categories share this definition.
func IsMetaDetector(detectorID string) bool {
	return metaDetectors[strings.ToUpper(detectorID)]
}

// SuspicionScorer accumulates suspicion scores for players.
type SuspicionScorer struct {
	config  ScorerConfig
	levels  model.LevelTable
	players map[string]*model.SuspicionScore
	// bonusBasis is, per player, the undecayed nominal bonus the player's
	// CorrelationBonus was last set from. ApplyCorrelationBonus only touches
	// the (possibly decayed) bonus when the nominal value changes.
	bonusBasis map[string]float64
	mu         sync.RWMutex
	now        func() time.Time
	matchStart time.Time
}

// NewSuspicionScorer creates a new scorer.
func NewSuspicionScorer(cfg ScorerConfig) *SuspicionScorer {
	return &SuspicionScorer{
		config:     cfg,
		levels:     cfg.EffectiveLevels(),
		players:    make(map[string]*model.SuspicionScore),
		bonusBasis: make(map[string]float64),
		now:        time.Now,
	}
}

// Levels returns the tier table this scorer classifies with.
func (s *SuspicionScorer) Levels() model.LevelTable {
	return s.levels
}

// SetClock overrides the wall clock (tests and deterministic replays).
func (s *SuspicionScorer) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	s.now = now
}

// SetMatchStart anchors event times on the match start: an event's wall-clock
// time becomes start + event.Timestamp seconds. Pass the zero time to revert
// to the wall clock at ingest.
func (s *SuspicionScorer) SetMatchStart(start time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.matchStart = start
}

func (s *SuspicionScorer) eventTime(event model.DetectionEvent) time.Time {
	if !s.matchStart.IsZero() {
		return s.matchStart.Add(time.Duration(event.Timestamp * float64(time.Second)))
	}
	return s.now()
}

func (s *SuspicionScorer) getOrCreate(playerID string) *model.SuspicionScore {
	sc, ok := s.players[playerID]
	if !ok {
		sc = &model.SuspicionScore{
			PlayerID:             playerID,
			Levels:               s.levels,
			ReviewThreshold:      s.levels.HighRisk,
			AutoEnforceThreshold: s.config.AutoEnforceThreshold,
			DecayHalfLifeHours:   s.config.DecayHalfLifeHours,
		}
		sc.Init()
		s.players[playerID] = sc
	}
	return sc
}

// recompute derives TotalScore and the threshold flags from BaseScore and
// CorrelationBonus. It is the only place TotalScore is assigned.
func (s *SuspicionScorer) recompute(sc *model.SuspicionScore) {
	if sc.BaseScore < 0 {
		sc.BaseScore = 0
	}
	if sc.CorrelationBonus < 0 {
		sc.CorrelationBonus = 0
	}
	total := sc.BaseScore + sc.CorrelationBonus
	if total > 100.0 {
		total = 100.0
	}
	sc.TotalScore = total
	sc.ExceedsReview = sc.TotalScore >= s.levels.HighRisk
	sc.ExceedsAutoEnforce = s.config.AutoEnforceThreshold > 0 && sc.TotalScore >= s.config.AutoEnforceThreshold
}

// IngestEvent processes a detection event and updates the player's score.
// The returned value is a deep copy and never aliases scorer state.
func (s *SuspicionScorer) IngestEvent(event model.DetectionEvent) model.SuspicionScore {
	score, _ := s.IngestEventWithResult(event)
	return score
}

// IngestEventWithResult is IngestEvent plus an accepted flag. accepted is
// true only when the event made a positive contribution after shadow,
// cooldown, per-detector-cap and zero-contribution gates. Pipeline consumers
// use it to ensure derived signals such as PAT_004 are built only from
// evidence that actually entered the score.
func (s *SuspicionScorer) IngestEventWithResult(event model.DetectionEvent) (model.SuspicionScore, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sc := s.getOrCreate(event.PlayerID)
	if event.IsShadow {
		return sc.Clone(), false
	}

	// Cooldown: suppress duplicate events within N frames (jittered to prevent score engineering)
	cooldownKey := event.DetectorID
	cooldownJitter := int(hashString(event.PlayerID+event.DetectorID)%101) - 50 // -50 to +50
	effectiveCooldown := s.config.CooldownFrames + cooldownJitter
	if effectiveCooldown < 100 {
		effectiveCooldown = 100
	}
	if lastFrame, ok := sc.LastEventFrames[cooldownKey]; ok {
		if event.FrameIndex-lastFrame < effectiveCooldown {
			return sc.Clone(), false
		}
	}

	// Per-detector cap (checked before recording the frame so a capped
	// detector does not keep resetting its own cooldown window).
	if s.config.MaxContribPerDetectorPerMatch > 0 &&
		sc.DetectorCounts[event.DetectorID] >= s.config.MaxContribPerDetectorPerMatch {
		return sc.Clone(), false
	}
	sc.LastEventFrames[cooldownKey] = event.FrameIndex

	// Compute contribution
	contribution := event.Severity * event.Confidence * event.EnforcementWeight * 100.0
	if contribution < 0 || math.IsNaN(contribution) {
		contribution = 0
	}
	if s.config.MaxSingleContribution > 0 && contribution > s.config.MaxSingleContribution {
		contribution = s.config.MaxSingleContribution
	}
	if contribution == 0 {
		// Nothing to score: the event must not add a category (correlation
		// bonus), diminish later events in its category, count toward the
		// per-detector cap, or mark the match as one with detections. The
		// cooldown frame recorded above still applies.
		return sc.Clone(), false
	}

	// Same-category diminishing returns
	category := detectorCategory(event.DetectorID)
	catCount := sc.CategoryCounts[category]
	if catCount > 0 && s.config.SameCategoryDiminishing > 0 {
		contribution *= math.Pow(s.config.SameCategoryDiminishing, float64(catCount))
	}

	// Update score
	sc.BaseScore += contribution
	sc.ScoreByDetector[event.DetectorID] += contribution
	sc.ScoreByCategory[category] += contribution
	sc.DetectorCounts[event.DetectorID]++
	sc.CategoryCounts[category]++
	sc.EventCount++

	if event.MatchID != "" {
		if !sc.MatchIDs[event.MatchID] {
			sc.MatchIDs[event.MatchID] = true
			sc.MatchCount++
		}
	}

	at := s.eventTime(event)
	if sc.FirstEventTime.IsZero() || at.Before(sc.FirstEventTime) {
		sc.FirstEventTime = at
	}
	if at.After(sc.LastEventTime) {
		sc.LastEventTime = at
	}

	if contribution > sc.HighestSingleEvent {
		sc.HighestSingleEvent = contribution
		sc.HighestSingleDetector = event.DetectorID
	}

	s.recompute(sc)
	sc.SnapshotTime = s.now()

	return sc.Clone(), true
}

// GetScore returns a deep copy of the current score for a player.
func (s *SuspicionScorer) GetScore(playerID string) model.SuspicionScore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sc, ok := s.players[playerID]; ok {
		return sc.Clone()
	}
	return model.SuspicionScore{PlayerID: playerID, Levels: s.levels}
}

// GetAllScores returns deep copies of all player scores.
func (s *SuspicionScorer) GetAllScores() map[string]model.SuspicionScore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]model.SuspicionScore, len(s.players))
	for id, sc := range s.players {
		out[id] = sc.Clone()
	}
	return out
}

// PlayerIDs returns the tracked player IDs in sorted order.
func (s *SuspicionScorer) PlayerIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.players))
	for id := range s.players {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ExceedsReviewThreshold checks if a player exceeds the review threshold.
func (s *SuspicionScorer) ExceedsReviewThreshold(playerID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sc, ok := s.players[playerID]; ok {
		return sc.TotalScore >= s.levels.HighRisk
	}
	return false
}

// ExceedsAutoEnforceThreshold checks if a player exceeds auto-enforce.
func (s *SuspicionScorer) ExceedsAutoEnforceThreshold(playerID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sc, ok := s.players[playerID]; ok {
		return sc.ExceedsAutoEnforce
	}
	return false
}

// ApplyDecay applies half-life decay to every score as of `now`. Decay is
// anchored on LastDecayTime, falling back to LastEventTime; a player with no
// scored events is left untouched. See the package doc for who calls this.
func (s *SuspicionScorer) ApplyDecay(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config.DecayHalfLifeHours <= 0 {
		return
	}
	for _, sc := range s.players {
		if sc.LastDecayTime.IsZero() {
			sc.LastDecayTime = sc.LastEventTime
		}
		if sc.LastDecayTime.IsZero() || sc.EventCount == 0 {
			continue
		}
		elapsed := now.Sub(sc.LastDecayTime).Hours()
		if elapsed <= 0 {
			continue
		}
		factor := math.Pow(0.5, elapsed/s.config.DecayHalfLifeHours)
		sc.BaseScore *= factor
		sc.CorrelationBonus *= factor
		for k := range sc.ScoreByDetector {
			sc.ScoreByDetector[k] *= factor
		}
		for k := range sc.ScoreByCategory {
			sc.ScoreByCategory[k] *= factor
		}
		sc.LastDecayTime = now
		s.recompute(sc)
	}
}

// ApplyCorrelationBonus recomputes the multi-category bonus for every player.
// It is idempotent: calling it repeatedly without new events leaves every
// score unchanged. A bonus that ApplyDecay has decayed is left alone until
// the nominal bonus changes (a category was added, or every scored detector
// in a category decayed away); only then is it reset to the nominal value.
func (s *SuspicionScorer) ApplyCorrelationBonus() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sc := range s.players {
		bonus := 0.0
		if n := independentCategories(sc); n >= 2 {
			bonus = float64(n-1) * 5.0
			if s.config.CorrelationBonusCap >= 0 && bonus > s.config.CorrelationBonusCap {
				bonus = s.config.CorrelationBonusCap
			}
		}
		if basis, ok := s.bonusBasis[id]; ok && basis == bonus {
			continue
		}
		s.bonusBasis[id] = bonus
		if bonus == sc.CorrelationBonus {
			continue
		}
		sc.CorrelationBonus = bonus
		s.recompute(sc)
	}
}

// independentCategories counts categories with at least one scored event from
// a non-meta detector. A detector counts only while it still carries score:
// zero-contribution events are never recorded, and a detector whose score has
// decayed to nothing no longer constitutes evidence.
func independentCategories(sc *model.SuspicionScore) int {
	cats := make(map[string]bool)
	for det, n := range sc.DetectorCounts {
		if n <= 0 || sc.ScoreByDetector[det] <= 0 || IsMetaDetector(det) {
			continue
		}
		cats[detectorCategory(det)] = true
	}
	return len(cats)
}

// Reset clears all scoring state.
func (s *SuspicionScorer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.players = make(map[string]*model.SuspicionScore)
	s.bonusBasis = make(map[string]float64)
}

func hashString(s string) uint64 {
	var h uint64
	for _, b := range []byte(s) {
		h = h*31 + uint64(b)
	}
	return h
}

func detectorCategory(detectorID string) string {
	parts := strings.SplitN(detectorID, "_", 2)
	if len(parts) == 0 {
		return "unknown"
	}
	switch strings.ToUpper(parts[0]) {
	case "THROW":
		return "throw"
	case "BIO":
		return "bio"
	case "MOV":
		return "movement"
	case "STATE":
		return "state"
	case "PAT":
		return "pattern"
	default:
		return "unknown"
	}
}
