package ingest

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// LiveMatch holds the state of an active match being analyzed in real-time.
type LiveMatch struct {
	MatchCtx     *model.MatchContext
	Pipeline     *pipeline.Pipeline
	Scorer       *scoring.SuspicionScorer
	Players      map[string]*model.PlayerState
	FrameCount   int
	StartTime    time.Time
	LastActivity time.Time
	initialized  bool // true after the first batch has been processed (detectors already reset)
	mu           sync.Mutex
}

// MatchManager manages active match pipelines.
type MatchManager struct {
	cfg        *config.Config
	store      *sqlite.Store
	detectorFn func() []detect.Detector // factory to create fresh detectors per match
	logger     *slog.Logger

	matches   map[string]*LiveMatch
	matchesMu sync.RWMutex

	// Enforcement callback
	OnEnforcement func(action model.EnforcementAction)
	// Review case callback
	OnReviewCase func(rc model.ReviewCase)
}

// NewMatchManager creates a new match manager.
func NewMatchManager(
	cfg *config.Config,
	store *sqlite.Store,
	detectorFn func() []detect.Detector,
	logger *slog.Logger,
) *MatchManager {
	return &MatchManager{
		cfg:        cfg,
		store:      store,
		detectorFn: detectorFn,
		logger:     logger,
		matches:    make(map[string]*LiveMatch),
	}
}

// HandleFrames processes a batch of frames for a match.
func (mm *MatchManager) HandleFrames(matchID string, frames []model.PlayerTelemetryFrame) {
	mm.matchesMu.Lock()
	match, ok := mm.matches[matchID]
	if !ok {
		match = mm.createMatch(matchID, frames)
		mm.matches[matchID] = match
	}
	mm.matchesMu.Unlock()

	match.mu.Lock()
	defer match.mu.Unlock()

	// Process frames through pipeline.
	// On the first batch, allow normal reset to initialize state.
	// On subsequent batches, skip reset to preserve accumulated detector state.
	if match.initialized {
		match.Pipeline.SetSkipReset(true)
	}
	ctx := context.Background()
	result, err := match.Pipeline.ProcessMatch(ctx, match.MatchCtx, frames)
	match.Pipeline.SetSkipReset(false)
	match.initialized = true
	if err != nil {
		mm.logger.Error("live match processing error", "match", matchID, "error", err)
		return
	}

	match.FrameCount += result.FramesProcessed
	match.LastActivity = time.Now()

	// Store detection events
	for _, ev := range result.DetectionEvents {
		if storeErr := mm.store.StoreDetectionEvent(ctx, ev); storeErr != nil {
			mm.logger.Error("failed to store event", "error", storeErr)
		}
	}

	// Store scores
	for _, score := range result.PlayerScores {
		if score.EventCount > 0 {
			_ = mm.store.StoreSuspicionScore(ctx, score)
		}
	}
}

func (mm *MatchManager) createMatch(matchID string, initialFrames []model.PlayerTelemetryFrame) *LiveMatch {
	// Derive player IDs from initial frames
	playerIDs := make([]string, 0)
	seen := make(map[string]bool)
	for _, f := range initialFrames {
		if !seen[f.PlayerID] {
			playerIDs = append(playerIDs, f.PlayerID)
			seen[f.PlayerID] = true
		}
	}

	matchCtx := &model.MatchContext{
		MatchID:         matchID,
		GameMode:        "Echo_Arena",
		StartTime:       time.Now(),
		PlayerIDs:       playerIDs,
		TeamAssignments: make(map[string]string),
		Physics:         model.DefaultPhysics(),
		Source:          "live_telemetry",
		TickRate:        15.0,
	}

	detectors := mm.detectorFn()
	scorer := scoring.NewSuspicionScorer(scoring.ScorerConfig{
		MaxSingleContribution:         mm.cfg.Scoring.MaxSingleContribution,
		MaxContribPerDetectorPerMatch: mm.cfg.Scoring.MaxContribPerDetectorPerMatch,
		SameCategoryDiminishing:       mm.cfg.Scoring.SameCategoryDiminishing,
		ReviewThreshold:               mm.cfg.Scoring.ReviewThreshold,
		AutoEnforceThreshold:          mm.cfg.Scoring.AutoEnforceThreshold,
		DecayHalfLifeHours:            mm.cfg.Scoring.DecayHalfLifeHours,
		CooldownFrames:                mm.cfg.Pipeline.CooldownFrames,
		CorrelationBonusCap:           mm.cfg.Scoring.CorrelationBonusCap,
	})

	pipe := pipeline.NewPipeline(mm.cfg, detectors, scorer, mm.logger)

	mm.logger.Info("live match created", "match", matchID, "players", len(playerIDs))

	return &LiveMatch{
		MatchCtx:     matchCtx,
		Pipeline:     pipe,
		Scorer:       scorer,
		Players:      make(map[string]*model.PlayerState),
		StartTime:    time.Now(),
		LastActivity: time.Now(),
	}
}

// EndMatch finalizes a match and stores summary.
func (mm *MatchManager) EndMatch(matchID string) {
	mm.matchesMu.Lock()
	match, ok := mm.matches[matchID]
	if ok {
		delete(mm.matches, matchID)
	}
	mm.matchesMu.Unlock()

	if !ok {
		return
	}

	match.mu.Lock()
	defer match.mu.Unlock()

	mm.logger.Info("live match ended", "match", matchID, "frames", match.FrameCount)

	// Store match summary
	summary := model.MatchSummary{
		MatchID:    matchID,
		GameMode:   match.MatchCtx.GameMode,
		StartTime:  match.StartTime,
		Duration:   time.Since(match.StartTime),
		FrameCount: match.FrameCount,
		Source:     "live_telemetry",
	}
	_ = mm.store.StoreMatchSummary(context.Background(), summary)
}

// CleanupStaleMatches removes matches with no activity for the given duration.
func (mm *MatchManager) CleanupStaleMatches(maxIdle time.Duration) {
	mm.matchesMu.Lock()
	defer mm.matchesMu.Unlock()
	cutoff := time.Now().Add(-maxIdle)
	for id, m := range mm.matches {
		if m.LastActivity.Before(cutoff) {
			mm.logger.Warn("removing stale match", "match", id, "idle", time.Since(m.LastActivity))
			delete(mm.matches, id)
		}
	}
}

// ActiveMatchCount returns the number of active matches.
func (mm *MatchManager) ActiveMatchCount() int {
	mm.matchesMu.RLock()
	defer mm.matchesMu.RUnlock()
	return len(mm.matches)
}

// GetMatchScores returns current scores for a match.
func (mm *MatchManager) GetMatchScores(matchID string) map[string]model.SuspicionScore {
	mm.matchesMu.RLock()
	match, ok := mm.matches[matchID]
	mm.matchesMu.RUnlock()
	if !ok {
		return nil
	}
	match.mu.Lock()
	defer match.mu.Unlock()
	return match.Scorer.GetAllScores()
}
