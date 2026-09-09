package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/catalog"
	"github.com/nevr-anticheat/nevr-anticheat/internal/logging"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// Engine wires the config-derived pieces every offline analysis needs (the
// physics, scorer, level table, detector set and store) so the CLI, the
// desktop app and tests build pipelines and store results the same way.
type Engine struct {
	cfg    *config.Config
	store  *sqlite.Store
	hp     *sqlite.StoreHistoryProvider
	logger *slog.Logger
}

// NewEngine binds a validated config to an open store.
func NewEngine(cfg *config.Config, store *sqlite.Store) *Engine {
	return &Engine{
		cfg:    cfg,
		store:  store,
		hp:     sqlite.NewStoreHistoryProvider(store),
		logger: logging.NewLogger(cfg.General.LogLevel, cfg.General.LogFormat),
	}
}

// Config returns the config the engine was built from.
func (e *Engine) Config() *config.Config { return e.cfg }

// Store returns the store the engine writes to.
func (e *Engine) Store() *sqlite.Store { return e.store }

// Logger returns the engine's logger (config log level and format).
func (e *Engine) Logger() *slog.Logger { return e.logger }

// ScorerConfig is the single place the [scoring] block is turned into scorer
// parameters. Levels carries the configured tier keys with review_threshold
// already mapped onto high_risk (config.ScoringConfig.LevelTable), so the
// scorer, review cases, cross-match severity and CLI output all classify
// with the same table.
func (e *Engine) ScorerConfig() scoring.ScorerConfig {
	cfg := e.cfg
	return scoring.ScorerConfig{
		MaxSingleContribution:         cfg.Scoring.MaxSingleContribution,
		MaxContribPerDetectorPerMatch: cfg.Scoring.MaxContribPerDetectorPerMatch,
		SameCategoryDiminishing:       cfg.Scoring.SameCategoryDiminishing,
		AutoEnforceThreshold:          cfg.Scoring.AutoEnforceThreshold,
		DecayHalfLifeHours:            cfg.Scoring.DecayHalfLifeHours,
		CooldownFrames:                cfg.Pipeline.CooldownFrames,
		CorrelationBonusCap:           cfg.Scoring.CorrelationBonusCap,
		Levels:                        cfg.Scoring.LevelTable(),
	}
}

// Levels is the tier table shared by the in-match scorer, single-match review
// cases, cross-match aggregation and CLI output (contract 7: one source of
// truth, review_threshold mapped onto high_risk).
func (e *Engine) Levels() model.LevelTable {
	return e.cfg.Scoring.LevelTable()
}

// Physics is the match physics built from the [physics] block.
func (e *Engine) Physics() model.PhysicsConstants {
	return e.cfg.Physics.Constants()
}

// AnalysisOptions is what StoreMatchAnalysis needs to write cases the way
// the scorer scored them.
func (e *Engine) AnalysisOptions() AnalysisOptions {
	return AnalysisOptions{
		Levels:        e.Levels(),
		DetectorNames: catalog.Names(),
		Logger:        e.logger,
	}
}

// NewPipeline builds a fresh pipeline (own detectors, own scorer). Pipelines
// are not goroutine-safe; build one per worker.
func (e *Engine) NewPipeline() *pipeline.Pipeline {
	detectors := catalog.Build(e.cfg, e.hp)
	scorer := scoring.NewSuspicionScorer(e.ScorerConfig())
	return pipeline.NewPipeline(e.cfg, detectors, scorer, e.logger)
}

// CrossMatchConfig is the [scoring] block as cross-match aggregation reads it.
func (e *Engine) CrossMatchConfig() sqlite.CrossMatchConfig {
	return sqlite.CrossMatchConfig{
		DecayHalfLifeHours:            e.cfg.Scoring.DecayHalfLifeHours,
		MaxSingleContribution:         e.cfg.Scoring.MaxSingleContribution,
		MaxContribPerDetectorPerMatch: e.cfg.Scoring.MaxContribPerDetectorPerMatch,
		SameCategoryDiminishing:       e.cfg.Scoring.SameCategoryDiminishing,
		CooldownFrames:                e.cfg.Pipeline.CooldownFrames,
		CorrelationBonusCap:           e.cfg.Scoring.CorrelationBonusCap,
		Levels:                        e.Levels(),
	}
}

// AnalyzeFile runs AnalyzeFile with the engine's pipeline, physics and
// analysis options. force re-analyzes a match that is already stored.
func (e *Engine) AnalyzeFile(ctx context.Context, path string, force bool) (*AnalyzeResult, error) {
	return AnalyzeFile(ctx, e.store, path, e.analyzeOptions(force))
}

// AnalyzeFileAll runs AnalyzeFileAll with the engine's pipeline, physics and
// analysis options: one result per match the replay holds. force
// re-analyzes the matches that are already stored.
func (e *Engine) AnalyzeFileAll(ctx context.Context, path string, force bool) ([]*AnalyzeResult, error) {
	return AnalyzeFileAll(ctx, e.store, path, e.analyzeOptions(force))
}

func (e *Engine) analyzeOptions(force bool) AnalyzeOptions {
	return AnalyzeOptions{
		Force:       force,
		Physics:     e.Physics(),
		NewPipeline: e.NewPipeline,
		Analysis:    e.AnalysisOptions(),
	}
}

// ErrMatchAlreadyStored is matched (errors.Is) by the error AnalyzeFile
// returns when the replay's match is already in the store and Force is not
// set. The concrete value is a *MatchStoredError carrying the match id.
var ErrMatchAlreadyStored = errors.New("match already stored")

// MatchStoredError reports which stored match a replay was refused for.
type MatchStoredError struct {
	MatchID string
}

func (e *MatchStoredError) Error() string {
	return "match " + e.MatchID + " already stored"
}

// Is makes errors.Is(err, ErrMatchAlreadyStored) true.
func (e *MatchStoredError) Is(target error) bool { return target == ErrMatchAlreadyStored }

// rawTickFlushEvery bounds how many raw session payloads an analysis keeps in
// memory before writing them to match_ticks.
const rawTickFlushEvery = 500

// AnalyzeOptions configures AnalyzeFileAll and AnalyzeFile.
type AnalyzeOptions struct {
	// Force re-analyzes a match that is already stored: the new outputs are
	// tagged source "reprocess" and the previous detection events and score
	// snapshots are cleared only after the match parsed completely, the
	// pipeline ran and the source data (telemetry, context) was stored,
	// immediately before the new outputs are written, so a truncated or
	// corrupt replay never destroys the analysis it was meant to replace.
	// Without it a stored match is refused before anything is written
	// (AnalyzeResult.AlreadyStored; ErrMatchAlreadyStored from AnalyzeFile).
	Force bool
	// Physics is stamped on the parsed match context (zero = model.DefaultPhysics).
	Physics model.PhysicsConstants
	// NewPipeline builds the pipeline each match is processed with (required).
	NewPipeline func() *pipeline.Pipeline
	// Analysis is what StoreMatchAnalysis needs (level table, detector names, logger).
	Analysis AnalysisOptions
}

// FrameSummary is what AnalyzeFileAll observed in a match's parsed frames,
// for reports that no longer have the frames in hand.
type FrameSummary struct {
	// FramesByPlayer counts the parsed player-frames per player id.
	FramesByPlayer map[string]int
	// FirstTimestamp and LastTimestamp are the match-relative seconds of the
	// first and last parsed frame.
	FirstTimestamp float64
	LastTimestamp  float64
	// BlueScore and OrangeScore are the team scores carried by the last
	// parsed frame; HasScore is false when no frame carried a score.
	BlueScore   int
	OrangeScore int
	HasScore    bool
}

// AnalyzeResult is what AnalyzeFileAll parsed, detected and stored for one
// match of a replay. Storage failures after detection are not returned as
// errors; they are reported in the *Err fields (Warnings renders them,
// PersistError joins them for callers that treat a partially stored analysis
// as a failure).
type AnalyzeResult struct {
	// Path is the replay that was analyzed.
	Path string
	// MatchCtx is the parsed match context (as stored).
	MatchCtx *model.MatchContext
	// AlreadyStored is true when the match was already in the store and
	// Force was not set: it was refused before anything of it was kept or
	// written, and only Path, MatchCtx and Diagnostics are set. (AnalyzeFile
	// reports this for its single match as ErrMatchAlreadyStored instead.)
	AlreadyStored bool
	// Frames is the number of player-frames parsed for the match.
	Frames int
	// Diagnostics is the adapter's mapping report for the whole file (it is
	// the same report on every match of a multi-session recording); nil for
	// a legacy JSON replay.
	Diagnostics *adapter.DiagnosticReport
	// Replaced is true when the match was already stored and Force cleared
	// its previous analysis (ClearedEvents / ClearedScores rows). It stays
	// false when the replacement was refused because the source data could
	// not be stored (AnalysisErr says so; the previous analysis is intact).
	Replaced      bool
	ClearedEvents int64
	ClearedScores int64
	// Result is the pipeline's output; Result.ReviewCases holds the cases
	// StoreMatchAnalysis created.
	Result *pipeline.MatchResult
	// Stored reports what StoreMatchAnalysis wrote (including the stale
	// pending cases it closed, CasesClosed).
	Stored StoredAnalysis
	// Telemetry reports the telemetry frame / raw tick writes. RawStored is
	// true when raw ticks were written (.echoreplay sources).
	Telemetry sqlite.TelemetryStoreResult
	RawStored bool
	// Summary is what the parsed frames showed.
	Summary FrameSummary
	// MatchSummary is the moderator-facing match summary (final player
	// stats, scoring timeline, throw log, suspicion) built from the parsed
	// snapshots; nil for legacy JSON replays or refused matches.
	MatchSummary *MatchSummary
	// SummaryErr is a non-fatal failure to persist MatchSummary.
	SummaryErr error
	// AdditionalMatches is set by AnalyzeFile only: the results of the
	// further matches a multi-session recording holds, in file order.
	// AnalyzeFileAll returns every match in one slice instead.
	AdditionalMatches []*AnalyzeResult

	// Non-fatal storage failures, in the order they happened.
	RawTickErr   error // writing raw ticks during parsing
	TelemetryErr error // storing telemetry frames
	ContextErr   error // storing the match context
	AnalysisErr  error // storing (or, after a source-data failure, skipping) events, scores and review cases
}

// Warnings renders the non-fatal storage failures as messages.
func (r *AnalyzeResult) Warnings() []string {
	var out []string
	if r.RawTickErr != nil {
		out = append(out, "failed to store raw ticks: "+r.RawTickErr.Error())
	}
	if r.TelemetryErr != nil {
		out = append(out, "failed to store telemetry: "+r.TelemetryErr.Error())
	}
	if r.ContextErr != nil {
		out = append(out, "failed to store match context: "+r.ContextErr.Error())
	}
	if r.AnalysisErr != nil {
		out = append(out, r.AnalysisErr.Error())
	}
	if r.SummaryErr != nil {
		out = append(out, "failed to store match summary: "+r.SummaryErr.Error())
	}
	return out
}

// PersistError joins the non-fatal storage failures into one error, nil when
// everything was stored. Callers for which an analysis only counts once it
// is fully persisted (the CLI exits non-zero) check it; Warnings renders the
// same failures as messages for callers that show them and carry on.
func (r *AnalyzeResult) PersistError() error {
	var errs []error
	if r.RawTickErr != nil {
		errs = append(errs, fmt.Errorf("storing raw ticks: %w", r.RawTickErr))
	}
	if r.TelemetryErr != nil {
		errs = append(errs, fmt.Errorf("storing telemetry: %w", r.TelemetryErr))
	}
	if r.ContextErr != nil {
		errs = append(errs, fmt.Errorf("storing match context: %w", r.ContextErr))
	}
	if r.AnalysisErr != nil {
		errs = append(errs, r.AnalysisErr)
	}
	if r.SummaryErr != nil {
		errs = append(errs, fmt.Errorf("storing match summary: %w", r.SummaryErr))
	}
	return errors.Join(errs...)
}

// IsEchoReplay reports whether the path has the .echoreplay extension.
func IsEchoReplay(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".echoreplay"
}

// IsSessionRecording selects the streaming session parser. Native .tape files
// are decoded directly, never passed through the lossy Echo JSON converter.
// The parser still validates the actual bytes; an extension is not validation.
func IsSessionRecording(path string) bool {
	return IsEchoReplay(path) || strings.EqualFold(filepath.Ext(path), ".tape")
}

// AnalyzeFile is AnalyzeFileAll for callers that expect one match per
// replay: it returns the first match's result, with the results of any
// further matches the recording holds (a rematch in the same lobby; they
// are analyzed and stored all the same) in AdditionalMatches, and reports a
// first match that is already stored as ErrMatchAlreadyStored (a
// *MatchStoredError) instead of a result. Any error AnalyzeFileAll returns
// is returned with a nil result.
func AnalyzeFile(ctx context.Context, store *sqlite.Store, path string, opts AnalyzeOptions) (*AnalyzeResult, error) {
	results, err := AnalyzeFileAll(ctx, store, path, opts)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, errors.New("replay holds no match")
	}
	first := results[0]
	if first.AlreadyStored {
		return nil, &MatchStoredError{MatchID: first.MatchCtx.MatchID}
	}
	if len(results) > 1 {
		first.AdditionalMatches = results[1:]
	}
	return first, nil
}

// AnalyzeFileAll is the analyze command: it parses one replay (.echoreplay
// streamed with its raw ticks written to match_ticks in bounded chunks, or a
// legacy JSON replay) and analyzes every match it holds, one result per
// match in file order. A recording normally holds one match; a session-id
// change mid-file (a rematch in the same lobby) starts another, which the
// adapter delivers with its own context, time base and frame index
// (adapter.ParsedTick) and which is finished and stored as a match of its
// own. Appending it to the first match instead would restart its frame
// indices at 0 under the first match's id, so its ticks would be dropped as
// duplicates and its context and detections never stored.
//
// Each match is checked against the store when its first tick arrives: a
// match that is already stored is refused unless opts.Force and reported as
// a result with AlreadyStored set (nothing of it is kept or written; the
// rest of the file is still read so later matches are analyzed). Otherwise,
// once its last tick has been read, the match runs through a fresh pipeline
// and its telemetry, match context and derived outputs (events, score
// snapshots, review cases) are persisted via StoreMatchAnalysis, which also
// closes the pending cases of players the new analysis no longer flags.
//
// With opts.Force on a stored match the previous events and score snapshots
// are cleared only after the match parsed completely, the pipeline ran and
// the telemetry and context were stored, immediately before the new outputs
// are written: a corrupt replay, a pipeline failure or an unwritable store
// leaves the previous analysis exactly as it was.
//
// Errors before a match's detection (unreadable file, no match id, pipeline
// failure) stop the analysis; they are returned together with the results
// of the matches finished (or refused) before them, so nothing that was
// stored goes unreported. Storage failures after detection are reported on
// the result, never returned; when the telemetry or context could not be
// stored the derived outputs are not written either (a match without its
// context is not recognised as stored, so a re-run would duplicate its
// events) and AnalysisErr says so.
func AnalyzeFileAll(ctx context.Context, store *sqlite.Store, path string, opts AnalyzeOptions) ([]*AnalyzeResult, error) {
	if opts.NewPipeline == nil {
		return nil, errors.New("AnalyzeFile: NewPipeline is required")
	}
	a := &fileAnalysis{ctx: ctx, store: store, path: path, opts: opts}

	if !IsSessionRecording(path) {
		reader := NewReplayReader(path, NewJSONFrameParser())
		reader.SetPhysics(opts.Physics)
		mc, frames, err := reader.ReadMatch()
		if err != nil {
			return nil, fmt.Errorf("reading replay: %w", err)
		}
		if mc == nil || mc.MatchID == "" {
			return nil, errors.New("replay has no match id")
		}
		run, err := a.begin(mc)
		if err != nil {
			return nil, err
		}
		run.frames = frames
		return a.results, a.finish(run)
	}

	// Stream the replay so raw profiler payloads are staged in bounded chunks.
	// The spool is copied to canonical storage only after a complete match parses;
	// a truncated/corrupt match therefore cannot seed partial immutable ticks.
	// Frames are still accumulated because ProcessMatch needs the whole match.
	parser := adapter.NewEchoReplayParser()
	if opts.Physics != (model.PhysicsConstants{}) {
		parser.SetPhysics(opts.Physics)
	}
	var run *matchRun
	diag, err := parseReplayMatches(parser, path,
		func(tick *adapter.ParsedTick, first bool) error {
			if first {
				if tick.MatchID == "" {
					return errors.New("replay has no match id")
				}
				var err error
				if run, err = a.begin(tick.MatchCtx); err != nil {
					return err
				}
			}
			return run.add(a, tick)
		},
		func(*model.MatchContext) error { return a.finish(run) })
	for _, res := range a.results {
		res.Diagnostics = diag
	}
	if err != nil && run != nil {
		run.abortRaw()
	}
	return a.results, err
}

// fileAnalysis is one AnalyzeFileAll call: its store and options, and the
// results of the matches decided so far (analyzed, or refused as stored).
type fileAnalysis struct {
	ctx     context.Context
	store   *sqlite.Store
	path    string
	opts    AnalyzeOptions
	results []*AnalyzeResult
}

// matchRun is the match being accumulated: its frames, raw ticks waiting in a
// bounded memory buffer/on-disk spool, and how its outputs will be stored.
type matchRun struct {
	res      *AnalyzeResult
	frames   []model.PlayerTelemetryFrame
	pending  map[int]string
	rawSpool *rawTickSpool
	source   string // "initial", or "reprocess" when replacing a stored match
	replace  bool
	summary  *SummaryBuilder
	start    time.Time // first sample time, the zero of the summary's clock
}

// begin starts a match once its id is known: a stored match is refused
// (AlreadyStored, and it counts as decided) unless Force, in which case it
// is marked for replacement. The previous analysis is not touched here; see
// finish.
func (a *fileAnalysis) begin(mc *model.MatchContext) (*matchRun, error) {
	run := &matchRun{
		res:     &AnalyzeResult{Path: a.path, MatchCtx: mc},
		pending: make(map[int]string),
		source:  "initial",
		summary: NewSummaryBuilder(mc),
	}
	exists, err := a.store.HasMatch(a.ctx, mc.MatchID)
	if err != nil {
		return nil, err
	}
	switch {
	case !exists:
	case a.opts.Force:
		run.source, run.replace = "reprocess", true
	default:
		run.res.AlreadyStored = true
		a.results = append(a.results, run.res)
	}
	return run, nil
}

// add keeps a tick's frames and raw payload (once per frame index), spooling
// raw payloads every rawTickFlushEvery ticks. Nothing of a refused match is
// kept, and canonical match_ticks remain untouched until parsing succeeds.
func (r *matchRun) add(a *fileAnalysis, tick *adapter.ParsedTick) error {
	if err := a.ctx.Err(); err != nil {
		return err
	}
	if r.res.AlreadyStored {
		return nil
	}
	if _, seen := r.pending[tick.FrameIndex]; !seen {
		r.pending[tick.FrameIndex] = tick.RawJSON
	}
	r.frames = append(r.frames, tick.Frames...)
	if tick.Session != nil && r.summary != nil {
		if r.start.IsZero() {
			r.start = tick.SampleTime
		}
		base := r.start
		if tick.MatchCtx.Source == "tape" {
			base = tick.MatchCtx.StartTime
		}
		r.summary.Add(tick.Session, tick.FrameIndex, tick.SampleTime.Sub(base).Seconds())
	}
	if len(r.pending) >= rawTickFlushEvery {
		return r.flush(a)
	}
	return nil
}

// flush stages pending raw payloads in a temporary spool. Canonical match_ticks
// are untouched until the complete match has parsed.
func (r *matchRun) flush(a *fileAnalysis) error {
	if len(r.pending) == 0 {
		return nil
	}
	if r.rawSpool == nil {
		var err error
		r.rawSpool, err = newRawTickSpool()
		if err != nil {
			return err
		}
	}
	if err := r.rawSpool.write(r.pending); err != nil {
		return err
	}
	r.pending = make(map[int]string)
	return nil
}

func (r *matchRun) finishRaw(a *fileAnalysis) error {
	if err := r.flush(a); err != nil {
		r.abortRaw()
		return err
	}
	if r.rawSpool == nil {
		return nil
	}
	spool := r.rawSpool
	r.rawSpool = nil
	defer spool.discard()
	tel, err := spool.store(a.ctx, a.store, r.res.MatchCtx.MatchID)
	if err != nil {
		r.res.Telemetry.TicksInserted = tel.TicksInserted
		r.res.Telemetry.TicksIgnored = tel.TicksIgnored
		return err
	}
	r.res.Telemetry.TicksInserted = tel.TicksInserted
	r.res.Telemetry.TicksIgnored = tel.TicksIgnored
	r.res.RawStored = r.res.Telemetry.TicksInserted+r.res.Telemetry.TicksIgnored > 0
	return nil
}

func (r *matchRun) abortRaw() {
	if r == nil || r.rawSpool == nil {
		return
	}
	r.rawSpool.discard()
	r.rawSpool = nil
	r.res.Telemetry.TicksInserted = 0
	r.res.Telemetry.TicksIgnored = 0
}

// finish runs a complete match through a fresh pipeline and persists it:
// the last raw ticks, then telemetry frames and match context (so
// reprocessing doesn't need replay files), then, with Force after clearing
// the previous analysis, the derived outputs. A pipeline failure is
// returned; storage failures are reported on the result, which counts as
// decided once the pipeline ran.
func (a *fileAnalysis) finish(run *matchRun) error {
	if run.res.AlreadyStored {
		return nil
	}
	res := run.res
	if err := run.flush(a); err != nil {
		run.abortRaw()
		return fmt.Errorf("stage original replay evidence: %w", err)
	}
	var next rawTickNext
	if run.rawSpool != nil {
		var err error
		next, err = run.rawSpool.iterator()
		if err != nil {
			run.abortRaw()
			return err
		}
	}
	if err := validateNativeSource(a.ctx, a.store, res.MatchCtx, next); err != nil {
		run.abortRaw()
		return err
	}
	if err := run.finishRaw(a); err != nil {
		res.RawTickErr = err
	}
	res.Frames = len(run.frames)
	res.Summary = summarizeFrames(run.frames)

	result, err := a.opts.NewPipeline().ProcessMatch(a.ctx, res.MatchCtx, run.frames)
	if err != nil {
		return err
	}
	res.Result = result
	a.results = append(a.results, res)
	if res.RawTickErr != nil {
		// Keep the existing result/error reporting contract, but do not publish
		// the computed result or replace any context/normalized/derived data.
		// A partial raw prefix is retriable only with matching source records.
		res.TelemetryErr = fmt.Errorf("normalized telemetry not stored: original replay evidence failed: %w", res.RawTickErr)
		if run.replace {
			res.AnalysisErr = errors.New("previous analysis kept: original replay evidence could not be stored")
		} else {
			res.AnalysisErr = errors.New("derived outputs not stored: original replay evidence could not be stored")
		}
		return nil
	}

	ctx, store := a.ctx, a.store
	if run.replace {
		// A forced replay analysis is also a mapper/schema refresh. Keeping the
		// old normalized rows here would make a later database-only reprocess
		// resurrect stale possession, hand or velocity mappings even though this
		// run used the corrected frames. Raw match_ticks remain immutable.
		n, err := store.ReplaceMatchTelemetryFrames(ctx, res.MatchCtx.MatchID, run.frames)
		if err != nil {
			res.TelemetryErr = err
		} else {
			res.Telemetry.Inserted = n
		}
	} else {
		fr, err := store.StoreTelemetryFramesWithRaw(ctx, res.MatchCtx.MatchID, run.frames, nil)
		if err != nil {
			res.TelemetryErr = err
		} else {
			res.Telemetry.Inserted, res.Telemetry.Ignored = fr.Inserted, fr.Ignored
		}
	}
	if err := store.StoreMatchContext(ctx, res.MatchCtx, len(run.frames)); err != nil {
		res.ContextErr = err
	}
	if res.TelemetryErr != nil || res.ContextErr != nil {
		// Source data first, derived outputs second (as batch does): without
		// its telemetry and context the match is not recognised as stored,
		// so a re-run would duplicate any events written now. With Force the
		// previous analysis is kept intact.
		if run.replace {
			res.AnalysisErr = errors.New("previous analysis kept: the replacement's telemetry or match context could not be stored")
		} else {
			res.AnalysisErr = errors.New("derived outputs not stored: telemetry or match context could not be stored")
		}
		return nil
	}
	if run.replace {
		res.Stored, res.AnalysisErr = ReplaceMatchAnalysis(ctx, store, res.MatchCtx, result, run.source, a.opts.Analysis)
		if res.AnalysisErr == nil {
			res.Replaced = true
			res.ClearedEvents = res.Stored.ClearedEvents
			res.ClearedScores = res.Stored.ClearedScores
		}
	} else {
		res.Stored, res.AnalysisErr = StoreMatchAnalysis(ctx, store, res.MatchCtx, result, run.source, a.opts.Analysis)
	}
	if run.summary != nil {
		sum := run.summary.Finish()
		sum.ApplySuspicion(res.MatchCtx, result.PlayerScores, result.DetectionEvents, a.opts.Analysis.Levels)
		sum.ApplyCoverage(result.PlayerCoverage, result.DetectionEvents)
		res.MatchSummary = sum
		if res.AnalysisErr == nil {
			if doc, err := json.Marshal(sum); err != nil {
				res.SummaryErr = err
			} else if err := store.StoreMatchSummaryJSON(ctx, sum.Meta(), doc); err != nil {
				res.SummaryErr = err
			}
		}
	}
	return nil
}

// parseReplayMatches streams the .echoreplay at path through parser and
// reports its matches one at a time: tick is called with every tick in file
// order (first is true on the first tick of each match: the file's first,
// or the first after a session-id change), end once a match's last tick has
// been read, with its context (Duration set to the recorded span, first to
// last sample, when the adapter left it zero). A session id that comes back
// after another session's is refused: the adapter would restart its frame
// index and the ticks would collide with the match already stored under
// that id. Parser errors are wrapped as "reading echoreplay"; callback
// errors are returned as they are. Either stops the parse.
func parseReplayMatches(parser *adapter.EchoReplayParser, path string,
	tick func(t *adapter.ParsedTick, first bool) error, end func(mc *model.MatchContext) error) (*adapter.DiagnosticReport, error) {
	var (
		cur        *model.MatchContext
		lastSample time.Time
		seen       = make(map[string]bool)
		cbErr      error
	)
	finish := func() error {
		if cur == nil {
			return nil
		}
		if cur.Duration == 0 && !lastSample.IsZero() && !cur.StartTime.IsZero() {
			// Match duration is the real span of the recording (first to last sample).
			cur.Duration = lastSample.Sub(cur.StartTime)
		}
		return end(cur)
	}
	_, diag, err := parser.ParseFileStream(path, func(t *adapter.ParsedTick) error {
		first := cur == nil || t.NewMatch || t.MatchID != cur.MatchID
		if first {
			if cbErr = finish(); cbErr != nil {
				return cbErr
			}
			if seen[t.MatchID] {
				cbErr = fmt.Errorf("session %q comes back after another session at %s; its frames would collide with the match already stored under that id",
					t.MatchID, t.SampleTime.Format("2006/01/02 15:04:05.000"))
				return cbErr
			}
			seen[t.MatchID] = true
			cur = t.MatchCtx
		}
		lastSample = t.SampleTime
		cbErr = tick(t, first)
		return cbErr
	})
	if cbErr != nil {
		return diag, cbErr
	}
	if err != nil {
		if strings.EqualFold(filepath.Ext(path), ".tape") {
			return diag, fmt.Errorf("reading native tape: %w", err)
		}
		return diag, fmt.Errorf("reading echoreplay: %w", err)
	}
	return diag, finish()
}

// summarizeFrames counts frames per player and picks the final team score
// off the last frame (frames arrive in file order).
func summarizeFrames(frames []model.PlayerTelemetryFrame) FrameSummary {
	s := FrameSummary{FramesByPlayer: make(map[string]int)}
	for i := range frames {
		f := &frames[i]
		s.FramesByPlayer[f.PlayerID]++
		if i == 0 || f.Timestamp < s.FirstTimestamp {
			s.FirstTimestamp = f.Timestamp
		}
		if f.Timestamp > s.LastTimestamp {
			s.LastTimestamp = f.Timestamp
		}
		if f.HasScore || f.BlueScore != 0 || f.OrangeScore != 0 {
			s.HasScore = true
		}
	}
	if n := len(frames); n > 0 {
		s.BlueScore, s.OrangeScore = frames[n-1].BlueScore, frames[n-1].OrangeScore
	}
	return s
}
