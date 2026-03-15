// Package enforce implements conservative enforcement policies.
package enforce

import (
	"log/slog"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Policy determines enforcement actions based on suspicion scores.
type Policy struct {
	mode   string // "shadow", "review", "enforce"
	logger *slog.Logger
}

// NewPolicy creates an enforcement policy. Default mode is "shadow" (safest).
func NewPolicy(mode string, logger *slog.Logger) *Policy {
	if mode == "" {
		mode = "shadow"
	}
	return &Policy{mode: mode, logger: logger}
}

// Evaluate determines what action, if any, is warranted.
func (p *Policy) Evaluate(
	playerID string,
	score model.SuspicionScore,
	events []model.DetectionEvent,
) *model.EnforcementAction {
	// Shadow mode: never enforce, only log
	if p.mode == "shadow" {
		return nil
	}

	// Review mode: only create review cases, no direct enforcement
	if p.mode == "review" {
		if score.TotalScore >= 60 {
			return &model.EnforcementAction{
				PlayerID:   playerID,
				ActionType: "review_queue",
				Reason:     "Score exceeded review threshold",
				IssuedBy:   "auto",
			}
		}
		return nil
	}

	// Enforce mode (conservative)
	switch {
	case score.TotalScore >= 95:
		// Only auto-ban if hard impossibility evidence exists
		hasHard := false
		for _, ev := range events {
			if ev.AutoEnforce && ev.Confidence > 0.95 {
				hasHard = true
				break
			}
		}
		if hasHard && score.MatchCount >= 2 {
			return &model.EnforcementAction{
				PlayerID:    playerID,
				ActionType:  "temp_ban",
				Reason:      "Hard impossibility confirmed across multiple matches",
				IssuedBy:    "auto",
				ScoreAtTime: score.TotalScore,
			}
		}
		return &model.EnforcementAction{
			PlayerID:    playerID,
			ActionType:  "restrict",
			Reason:      "Score exceeds action threshold - pending review",
			IssuedBy:    "auto",
			ScoreAtTime: score.TotalScore,
		}
	case score.TotalScore >= 80:
		return &model.EnforcementAction{
			PlayerID:    playerID,
			ActionType:  "restrict",
			Reason:      "Score exceeds critical threshold",
			IssuedBy:    "auto",
			ScoreAtTime: score.TotalScore,
		}
	case score.TotalScore >= 60:
		return &model.EnforcementAction{
			PlayerID:    playerID,
			ActionType:  "review_queue",
			Reason:      "Score exceeds review threshold",
			IssuedBy:    "auto",
			ScoreAtTime: score.TotalScore,
		}
	}
	return nil
}

// ShouldCreateCase returns true if the score warrants a review case.
func (p *Policy) ShouldCreateCase(score model.SuspicionScore) bool {
	return score.TotalScore >= 40 // shadow flag at 40+
}

// RecommendedAction returns the recommended action string for a score.
func (p *Policy) RecommendedAction(score model.SuspicionScore) string {
	switch {
	case score.TotalScore >= 95:
		return "temp_restrict"
	case score.TotalScore >= 80:
		return "temp_restrict"
	case score.TotalScore >= 60:
		return "review_only"
	case score.TotalScore >= 40:
		return "enhanced_monitoring"
	default:
		return "none"
	}
}
