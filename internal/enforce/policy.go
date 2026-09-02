// Policy generates enforcement recommendations based on suspicion scores.
// Recommendations are stored in the database for moderator review.
// See engine.go package doc for the full architectural context.
package enforce

import (
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Policy determines enforcement actions based on suspicion scores. It is the
// stateless (no cooldown, no store) counterpart of Engine.
type Policy struct {
	mode        string // "shadow", "review", "enforce"
	logger      *slog.Logger
	levels      model.LevelTable
	banDuration time.Duration
	now         func() time.Time
}

// NewPolicy creates an enforcement policy. Default mode is "shadow" (safest).
func NewPolicy(mode string, logger *slog.Logger) *Policy {
	if mode == "" {
		mode = string(ModeShadow)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Policy{
		mode:        mode,
		logger:      logger,
		levels:      model.DefaultLevelTable(),
		banDuration: 7 * 24 * time.Hour,
		now:         time.Now,
	}
}

// WithLevels sets the tier table the policy gates on and returns the policy.
func (p *Policy) WithLevels(t model.LevelTable) *Policy {
	if !t.IsZero() {
		p.levels = t
	}
	return p
}

// SetClock overrides the wall clock (tests).
func (p *Policy) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	p.now = now
}

func (p *Policy) action(playerID, actionType, reason string, score model.SuspicionScore, events []model.DetectionEvent) *model.EnforcementAction {
	return &model.EnforcementAction{
		ActionID:    uuid.New().String(),
		PlayerID:    playerID,
		ActionType:  actionType,
		Reason:      reason,
		IssuedBy:    "auto",
		IssuedAt:    p.now(),
		EvidenceIDs: eventIDs(events),
		MatchIDs:    matchIDsFor("", score),
		ScoreAtTime: score.TotalScore,
	}
}

// Evaluate determines what action, if any, is warranted. Only the caller's
// non-shadow events for playerID are considered as evidence.
func (p *Policy) Evaluate(
	playerID string,
	score model.SuspicionScore,
	events []model.DetectionEvent,
) *model.EnforcementAction {
	// Shadow mode: never enforce, only log
	if p.mode == string(ModeShadow) {
		return nil
	}

	var playerEvents []model.DetectionEvent
	for _, ev := range events {
		if ev.PlayerID == playerID && !ev.IsShadow {
			playerEvents = append(playerEvents, ev)
		}
	}
	level := p.levels.LevelFor(score.TotalScore)

	// Review mode: only create review cases, no direct enforcement
	if p.mode == string(ModeReview) {
		if level.AtLeast(model.LevelHighRisk) {
			return p.action(playerID, model.ActionReviewQueue, "Score exceeded review threshold", score, playerEvents)
		}
		return nil
	}

	// Enforce mode (conservative)
	switch {
	case level.AtLeast(model.LevelActionWorthy):
		// Only recommend a temp ban if hard impossibility evidence exists across matches
		hasHard := false
		for _, ev := range playerEvents {
			if ev.AutoEnforce && ev.Confidence > 0.95 {
				hasHard = true
				break
			}
		}
		if hasHard && score.MatchCount >= 2 {
			a := p.action(playerID, model.ActionTempBan, "Hard impossibility confirmed across multiple matches", score, playerEvents)
			a.Duration = p.banDuration
			return a
		}
		return p.action(playerID, model.ActionRestrict, "Score exceeds action threshold - pending review", score, playerEvents)
	case level.AtLeast(model.LevelCritical):
		return p.action(playerID, model.ActionRestrict, "Score exceeds critical threshold", score, playerEvents)
	case level.AtLeast(model.LevelHighRisk):
		return p.action(playerID, model.ActionReviewQueue, "Score exceeds review threshold", score, playerEvents)
	}
	return nil
}

// ShouldCreateCase returns true if the score warrants a review case
// (suspicious tier or above: the README "shadow flag for monitoring" band).
func (p *Policy) ShouldCreateCase(score model.SuspicionScore) bool {
	return p.levels.LevelFor(score.TotalScore).AtLeast(model.LevelSuspicious)
}

// RecommendedAction returns the recommended action string for a score.
func (p *Policy) RecommendedAction(score model.SuspicionScore) string {
	return model.RecommendedActionForLevel(p.levels.LevelFor(score.TotalScore))
}
