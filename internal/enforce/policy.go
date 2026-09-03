// Policy generates enforcement recommendations based on suspicion scores.
// Recommendations are stored in the database for moderator review.
// See engine.go package doc for the full architectural context.
package enforce

import (
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Policy determines enforcement actions based on suspicion scores. It is the
// stateless (no cooldown, no store) counterpart of Engine.
type Policy struct {
	mode        string // one of the Mode constants: "shadow", "flag", "review", "enforce"
	logger      *slog.Logger
	levels      model.LevelTable
	banDuration time.Duration
	now         func() time.Time
}

// NewPolicy creates an enforcement policy. Default mode is "shadow" (safest).
// The mode is matched case-insensitively after trimming whitespace; a value
// that is not one of the Mode constants is logged and treated as shadow, so a
// typo can never switch enforcement on.
func NewPolicy(mode string, logger *slog.Logger) *Policy {
	if logger == nil {
		logger = slog.Default()
	}
	raw := mode
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch Mode(mode) {
	case ModeShadow, ModeFlag, ModeReview, ModeEnforce:
	case "":
		mode = string(ModeShadow)
	default:
		logger.Warn("unknown enforcement mode, falling back to shadow", "mode", raw)
		mode = string(ModeShadow)
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

// Mode returns the effective (validated) mode of the policy.
func (p *Policy) Mode() Mode { return Mode(p.mode) }

// Evaluate determines what action, if any, is warranted. Only the caller's
// non-shadow events for playerID are considered as evidence. Shadow never
// acts; flag recommends a moderator flag from the suspicious tier; review
// queues a case from high_risk; only enforce produces restrict/temp_ban
// recommendations.
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

	switch Mode(p.mode) {
	case ModeFlag:
		if level.AtLeast(model.LevelSuspicious) {
			return p.action(playerID, model.ActionFlag, "Score reached suspicious threshold", score, playerEvents)
		}
		return nil
	case ModeReview:
		// Review mode: only create review cases, no direct enforcement
		if level.AtLeast(model.LevelHighRisk) {
			return p.action(playerID, model.ActionReviewQueue, "Score exceeded review threshold", score, playerEvents)
		}
		return nil
	case ModeEnforce:
		// handled below
	default:
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
