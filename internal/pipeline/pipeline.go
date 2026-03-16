// Package pipeline orchestrates frame-by-frame detection processing.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/pattern"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
)

// MatchResult holds the complete output of a match analysis.
type MatchResult struct {
	MatchID         string                          `json:"match_id"`
	FramesProcessed int                             `json:"frames_processed"`
	InvalidFrames   int                             `json:"invalid_frames"`
	PlayerScores    map[string]model.SuspicionScore `json:"player_scores"`
	DetectionEvents []model.DetectionEvent          `json:"detection_events"`
	ReviewCases     []model.ReviewCase              `json:"review_cases"`
	Duration        time.Duration                   `json:"duration"`
}

// Pipeline orchestrates frame processing through detectors and scoring.
type Pipeline struct {
	detectors   []detect.Detector
	scorer      *scoring.SuspicionScorer
	dedup       *Deduplicator
	rateLimiter *RateLimiter
	validator   *FrameValidator
	extractor   *FeatureExtractor
	cfg         *config.Config
	logger      *slog.Logger
	shadowIDs   map[string]bool
	skipReset   bool
}

// SetSkipReset controls whether ProcessMatch skips resetting detector and scorer state.
// Used by the live pipeline to preserve per-match state across frame batches.
func (p *Pipeline) SetSkipReset(skip bool) {
	p.skipReset = skip
}

// NewPipeline creates a new detection pipeline.
func NewPipeline(
	cfg *config.Config,
	detectors []detect.Detector,
	scorer *scoring.SuspicionScorer,
	logger *slog.Logger,
) *Pipeline {
	shadowIDs := make(map[string]bool)
	for _, id := range cfg.Shadow.ShadowDetectors {
		shadowIDs[id] = true
	}
	for id, dc := range cfg.Detectors {
		if dc.Mode == "shadow" {
			shadowIDs[id] = true
		}
	}
	return &Pipeline{
		detectors:   detectors,
		scorer:      scorer,
		dedup:       NewDeduplicator(20),
		rateLimiter: NewRateLimiter(cfg.Pipeline.MaxEventsPerPlayerPerDetector),
		validator:   NewFrameValidator(cfg),
		extractor:   NewFeatureExtractor(cfg.Pipeline.HistoryWindow),
		cfg:         cfg,
		logger:      logger,
		shadowIDs:   shadowIDs,
	}
}

// detectorCategory returns the category for a given detector ID.
func (p *Pipeline) detectorCategory(detectorID string) string {
	for _, d := range p.detectors {
		if d.ID() == detectorID {
			return d.Category()
		}
	}
	return detectorID
}

// ProcessMatch runs the full detection pipeline over a match.
func (p *Pipeline) ProcessMatch(
	ctx context.Context,
	matchCtx *model.MatchContext,
	frames []model.PlayerTelemetryFrame,
) (*MatchResult, error) {
	start := time.Now()
	result := &MatchResult{
		MatchID:      matchCtx.MatchID,
		PlayerScores: make(map[string]model.SuspicionScore),
	}

	// Reset all detector state (skipped for incremental live processing)
	if !p.skipReset {
		for _, d := range p.detectors {
			d.Reset()
		}
		p.scorer.Reset()
		p.dedup.Reset()
		p.rateLimiter.Reset()
	}

	// Build player states
	players := make(map[string]*model.PlayerState)
	for _, pid := range matchCtx.PlayerIDs {
		players[pid] = &model.PlayerState{
			PlayerID: pid,
			Team:     matchCtx.TeamAssignments[pid],
		}
	}

	// Group frames by index
	frameGroups := make(map[int][]model.PlayerTelemetryFrame)
	maxFrame := 0
	for _, f := range frames {
		frameGroups[f.FrameIndex] = append(frameGroups[f.FrameIndex], f)
		if f.FrameIndex > maxFrame {
			maxFrame = f.FrameIndex
		}
	}

	// Process frames in order
	for fi := 0; fi <= maxFrame; fi++ {
		pFrames, ok := frameGroups[fi]
		if !ok {
			continue
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		// Validate and update player states
		validFrame := false
		for _, pf := range pFrames {
			if err := p.validator.Validate(&pf, matchCtx); err != nil {
				result.InvalidFrames++
				continue
			}
			validFrame = true
			ps, ok := players[pf.PlayerID]
			if !ok {
				ps = &model.PlayerState{PlayerID: pf.PlayerID}
				players[pf.PlayerID] = ps
			}
			p.extractor.UpdatePlayerState(ps, &pf, matchCtx)
		}
		if !validFrame {
			continue
		}
		result.FramesProcessed++

		// Skip detectors during non-active game phases (round_start, score, pre_match, post_match).
		// CONFIRMED from real replay: players teleport during round transitions, causing
		// massive false positives from MOV_002 and other spatial detectors.
		activePhase := true
		for _, pf := range pFrames {
			if pf.GamePhase != "" && !matchCtx.IsActivePhase(pf.GamePhase) {
				activePhase = false
				break
			}
		}

		// Run detectors (only during active gameplay)
		var frameEvents []model.DetectionEvent
		if !activePhase {
			// Still update feature extractor (above) to maintain state continuity,
			// but don't run detectors during non-active phases.
			continue
		}
		for _, det := range p.detectors {
			if fi < det.WarmupFrames() {
				continue
			}
			events := det.Evaluate(matchCtx, players, fi)
			for i := range events {
				if p.shadowIDs[events[i].DetectorID] {
					events[i].IsShadow = true
				}
				frameEvents = append(frameEvents, events[i])
			}
		}

		// Feed detection events to PAT_004 (composite multi-cheat)
		for _, ev := range frameEvents {
			// Derive category from the detector that produced this event
			category := p.detectorCategory(ev.DetectorID)
			for _, det := range p.detectors {
				if pat, ok := det.(*pattern.Pat004); ok {
					pat.RecordDetection(ev.PlayerID, category)
				}
			}
		}

		// Deduplicate
		frameEvents = p.dedup.Deduplicate(frameEvents)

		// Rate limit
		frameEvents = p.rateLimiter.Filter(frameEvents)

		// Score
		for _, ev := range frameEvents {
			p.scorer.IngestEvent(ev)
			result.DetectionEvents = append(result.DetectionEvents, ev)

			if !ev.IsShadow {
				p.logger.Info("detection",
					"detector", ev.DetectorID,
					"player", ev.PlayerID,
					"severity", fmt.Sprintf("%.2f", ev.Severity),
					"confidence", fmt.Sprintf("%.2f", ev.Confidence),
					"observed", ev.ObservedValue,
				)
			}
		}
	}

	// Apply correlation bonus for players with detections across multiple categories
	p.scorer.ApplyCorrelationBonus()

	// Collect final scores
	result.PlayerScores = p.scorer.GetAllScores()
	result.Duration = time.Since(start)
	return result, nil
}
