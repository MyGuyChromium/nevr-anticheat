// Package pipeline orchestrates frame-by-frame cheat detection processing.
//
// # Architecture
//
// This is the core of the NEVR async cheat detection engine. The system
// follows a profiler-database architecture:
//
//   - Game servers emit telemetry to a profiler. They run NO detection logic.
//   - The profiler database stores ALL telemetry as the canonical source of truth.
//   - This pipeline reads telemetry from the database (or from replay files during
//     initial ingestion) and runs 29 detectors to produce detection events.
//   - Detection results are DERIVED DATA, recomputable from stored telemetry at
//     any time by reprocessing with updated detector logic or thresholds.
//   - Replay files are an ingestion format, not the primary analysis substrate.
//     After ingestion, all analysis operates against the database.
//
// The pipeline may run:
//   - During initial ingestion (inline, for immediate feedback)
//   - As async reprocessing of stored telemetry (canonical analysis path)
//   - In batch over many matches, with cross-match aggregation afterward
//
// The pipeline does NOT make enforcement decisions. It produces scored
// detection events that feed into moderator review cases.
//
// # Live (incremental) mode
//
// With SetSkipReset(true) the pipeline treats successive ProcessMatch calls
// as consecutive slices of ONE match: detector, scorer, dedup and rate-limit
// state persist, and so does the per-player PlayerState map, so kinematics,
// throw detection, warmup and history buffers continue across batches even
// when the producer sends one frame per batch. Call Finalize at match end to
// close the open incidents held by the deduplicator.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/pattern"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
)

// DefaultMergeWindow is the dedup merge window in frames (~1.3 s at 15 Hz).
const DefaultMergeWindow = 20

// pat004MinConfidence is the confidence floor for events fed to PAT_004.
// Low-confidence events (replay artifacts, borderline triggers) should not
// cascade into composite multi-cheat flags.
const pat004MinConfidence = 0.7

// MatchResult holds the complete output of a match analysis.
type MatchResult struct {
	MatchID         string                          `json:"match_id"`
	FramesProcessed int                             `json:"frames_processed"`
	InvalidFrames   int                             `json:"invalid_frames"`
	PlayerScores    map[string]model.SuspicionScore `json:"player_scores"`
	DetectionEvents []model.DetectionEvent          `json:"detection_events"`
	ReviewCases     []model.ReviewCase              `json:"review_cases"`
	Duration        time.Duration                   `json:"duration"`

	// InvalidFrameReasons counts rejected frames by validation reason code.
	InvalidFrameReasons map[string]int `json:"invalid_frame_reasons,omitempty"`
	// InvalidFramesByPlayer counts rejected frames per player.
	InvalidFramesByPlayer map[string]int `json:"invalid_frames_by_player,omitempty"`
	// SanitizedFrames counts in-place repairs (NaN hands, bad disc, ...) by reason.
	SanitizedFrames map[string]int `json:"sanitized_frames,omitempty"`
	// EventsMerged is the number of raw detector emissions folded into an
	// existing incident by the deduplicator during this call.
	EventsMerged int `json:"events_merged"`
	// EventsRateLimited is the number of incidents dropped by the per
	// player/detector cap during this call; EventsRateLimitedByKey breaks it
	// down by "player:detector".
	EventsRateLimited      int            `json:"events_rate_limited"`
	EventsRateLimitedByKey map[string]int `json:"events_rate_limited_by_key,omitempty"`
	// EventsInvalid counts detector emissions dropped by DetectionEvent.Validate.
	EventsInvalid int `json:"events_invalid"`
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

	// players persists across ProcessMatch calls while skipReset is set so
	// live batches accumulate kinematic and throw state.
	players map[string]*model.PlayerState

	// invalidLogged throttles per (player, reason) warnings across batches.
	invalidLogged map[string]int
}

// SetSkipReset controls whether ProcessMatch skips resetting detector, scorer,
// dedup, rate-limit and per-player state. Used by the live pipeline to
// preserve per-match state across frame batches.
func (p *Pipeline) SetSkipReset(skip bool) {
	p.skipReset = skip
}

// Players returns the persistent per-player state map. The map is owned by
// the pipeline; callers must not mutate it while ProcessMatch may run.
func (p *Pipeline) Players() map[string]*model.PlayerState {
	return p.players
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
	if logger == nil {
		logger = slog.Default()
	}
	return &Pipeline{
		detectors:     detectors,
		scorer:        scorer,
		dedup:         NewDeduplicator(DefaultMergeWindow),
		rateLimiter:   NewRateLimiter(cfg.Pipeline.MaxEventsPerPlayerPerDetector),
		validator:     NewFrameValidator(cfg),
		extractor:     NewFeatureExtractor(cfg.Pipeline.HistoryWindow),
		cfg:           cfg,
		logger:        logger,
		shadowIDs:     shadowIDs,
		invalidLogged: make(map[string]int),
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

// resetState clears every piece of per-match state.
func (p *Pipeline) resetState(matchCtx *model.MatchContext) {
	for _, d := range p.detectors {
		d.Reset()
	}
	p.scorer.Reset()
	p.dedup.Reset()
	p.rateLimiter.Reset()
	p.invalidLogged = make(map[string]int)
	p.players = make(map[string]*model.PlayerState, len(matchCtx.PlayerIDs))
	for _, pid := range matchCtx.PlayerIDs {
		p.players[pid] = &model.PlayerState{
			PlayerID: pid,
			Team:     matchCtx.TeamAssignments[pid],
		}
	}
}

// ProcessMatch runs the full detection pipeline over a match (or, in live
// mode, over the next slice of one).
func (p *Pipeline) ProcessMatch(
	ctx context.Context,
	matchCtx *model.MatchContext,
	frames []model.PlayerTelemetryFrame,
) (*MatchResult, error) {
	start := time.Now()
	result := newMatchResult(matchCtx.MatchID)

	// Reset all state (skipped for incremental live processing)
	if !p.skipReset || p.players == nil {
		p.resetState(matchCtx)
	}

	// Group frames by index and process them in ascending index order. Only
	// indices present in this call are visited (a live match at frame 54k
	// must not scan 54k empty slots per poll).
	frameGroups := make(map[int][]model.PlayerTelemetryFrame)
	for _, f := range frames {
		frameGroups[f.FrameIndex] = append(frameGroups[f.FrameIndex], f)
	}
	indices := make([]int, 0, len(frameGroups))
	for fi := range frameGroups {
		indices = append(indices, fi)
	}
	sort.Ints(indices)

	// eligible caches, per frame, the subset of framePlayers that has passed
	// a given warmup length.
	eligible := make(map[int]map[string]*model.PlayerState)

	for _, fi := range indices {
		pFrames := frameGroups[fi]

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		// Validate and update player states. framePlayers holds only the
		// players that produced a valid frame at this index; stale PlayerState
		// from earlier frames is never re-scored.
		framePlayers := make(map[string]*model.PlayerState, len(pFrames))
		sort.SliceStable(pFrames, func(i, j int) bool { return pFrames[i].PlayerID < pFrames[j].PlayerID })
		for i := range pFrames {
			pf := &pFrames[i]
			sanitized, err := p.validator.Validate(pf, matchCtx)
			if err != nil {
				p.recordInvalid(result, matchCtx.MatchID, pf.PlayerID, err)
				continue
			}
			for _, s := range sanitized {
				result.SanitizedFrames[s]++
			}
			ps, ok := p.players[pf.PlayerID]
			if !ok {
				ps = &model.PlayerState{PlayerID: pf.PlayerID, Team: matchCtx.TeamAssignments[pf.PlayerID]}
				p.players[pf.PlayerID] = ps
			}
			if pf.Team != "" {
				ps.Team = pf.Team
			}
			p.extractor.UpdatePlayerState(ps, pf, matchCtx)
			framePlayers[pf.PlayerID] = ps
		}
		if len(framePlayers) == 0 {
			continue
		}
		result.FramesProcessed++

		// Skip detectors during non-active game phases (round_start, score, pre_match, post_match).
		// CONFIRMED from real replay: players teleport during round transitions, causing
		// massive false positives from MOV_002 and other spatial detectors.
		activePhase := true
		for i := range pFrames {
			if pFrames[i].GamePhase != "" && !matchCtx.IsActivePhase(pFrames[i].GamePhase) {
				activePhase = false
				break
			}
		}
		if !activePhase {
			// Feature extractor state was updated above to maintain continuity,
			// but detectors do not run during non-active phases.
			continue
		}

		// Run detectors. Warmup is per player: a detector only sees players
		// that have accumulated more than WarmupFrames() valid frames, so a
		// late joiner or reconnecting player is not evaluated on an empty
		// history.
		for k := range eligible {
			delete(eligible, k)
		}
		var frameEvents []model.DetectionEvent
		for _, det := range p.detectors {
			warm := det.WarmupFrames()
			subset, ok := eligible[warm]
			if !ok {
				subset = make(map[string]*model.PlayerState, len(framePlayers))
				for pid, ps := range framePlayers {
					if ps.FrameCount > warm {
						subset[pid] = ps
					}
				}
				eligible[warm] = subset
			}
			if len(subset) == 0 {
				continue
			}
			events := det.Evaluate(matchCtx, subset, fi)
			sortEmissions(events)
			for i := range events {
				ev := events[i]
				if err := ev.Validate(); err != nil {
					result.EventsInvalid++
					p.logger.Warn("dropping invalid detection event",
						"detector", ev.DetectorID, "player", ev.PlayerID, "frame", fi, "error", err)
					continue
				}
				if p.shadowIDs[ev.DetectorID] {
					ev.IsShadow = true
				}
				frameEvents = append(frameEvents, ev)
			}
		}

		// Deduplicate: fold this frame's emissions into open incidents and
		// emit the incidents that have closed.
		mergedBefore := p.dedup.MergedCount()
		closed := p.dedup.Deduplicate(frameEvents, fi)
		result.EventsMerged += int(p.dedup.MergedCount() - mergedBefore)
		p.emit(closed, result)
	}

	// Offline mode: the match is complete, close every open incident.
	if !p.skipReset {
		p.emit(p.dedup.Flush(), result)
	}

	// Apply correlation bonus for players with detections across multiple
	// categories. The scorer keeps the bonus idempotent, so calling it once
	// per ProcessMatch is safe in live mode.
	p.scorer.ApplyCorrelationBonus()

	// Collect final scores
	result.PlayerScores = p.scorer.GetAllScores()
	result.Duration = time.Since(start)
	return result, nil
}

// Finalize closes every open dedup incident (live match end), scores the
// resulting events and returns them so the caller can persist them.
func (p *Pipeline) Finalize(matchCtx *model.MatchContext) *MatchResult {
	result := newMatchResult(matchCtx.MatchID)
	p.emit(p.dedup.Flush(), result)
	p.scorer.ApplyCorrelationBonus()
	result.PlayerScores = p.scorer.GetAllScores()
	return result
}

// emit runs closed incidents through the rate limiter, scoring, the PAT_004
// feed and logging, appending the kept events to result.
func (p *Pipeline) emit(events []model.DetectionEvent, result *MatchResult) {
	if len(events) == 0 {
		return
	}
	kept, dropped := p.rateLimiter.Filter(events)
	for key, n := range dropped {
		result.EventsRateLimited += n
		if result.EventsRateLimitedByKey[key] == 0 {
			p.logger.Warn("detection events rate limited; later incidents for this player/detector are not stored",
				"match", result.MatchID, "key", key, "cap", p.cfg.Pipeline.MaxEventsPerPlayerPerDetector)
		}
		result.EventsRateLimitedByKey[key] += n
	}
	for _, ev := range kept {
		p.scorer.IngestEvent(ev)
		result.DetectionEvents = append(result.DetectionEvents, ev)

		if ev.IsShadow {
			continue
		}

		// Feed PAT_004 (composite multi-cheat) only with events that are
		// non-shadow, deduplicated and not rate limited, so a composite flag
		// can never be built from detections that are never scored.
		if ev.Confidence >= pat004MinConfidence && ev.DetectorID != "PAT_004" {
			category := p.detectorCategory(ev.DetectorID)
			for _, det := range p.detectors {
				if pat, ok := det.(*pattern.Pat004); ok {
					pat.RecordDetection(ev.PlayerID, category)
				}
			}
		}

		p.logger.Info("detection",
			"detector", ev.DetectorID,
			"player", ev.PlayerID,
			"severity", fmt.Sprintf("%.2f", ev.Severity),
			"confidence", fmt.Sprintf("%.2f", ev.Confidence),
			"frames", fmt.Sprintf("%d-%d", ev.FrameRangeStart, ev.FrameRangeEnd),
			"merged", ev.MergedCount,
			"observed", ev.ObservedValue,
		)
	}
}

// recordInvalid accounts a rejected frame and logs it with throttling.
func (p *Pipeline) recordInvalid(result *MatchResult, matchID, playerID string, err error) {
	result.InvalidFrames++
	reason := "invalid"
	var verr *ValidationError
	if errors.As(err, &verr) {
		reason = verr.Reason
	}
	result.InvalidFrameReasons[reason]++
	if playerID != "" {
		result.InvalidFramesByPlayer[playerID]++
	}
	key := playerID + ":" + reason
	n := p.invalidLogged[key] + 1
	p.invalidLogged[key] = n
	// First occurrence, then every 1000th, so a persistently broken player
	// or a physics mismatch stays visible without flooding the log.
	if n == 1 || n%1000 == 0 {
		p.logger.Warn("telemetry frame rejected",
			"match", matchID, "player", playerID, "reason", reason, "count", n, "error", err)
	}
}

func newMatchResult(matchID string) *MatchResult {
	return &MatchResult{
		MatchID:                matchID,
		PlayerScores:           make(map[string]model.SuspicionScore),
		InvalidFrameReasons:    make(map[string]int),
		InvalidFramesByPlayer:  make(map[string]int),
		SanitizedFrames:        make(map[string]int),
		EventsRateLimitedByKey: make(map[string]int),
	}
}

// sortEmissions orders one detector's per-frame emissions deterministically
// regardless of the detector's internal map iteration order.
func sortEmissions(evs []model.DetectionEvent) {
	sort.SliceStable(evs, func(i, j int) bool {
		a, b := evs[i], evs[j]
		if a.PlayerID != b.PlayerID {
			return a.PlayerID < b.PlayerID
		}
		if a.FrameRangeStart != b.FrameRangeStart {
			return a.FrameRangeStart < b.FrameRangeStart
		}
		return a.CausalKey.AnomalyType < b.CausalKey.AnomalyType
	})
}
