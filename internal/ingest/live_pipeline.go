package ingest

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/metrics"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// LiveMatch holds the state of an active match being analyzed in real-time.
type LiveMatch struct {
	MatchCtx *model.MatchContext
	Pipeline *pipeline.Pipeline
	Scorer   *scoring.SuspicionScorer
	// Players is the pipeline's persistent per-player state (valid after the
	// first batch). It is what makes kinematics, throw detection and warmup
	// continue across batches.
	Players       map[string]*model.PlayerState
	FrameCount    int // frame ticks processed by the pipeline
	RowsStored    int // telemetry rows the store reported as inserted
	InvalidFrames int
	StartTime     time.Time
	LastActivity  time.Time

	lastFrameIndex   int     // highest frame index seen; batches at or below it are re-based
	firstTimestamp   float64 // first frame timestamp seen (relative game time)
	lastTimestamp    float64 // highest frame timestamp seen
	haveTimestamp    bool
	rebasedBatches   int
	lastPersist      time.Time
	eventsByDetector map[string]int
	totalEvents      int
	lastLevel        map[string]model.ScoringLevel
	lastScore        map[string]float64
	initialized      bool // true after the first batch (pipeline state is then persistent)
	ended            bool
	mu               sync.Mutex
}

// MatchManager manages active match pipelines.
type MatchManager struct {
	cfg        *config.Config
	store      *sqlite.Store
	detectorFn func() []detect.Detector // factory to create fresh detectors per match
	logger     *slog.Logger
	metrics    *metrics.Metrics

	maxMatches         int
	maxPlayersPerMatch int
	persistInterval    time.Duration

	matches   map[string]*LiveMatch
	matchesMu sync.RWMutex

	warnMu     sync.Mutex
	warnCounts map[string]int

	// Enforcement callback
	OnEnforcement func(action model.EnforcementAction)
	// Review case callback
	OnReviewCase func(rc model.ReviewCase)
}

const (
	// DefaultMaxMatches caps concurrently tracked live matches.
	DefaultMaxMatches = 64
	// DefaultPersistInterval is how often a live match's context is re-persisted.
	DefaultPersistInterval = 2 * time.Minute
	// AnalysisSourceInitial tags inline detection results.
	AnalysisSourceInitial = "initial"
)

// NewMatchManager creates a new match manager.
func NewMatchManager(
	cfg *config.Config,
	store *sqlite.Store,
	detectorFn func() []detect.Detector,
	logger *slog.Logger,
) *MatchManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &MatchManager{
		cfg:                cfg,
		store:              store,
		detectorFn:         detectorFn,
		logger:             logger,
		maxMatches:         DefaultMaxMatches,
		maxPlayersPerMatch: DefaultServerConfig().MaxPlayersPerMatch,
		persistInterval:    DefaultPersistInterval,
		matches:            make(map[string]*LiveMatch),
		warnCounts:         make(map[string]int),
	}
}

// SetMetrics attaches a metrics registry (optional).
func (mm *MatchManager) SetMetrics(m *metrics.Metrics) { mm.metrics = m }

// SetLimits configures the active-match cap and per-match player cap.
// Non-positive values keep the current setting.
func (mm *MatchManager) SetLimits(maxMatches, maxPlayersPerMatch int) {
	if maxMatches > 0 {
		mm.maxMatches = maxMatches
	}
	if maxPlayersPerMatch > 0 {
		mm.maxPlayersPerMatch = maxPlayersPerMatch
	}
}

// SetPersistInterval configures how often a live match context is re-persisted.
func (mm *MatchManager) SetPersistInterval(d time.Duration) {
	if d > 0 {
		mm.persistInterval = d
	}
}

// getOrCreate returns the live match for matchID, creating it if allowed.
// The returned match is locked; callers must unlock it. ok is false when the
// match cap prevents creation.
func (mm *MatchManager) getOrCreate(matchID string) (*LiveMatch, bool) {
	for {
		mm.matchesMu.Lock()
		match, ok := mm.matches[matchID]
		if !ok {
			if len(mm.matches) >= mm.maxMatches {
				mm.matchesMu.Unlock()
				mm.warnThrottled("maxmatches", "active match cap reached; telemetry for new match rejected",
					"match", matchID, "cap", mm.maxMatches)
				return nil, false
			}
			match = mm.createMatch(matchID)
			mm.matches[matchID] = match
			mm.updateActiveGauge()
		}
		mm.matchesMu.Unlock()

		match.mu.Lock()
		if match.ended {
			// EndMatch raced with us; retry so a fresh LiveMatch is created.
			match.mu.Unlock()
			continue
		}
		return match, true
	}
}

// HandleFrames processes a batch of frames for a match.
func (mm *MatchManager) HandleFrames(matchID string, frames []model.PlayerTelemetryFrame) FrameResult {
	if len(frames) == 0 {
		return FrameResult{}
	}
	match, ok := mm.getOrCreate(matchID)
	if !ok {
		return FrameResult{Rejected: len(frames)}
	}
	defer match.mu.Unlock()

	ctx := context.Background()
	res := FrameResult{}

	// Frame identity: indices are match-relative and monotonic. Re-base any
	// batch that would go backwards (producer restart, replayed poll).
	minIdx, maxIdx := frames[0].FrameIndex, frames[0].FrameIndex
	for _, f := range frames[1:] {
		if f.FrameIndex < minIdx {
			minIdx = f.FrameIndex
		}
		if f.FrameIndex > maxIdx {
			maxIdx = f.FrameIndex
		}
	}
	if minIdx <= match.lastFrameIndex {
		offset := match.lastFrameIndex + 1 - minIdx
		for i := range frames {
			frames[i].FrameIndex += offset
		}
		maxIdx += offset
		match.rebasedBatches++
		if mm.metrics != nil {
			mm.metrics.FramesRebased.Add(int64(len(frames)))
		}
		if match.rebasedBatches == 1 || match.rebasedBatches%1000 == 0 {
			mm.logger.Warn("re-based non-monotonic frame batch",
				"match", matchID, "frames", len(frames), "batch_min", minIdx,
				"last_index", match.lastFrameIndex, "offset", offset, "rebased_batches", match.rebasedBatches)
		}
	}
	match.lastFrameIndex = maxIdx

	// Player roster: cap, then register new players and team assignments.
	kept := make([]model.PlayerTelemetryFrame, 0, len(frames))
	for _, f := range frames {
		if !containsString(match.MatchCtx.PlayerIDs, f.PlayerID) {
			if len(match.MatchCtx.PlayerIDs) >= mm.maxPlayersPerMatch {
				res.Rejected++
				mm.warnThrottled("maxplayers:"+matchID, "player cap reached; frames for additional players rejected",
					"match", matchID, "player", f.PlayerID, "cap", mm.maxPlayersPerMatch)
				continue
			}
			match.MatchCtx.PlayerIDs = append(match.MatchCtx.PlayerIDs, f.PlayerID)
			sort.Strings(match.MatchCtx.PlayerIDs)
		}
		if team := normalizeTeam(f.Team); team != "" {
			match.MatchCtx.TeamAssignments[f.PlayerID] = team
		}
		if !match.haveTimestamp {
			match.firstTimestamp, match.lastTimestamp = f.Timestamp, f.Timestamp
			match.haveTimestamp = true
		} else {
			if f.Timestamp < match.firstTimestamp {
				match.firstTimestamp = f.Timestamp
			}
			if f.Timestamp > match.lastTimestamp {
				match.lastTimestamp = f.Timestamp
			}
		}
		kept = append(kept, f)
	}
	frames = kept
	if len(frames) == 0 {
		return res
	}

	// Persist raw telemetry first: it is the source of truth for reprocessing.
	stored, storeErr := mm.store.StoreTelemetryFrames(ctx, matchID, frames)
	if storeErr != nil {
		mm.logger.Warn("failed to store telemetry", "match", matchID, "error", storeErr)
		if mm.metrics != nil {
			mm.metrics.StoreErrors.Inc()
		}
	} else {
		match.RowsStored += stored
		if ignored := len(frames) - stored; ignored > 0 {
			res.Ignored += ignored
			if mm.metrics != nil {
				mm.metrics.FramesIgnored.Add(int64(ignored))
			}
			mm.warnThrottled("dup:"+matchID, "store ignored duplicate telemetry rows",
				"match", matchID, "ignored", ignored)
		}
	}

	// Process frames through the pipeline. The first batch initializes state;
	// afterwards SetSkipReset keeps detectors, scorer, dedup and per-player
	// state alive across batches.
	if match.initialized {
		match.Pipeline.SetSkipReset(true)
	}
	result, err := match.Pipeline.ProcessMatch(ctx, match.MatchCtx, frames)
	match.initialized = true
	match.Pipeline.SetSkipReset(true)
	match.Players = match.Pipeline.Players()
	if err != nil {
		mm.logger.Error("live match processing error", "match", matchID, "error", err)
		return res
	}
	res.Accepted += len(frames)

	match.FrameCount += result.FramesProcessed
	match.InvalidFrames += result.InvalidFrames
	match.LastActivity = time.Now()
	mm.recordResult(match, result)
	mm.persistEvents(ctx, match, result)
	mm.persistScores(ctx, match, result.PlayerScores, false)

	if time.Since(match.lastPersist) >= mm.persistInterval {
		mm.persistContext(ctx, match)
	}
	return res
}

// HandleControl applies producer control messages (match_start / match_end).
func (mm *MatchManager) HandleControl(msg model.ControlMessage) {
	switch msg.Type {
	case model.ControlMatchStart:
		match, ok := mm.getOrCreate(msg.MatchID)
		if !ok {
			return
		}
		mc := match.MatchCtx
		if msg.GameMode != "" {
			mc.GameMode = msg.GameMode
		}
		if msg.Map != "" {
			mc.Map = msg.Map
		}
		mc.IsPrivate = msg.IsPrivate
		for pid, team := range msg.Teams {
			if t := normalizeTeam(team); t != "" {
				mc.TeamAssignments[pid] = t
				if !containsString(mc.PlayerIDs, pid) && len(mc.PlayerIDs) < mm.maxPlayersPerMatch {
					mc.PlayerIDs = append(mc.PlayerIDs, pid)
				}
			}
		}
		sort.Strings(mc.PlayerIDs)
		match.LastActivity = time.Now()
		mm.logger.Info("match_start received", "match", msg.MatchID, "server", msg.ServerID,
			"mode", mc.GameMode, "map", mc.Map, "players", len(mc.PlayerIDs))
		mm.persistContext(context.Background(), match)
		match.mu.Unlock()
	case model.ControlMatchEnd:
		mm.logger.Info("match_end received", "match", msg.MatchID, "reason", msg.Reason)
		mm.EndMatch(msg.MatchID)
	}
}

func (mm *MatchManager) createMatch(matchID string) *LiveMatch {
	now := time.Now()
	matchCtx := &model.MatchContext{
		MatchID:         matchID,
		GameMode:        "Echo_Arena",
		StartTime:       now,
		PlayerIDs:       []string{},
		TeamAssignments: make(map[string]string),
		Physics:         physicsFromConfig(mm.cfg),
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

	match := &LiveMatch{
		MatchCtx:         matchCtx,
		Pipeline:         pipe,
		Scorer:           scorer,
		Players:          make(map[string]*model.PlayerState),
		StartTime:        now,
		LastActivity:     now,
		lastFrameIndex:   -1,
		eventsByDetector: make(map[string]int),
		lastLevel:        make(map[string]model.ScoringLevel),
		lastScore:        make(map[string]float64),
	}

	// Seed the monotonic frame counter from rows already stored for this
	// match (a producer restart or server restart mid-match).
	ctx := context.Background()
	if maxIdx, err := mm.store.GetMaxFrameIndex(ctx, matchID); err != nil {
		mm.logger.Warn("could not read stored frame index; starting at 0", "match", matchID, "error", err)
	} else if maxIdx >= 0 {
		match.lastFrameIndex = maxIdx
		mm.logger.Info("resuming live match with stored telemetry", "match", matchID, "last_frame_index", maxIdx)
		if prev, err := mm.store.GetMatchContext(ctx, matchID); err == nil && prev != nil {
			// Keep the original start time and any roster/metadata already known.
			if !prev.StartTime.IsZero() {
				matchCtx.StartTime = prev.StartTime
				match.StartTime = prev.StartTime
			}
			if prev.GameMode != "" {
				matchCtx.GameMode = prev.GameMode
			}
			matchCtx.Map = prev.Map
			matchCtx.IsPrivate = prev.IsPrivate
			for pid, team := range prev.TeamAssignments {
				matchCtx.TeamAssignments[pid] = team
			}
			for _, pid := range prev.PlayerIDs {
				if !containsString(matchCtx.PlayerIDs, pid) {
					matchCtx.PlayerIDs = append(matchCtx.PlayerIDs, pid)
				}
			}
			sort.Strings(matchCtx.PlayerIDs)
		}
	}

	mm.persistContext(ctx, match)
	if mm.metrics != nil {
		mm.metrics.MatchesCreated.Inc()
	}
	mm.logger.Info("live match created", "match", matchID, "detectors", len(detectors))
	return match
}

// recordResult folds a batch result into the match counters and metrics.
func (mm *MatchManager) recordResult(match *LiveMatch, result *pipeline.MatchResult) {
	for _, ev := range result.DetectionEvents {
		match.eventsByDetector[ev.DetectorID]++
		match.totalEvents++
	}
	if mm.metrics == nil {
		return
	}
	m := mm.metrics
	m.FramesProcessed.Add(int64(result.FramesProcessed))
	m.FramesInvalid.Add(int64(result.InvalidFrames))
	for reason, n := range result.InvalidFrameReasons {
		m.FramesInvalidReasons.Add(reason, int64(n))
	}
	for _, ev := range result.DetectionEvents {
		if ev.IsShadow {
			m.ShadowEvents.Inc(ev.DetectorID)
		}
		m.DetectionEvents.Inc(ev.DetectorID)
	}
	m.EventsDeduplicated.Add(int64(result.EventsMerged))
	m.EventsRateLimited.Add(int64(result.EventsRateLimited))
	m.EventsInvalid.Add(int64(result.EventsInvalid))
}

// persistEvents stores the batch's detection events.
func (mm *MatchManager) persistEvents(ctx context.Context, match *LiveMatch, result *pipeline.MatchResult) {
	for _, ev := range result.DetectionEvents {
		if err := mm.store.StoreDetectionEventWithSource(ctx, ev, AnalysisSourceInitial); err != nil {
			mm.logger.Error("failed to store event", "match", match.MatchCtx.MatchID, "detector", ev.DetectorID, "error", err)
			if mm.metrics != nil {
				mm.metrics.StoreErrors.Inc()
			}
		}
	}
}

// persistScores writes suspicion score snapshots. A snapshot is stored when a
// player's tier changes (or, with final=true, whenever the score moved since
// the last snapshot). Zero scores are never written, so shadow-only players
// leave no rows and GetPlayerScore cannot regress a real score to 0.
func (mm *MatchManager) persistScores(ctx context.Context, match *LiveMatch, scores map[string]model.SuspicionScore, final bool) {
	ids := make([]string, 0, len(scores))
	for pid := range scores {
		ids = append(ids, pid)
	}
	sort.Strings(ids)
	for _, pid := range ids {
		score := scores[pid]
		if score.TotalScore <= 0 || score.EventCount == 0 {
			continue
		}
		level := score.Level()
		prevLevel, seen := match.lastLevel[pid]
		changed := !seen || level != prevLevel
		if final && score.TotalScore != match.lastScore[pid] {
			changed = true
		}
		if !changed {
			continue
		}
		if score.SnapshotTime.IsZero() {
			score.SnapshotTime = time.Now()
		}
		if err := mm.store.StoreSuspicionScore(ctx, score); err != nil {
			mm.logger.Error("failed to store score snapshot", "match", match.MatchCtx.MatchID, "player", pid, "error", err)
			if mm.metrics != nil {
				mm.metrics.StoreErrors.Inc()
			}
			continue
		}
		match.lastLevel[pid] = level
		match.lastScore[pid] = score.TotalScore
		if mm.metrics != nil {
			mm.metrics.ScoreSnapshotsStored.Inc()
		}
		if !seen || level != prevLevel {
			mm.logger.Info("player suspicion tier changed", "match", match.MatchCtx.MatchID, "player", pid,
				"level", string(level), "score", score.TotalScore, "events", score.EventCount)
		}
	}
}

// matchDuration returns the elapsed game time covered by the match.
func (match *LiveMatch) matchDuration() time.Duration {
	if match.haveTimestamp && match.lastTimestamp > match.firstTimestamp {
		return time.Duration((match.lastTimestamp - match.firstTimestamp) * float64(time.Second))
	}
	return time.Since(match.StartTime)
}

// persistContext upserts the match context (caller holds match.mu).
func (mm *MatchManager) persistContext(ctx context.Context, match *LiveMatch) {
	match.MatchCtx.Duration = match.matchDuration()
	if err := mm.store.StoreMatchContext(ctx, match.MatchCtx, match.FrameCount); err != nil {
		mm.logger.Error("failed to store match context", "match", match.MatchCtx.MatchID, "error", err)
		if mm.metrics != nil {
			mm.metrics.StoreErrors.Inc()
		}
		return
	}
	match.lastPersist = time.Now()
}

// EndMatch finalizes a match: closes open dedup incidents, persists the
// remaining events and scores, the match context and a summary.
func (mm *MatchManager) EndMatch(matchID string) {
	mm.matchesMu.Lock()
	match, ok := mm.matches[matchID]
	if ok {
		delete(mm.matches, matchID)
		mm.updateActiveGauge()
	}
	mm.matchesMu.Unlock()

	if !ok {
		return
	}

	match.mu.Lock()
	defer match.mu.Unlock()
	mm.finalizeLocked(match)
}

// finalizeLocked persists everything for a match; caller holds match.mu.
func (mm *MatchManager) finalizeLocked(match *LiveMatch) {
	if match.ended {
		return
	}
	match.ended = true
	matchID := match.MatchCtx.MatchID
	ctx := context.Background()

	if match.initialized {
		final := match.Pipeline.Finalize(match.MatchCtx)
		mm.recordResult(match, final)
		mm.persistEvents(ctx, match, final)
		mm.persistScores(ctx, match, final.PlayerScores, true)
	}

	mm.logger.Info("live match ended", "match", matchID, "frames", match.FrameCount,
		"rows_stored", match.RowsStored, "events", match.totalEvents, "invalid_frames", match.InvalidFrames,
		"duration", match.matchDuration().Round(time.Second))

	mm.persistContext(ctx, match)

	var flagged []string
	for pid, sc := range match.Scorer.GetAllScores() {
		if sc.ExceedsReview {
			flagged = append(flagged, pid)
		}
	}
	sort.Strings(flagged)

	summary := model.MatchSummary{
		MatchID:              matchID,
		Map:                  match.MatchCtx.Map,
		GameMode:             match.MatchCtx.GameMode,
		IsRanked:             match.MatchCtx.IsRanked,
		StartTime:            match.StartTime,
		Duration:             match.matchDuration(),
		FrameCount:           match.FrameCount,
		InvalidFrameCount:    match.InvalidFrames,
		TotalDetectionEvents: match.totalEvents,
		DetectionsByDetector: match.eventsByDetector,
		FlaggedPlayers:       flagged,
		Source:               "live_telemetry",
	}
	if err := mm.store.StoreMatchSummary(ctx, summary); err != nil {
		mm.logger.Error("failed to store match summary", "match", matchID, "error", err)
		if mm.metrics != nil {
			mm.metrics.StoreErrors.Inc()
		}
	}
	if mm.metrics != nil {
		mm.metrics.MatchesEnded.Inc()
	}
}

// CleanupStaleMatches finalizes (and persists) matches with no activity for
// the given duration.
func (mm *MatchManager) CleanupStaleMatches(maxIdle time.Duration) {
	cutoff := time.Now().Add(-maxIdle)
	var stale []string
	mm.matchesMu.RLock()
	for id, m := range mm.matches {
		m.mu.Lock()
		idle := m.LastActivity.Before(cutoff)
		m.mu.Unlock()
		if idle {
			stale = append(stale, id)
		}
	}
	mm.matchesMu.RUnlock()
	sort.Strings(stale)
	for _, id := range stale {
		mm.logger.Warn("finalizing stale match", "match", id, "idle_limit", maxIdle)
		mm.EndMatch(id)
	}
}

// Close finalizes every active match (graceful shutdown). It must run after
// the telemetry server has stopped delivering batches and before the store
// is closed.
func (mm *MatchManager) Close() {
	mm.matchesMu.RLock()
	ids := make([]string, 0, len(mm.matches))
	for id := range mm.matches {
		ids = append(ids, id)
	}
	mm.matchesMu.RUnlock()
	sort.Strings(ids)
	for _, id := range ids {
		mm.EndMatch(id)
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

// GetMatchContext returns a copy of the live match context, or nil.
func (mm *MatchManager) GetMatchContext(matchID string) *model.MatchContext {
	mm.matchesMu.RLock()
	match, ok := mm.matches[matchID]
	mm.matchesMu.RUnlock()
	if !ok {
		return nil
	}
	match.mu.Lock()
	defer match.mu.Unlock()
	mc := *match.MatchCtx
	mc.Duration = match.matchDuration()
	mc.PlayerIDs = append([]string(nil), match.MatchCtx.PlayerIDs...)
	mc.TeamAssignments = make(map[string]string, len(match.MatchCtx.TeamAssignments))
	for k, v := range match.MatchCtx.TeamAssignments {
		mc.TeamAssignments[k] = v
	}
	return &mc
}

// updateActiveGauge mirrors the match count into metrics (caller holds matchesMu).
func (mm *MatchManager) updateActiveGauge() {
	if mm.metrics != nil {
		mm.metrics.ActiveMatches.Set(int64(len(mm.matches)))
	}
}

// warnThrottled logs the first occurrence of key at Warn, then every 1000th.
func (mm *MatchManager) warnThrottled(key, msg string, args ...any) {
	mm.warnMu.Lock()
	n := mm.warnCounts[key] + 1
	mm.warnCounts[key] = n
	if len(mm.warnCounts) > 10000 {
		mm.warnCounts = make(map[string]int)
	}
	mm.warnMu.Unlock()
	if n == 1 || n%1000 == 0 {
		mm.logger.Warn(msg, append(args, "count", n)...)
	}
}

// physicsFromConfig builds the match physics from the loaded [physics] block,
// falling back to DefaultPhysics for any field the config leaves at zero.
func physicsFromConfig(cfg *config.Config) model.PhysicsConstants {
	ph := model.DefaultPhysics()
	if cfg == nil {
		return ph
	}
	c := cfg.Physics
	if c.DiscSpeedCap > 0 {
		ph.DiscSpeedCap = c.DiscSpeedCap
	}
	if c.BoostSpeedCap > 0 {
		ph.BoostSpeedCap = c.BoostSpeedCap
	}
	if c.MaxPlayerSpeed > 0 {
		ph.MaxPlayerSpeed = c.MaxPlayerSpeed
	}
	if c.MaxThrowSpeed > 0 {
		ph.MaxThrowSpeed = c.MaxThrowSpeed
	}
	if c.StunDuration > 0 {
		ph.StunDuration = c.StunDuration
	}
	if c.ShieldCooldown > 0 {
		ph.ShieldCooldown = c.ShieldCooldown
	}
	if c.ImmunityWindow > 0 {
		ph.ImmunityWindow = c.ImmunityWindow
	}
	if c.GrabRange > 0 {
		ph.GrabRange = c.GrabRange
	}
	return ph
}

// normalizeTeam maps a producer team label to "blue"/"orange" or "".
func normalizeTeam(team string) string {
	switch strings.ToLower(strings.TrimSpace(team)) {
	case "blue":
		return "blue"
	case "orange":
		return "orange"
	default:
		return ""
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
