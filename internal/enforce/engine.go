// Package enforce implements the configurable enforcement engine.
package enforce

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// Mode represents enforcement operation modes.
type Mode string

const (
	ModeShadow  Mode = "shadow"  // Log only, no player impact
	ModeFlag    Mode = "flag"    // Notify moderators via webhook
	ModeReview  Mode = "review"  // Create review cases with replay clips
	ModeEnforce Mode = "enforce" // Auto-kick or auto-ban
)

// Engine makes enforcement decisions based on suspicion scores and detection events.
type Engine struct {
	mode   Mode
	store  *sqlite.Store
	logger *slog.Logger
	mu     sync.Mutex

	// Enforcement callbacks
	OnKick       func(playerID, matchID, reason string)
	OnBan        func(playerID string, duration time.Duration, reason string)
	OnFlag       func(playerID, matchID string, score float64, events []model.DetectionEvent)
	OnReviewCase func(rc model.ReviewCase)

	// Cooldown tracking: prevent re-enforcement within window
	enforcementCooldowns map[string]time.Time // playerID -> last enforcement time
	cooldownDuration     time.Duration

	// Multi-detector confirmation thresholds
	minCategoriesForKick int
	minCategoriesForBan  int
	minMatchesForBan     int
	minConfidenceForBan  float64
}

// EngineConfig configures the enforcement engine.
type EngineConfig struct {
	Mode                 Mode
	CooldownDuration     time.Duration
	MinCategoriesForKick int     // minimum detector categories for auto-kick (default 2)
	MinCategoriesForBan  int     // minimum detector categories for auto-ban (default 2)
	MinMatchesForBan     int     // minimum matches with detections for auto-ban (default 2)
	MinConfidenceForBan  float64 // minimum avg confidence for auto-ban (default 0.9)
}

func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		Mode:                 ModeShadow,
		CooldownDuration:     10 * time.Minute,
		MinCategoriesForKick: 2,
		MinCategoriesForBan:  2,
		MinMatchesForBan:     2,
		MinConfidenceForBan:  0.9,
	}
}

func NewEngine(cfg EngineConfig, store *sqlite.Store, logger *slog.Logger) *Engine {
	return &Engine{
		mode:                 cfg.Mode,
		store:                store,
		logger:               logger,
		enforcementCooldowns: make(map[string]time.Time),
		cooldownDuration:     cfg.CooldownDuration,
		minCategoriesForKick: cfg.MinCategoriesForKick,
		minCategoriesForBan:  cfg.MinCategoriesForBan,
		minMatchesForBan:     cfg.MinMatchesForBan,
		minConfidenceForBan:  cfg.MinConfidenceForBan,
	}
}

// Evaluate processes a player's score and events, returning any enforcement action taken.
func (e *Engine) Evaluate(
	ctx context.Context,
	playerID string,
	matchID string,
	score model.SuspicionScore,
	events []model.DetectionEvent,
) *model.EnforcementAction {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Check cooldown
	if lastEnforce, ok := e.enforcementCooldowns[playerID]; ok {
		if time.Since(lastEnforce) < e.cooldownDuration {
			return nil
		}
	}

	// Collect non-shadow events for this player
	var playerEvents []model.DetectionEvent
	categories := make(map[string]bool)
	var totalConf float64
	for _, ev := range events {
		if ev.PlayerID == playerID && !ev.IsShadow {
			playerEvents = append(playerEvents, ev)
			categories[detectorCategory(ev.DetectorID)] = true
			totalConf += ev.Confidence
		}
	}
	if len(playerEvents) == 0 {
		return nil
	}
	avgConf := totalConf / float64(len(playerEvents))
	catCount := len(categories)

	// Has hard impossibility evidence?
	hasHard := false
	for _, ev := range playerEvents {
		if ev.AutoEnforce && ev.Confidence > 0.95 {
			hasHard = true
			break
		}
	}

	var action *model.EnforcementAction

	switch e.mode {
	case ModeShadow:
		// Log only — no action
		e.logger.Info("shadow_enforcement",
			"player", playerID, "match", matchID,
			"score", fmt.Sprintf("%.1f", score.TotalScore),
			"events", len(playerEvents), "categories", catCount,
		)
		return nil

	case ModeFlag:
		if score.TotalScore >= 40 {
			e.logger.Info("flagging_player",
				"player", playerID, "score", fmt.Sprintf("%.1f", score.TotalScore),
			)
			if e.OnFlag != nil {
				e.OnFlag(playerID, matchID, score.TotalScore, playerEvents)
			}
			action = &model.EnforcementAction{
				ActionID:    uuid.New().String(),
				PlayerID:    playerID,
				ActionType:  "flag",
				Reason:      fmt.Sprintf("Score %.1f with %d detections across %d categories", score.TotalScore, len(playerEvents), catCount),
				IssuedBy:    "auto",
				IssuedAt:    time.Now(),
				ScoreAtTime: score.TotalScore,
			}
		}

	case ModeReview:
		if score.TotalScore >= 60 {
			action = &model.EnforcementAction{
				ActionID:    uuid.New().String(),
				PlayerID:    playerID,
				ActionType:  "review_queue",
				Reason:      fmt.Sprintf("Score %.1f, %d categories", score.TotalScore, catCount),
				IssuedBy:    "auto",
				IssuedAt:    time.Now(),
				ScoreAtTime: score.TotalScore,
			}
		}

	case ModeEnforce:
		switch {
		case score.TotalScore >= 95 && hasHard && score.MatchCount >= e.minMatchesForBan &&
			catCount >= e.minCategoriesForBan && avgConf >= e.minConfidenceForBan:
			// Auto-ban: requires hard impossibility + multi-match + multi-category + high confidence
			action = &model.EnforcementAction{
				ActionID:    uuid.New().String(),
				PlayerID:    playerID,
				ActionType:  "temp_ban",
				Reason:      fmt.Sprintf("Hard impossibility confirmed: score=%.1f, %d matches, %d categories, avg_conf=%.2f", score.TotalScore, score.MatchCount, catCount, avgConf),
				Duration:    7 * 24 * time.Hour,
				IssuedBy:    "auto",
				IssuedAt:    time.Now(),
				ScoreAtTime: score.TotalScore,
			}
			if e.OnBan != nil {
				e.OnBan(playerID, 7*24*time.Hour, action.Reason)
			}

		case score.TotalScore >= 80 && catCount >= e.minCategoriesForKick:
			// Auto-kick from current match
			action = &model.EnforcementAction{
				ActionID:    uuid.New().String(),
				PlayerID:    playerID,
				ActionType:  "kick",
				Reason:      fmt.Sprintf("Score %.1f with %d categories", score.TotalScore, catCount),
				IssuedBy:    "auto",
				IssuedAt:    time.Now(),
				ScoreAtTime: score.TotalScore,
			}
			if e.OnKick != nil {
				e.OnKick(playerID, matchID, action.Reason)
			}

		case score.TotalScore >= 60:
			action = &model.EnforcementAction{
				ActionID:    uuid.New().String(),
				PlayerID:    playerID,
				ActionType:  "review_queue",
				Reason:      fmt.Sprintf("Score %.1f pending review", score.TotalScore),
				IssuedBy:    "auto",
				IssuedAt:    time.Now(),
				ScoreAtTime: score.TotalScore,
			}
		}
	}

	if action != nil {
		e.enforcementCooldowns[playerID] = time.Now()
		if err := e.store.StoreEnforcementAction(ctx, *action); err != nil {
			e.logger.Error("failed to store enforcement action", "error", err)
		}
		e.logger.Info("enforcement_action",
			"player", playerID, "action", action.ActionType,
			"score", fmt.Sprintf("%.1f", score.TotalScore), "reason", action.Reason,
		)
	}

	return action
}

func detectorCategory(id string) string {
	upper := strings.ToUpper(id)
	switch {
	case strings.HasPrefix(upper, "THROW"):
		return "throw"
	case strings.HasPrefix(upper, "BIO"):
		return "bio"
	case strings.HasPrefix(upper, "MOV"):
		return "movement"
	case strings.HasPrefix(upper, "STATE"):
		return "state"
	case strings.HasPrefix(upper, "PAT"):
		return "pattern"
	default:
		return "unknown"
	}
}
