// Package enforce implements the configurable enforcement recommendation engine.
//
// IMPORTANT: This package generates enforcement RECOMMENDATIONS, not live actions.
// In the async profiler-database architecture, enforcement decisions are made by
// moderators reviewing cases, not by automated in-match actions. The Engine
// evaluates scores and events to produce recommended actions (flag, review, restrict)
// that are stored in the database for moderator review.
//
// Default mode is "shadow" (logging only). The "enforce" mode generates the
// strongest possible recommendations but still requires moderator confirmation.
// The canonical workflow is: detect → score → review case → moderator decision.
//
// # Score semantics
//
// Every gate in this package is expressed in terms of model.LevelTable tiers
// (flag at suspicious, review at high_risk, kick at critical, temp_ban at
// action_worthy) so it agrees with Level(), the scorer and the evidence
// builder. Pass the same table the scorer uses via EngineConfig.Levels.
//
// # Hard-evidence ban gate
//
// The temp_ban recommendation additionally requires (a) at least one event
// with AutoEnforce=true and Confidence>0.95 and (b) score.MatchCount >=
// MinMatchesForBan. Both inputs are supplied by the caller: AutoEnforce is
// stamped by the event producer (detect.BaseDetector / config plumbing) and
// MatchCount by a cross-match score, since a single-match scorer never sees
// more than one match. With single-match scores and producers that never set
// AutoEnforce, the gate is deliberately unreachable.
package enforce

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
)

// Mode represents enforcement operation modes.
type Mode string

const (
	ModeShadow  Mode = "shadow"  // Log only, no player impact
	ModeFlag    Mode = "flag"    // Notify moderators via webhook
	ModeReview  Mode = "review"  // Create review cases with replay clips
	ModeEnforce Mode = "enforce" // Generate strongest recommendations (requires moderator review)
)

// ActionStore is the persistence surface the engine needs. *sqlite.Store
// satisfies it. A nil store is allowed: actions are then only logged and
// returned to the caller.
type ActionStore interface {
	StoreEnforcementAction(ctx context.Context, action model.EnforcementAction) error
}

// DurableActionStore claims an immutable recommendation once and verifies its
// evidence exists. A duplicate returns false. Callbacks require this interface;
// they remain review notifications, never authorization for a live ban.
type DurableActionStore interface {
	StoreEnforcementActionOnce(ctx context.Context, action model.EnforcementAction) (bool, error)
}

// Engine makes enforcement decisions based on suspicion scores and detection events.
type Engine struct {
	mode   Mode
	store  ActionStore
	logger *slog.Logger
	levels model.LevelTable
	mu     sync.Mutex

	// Recommendation callbacks — notify external systems of recommended actions.
	// These are recommendations for moderator review, not automatic enforcement.
	// Callbacks are invoked WITHOUT the engine lock held, so they may safely
	// call back into the engine.
	OnKick       func(playerID, matchID, reason string)
	OnBan        func(playerID string, duration time.Duration, reason string)
	OnFlag       func(playerID, matchID string, score float64, events []model.DetectionEvent)
	OnReviewCase func(rc model.ReviewCase)

	// Cooldown tracking: prevent re-enforcement within window
	enforcementCooldowns map[string]time.Time // playerID -> last enforcement time
	cooldownDuration     time.Duration
	lastPrune            time.Time

	// Multi-detector confirmation thresholds
	minCategoriesForKick int
	minCategoriesForBan  int
	minMatchesForBan     int
	minConfidenceForBan  float64
	banDuration          time.Duration

	now func() time.Time
}

// EngineConfig configures the enforcement engine.
type EngineConfig struct {
	Mode                 Mode
	CooldownDuration     time.Duration
	MinCategoriesForKick int           // minimum detector categories for auto-kick (default 2)
	MinCategoriesForBan  int           // minimum detector categories for auto-ban (default 2)
	MinMatchesForBan     int           // minimum matches with detections for auto-ban (default 2)
	MinConfidenceForBan  float64       // minimum avg confidence for auto-ban (default 0.9)
	BanDuration          time.Duration // duration of a recommended temp_ban (default 7d)
	// Levels is the tier table the gates are expressed in. Zero means
	// model.DefaultLevelTable().
	Levels model.LevelTable
}

func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		Mode:                 ModeShadow,
		CooldownDuration:     10 * time.Minute,
		MinCategoriesForKick: 2,
		MinCategoriesForBan:  2,
		MinMatchesForBan:     2,
		MinConfidenceForBan:  0.9,
		BanDuration:          7 * 24 * time.Hour,
		Levels:               model.DefaultLevelTable(),
	}
}

func NewEngine(cfg EngineConfig, store ActionStore, logger *slog.Logger) *Engine {
	if isNilStore(store) {
		store = nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	levels := cfg.Levels
	if levels.IsZero() {
		levels = model.DefaultLevelTable()
	}
	banDuration := cfg.BanDuration
	if banDuration <= 0 {
		banDuration = 7 * 24 * time.Hour
	}
	return &Engine{
		mode:                 cfg.Mode,
		store:                store,
		logger:               logger,
		levels:               levels,
		enforcementCooldowns: make(map[string]time.Time),
		cooldownDuration:     cfg.CooldownDuration,
		minCategoriesForKick: cfg.MinCategoriesForKick,
		minCategoriesForBan:  cfg.MinCategoriesForBan,
		minMatchesForBan:     cfg.MinMatchesForBan,
		minConfidenceForBan:  cfg.MinConfidenceForBan,
		banDuration:          banDuration,
		now:                  time.Now,
	}
}

// isNilStore treats a typed nil pointer (e.g. (*sqlite.Store)(nil)) as nil.
func isNilStore(s ActionStore) bool {
	if s == nil {
		return true
	}
	v := reflect.ValueOf(s)
	return v.Kind() == reflect.Ptr && v.IsNil()
}

// SetClock overrides the wall clock (tests).
func (e *Engine) SetClock(now func() time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	e.now = now
}

// Levels returns the tier table the engine gates on.
func (e *Engine) Levels() model.LevelTable { return e.levels }

// Evaluate processes a player's score and events, returning any enforcement action taken.
// The action is persisted (when a store is configured) and callbacks are
// invoked after the engine lock is released.
func (e *Engine) Evaluate(
	ctx context.Context,
	playerID string,
	matchID string,
	score model.SuspicionScore,
	events []model.DetectionEvent,
) *model.EnforcementAction {
	action, playerEvents := e.decide(playerID, matchID, score, events)
	if action == nil {
		return nil
	}

	if e.store == nil {
		return action // pure recommendation only; no durable evidence means no callback
	}
	store, ok := e.store.(DurableActionStore)
	if !ok {
		e.releaseFailedReservation(playerID, action.IssuedAt)
		e.logger.Error("recommendation store does not support durable idempotent claims")
		return nil
	}
	created, err := store.StoreEnforcementActionOnce(ctx, *action)
	if err != nil {
		e.releaseFailedReservation(playerID, action.IssuedAt)
		e.logger.Error("failed to store recommendation; callback withheld", "error", err)
		return nil
	}
	if !created {
		return nil // already recorded by an earlier attempt/process
	}
	e.logger.Info("enforcement_action",
		"player", playerID, "action", action.ActionType,
		"score", fmt.Sprintf("%.1f", score.TotalScore), "reason", action.Reason,
	)

	switch action.ActionType {
	case model.ActionFlag:
		if e.OnFlag != nil {
			e.OnFlag(playerID, matchID, score.TotalScore, playerEvents)
		}
	case model.ActionTempBan:
		if e.OnBan != nil {
			e.OnBan(playerID, action.Duration, action.Reason)
		}
	case model.ActionKick:
		if e.OnKick != nil {
			e.OnKick(playerID, matchID, action.Reason)
		}
	}
	return action
}

func (e *Engine) releaseFailedReservation(playerID string, at time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.enforcementCooldowns[playerID] == at {
		delete(e.enforcementCooldowns, playerID)
	}
}

// decide computes the action under the lock without side effects beyond the
// cooldown table.
func (e *Engine) decide(
	playerID, matchID string,
	score model.SuspicionScore,
	events []model.DetectionEvent,
) (*model.EnforcementAction, []model.DetectionEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.now()
	e.pruneCooldownsLocked(now)

	// Check cooldown
	if lastEnforce, ok := e.enforcementCooldowns[playerID]; ok {
		if now.Sub(lastEnforce) < e.cooldownDuration {
			return nil, nil
		}
	}

	// Collect non-shadow events for this player. Meta-detectors (PAT_003,
	// PAT_004) are derived from other detectors' events and do not add an
	// independent category for the multi-category gates, exactly as in the
	// scorer's correlation bonus.
	var playerEvents []model.DetectionEvent
	categories := make(map[string]bool)
	var totalConf float64
	for _, ev := range events {
		if ev.PlayerID == playerID && !ev.IsShadow {
			playerEvents = append(playerEvents, ev)
			if !scoring.IsMetaDetector(ev.DetectorID) {
				categories[detectorCategory(ev.DetectorID)] = true
			}
			totalConf += ev.Confidence
		}
	}
	if len(playerEvents) == 0 {
		return nil, nil
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

	level := e.levels.LevelFor(score.TotalScore)
	newAction := func(actionType, reason string) *model.EnforcementAction {
		return &model.EnforcementAction{
			PlayerID:    playerID,
			ActionType:  actionType,
			Reason:      reason,
			IssuedBy:    "auto",
			IssuedAt:    now,
			EvidenceIDs: eventIDs(playerEvents),
			MatchIDs:    matchIDsFor(matchID, score),
			ScoreAtTime: score.TotalScore,
		}
	}

	var action *model.EnforcementAction

	switch e.mode {
	case ModeShadow:
		// Log only — no action
		e.logger.Info("shadow_enforcement",
			"player", playerID, "match", matchID,
			"score", fmt.Sprintf("%.1f", score.TotalScore), "level", string(level),
			"events", len(playerEvents), "categories", catCount,
		)
		return nil, nil

	case ModeFlag:
		if level.AtLeast(model.LevelSuspicious) {
			e.logger.Info("flagging_player",
				"player", playerID, "score", fmt.Sprintf("%.1f", score.TotalScore),
			)
			action = newAction(model.ActionFlag,
				fmt.Sprintf("Score %.1f (%s) with %d detections across %d categories", score.TotalScore, level, len(playerEvents), catCount))
		}

	case ModeReview:
		if level.AtLeast(model.LevelHighRisk) {
			action = newAction(model.ActionReviewQueue,
				fmt.Sprintf("Score %.1f (%s), %d categories", score.TotalScore, level, catCount))
		}

	case ModeEnforce:
		switch {
		case level.AtLeast(model.LevelActionWorthy) && hasHard && score.MatchCount >= e.minMatchesForBan &&
			catCount >= e.minCategoriesForBan && avgConf >= e.minConfidenceForBan:
			// Auto-ban recommendation: requires hard impossibility + multi-match + multi-category + high confidence
			action = newAction(model.ActionTempBan,
				fmt.Sprintf("Hard impossibility confirmed: score=%.1f, %d matches, %d categories, avg_conf=%.2f", score.TotalScore, score.MatchCount, catCount, avgConf))
			action.Duration = e.banDuration

		case level.AtLeast(model.LevelCritical) && catCount >= e.minCategoriesForKick:
			// Auto-kick recommendation for the current match
			action = newAction(model.ActionKick,
				fmt.Sprintf("Score %.1f (%s) with %d categories", score.TotalScore, level, catCount))

		case level.AtLeast(model.LevelHighRisk):
			action = newAction(model.ActionReviewQueue,
				fmt.Sprintf("Score %.1f (%s) pending review", score.TotalScore, level))
		}
	}

	if action != nil {
		action.ActionID = recommendationID(*action)
		e.enforcementCooldowns[playerID] = now
	}
	return action, playerEvents
}

// pruneCooldownsLocked drops expired cooldown entries at most once per
// cooldown window so the map cannot grow without bound.
func (e *Engine) pruneCooldownsLocked(now time.Time) {
	if e.cooldownDuration <= 0 {
		e.enforcementCooldowns = make(map[string]time.Time)
		return
	}
	if !e.lastPrune.IsZero() && now.Sub(e.lastPrune) < e.cooldownDuration {
		return
	}
	e.lastPrune = now
	for pid, at := range e.enforcementCooldowns {
		if now.Sub(at) >= e.cooldownDuration {
			delete(e.enforcementCooldowns, pid)
		}
	}
}

func eventIDs(events []model.DetectionEvent) []string {
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.EventID != "" {
			ids = append(ids, ev.EventID)
		}
	}
	return ids
}

func matchIDsFor(matchID string, score model.SuspicionScore) []string {
	set := make(map[string]bool)
	if matchID != "" {
		set[matchID] = true
	}
	for id := range score.MatchIDs {
		if id != "" {
			set[id] = true
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
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
