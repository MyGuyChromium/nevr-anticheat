// Package scoring implements the suspicion scoring engine.
package scoring

import (
	"math"
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
	ReviewThreshold               float64
	AutoEnforceThreshold          float64
	DecayHalfLifeHours            float64
	CooldownFrames                int
	CorrelationBonusCap           float64
}

// SuspicionScorer accumulates suspicion scores for players.
type SuspicionScorer struct {
	config  ScorerConfig
	players map[string]*model.SuspicionScore
	mu      sync.RWMutex
}

// NewSuspicionScorer creates a new scorer.
func NewSuspicionScorer(cfg ScorerConfig) *SuspicionScorer {
	return &SuspicionScorer{
		config:  cfg,
		players: make(map[string]*model.SuspicionScore),
	}
}

func (s *SuspicionScorer) getOrCreate(playerID string) *model.SuspicionScore {
	sc, ok := s.players[playerID]
	if !ok {
		sc = &model.SuspicionScore{
			PlayerID:             playerID,
			ReviewThreshold:      s.config.ReviewThreshold,
			AutoEnforceThreshold: s.config.AutoEnforceThreshold,
			DecayHalfLifeHours:   s.config.DecayHalfLifeHours,
		}
		sc.Init()
		s.players[playerID] = sc
	}
	return sc
}

// IngestEvent processes a detection event and updates the player's score.
func (s *SuspicionScorer) IngestEvent(event model.DetectionEvent) model.SuspicionScore {
	s.mu.Lock()
	defer s.mu.Unlock()

	if event.IsShadow {
		sc := s.getOrCreate(event.PlayerID)
		return *sc
	}

	sc := s.getOrCreate(event.PlayerID)

	// Cooldown: suppress duplicate events within N frames (jittered to prevent score engineering)
	cooldownKey := event.DetectorID
	cooldownJitter := int(hashString(event.PlayerID+event.DetectorID) % 101) - 50 // -50 to +50
	effectiveCooldown := s.config.CooldownFrames + cooldownJitter
	if effectiveCooldown < 100 {
		effectiveCooldown = 100
	}
	if lastFrame, ok := sc.LastEventFrames[cooldownKey]; ok {
		if event.FrameIndex-lastFrame < effectiveCooldown {
			return *sc
		}
	}
	sc.LastEventFrames[cooldownKey] = event.FrameIndex

	// Per-detector cap
	if sc.DetectorCounts[event.DetectorID] >= s.config.MaxContribPerDetectorPerMatch {
		return *sc
	}

	// Compute contribution
	contribution := event.Severity * event.Confidence * event.EnforcementWeight * 100.0
	if contribution > s.config.MaxSingleContribution {
		contribution = s.config.MaxSingleContribution
	}

	// Same-category diminishing returns
	category := detectorCategory(event.DetectorID)
	catCount := sc.CategoryCounts[category]
	if catCount > 0 {
		contribution *= math.Pow(s.config.SameCategoryDiminishing, float64(catCount))
	}

	// Update score
	sc.TotalScore += contribution
	if sc.TotalScore > 100.0 {
		sc.TotalScore = 100.0
	}
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

	now := time.Now()
	if sc.FirstEventTime.IsZero() {
		sc.FirstEventTime = now
	}
	sc.LastEventTime = now

	if contribution > sc.HighestSingleEvent {
		sc.HighestSingleEvent = contribution
		sc.HighestSingleDetector = event.DetectorID
	}

	sc.ExceedsReview = sc.TotalScore >= s.config.ReviewThreshold
	sc.ExceedsAutoEnforce = sc.TotalScore >= s.config.AutoEnforceThreshold
	sc.SnapshotTime = now

	return *sc
}

// GetScore returns the current score for a player.
func (s *SuspicionScorer) GetScore(playerID string) model.SuspicionScore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sc, ok := s.players[playerID]; ok {
		return *sc
	}
	return model.SuspicionScore{PlayerID: playerID}
}

// GetAllScores returns all player scores.
func (s *SuspicionScorer) GetAllScores() map[string]model.SuspicionScore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]model.SuspicionScore, len(s.players))
	for id, sc := range s.players {
		out[id] = *sc
	}
	return out
}

// ExceedsReviewThreshold checks if a player exceeds the review threshold.
func (s *SuspicionScorer) ExceedsReviewThreshold(playerID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sc, ok := s.players[playerID]; ok {
		return sc.TotalScore >= s.config.ReviewThreshold
	}
	return false
}

// ExceedsAutoEnforceThreshold checks if a player exceeds auto-enforce.
func (s *SuspicionScorer) ExceedsAutoEnforceThreshold(playerID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sc, ok := s.players[playerID]; ok {
		return sc.TotalScore >= s.config.AutoEnforceThreshold
	}
	return false
}

// ApplyDecay applies time-based decay to all scores.
func (s *SuspicionScorer) ApplyDecay(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sc := range s.players {
		if sc.LastDecayTime.IsZero() {
			sc.LastDecayTime = sc.LastEventTime
		}
		elapsed := now.Sub(sc.LastDecayTime).Hours()
		if elapsed <= 0 {
			continue
		}
		factor := math.Pow(0.5, elapsed/s.config.DecayHalfLifeHours)
		sc.TotalScore *= factor
		for k := range sc.ScoreByDetector {
			sc.ScoreByDetector[k] *= factor
		}
		for k := range sc.ScoreByCategory {
			sc.ScoreByCategory[k] *= factor
		}
		sc.LastDecayTime = now
		sc.ExceedsReview = sc.TotalScore >= s.config.ReviewThreshold
		sc.ExceedsAutoEnforce = sc.TotalScore >= s.config.AutoEnforceThreshold
	}
}

// ApplyCorrelationBonus adds a bonus when a player has detections across multiple categories.
func (s *SuspicionScorer) ApplyCorrelationBonus() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sc := range s.players {
		distinctCategories := len(sc.CategoryCounts)
		if distinctCategories >= 2 {
			bonus := float64(distinctCategories-1) * 5.0
			if bonus > s.config.CorrelationBonusCap {
				bonus = s.config.CorrelationBonusCap
			}
			sc.TotalScore += bonus
			if sc.TotalScore > 100.0 {
				sc.TotalScore = 100.0
			}
			sc.ExceedsReview = sc.TotalScore >= s.config.ReviewThreshold
			sc.ExceedsAutoEnforce = sc.TotalScore >= s.config.AutoEnforceThreshold
		}
	}
}

// Reset clears all scoring state.
func (s *SuspicionScorer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.players = make(map[string]*model.SuspicionScore)
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
