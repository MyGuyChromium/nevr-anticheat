package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	"github.com/nevr-anticheat/nevr-anticheat/internal/review"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// LiveMatch holds the state of an active match being analyzed in real-time.
//
// A LiveMatch is one SEGMENT of a match_id: the frames seen between its
// creation and its finalization. The store can already hold earlier segments
// of the same match_id (a server restart mid-match, frames arriving after
// match_end, a rematch that reuses the lobby's match_id). A segment created
// over stored telemetry is a resume: its counters (FrameCount, first/last
// timestamp and therefore Duration, StartTime) continue from the stored
// data, and its summary is folded into the previous one at finalization.
// Detection state (pipeline, scorer, review cases) is per segment.
type LiveMatch struct {
	MatchCtx *model.MatchContext
	Pipeline *pipeline.Pipeline
	Scorer   *scoring.SuspicionScorer
	// Players is the pipeline's persistent per-player state (valid after the
	// first batch). It is what makes kinematics, throw detection and warmup
	// continue across batches.
	Players       map[string]*model.PlayerState
	FrameCount    int // frame ticks stored for the match (all segments)
	RowsStored    int // telemetry rows stored for the match (all segments)
	InvalidFrames int
	StartTime     time.Time
	LastActivity  time.Time

	// Frame identity (contract F). Indices are match-relative and monotonic
	// per player. A batch in which a player's index is <= that player's last
	// index is a producer restart: one shared offset per match is added to
	// every later frame's index and timestamp so the stored stream stays
	// contiguous. A batch that merely repeats the match's last index for a
	// different player (per-player batches of one tick) is not a restart.
	lastFrameIndex  int            // highest frame index stored for the match
	lastPlayerIndex map[string]int // highest frame index stored per player
	indexOffset     int            // sticky re-base offset applied to raw indices
	timestampOffset float64        // sticky re-base offset applied to raw timestamps
	firstTimestamp  float64        // first frame timestamp stored (relative game time)
	lastTimestamp   float64        // highest frame timestamp stored
	haveTimestamp   bool
	rebasedBatches  int

	// pending holds stored frames of the newest tick until a higher index
	// closes it: the pipeline evaluates every tick exactly once, with all
	// the players that produced a frame for it, however the producer split
	// the tick across batches.
	pending []model.PlayerTelemetryFrame

	resumed          bool // created over telemetry already stored for the match
	priorDetections  int  // total_detections of the previous summary row, if any
	priorFlagged     []string
	lastPersist      time.Time
	eventsByDetector map[string]int
	totalEvents      int
	segmentEvents    []model.DetectionEvent // events this segment stored (review cases)
	lastLevel        map[string]model.ScoringLevel
	lastScore        map[string]float64
	detectorNames    map[string]string // detector ID -> name, for review case evidence
	initialized      bool              // true once the pipeline has processed a batch
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
	// defaultTickRate is the nominal producer rate assumed when the match
	// context has none (the bridge polls at 15 Hz).
	defaultTickRate = 15.0
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
//
// Creation is expensive (detector build, store round-trips), so only the
// map insertion happens under matchesMu: a locked shell is published first
// and initialised under its own mutex, so other connections and /health are
// never stalled behind a match they are not touching.
func (mm *MatchManager) getOrCreate(matchID, serverID string) (*LiveMatch, bool) {
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
			match = &LiveMatch{}
			match.mu.Lock()
			mm.matches[matchID] = match
			mm.updateActiveGauge()
			mm.matchesMu.Unlock()
			mm.initMatch(match, matchID, serverID)
			return match, true
		}
		mm.matchesMu.Unlock()

		match.mu.Lock()
		if match.ended {
			// EndMatch raced with us; retry so a fresh LiveMatch is created.
			match.mu.Unlock()
			continue
		}
		mm.noteServerID(match, serverID)
		return match, true
	}
}

// noteServerID records the producer's provenance on the match context the
// first time it is seen (caller holds match.mu). Telemetry for one match
// from a second server_id is logged and keeps the first.
func (mm *MatchManager) noteServerID(match *LiveMatch, serverID string) {
	if serverID == "" {
		return
	}
	mc := match.MatchCtx
	switch {
	case mc.ServerID == "":
		mc.ServerID = serverID
		mm.persistContext(context.Background(), match)
	case mc.ServerID != serverID:
		mm.warnThrottled("serverid:"+mc.MatchID, "telemetry for match arrived from a different server_id; keeping the first",
			"match", mc.MatchID, "server_id", mc.ServerID, "other", serverID)
	}
}

// nominalDt is the producer's nominal frame spacing in seconds.
func (match *LiveMatch) nominalDt() float64 {
	if match.MatchCtx != nil && match.MatchCtx.TickRate > 0 {
		return 1 / match.MatchCtx.TickRate
	}
	return 1 / defaultTickRate
}

// HandleFrames processes a batch of frames for a match. serverID is the
// batch's provenance stamp (may be empty).
func (mm *MatchManager) HandleFrames(matchID, serverID string, frames []model.PlayerTelemetryFrame) FrameResult {
	return mm.HandleFramesWithRaw(matchID, serverID, frames, "")
}

// HandleFramesWithRaw processes normalized frames and preserves the exact
// broadcaster payload once for their tick. Existing producers may omit rawJSON;
// bridge producers send it so future schema changes and disputed detections can
// be investigated without relying only on derived frames.
func (mm *MatchManager) HandleFramesWithRaw(matchID, serverID string, frames []model.PlayerTelemetryFrame, rawJSON string) FrameResult {
	if len(frames) == 0 {
		return FrameResult{}
	}
	match, ok := mm.getOrCreate(matchID, serverID)
	if !ok {
		return FrameResult{Rejected: len(frames)}
	}
	defer match.mu.Unlock()
	match.LastActivity = time.Now()

	ctx := context.Background()
	res := FrameResult{}

	// Frame identity (contract F): apply the match's sticky re-base offset,
	// then detect a producer restart per player.
	if match.indexOffset != 0 || match.timestampOffset != 0 {
		for i := range frames {
			frames[i].FrameIndex += match.indexOffset
			frames[i].Timestamp += match.timestampOffset
		}
	}
	minIdx, minTS := frames[0].FrameIndex, frames[0].Timestamp
	restart := false
	for _, f := range frames {
		if f.FrameIndex < minIdx {
			minIdx = f.FrameIndex
		}
		if f.Timestamp < minTS {
			minTS = f.Timestamp
		}
		if last, seen := match.lastPlayerIndex[f.PlayerID]; seen && f.FrameIndex <= last {
			restart = true
		}
	}
	if restart {
		offset := match.lastFrameIndex + 1 - minIdx
		tsOffset := 0.0
		if match.haveTimestamp {
			tsOffset = match.lastTimestamp + match.nominalDt() - minTS
		}
		for i := range frames {
			frames[i].FrameIndex += offset
			frames[i].Timestamp += tsOffset
		}
		match.indexOffset += offset
		match.timestampOffset += tsOffset
		match.rebasedBatches++
		if mm.metrics != nil {
			mm.metrics.FramesRebased.Add(int64(len(frames)))
		}
		if match.rebasedBatches == 1 || match.rebasedBatches%1000 == 0 {
			mm.logger.Warn("re-based frame batch after producer restart",
				"match", matchID, "frames", len(frames), "batch_min", minIdx,
				"last_index", match.lastFrameIndex, "offset", offset,
				"timestamp_offset", tsOffset, "rebased_batches", match.rebasedBatches)
		}
	}

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
		kept = append(kept, f)
	}
	frames = kept
	if len(frames) == 0 {
		return res
	}

	// Persist raw telemetry first: it is the source of truth for reprocessing.
	// Ack accounting (contract 3): every frame of the batch is counted
	// exactly once. Accepted = rows the store inserted, Ignored = rows it
	// already had, Rejected = rows it could not write. A batch the store
	// could not write is not processed either: inline results must never
	// exist for frames the store does not hold, and the frame bookkeeping
	// stays where it was so the producer can re-send the batch unchanged.
	var rawByFrame map[int]string
	if rawJSON != "" {
		rawByFrame = map[int]string{frames[0].FrameIndex: rawJSON}
	}
	storeResult, storeErr := mm.store.StoreTelemetryFramesWithRaw(ctx, matchID, frames, rawByFrame)
	if storeErr != nil {
		mm.logger.Warn("failed to store telemetry; batch not processed", "match", matchID, "frames", len(frames), "error", storeErr)
		if mm.metrics != nil {
			mm.metrics.StoreErrors.Inc()
		}
		res.Rejected += len(frames)
		return res
	}
	match.RowsStored += storeResult.Inserted
	res.Accepted += storeResult.Inserted
	if ignored := len(frames) - storeResult.Inserted; ignored > 0 {
		res.Ignored += ignored
		if mm.metrics != nil {
			mm.metrics.FramesIgnored.Add(int64(ignored))
		}
		mm.warnThrottled("dup:"+matchID, "store ignored duplicate telemetry rows",
			"match", matchID, "ignored", ignored)
	}
	match.noteStored(frames)

	// Tick assembly: hand the pipeline only closed ticks. A tick is closed
	// once a higher index has been stored (per-player batches of one tick
	// are then evaluated together, as one /session poll delivers them); the
	// newest tick waits for the next batch or for finalization.
	match.pending = append(match.pending, frames...)
	ready, held := splitClosedTicks(match.pending)
	match.pending = held
	if len(ready) > 0 {
		mm.processLocked(ctx, match, ready)
	}

	if time.Since(match.lastPersist) >= mm.persistInterval {
		mm.persistContext(ctx, match)
	}
	return res
}

// splitClosedTicks partitions frames into those of closed ticks (index below
// the highest index present) and those of the newest tick.
func splitClosedTicks(frames []model.PlayerTelemetryFrame) (ready, held []model.PlayerTelemetryFrame) {
	if len(frames) == 0 {
		return nil, nil
	}
	maxIdx := frames[0].FrameIndex
	for _, f := range frames[1:] {
		if f.FrameIndex > maxIdx {
			maxIdx = f.FrameIndex
		}
	}
	for _, f := range frames {
		if f.FrameIndex < maxIdx {
			ready = append(ready, f)
		} else {
			held = append(held, f)
		}
	}
	return ready, held
}

// processLocked runs stored frames through the pipeline and persists the
// results (caller holds match.mu). The pipeline was put in live mode at
// creation (SetSkipReset), so the first call only creates the roster and
// every later call continues detector, scorer, dedup and per-player state.
func (mm *MatchManager) processLocked(ctx context.Context, match *LiveMatch, frames []model.PlayerTelemetryFrame) {
	matchID := match.MatchCtx.MatchID
	result, err := match.Pipeline.ProcessMatch(ctx, match.MatchCtx, frames)
	match.initialized = true
	match.Players = match.Pipeline.Players()
	if err != nil {
		mm.logger.Error("live match processing error", "match", matchID, "error", err)
		return
	}

	match.FrameCount += result.FramesProcessed
	match.InvalidFrames += result.InvalidFrames
	mm.recordResult(match, result)
	if mm.metrics != nil {
		if n := len(frames) - result.InvalidFrames; n > 0 {
			mm.metrics.FramesProcessed.Add(int64(n))
		}
	}
	mm.persistEvents(ctx, match, result)
	mm.persistScores(ctx, match, result.PlayerScores, false)
}

// noteStored advances the frame bookkeeping over frames the store now holds.
func (match *LiveMatch) noteStored(frames []model.PlayerTelemetryFrame) {
	for _, f := range frames {
		if f.FrameIndex > match.lastFrameIndex {
			match.lastFrameIndex = f.FrameIndex
		}
		if last, seen := match.lastPlayerIndex[f.PlayerID]; !seen || f.FrameIndex > last {
			match.lastPlayerIndex[f.PlayerID] = f.FrameIndex
		}
		if !match.haveTimestamp {
			match.firstTimestamp, match.lastTimestamp = f.Timestamp, f.Timestamp
			match.haveTimestamp = true
			continue
		}
		if f.Timestamp < match.firstTimestamp {
			match.firstTimestamp = f.Timestamp
		}
		if f.Timestamp > match.lastTimestamp {
			match.lastTimestamp = f.Timestamp
		}
	}
}

// HandleControl applies producer control messages (match_start / match_end).
func (mm *MatchManager) HandleControl(msg model.ControlMessage) {
	switch msg.Type {
	case model.ControlMatchStart:
		match, ok := mm.getOrCreate(msg.MatchID, msg.ServerID)
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

// initMatch fills in a freshly published LiveMatch shell (caller holds
// match.mu; the shell is already in the map so concurrent users of the same
// match wait on match.mu, not on matchesMu).
func (mm *MatchManager) initMatch(match *LiveMatch, matchID, serverID string) {
	now := time.Now()
	matchCtx := &model.MatchContext{
		MatchID:         matchID,
		GameMode:        "Echo_Arena",
		StartTime:       now,
		PlayerIDs:       []string{},
		TeamAssignments: make(map[string]string),
		Physics:         mm.cfg.Physics.Constants(),
		Source:          "live_telemetry",
		ServerID:        serverID,
		TickRate:        defaultTickRate,
	}

	detectors := mm.detectorFn()
	names := make(map[string]string, len(detectors))
	for _, d := range detectors {
		names[d.ID()] = d.Name()
	}
	// Levels is the configured tier table with review_threshold mapped onto
	// high_risk; the review cases created at match end classify with the
	// same table (createReviewCases uses Scorer.Levels()).
	scorer := scoring.NewSuspicionScorer(scoring.ScorerConfig{
		MaxSingleContribution:         mm.cfg.Scoring.MaxSingleContribution,
		MaxContribPerDetectorPerMatch: mm.cfg.Scoring.MaxContribPerDetectorPerMatch,
		SameCategoryDiminishing:       mm.cfg.Scoring.SameCategoryDiminishing,
		AutoEnforceThreshold:          mm.cfg.Scoring.AutoEnforceThreshold,
		DecayHalfLifeHours:            mm.cfg.Scoring.DecayHalfLifeHours,
		CooldownFrames:                mm.cfg.Pipeline.CooldownFrames,
		CorrelationBonusCap:           mm.cfg.Scoring.CorrelationBonusCap,
		Levels:                        mm.cfg.Scoring.LevelTable(),
	})

	// Live mode from the start: batches are slices of one match and the
	// pipeline must never reset between them (nor flush incidents early).
	pipe := pipeline.NewPipeline(mm.cfg, detectors, scorer, mm.logger)
	pipe.SetSkipReset(true)

	match.MatchCtx = matchCtx
	match.Pipeline = pipe
	match.Scorer = scorer
	match.Players = make(map[string]*model.PlayerState)
	match.StartTime = now
	match.LastActivity = now
	match.lastFrameIndex = -1
	match.lastPlayerIndex = make(map[string]int)
	match.eventsByDetector = make(map[string]int)
	match.lastLevel = make(map[string]model.ScoringLevel)
	match.lastScore = make(map[string]float64)
	match.detectorNames = names

	ctx := context.Background()
	mm.resumeFromStore(ctx, match)

	// Event times are match start + event timestamp (the pipeline repeats
	// this on its first slice; a resumed match keeps the stored start).
	scorer.SetMatchStart(matchCtx.StartTime)

	mm.persistContext(ctx, match)
	if mm.metrics != nil {
		mm.metrics.MatchesCreated.Inc()
	}
	mm.logger.Info("live match created", "match", matchID, "server_id", serverID, "detectors", len(detectors))
}

// resumeFromStore continues a match over telemetry already stored for its
// match_id (server restart mid-match, frames after match_end, a rematch on
// the same lobby id): the frame bookkeeping, tick/row counters, timestamp
// span and the stored context's start time, roster and provenance carry
// over so the persisted context and summary describe the whole match rather
// than the segment after the restart.
func (mm *MatchManager) resumeFromStore(ctx context.Context, match *LiveMatch) {
	matchCtx := match.MatchCtx
	matchID := matchCtx.MatchID
	maxIdx, err := mm.store.GetMaxFrameIndex(ctx, matchID)
	if err != nil {
		mm.logger.Warn("could not read stored frame index; starting at 0", "match", matchID, "error", err)
		return
	}
	if maxIdx < 0 {
		return
	}
	match.resumed = true
	match.lastFrameIndex = maxIdx
	span, err := mm.storedSpan(ctx, matchID)
	if err != nil {
		mm.logger.Warn("could not read stored telemetry span; counters restart", "match", matchID, "error", err)
	} else {
		match.FrameCount = span.ticks
		match.RowsStored = span.rows
		for pid, idx := range span.playerMax {
			match.lastPlayerIndex[pid] = idx
		}
		if span.haveTS {
			match.firstTimestamp, match.lastTimestamp, match.haveTimestamp = span.minTS, span.maxTS, true
		}
	}
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
		if matchCtx.ServerID == "" {
			matchCtx.ServerID = prev.ServerID
		} else if prev.ServerID != "" && prev.ServerID != matchCtx.ServerID {
			mm.warnThrottled("serverid:"+matchID, "resumed match has a different stored server_id; keeping the stored one",
				"match", matchID, "server_id", prev.ServerID, "other", matchCtx.ServerID)
			matchCtx.ServerID = prev.ServerID
		}
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
	if prior, err := mm.priorSummary(ctx, matchID); err != nil {
		mm.logger.Warn("could not read previous match summary", "match", matchID, "error", err)
	} else {
		match.priorDetections = prior.detections
		match.priorFlagged = prior.flagged
	}
	mm.logger.Info("resuming live match over stored telemetry", "match", matchID,
		"last_frame_index", maxIdx, "ticks", match.FrameCount, "rows", match.RowsStored,
		"duration", match.matchDuration().Round(time.Second), "server_id", matchCtx.ServerID)
}

// storedSpan describes the telemetry already stored for a match.
type storedSpan struct {
	rows, ticks  int
	minTS, maxTS float64
	haveTS       bool
	playerMax    map[string]int
}

// storedSpan reads the row/tick counts, timestamp span and per-player
// highest frame index of a match's stored telemetry.
func (mm *MatchManager) storedSpan(ctx context.Context, matchID string) (storedSpan, error) {
	var sp storedSpan
	var minTS, maxTS sql.NullFloat64
	db := mm.store.DB()
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(DISTINCT frame_index), MIN(timestamp), MAX(timestamp)
		 FROM telemetry_frames WHERE match_id = ?`, matchID).Scan(&sp.rows, &sp.ticks, &minTS, &maxTS)
	if err != nil {
		return sp, err
	}
	if minTS.Valid && maxTS.Valid {
		sp.minTS, sp.maxTS, sp.haveTS = minTS.Float64, maxTS.Float64, true
	}
	rows, err := db.QueryContext(ctx,
		`SELECT player_id, MAX(frame_index) FROM telemetry_frames WHERE match_id = ? GROUP BY player_id`, matchID)
	if err != nil {
		return sp, err
	}
	defer rows.Close()
	sp.playerMax = make(map[string]int)
	for rows.Next() {
		var pid string
		var idx int
		if err := rows.Scan(&pid, &idx); err != nil {
			return sp, err
		}
		sp.playerMax[pid] = idx
	}
	return sp, rows.Err()
}

// priorSummaryRow is what an earlier segment's finalization left behind.
type priorSummaryRow struct {
	detections int
	flagged    []string
}

// priorSummary reads the match's existing summary row, if any.
func (mm *MatchManager) priorSummary(ctx context.Context, matchID string) (priorSummaryRow, error) {
	var out priorSummaryRow
	var flaggedJSON sql.NullString
	err := mm.store.DB().QueryRowContext(ctx,
		`SELECT total_detections, flagged_players FROM match_summaries WHERE match_id = ?`, matchID,
	).Scan(&out.detections, &flaggedJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if flaggedJSON.Valid && flaggedJSON.String != "" {
		_ = json.Unmarshal([]byte(flaggedJSON.String), &out.flagged)
	}
	return out, nil
}

// recordResult folds a batch result into the match counters and metrics.
func (mm *MatchManager) recordResult(match *LiveMatch, result *pipeline.MatchResult) {
	if err := mm.store.MergeMatchCatchReviews(context.Background(), match.MatchCtx.MatchID, result.PlayerCoverage); err != nil {
		mm.logger.Error("failed to store live catch diagnostics", "match", match.MatchCtx.MatchID, "error", err)
		if mm.metrics != nil {
			mm.metrics.StoreErrors.Inc()
		}
	}
	for _, ev := range result.DetectionEvents {
		match.eventsByDetector[ev.DetectorID]++
		match.totalEvents++
	}
	if mm.metrics == nil {
		return
	}
	m := mm.metrics
	m.TicksProcessed.Add(int64(result.FramesProcessed))
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

// persistEvents stores the batch's detection events and keeps the stored
// ones for this segment's review cases.
func (mm *MatchManager) persistEvents(ctx context.Context, match *LiveMatch, result *pipeline.MatchResult) {
	for _, ev := range result.DetectionEvents {
		if err := mm.store.StoreDetectionEventWithSource(ctx, ev, AnalysisSourceInitial); err != nil {
			mm.logger.Error("failed to store event", "match", match.MatchCtx.MatchID, "detector", ev.DetectorID, "error", err)
			if mm.metrics != nil {
				mm.metrics.StoreErrors.Inc()
			}
			continue
		}
		match.segmentEvents = append(match.segmentEvents, ev)
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

// matchDuration returns the elapsed game time covered by the match: last
// stored timestamp minus first (all segments), else wall time since start.
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
// The summary row is per match_id: a resumed segment folds its totals into
// the previous segment's row (frame count and duration are already
// cumulative, detections add up, flagged players are the union).
func (mm *MatchManager) finalizeLocked(match *LiveMatch) {
	if match.ended {
		return
	}
	match.ended = true
	matchID := match.MatchCtx.MatchID
	ctx := context.Background()

	if len(match.pending) > 0 {
		mm.processLocked(ctx, match, match.pending)
		match.pending = nil
	}
	if match.initialized {
		final := match.Pipeline.Finalize(match.MatchCtx)
		mm.recordResult(match, final)
		mm.persistEvents(ctx, match, final)
		mm.persistScores(ctx, match, final.PlayerScores, true)
	}

	mm.logger.Info("live match ended", "match", matchID, "frames", match.FrameCount,
		"rows_stored", match.RowsStored, "events", match.totalEvents, "invalid_frames", match.InvalidFrames,
		"duration", match.matchDuration().Round(time.Second), "resumed", match.resumed)

	mm.persistContext(ctx, match)

	flagged := mm.createReviewCases(ctx, match)
	for _, pid := range match.priorFlagged {
		if !containsString(flagged, pid) {
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
		TotalDetectionEvents: match.priorDetections + match.totalEvents,
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

// createReviewCases builds the match's single-match review cases through the
// same mechanism as the offline paths (review.CreateCasesFromResult with the
// scorer's level table) from this segment's stored events and the live
// scorer's scores. Events of earlier segments (a previous game on the same
// lobby id, or the part of the match before a server restart) belong to a
// different scorer and are not mixed in. It returns the sorted player IDs
// that got a case (the summary's flagged list) and invokes OnReviewCase for
// each. Caller holds match.mu.
func (mm *MatchManager) createReviewCases(ctx context.Context, match *LiveMatch) []string {
	matchID := match.MatchCtx.MatchID
	scores := match.Scorer.GetAllScores()
	if len(scores) == 0 {
		return nil
	}
	events := append([]model.DetectionEvent(nil), match.segmentEvents...)
	result := &pipeline.MatchResult{MatchID: matchID, PlayerScores: scores, DetectionEvents: events}
	cases, err := review.CreateCasesFromResult(ctx, mm.store, match.MatchCtx, result, match.Scorer.Levels(),
		review.WithDetectorNames(match.detectorNames), review.WithLogger(mm.logger))
	if err != nil {
		mm.logger.Error("failed to store review cases", "match", matchID, "error", err)
		if mm.metrics != nil {
			mm.metrics.StoreErrors.Inc()
		}
	}
	flagged := make([]string, 0, len(cases))
	for _, rc := range cases {
		flagged = append(flagged, rc.PlayerID)
		if mm.OnReviewCase != nil {
			mm.OnReviewCase(rc)
		}
	}
	sort.Strings(flagged)
	return flagged
}

// CleanupStaleMatches finalizes (and persists) matches with no activity for
// the given duration. The match map is only snapshotted under matchesMu;
// per-match locks are taken afterwards so a batch in flight on one match
// never blocks the others.
func (mm *MatchManager) CleanupStaleMatches(maxIdle time.Duration) {
	cutoff := time.Now().Add(-maxIdle)
	type entry struct {
		id string
		m  *LiveMatch
	}
	mm.matchesMu.RLock()
	all := make([]entry, 0, len(mm.matches))
	for id, m := range mm.matches {
		all = append(all, entry{id, m})
	}
	mm.matchesMu.RUnlock()
	var stale []string
	for _, e := range all {
		e.m.mu.Lock()
		idle := e.m.LastActivity.Before(cutoff)
		e.m.mu.Unlock()
		if idle {
			stale = append(stale, e.id)
		}
	}
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
