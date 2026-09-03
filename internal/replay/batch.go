package replay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/review"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// BatchResult holds the results of batch analysis.
type BatchResult struct {
	TotalFiles   int `json:"total_files"`
	IgnoredFiles int `json:"ignored_files"` // .json files that are not legacy replays (bridge dumps, exports)
	Processed    int `json:"processed"`     // analyzed AND fully persisted
	Skipped      int `json:"skipped"`       // duplicate match in this run, or already stored (no --force)
	Errors       int `json:"errors"`        // parse/analysis failures plus PersistFailed
	// PersistFailed counts matches that were analyzed but whose telemetry,
	// context or derived outputs could not be written (disk full, read-only
	// or locked database, cancelled context). They are included in Errors
	// and excluded from Processed so a run that stored nothing never reports
	// success.
	PersistFailed  int           `json:"persist_failed"`
	FlaggedPlayers []string      `json:"flagged_players"`
	FramesInserted int           `json:"frames_inserted"`
	FramesIgnored  int           `json:"frames_ignored"`
	EventsStored   int           `json:"events_stored"`
	Duration       time.Duration `json:"duration"`
}

// BatchAnalyzer processes directories of replay files.
//
// Parsing runs fully in parallel across workers. Detection runs in parallel
// too when a pipeline factory is set (one Pipeline per worker); without a
// factory the single shared Pipeline is not goroutine-safe and ProcessMatch is
// serialized, but parsing still overlaps. All database writes go through one
// writer goroutine so the store sees a single, ordered stream.
type BatchAnalyzer struct {
	pipeline        *pipeline.Pipeline
	pipelineFactory func() *pipeline.Pipeline
	store           *sqlite.Store
	parser          func() FrameParser
	workers         int
	logger          *slog.Logger
	force           bool
	opts            AnalysisOptions
	physics         model.PhysicsConstants
	pipelineMu      sync.Mutex
}

// SetAnalysisOptions sets the level table and detector names used when the
// derived outputs (score snapshots, review cases) are stored.
func (ba *BatchAnalyzer) SetAnalysisOptions(opts AnalysisOptions) {
	ba.opts = opts
}

// SetPhysics sets the physics constants stamped on every parsed match
// context (from the [physics] config block). Zero means model.DefaultPhysics.
func (ba *BatchAnalyzer) SetPhysics(phys model.PhysicsConstants) {
	ba.physics = phys
}

// NewBatchAnalyzer creates a new batch analyzer using one shared pipeline.
// Call SetPipelineFactory to enable parallel detection.
func NewBatchAnalyzer(
	p *pipeline.Pipeline,
	store *sqlite.Store,
	parserFactory func() FrameParser,
	workers int,
	logger *slog.Logger,
) *BatchAnalyzer {
	if workers < 1 {
		workers = 1
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &BatchAnalyzer{
		pipeline: p,
		store:    store,
		parser:   parserFactory,
		workers:  workers,
		logger:   logger,
	}
}

// SetPipelineFactory supplies a constructor for per-worker pipelines so
// detection runs in parallel. Each pipeline must own its detectors and scorer.
func (ba *BatchAnalyzer) SetPipelineFactory(f func() *pipeline.Pipeline) {
	ba.pipelineFactory = f
}

// SetForce controls whether matches that already exist in the store are
// re-analyzed (their derived events/scores replaced) instead of skipped.
func (ba *BatchAnalyzer) SetForce(force bool) {
	ba.force = force
}

// parsedMatch is a replay that has been read and analyzed by a worker and is
// waiting for the single writer goroutine.
type parsedMatch struct {
	path       string
	matchCtx   *model.MatchContext
	frames     []model.PlayerTelemetryFrame
	rawByFrame map[int]string
	result     *pipeline.MatchResult
	replaced   bool // an existing match was cleared because force is set
}

// AnalyzeDirectory processes all replay files in a directory.
func (ba *BatchAnalyzer) AnalyzeDirectory(ctx context.Context, dir string) (*BatchResult, error) {
	start := time.Now()
	result := &BatchResult{}

	files, ignored, err := ba.findReplayFiles(dir)
	if err != nil {
		return nil, fmt.Errorf("walking directory: %w", err)
	}
	result.TotalFiles = len(files)
	result.IgnoredFiles = ignored
	if len(files) == 0 {
		result.Duration = time.Since(start)
		return result, nil
	}

	fileCh := make(chan string, len(files))
	for _, f := range files {
		fileCh <- f
	}
	close(fileCh)

	var statsMu sync.Mutex
	seenMatches := make(map[string]string) // match_id -> first file path

	writeCh := make(chan parsedMatch, ba.workers)
	var writerWg sync.WaitGroup
	writerWg.Add(1)
	go func() {
		defer writerWg.Done()
		for pm := range writeCh {
			if err := ba.persist(ctx, pm, result, &statsMu); err != nil {
				ba.logger.Error("failed to persist match", "match_id", pm.matchCtx.MatchID, "path", pm.path, "error", err)
				statsMu.Lock()
				result.Errors++
				result.PersistFailed++
				statsMu.Unlock()
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < ba.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := ba.pipeline
			if ba.pipelineFactory != nil {
				p = ba.pipelineFactory()
			}
			for path := range fileCh {
				select {
				case <-ctx.Done():
					return
				default:
				}
				pm, skipped, err := ba.analyzeFile(ctx, p, path, seenMatches, &statsMu)
				statsMu.Lock()
				switch {
				case err != nil:
					result.Errors++
					ba.logger.Warn("replay analysis failed", "path", path, "error", err)
				case skipped:
					result.Skipped++
				}
				statsMu.Unlock()
				if err == nil && !skipped {
					writeCh <- pm
				}
			}
		}()
	}

	wg.Wait()
	close(writeCh)
	writerWg.Wait()

	sort.Strings(result.FlaggedPlayers)
	result.Duration = time.Since(start)
	return result, nil
}

// findReplayFiles walks dir and returns replay files in deterministic order.
// .echoreplay is matched case-insensitively. A .json file is only accepted
// when it looks like a legacy JSON replay (has "header" and "frames" keys);
// bridge dumps, config exports and evidence bundles are ignored and counted.
func (ba *BatchAnalyzer) findReplayFiles(dir string) ([]string, int, error) {
	var files []string
	ignored := 0
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".echoreplay":
			files = append(files, path)
		case ".json":
			if looksLikeLegacyReplay(path) {
				files = append(files, path)
			} else {
				ignored++
				ba.logger.Debug("ignoring non-replay json", "path", path)
			}
		}
		return nil
	})
	sort.Strings(files)
	return files, ignored, err
}

// looksLikeLegacyReplay sniffs the first bytes of a .json file for the legacy
// replay envelope ({"header": ..., "frames": [...]}).
func looksLikeLegacyReplay(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 64*1024)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	return bytes.Contains(head, []byte(`"header"`)) && bytes.Contains(head, []byte(`"frames"`))
}

// analyzeFile parses and analyzes one replay. It returns skipped=true for a
// match already handled in this run or already present in the store (unless
// force is set). The store is only read here; writes happen in persist.
func (ba *BatchAnalyzer) analyzeFile(ctx context.Context, p *pipeline.Pipeline, path string, seenMatches map[string]string, mu *sync.Mutex) (parsedMatch, bool, error) {
	var pm parsedMatch
	pm.path = path

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".echoreplay" {
		// Stream the replay: frames are accumulated once (ProcessMatch needs
		// the whole match) and the raw payload is kept once per tick, keyed
		// by frame index, so no second copy of the raw strings exists.
		parser := adapter.NewEchoReplayParser()
		if ba.physics != (model.PhysicsConstants{}) {
			parser.SetPhysics(ba.physics)
		}
		pm.rawByFrame = make(map[int]string)
		var lastSample time.Time
		matchCtx, _, err := parser.ParseFileStream(path, func(tick *adapter.ParsedTick) error {
			if _, seen := pm.rawByFrame[tick.FrameIndex]; !seen {
				pm.rawByFrame[tick.FrameIndex] = tick.RawJSON
			}
			pm.frames = append(pm.frames, tick.Frames...)
			lastSample = tick.SampleTime
			return nil
		})
		if err != nil {
			return pm, false, err
		}
		if matchCtx != nil && matchCtx.Duration == 0 && !lastSample.IsZero() && !matchCtx.StartTime.IsZero() {
			// Match duration is the real span of the recording (first to last sample).
			matchCtx.Duration = lastSample.Sub(matchCtx.StartTime)
		}
		pm.matchCtx = matchCtx
	} else {
		reader := NewReplayReader(path, ba.parser())
		if ba.physics != (model.PhysicsConstants{}) {
			reader.SetPhysics(ba.physics)
		}
		matchCtx, frames, err := reader.ReadMatch()
		if err != nil {
			return pm, false, err
		}
		pm.matchCtx, pm.frames = matchCtx, frames
	}
	if pm.matchCtx == nil || pm.matchCtx.MatchID == "" {
		return pm, false, fmt.Errorf("replay has no match id")
	}

	// Duplicate same-match suppression within this run.
	mu.Lock()
	firstFile, seen := seenMatches[pm.matchCtx.MatchID]
	if !seen {
		seenMatches[pm.matchCtx.MatchID] = path
	}
	mu.Unlock()
	if seen {
		ba.logger.Info("skipping duplicate match in batch",
			"match_id", pm.matchCtx.MatchID, "skipped_file", path, "first_file", firstFile)
		return pm, true, nil
	}

	// Already-stored suppression across runs (idempotent re-ingest).
	exists, err := ba.store.HasMatch(ctx, pm.matchCtx.MatchID)
	if err != nil {
		return pm, false, fmt.Errorf("checking store: %w", err)
	}
	if exists {
		if !ba.force {
			ba.logger.Info("skipping match already in store (use --force to re-analyze)",
				"match_id", pm.matchCtx.MatchID, "file", path)
			return pm, true, nil
		}
		pm.replaced = true
	}

	if ba.pipelineFactory == nil {
		ba.pipelineMu.Lock()
		defer ba.pipelineMu.Unlock()
	}
	res, err := p.ProcessMatch(ctx, pm.matchCtx, pm.frames)
	if err != nil {
		return pm, false, err
	}
	pm.result = res
	return pm, false, nil
}

// persist writes one analyzed match: source telemetry, context, then derived
// events and per-match score snapshots. Runs only on the writer goroutine.
// Any store failure is returned (and the match is not counted as processed):
// a silent failure here would let `batch` report success for a run that
// stored nothing. Telemetry and context are written before the derived
// outputs, so a failure part-way leaves source data that reprocess-match can
// analyze again; the partial match is still reported as failed.
func (ba *BatchAnalyzer) persist(ctx context.Context, pm parsedMatch, result *BatchResult, mu *sync.Mutex) error {
	matchID := pm.matchCtx.MatchID
	source := "initial"
	if pm.replaced {
		source = "reprocess"
	}

	tel, err := ba.store.StoreTelemetryFramesWithRaw(ctx, matchID, pm.frames, pm.rawByFrame)
	if err != nil {
		return fmt.Errorf("storing telemetry: %w", err)
	}
	if err := ba.store.StoreMatchContext(ctx, pm.matchCtx, len(pm.frames)); err != nil {
		return fmt.Errorf("storing match context: %w", err)
	}
	if pm.replaced {
		// The previous derived outputs are only cleared once the replacement
		// is fully analyzed and its source data is stored, immediately before
		// the new outputs are written.
		ev, sc, err := ba.store.DeleteMatchAnalysis(ctx, matchID)
		if err != nil {
			return fmt.Errorf("clearing previous analysis: %w", err)
		}
		if ev > 0 || sc > 0 {
			ba.logger.Info("replaced previous analysis", "match_id", matchID, "events_deleted", ev, "scores_deleted", sc)
		}
	}
	opts := ba.opts
	if opts.Logger == nil {
		opts.Logger = ba.logger
	}
	stored, err := StoreMatchAnalysis(ctx, ba.store, pm.matchCtx, pm.result, source, opts)

	mu.Lock()
	result.FramesInserted += tel.Inserted
	result.FramesIgnored += tel.Ignored
	result.EventsStored += stored.EventsStored
	if err == nil {
		result.Processed++
		result.FlaggedPlayers = append(result.FlaggedPlayers, stored.FlaggedPlayers...)
	}
	mu.Unlock()
	if err != nil {
		return fmt.Errorf("storing analysis: %w", err)
	}
	return nil
}

// StoredAnalysis reports what StoreMatchAnalysis wrote.
type StoredAnalysis struct {
	EventsStored   int
	ScoresStored   int
	CasesStored    int
	CasesClosed    int      // stale pending cases of this match closed (see sqlite.CloseStaleReviewCases)
	FlaggedPlayers []string // sorted
}

// AnalysisOptions carries what StoreMatchAnalysis needs beyond the result.
type AnalysisOptions struct {
	// Levels is the tier table review cases are classified with. Pass the
	// scorer's table (scoring.ScorerConfig.EffectiveLevels()) so a case is
	// created exactly when the scorer says ExceedsReview; a zero table means
	// model.DefaultLevelTable().
	Levels model.LevelTable
	// DetectorNames maps detector ID -> human-readable name for case evidence.
	DetectorNames map[string]string
	// Logger receives case-creation log lines (nil = slog.Default()).
	Logger *slog.Logger
}

// StoreMatchAnalysis persists a match's derived outputs: detection events
// (tagged with source "initial" or "reprocess"), one per-match score snapshot
// per scored player, and the single-match review cases produced by
// review.CreateCasesFromResult (also written to result.ReviewCases).
// Telemetry and context are stored by the caller. Players are processed in
// sorted order for reproducibility.
//
// Cases are upserted under their deterministic ids, so a re-analysis
// refreshes the cases of players still flagged and keeps any moderator
// status. Pending cases of this match whose player is no longer flagged are
// then closed with a close_reason (never deleted) so `flagged` and
// calibration do not carry a case whose events no longer exist; a later
// analysis that flags the player again reopens the case.
//
// Score snapshots are only written for players that actually scored
// (TotalScore > 0 with at least one event), matching the live path: a
// zero row would let GetPlayerScore regress a real score to 0 and shadow-only
// players leave no trace either way.
func StoreMatchAnalysis(ctx context.Context, store *sqlite.Store, matchCtx *model.MatchContext, result *pipeline.MatchResult, source string, opts AnalysisOptions) (StoredAnalysis, error) {
	var out StoredAnalysis
	stored, err := store.StoreDetectionEvents(ctx, result.DetectionEvents, source)
	out.EventsStored = stored
	if err != nil {
		return out, fmt.Errorf("storing events: %w", err)
	}
	pids := make([]string, 0, len(result.PlayerScores))
	for pid := range result.PlayerScores {
		pids = append(pids, pid)
	}
	sort.Strings(pids)
	for _, pid := range pids {
		score := result.PlayerScores[pid]
		if score.TotalScore <= 0 || score.EventCount == 0 {
			continue
		}
		if err := store.StoreMatchSuspicionScore(ctx, matchCtx.MatchID, score); err != nil {
			return out, fmt.Errorf("storing score for %s: %w", pid, err)
		}
		out.ScoresStored++
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cases, caseErr := review.CreateCasesFromResult(ctx, store, matchCtx, result, opts.Levels,
		review.WithDetectorNames(opts.DetectorNames), review.WithLogger(logger))
	result.ReviewCases = cases
	out.CasesStored = len(cases)
	for _, rc := range cases {
		out.FlaggedPlayers = append(out.FlaggedPlayers, rc.PlayerID)
	}
	sort.Strings(out.FlaggedPlayers)
	if caseErr != nil {
		// A case that failed to store keeps its old row; closing it as stale
		// would hide the failure, so leave every case alone.
		return out, fmt.Errorf("storing review cases: %w", caseErr)
	}
	closed, err := store.CloseStaleReviewCases(ctx, matchCtx.MatchID, out.FlaggedPlayers,
		fmt.Sprintf("player no longer reaches the review tier after %s analysis", source))
	if err != nil {
		return out, fmt.Errorf("closing stale review cases: %w", err)
	}
	out.CasesClosed = int(closed)
	if closed > 0 {
		logger.Info("closed stale review cases", "match_id", matchCtx.MatchID, "closed", closed, "source", source)
	}
	return out, nil
}
