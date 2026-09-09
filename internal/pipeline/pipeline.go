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
//     initial ingestion) and runs the detector catalog to produce detection events.
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
// when the producer sends one frame per batch. The first slice only creates
// the roster; it never resets state that was seeded before it (a scorer
// carrying events, for example). Call Finalize at match end to flush open
// throw tracks and close the open incidents held by the deduplicator.
//
// # What detectors receive
//
// Detectors get the FULL per-match player map filtered only by their own
// warmup (every player whose FrameCount exceeds WarmupFrames), including
// players whose latest frame is older than the current index. That is
// deliberate: STATE_007 needs the victims of a punch and MOV_003 needs
// nearby players for collision exclusion, and those may not have a frame at
// exactly this index. A stale PlayerState must never be re-scored, so every
// detector iterates detect.ActivePlayers (or checks LastFrameIdx itself, as
// the throw detectors do through throwAt/currentDisc).
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

// TrackFlusher is implemented by detectors that keep open per-throw tracks
// (THROW_006 trajectory, THROW_008 speed/distance) which must be judged at
// match end instead of being dropped with the detector state. The pipeline
// calls it once at the end of an offline match and once from Finalize on the
// live path; the returned events go through the same dedup, rate-limit,
// validation and scoring path as frame events.
type TrackFlusher interface {
	FlushTracks(matchCtx *model.MatchContext, frameIdx int) []model.DetectionEvent
}

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
	// TelemetryQuality is assessed before offline detectors run. Low-quality
	// input can only reduce confidence and force shadow mode.
	TelemetryQuality TelemetryQualityReport           `json:"telemetry_quality"`
	PlayerCoverage   map[string]*model.PlayerCoverage `json:"player_coverage,omitempty"`
}

// Pipeline orchestrates frame processing through detectors and scoring.
type Pipeline struct {
	health           map[string]*healthEntry
	capabilities     map[string]*model.DetectorCapability
	decisionCoverage *coverageTracker // current-call diagnostics, detached before returning
	detectors        []detect.Detector
	scorer           *scoring.SuspicionScorer
	dedup            *Deduplicator
	rateLimiter      *RateLimiter
	validator        *FrameValidator
	extractor        *FeatureExtractor
	cfg              *config.Config
	logger           *slog.Logger
	shadowIDs        map[string]bool
	skipReset        bool

	// players persists across ProcessMatch calls while skipReset is set so
	// live batches accumulate kinematic and throw state.
	players map[string]*model.PlayerState

	// lastFrameIdx is the highest frame index that produced at least one
	// valid frame; FlushTracks is evaluated "as of" it. -1 before any frame.
	lastFrameIdx int

	// invalidLogged throttles per (player, reason) warnings across batches.
	invalidLogged map[string]int
	quality       TelemetryQualityReport
}

// SetSkipReset controls whether ProcessMatch skips resetting detector, scorer,
// dedup, rate-limit and per-player state. Used by the live pipeline to
// preserve per-match state across frame batches. Set it BEFORE the first
// batch: with it set, ProcessMatch never resets anything, it only creates the
// roster on first use.
func (p *Pipeline) SetSkipReset(skip bool) {
	p.skipReset = skip
}

// Players returns the persistent per-player state map. The map is owned by
// the pipeline; callers must not mutate it while ProcessMatch may run.
func (p *Pipeline) Players() map[string]*model.PlayerState {
	return p.players
}

// Extractor exposes the feature extractor (goal-side configuration, tests).
func (p *Pipeline) Extractor() *FeatureExtractor {
	return p.extractor
}

// NewPipeline creates a new detection pipeline.
func NewPipeline(
	cfg *config.Config,
	detectors []detect.Detector,
	scorer *scoring.SuspicionScorer,
	logger *slog.Logger,
) *Pipeline {
	// Also guard callers supplying detector instances directly. Filter into a
	// new slice so caller-owned instances/order are not mutated. The shared
	// feature extractor remains intact for legal-motion context on other checks.
	activeDetectors := make([]detect.Detector, 0, len(detectors))
	for _, d := range detectors {
		if !model.IsPlayspaceDetector(d.ID()) {
			activeDetectors = append(activeDetectors, d)
		}
	}
	detectors = activeDetectors
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
	extractor := NewFeatureExtractor(cfg.Pipeline.HistoryWindow)
	extractor.SetHighPingThreshold(cfg.Pipeline.HighPingThresholdMs)
	// pipeline.max_frame_dt is the extractor's gap threshold (contract B):
	// a known dt above it derives no kinematics. It is NOT a rejection
	// bound; the validator's hard bound is MaxProducerDt.
	extractor.SetMaxFrameDt(cfg.Pipeline.MaxFrameDt)
	return &Pipeline{
		capabilities:  detectorCapabilities(detectors),
		detectors:     detectors,
		scorer:        scorer,
		dedup:         NewDeduplicator(DefaultMergeWindow),
		rateLimiter:   NewRateLimiter(cfg.Pipeline.MaxEventsPerPlayerPerDetector),
		validator:     NewFrameValidator(cfg),
		extractor:     extractor,
		cfg:           cfg,
		logger:        logger,
		shadowIDs:     shadowIDs,
		lastFrameIdx:  -1,
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

// resetState clears every piece of per-match state (offline mode: one
// ProcessMatch call is one whole match).
func (p *Pipeline) resetState(matchCtx *model.MatchContext) {
	for _, d := range p.detectors {
		d.Reset()
	}
	p.scorer.Reset()
	p.dedup.Reset()
	p.rateLimiter.Reset()
	p.invalidLogged = make(map[string]int)
	p.initMatch(matchCtx)
}

// initMatch seeds the roster from the match context and anchors the scorer
// on the match start so event times are match-relative, not ingest-relative.
func (p *Pipeline) initMatch(matchCtx *model.MatchContext) {
	p.health = make(map[string]*healthEntry)
	p.lastFrameIdx = -1
	p.players = make(map[string]*model.PlayerState, len(matchCtx.PlayerIDs))
	for _, pid := range matchCtx.PlayerIDs {
		p.players[pid] = &model.PlayerState{
			PlayerID: pid,
			Team:     matchCtx.TeamAssignments[pid],
		}
	}
	p.scorer.SetMatchStart(matchCtx.StartTime)
}

// warmView is the player map handed to detectors sharing one warmup length:
// every player past the warmup (stale ones included, for cross-player
// lookups) plus the number of those that have a fresh frame at this index.
type warmView struct {
	players map[string]*model.PlayerState
	active  int
}

// ProcessMatch runs the full detection pipeline over a match (or, in live
// mode, over the next slice of one).
func (p *Pipeline) ProcessMatch(
	ctx context.Context,
	matchCtx *model.MatchContext,
	frames []model.PlayerTelemetryFrame,
) (*MatchResult, error) {
	start := time.Now()
	// Rules are the active local project configuration, not a producer's claim
	// of engine verification. This does not change the independent speed cap.
	matchCtx.ProjectRules = p.cfg.ProjectRules
	result := newMatchResult(matchCtx.MatchID)
	if !p.skipReset {
		p.quality = AssessTelemetryQuality(frames, p.cfg)
	} else {
		// A live slice is often only one frame. The persistent live validation
		// path handles it incrementally; never gate it as a short offline file.
		p.quality = TelemetryQualityReport{Grade: "source_scoped", ConfidenceMultiplier: 1}
	}
	result.TelemetryQuality = p.quality
	coverage := newCoverageTracker(p.detectors, p.cfg, matchCtx.PlayerIDs)

	// Offline: every call is a whole match, reset everything. Live: the
	// first slice only creates the roster; state seeded before it (and by
	// earlier slices) is kept.
	if !p.skipReset {
		p.resetState(matchCtx)
	} else if p.players == nil {
		p.initMatch(matchCtx)
	}
	detachDecisionCoverage := p.attachDecisionCoverage(coverage)
	defer detachDecisionCoverage()

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

	// eligible caches, per frame, the detector view for a given warmup length.
	eligible := make(map[int]warmView)

	for _, fi := range indices {
		pFrames := frameGroups[fi]
		sharedJumps := p.sharedOrientationJumps(pFrames)

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		// Validate and update player states. framePlayers holds only the
		// players that produced a valid frame at this index.
		framePlayers := make(map[string]*model.PlayerState, len(pFrames))
		sourceReset := false
		sort.SliceStable(pFrames, func(i, j int) bool { return pFrames[i].PlayerID < pFrames[j].PlayerID })
		for i := range pFrames {
			pf := &pFrames[i]
			sanitized, err := p.validator.Validate(pf, matchCtx)
			if err != nil {
				if p.players[pf.PlayerID] != nil {
					p.observeHealth(pf, p.players[pf.PlayerID], true, sanitized, false)
				}
				p.recordInvalid(result, matchCtx.MatchID, pf.PlayerID, err)
				coverage.player(pf.PlayerID).RejectedFrames++
				coverage.allEnabled(pf.PlayerID, fi, "frame_rejected")
				continue
			}
			// Keep malformed time/identity classified by the validator. Only
			// valid observations reach ordering checks, before any history write.
			if previous := p.players[pf.PlayerID]; previous != nil && previous.FrameCount > 0 && (pf.FrameIndex <= previous.LastFrameIdx || pf.Timestamp <= previous.LastTimestamp) {
				p.observeHealth(pf, previous, true, sanitized, false)
				p.recordInvalid(result, matchCtx.MatchID, pf.PlayerID, &ValidationError{Reason: "stale_player_sample", PlayerID: pf.PlayerID, Detail: "frame index and timestamp must both advance"})
				coverage.player(pf.PlayerID).RejectedFrames++
				coverage.allEnabled(pf.PlayerID, fi, "stale_player_sample")
				continue
			}
			coverage.player(pf.PlayerID).ValidFrames++
			p.observeHealth(pf, p.players[pf.PlayerID], false, sanitized, sharedJumps[pf.PlayerID])
			for _, s := range sanitized {
				result.SanitizedFrames[s]++
				coverage.traceSanitizedDisc(pf.PlayerID, fi, s)
			}
			ps, ok := p.players[pf.PlayerID]
			if !ok {
				ps = &model.PlayerState{PlayerID: pf.PlayerID, Team: matchCtx.TeamAssignments[pf.PlayerID]}
				p.players[pf.PlayerID] = ps
			}
			if pf.Team != "" {
				ps.Team = pf.Team
			}
			if !sourceReset && ps.FrameCount > 0 && !sameObservationSource(ps.Observation, pf.Observation) {
				// Do not combine behavioral windows from different recording
				// sources. Prior independent incidents retain their existing score.
				p.extractor.DrainPendingReleases("release_source_changed")
				p.emit(p.dedup.Flush(), result)
				for _, d := range p.detectors {
					if resetter, ok := d.(interface{ ResetSource() }); ok {
						resetter.ResetSource()
					} else {
						d.Reset()
					}
				}
				sourceReset = true
			}
			p.extractor.UpdatePlayerState(ps, pf, matchCtx)
			p.reviewRelease(ps, fi)
			framePlayers[pf.PlayerID] = ps
		}
		if len(framePlayers) == 0 {
			continue
		}
		result.FramesProcessed++
		if fi > p.lastFrameIdx {
			p.lastFrameIdx = fi
		}

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
			for pid := range framePlayers {
				coverage.allEnabled(pid, fi, "inactive_phase")
			}
			continue
		}

		// Run detectors. Warmup is per player: a detector only sees players
		// that have accumulated more than WarmupFrames() valid frames, so a
		// late joiner or reconnecting player is not evaluated on an empty
		// history. The view keeps warmed-up players whose latest frame is
		// older than fi (cross-player lookups); detectors skip them via
		// detect.ActivePlayers. A detector with no fresh warm player is not
		// called at all.
		for k := range eligible {
			delete(eligible, k)
		}
		var frameEvents []model.DetectionEvent
		for _, det := range p.detectors {
			warm := det.WarmupFrames()
			view, ok := eligible[warm]
			if !ok {
				view = warmView{players: make(map[string]*model.PlayerState, len(p.players))}
				for pid, ps := range p.players {
					if ps.FrameCount > warm {
						view.players[pid] = ps
						if _, fresh := framePlayers[pid]; fresh {
							view.active++
						}
					}
				}
				eligible[warm] = view
			}
			for pid, ps := range framePlayers {
				if _, ok := view.players[pid]; ok {
					coverage.candidate(det.ID(), ps, fi)
					coverage.trace(det.ID(), pid, fi, "detector_evaluated")
				} else {
					coverage.trace(det.ID(), pid, fi, "warming_up")
				}
			}
			if view.active == 0 {
				continue
			}
			events := det.Evaluate(matchCtx, view.players, fi)
			emitted := make(map[string]bool, len(events))
			for _, event := range events {
				emitted[event.PlayerID] = true
			}
			for pid := range framePlayers {
				if _, included := view.players[pid]; included && !emitted[pid] {
					coverage.trace(det.ID(), pid, fi, "no_raw_emission")
				}
			}
			frameEvents = append(frameEvents, p.acceptEmissions(events, fi, result)...)
		}

		// Deduplicate: fold this frame's emissions into open incidents and
		// emit the incidents that have closed.
		p.dedupAndEmit(frameEvents, fi, result)
	}

	// Offline mode: the match is complete. Judge the throws still in flight,
	// then close every open incident.
	if !p.skipReset {
		p.extractor.DrainPendingReleases("release_confirmation_unavailable")
		p.flushTracks(matchCtx, result)
		p.emit(p.dedup.Flush(), result)
	}

	// Apply correlation bonus for players with detections across multiple
	// categories. The scorer keeps the bonus idempotent, so calling it once
	// per ProcessMatch is safe in live mode.
	p.scorer.ApplyCorrelationBonus()

	// Collect final scores
	result.PlayerScores = p.scorer.GetAllScores()
	result.PlayerCoverage = coverage.finish(p.quality)
	p.finishHealth(coverage, result)
	result.Duration = time.Since(start)
	return result, nil
}

// Finalize ends a live match: open throw tracks are judged, every open dedup
// incident is closed, the resulting events are scored and returned so the
// caller can persist them.
func (p *Pipeline) Finalize(matchCtx *model.MatchContext) *MatchResult {
	result := newMatchResult(matchCtx.MatchID)
	coverage := newCoverageTracker(p.detectors, p.cfg, matchCtx.PlayerIDs)
	for playerID := range p.players {
		coverage.player(playerID) // include players discovered after match start
	}
	detach := p.attachDecisionCoverage(coverage)
	defer detach()
	p.extractor.DrainPendingReleases("release_confirmation_unavailable")
	p.flushTracks(matchCtx, result)
	p.emit(p.dedup.Flush(), result)
	p.scorer.ApplyCorrelationBonus()
	result.PlayerScores = p.scorer.GetAllScores()
	result.PlayerCoverage = coverage.finish(p.quality)
	p.finishHealth(coverage, result)
	return result
}

// acceptEmissions validates one detector's emissions, stamps shadow status
// and returns the kept events in deterministic order.
func (p *Pipeline) acceptEmissions(events []model.DetectionEvent, fi int, result *MatchResult) []model.DetectionEvent {
	sortEmissions(events)
	kept := events[:0:0]
	for i := range events {
		ev := events[i]
		p.traceEvent(ev, "raw_emission_returned")
		if err := ev.Validate(); err != nil {
			p.traceEvent(ev, "invalid_emission_dropped")
			result.EventsInvalid++
			p.logger.Warn("dropping invalid detection event",
				"detector", ev.DetectorID, "player", ev.PlayerID, "frame", fi, "error", err)
			continue
		}
		if p.shadowIDs[ev.DetectorID] {
			ev.IsShadow = true
		}
		if p.quality.ConfidenceMultiplier > 0 && p.quality.ConfidenceMultiplier < 1 {
			p.traceEvent(ev, "quality_confidence_reduced")
			ev.Confidence = model.Clamp(ev.Confidence*p.quality.ConfidenceMultiplier, 0, 1)
		}
		if p.quality.Gated {
			p.traceEvent(ev, "quality_forced_shadow")
			ev.IsShadow = true
			ev.AutoEnforce = false
			ev.EnforcementWeight = 0
		}
		p.applyLegalContext(&ev)
		p.applyEvidenceSafety(&ev)
		kept = append(kept, ev)
	}
	return kept
}

// applyLegalContext only lowers confidence when the shared motion context
// offers a plausible legal/tracking explanation. It never suppresses the
// dedicated playspace-abuse detectors, and never increases a signal.
func (p *Pipeline) applyLegalContext(ev *model.DetectionEvent) {
	state := p.players[ev.PlayerID]
	if state == nil {
		return
	}
	ctx := state.LegalContext
	multiplier := 1.0
	switch {
	case ctx.TrackingLimited:
		p.traceEvent(*ev, "context_tracking_limited")
		multiplier = 0.55
	case ctx.PossibleHeadContact && (ev.DetectorID == "THROW_003" || ev.DetectorID == "BIO_001"):
		p.traceEvent(*ev, "context_possible_head_contact")
		multiplier = 0.35
	case ctx.PossibleSlapOrPush && (ev.DetectorID == "BIO_001" || ev.DetectorID == "BIO_002" || ev.DetectorID == "MOV_001" || ev.DetectorID == "MOV_002" || ev.DetectorID == "MOV_003"):
		p.traceEvent(*ev, "context_possible_slap_or_push")
		multiplier = 0.6
	case (ctx.Leaning || ctx.PlayspaceStep) && (ev.DetectorID == "MOV_001" || ev.DetectorID == "MOV_002" || ev.DetectorID == "MOV_003"):
		p.traceEvent(*ev, "context_lean_or_step")
		multiplier = 0.65
	case ctx.BoostingKnown && ctx.Boosting && (ev.DetectorID == "MOV_001" || ev.DetectorID == "MOV_002" || ev.DetectorID == "MOV_003"):
		p.traceEvent(*ev, "context_boost")
		multiplier = 0.75
	}
	if multiplier < 1 {
		ev.Confidence = model.Clamp(ev.Confidence*multiplier*model.Clamp(ctx.Confidence, 0.25, 1), 0, 1)
	}
}

// dedupAndEmit folds events emitted at frame fi into the open incidents and
// emits the incidents that closed.
func (p *Pipeline) dedupAndEmit(events []model.DetectionEvent, fi int, result *MatchResult) {
	mergedBefore := p.dedup.MergedCount()
	closed := p.dedup.Deduplicate(events, fi)
	result.EventsMerged += int(p.dedup.MergedCount() - mergedBefore)
	p.emit(closed, result)
}

// flushTracks asks every TrackFlusher detector to judge its open tracks as
// of the last processed frame and routes the results like frame events.
func (p *Pipeline) flushTracks(matchCtx *model.MatchContext, result *MatchResult) {
	if p.lastFrameIdx < 0 {
		return
	}
	var events []model.DetectionEvent
	for _, det := range p.detectors {
		tf, ok := det.(TrackFlusher)
		if !ok {
			continue
		}
		events = append(events, p.acceptEmissions(tf.FlushTracks(matchCtx, p.lastFrameIdx), p.lastFrameIdx, result)...)
	}
	if len(events) == 0 {
		return
	}
	p.dedupAndEmit(events, p.lastFrameIdx, result)
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
		// Merging can expand an incident over a later source fault. Recheck
		// the final causal range, not only its strongest raw emission.
		p.applyEvidenceSafety(&ev)
		_, scored := p.scorer.IngestEventWithResult(ev)
		result.DetectionEvents = append(result.DetectionEvents, ev)
		if ev.IsShadow {
			p.traceEvent(ev, "incident_retained_shadow")
		} else {
			p.traceEvent(ev, "incident_retained_review")
		}
		if scored {
			p.traceEvent(ev, "incident_scored")
		} else {
			p.traceEvent(ev, "incident_not_scored")
		}

		if ev.IsShadow || !scored {
			continue
		}

		// Feed PAT_004 (composite multi-cheat) only with events that are
		// non-shadow, deduplicated, not rate limited and actually accepted by
		// the scorer (positive weight/severity/confidence, outside cooldown and
		// cap), so a composite flag can never be built from unscored evidence.
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
