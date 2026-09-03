package replay

import (
	"context"
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
		Levels:                        e.Levels(),
	}
}

// AnalyzeFile runs AnalyzeFile with the engine's pipeline, physics and
// analysis options. force re-analyzes a match that is already stored.
func (e *Engine) AnalyzeFile(ctx context.Context, path string, force bool) (*AnalyzeResult, error) {
	return AnalyzeFile(ctx, e.store, path, AnalyzeOptions{
		Force:       force,
		Physics:     e.Physics(),
		NewPipeline: e.NewPipeline,
		Analysis:    e.AnalysisOptions(),
	})
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

// AnalyzeOptions configures AnalyzeFile.
type AnalyzeOptions struct {
	// Force re-analyzes a match that is already stored: its detection events
	// and score snapshots are cleared first and the new outputs are tagged
	// source "reprocess". Without it a stored match is refused with
	// ErrMatchAlreadyStored before anything is written.
	Force bool
	// Physics is stamped on the parsed match context (zero = model.DefaultPhysics).
	Physics model.PhysicsConstants
	// NewPipeline builds the pipeline the match is processed with (required).
	NewPipeline func() *pipeline.Pipeline
	// Analysis is what StoreMatchAnalysis needs (level table, detector names, logger).
	Analysis AnalysisOptions
}

// FrameSummary is what AnalyzeFile observed in the parsed frames, for
// reports that no longer have the frames in hand.
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

// AnalyzeResult is what AnalyzeFile parsed, detected and stored for one
// replay. Storage failures after detection are not fatal (the CLI has always
// warned and continued); they are reported in the *Err fields.
type AnalyzeResult struct {
	// Path is the replay that was analyzed.
	Path string
	// MatchCtx is the parsed match context (as stored).
	MatchCtx *model.MatchContext
	// Frames is the number of player-frames parsed from the file.
	Frames int
	// Diagnostics is the adapter's mapping report; nil for a legacy JSON replay.
	Diagnostics *adapter.DiagnosticReport
	// Replaced is true when the match was already stored and Force cleared
	// its previous analysis (ClearedEvents / ClearedScores rows).
	Replaced      bool
	ClearedEvents int64
	ClearedScores int64
	// Result is the pipeline's output; Result.ReviewCases holds the cases
	// StoreMatchAnalysis created.
	Result *pipeline.MatchResult
	// Stored reports what StoreMatchAnalysis wrote.
	Stored StoredAnalysis
	// Telemetry reports the telemetry frame / raw tick writes. RawStored is
	// true when raw ticks were written (.echoreplay sources).
	Telemetry sqlite.TelemetryStoreResult
	RawStored bool
	// Summary is what the parsed frames showed.
	Summary FrameSummary

	// Non-fatal storage failures, in the order they happened.
	RawTickErr   error // writing raw ticks during parsing
	TelemetryErr error // storing telemetry frames
	ContextErr   error // storing the match context
	AnalysisErr  error // storing events, scores or review cases
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
	return out
}

// IsEchoReplay reports whether the path has the .echoreplay extension
// (case-insensitive); anything else is read as a legacy JSON replay.
func IsEchoReplay(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".echoreplay"
}

// AnalyzeFile is the `analyze` command: it parses one replay (.echoreplay
// streamed with its raw ticks written to match_ticks in bounded chunks, or a
// legacy JSON replay), refuses a match that is already stored unless
// opts.Force (which clears the previous analysis first), runs the match
// through a fresh pipeline, then persists telemetry, match context and the
// derived outputs (events, score snapshots, review cases) via
// StoreMatchAnalysis.
//
// Errors before detection (unreadable file, no match id, ErrMatchAlreadyStored,
// pipeline failure) are returned with a nil result. Storage failures after
// detection are reported on the result, never returned.
func AnalyzeFile(ctx context.Context, store *sqlite.Store, path string, opts AnalyzeOptions) (*AnalyzeResult, error) {
	if opts.NewPipeline == nil {
		return nil, errors.New("AnalyzeFile: NewPipeline is required")
	}
	res := &AnalyzeResult{Path: path}
	var frames []model.PlayerTelemetryFrame
	source := "initial"

	// prepareMatch runs once the match id is known (first tick for a
	// streamed .echoreplay, after reading for a legacy replay): skip a stored
	// match unless Force, in which case the previous analysis is cleared.
	prepareMatch := func(matchID string) error {
		exists, err := store.HasMatch(ctx, matchID)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if !opts.Force {
			return &MatchStoredError{MatchID: matchID}
		}
		source = "reprocess"
		ev, sc, err := store.DeleteMatchAnalysis(ctx, matchID)
		if err != nil {
			return fmt.Errorf("clearing previous analysis: %w", err)
		}
		res.Replaced, res.ClearedEvents, res.ClearedScores = true, ev, sc
		return nil
	}

	if IsEchoReplay(path) {
		// Stream the replay so the raw profiler payloads are written to
		// match_ticks (once per frame index) in bounded chunks instead of
		// being held for the whole file. Frames are still accumulated once:
		// ProcessMatch needs the complete match.
		parser := adapter.NewEchoReplayParser()
		if opts.Physics != (model.PhysicsConstants{}) {
			parser.SetPhysics(opts.Physics)
		}
		matchID := ""
		var lastSample time.Time
		pending := make(map[int]string)
		flush := func() error {
			if len(pending) == 0 {
				return nil
			}
			tel, err := store.StoreTelemetryFramesWithRaw(ctx, matchID, nil, pending)
			if err != nil {
				return err
			}
			res.Telemetry.TicksInserted += tel.TicksInserted
			res.Telemetry.TicksIgnored += tel.TicksIgnored
			res.RawStored = true
			pending = make(map[int]string)
			return nil
		}
		mc, diag, err := parser.ParseFileStream(path, func(tick *adapter.ParsedTick) error {
			if matchID == "" {
				matchID = tick.MatchID
				if matchID == "" {
					return errors.New("replay has no match id")
				}
				if err := prepareMatch(matchID); err != nil {
					return err
				}
			}
			if _, seen := pending[tick.FrameIndex]; !seen {
				pending[tick.FrameIndex] = tick.RawJSON
			}
			frames = append(frames, tick.Frames...)
			lastSample = tick.SampleTime
			if len(pending) >= rawTickFlushEvery {
				return flush()
			}
			return nil
		})
		var stored *MatchStoredError
		if errors.As(err, &stored) {
			return nil, err
		}
		if err != nil {
			return nil, fmt.Errorf("reading echoreplay: %w", err)
		}
		if err := flush(); err != nil {
			res.RawTickErr = err
		}
		if mc != nil && mc.Duration == 0 && !lastSample.IsZero() && !mc.StartTime.IsZero() {
			// Match duration is the real span of the recording (first to last sample).
			mc.Duration = lastSample.Sub(mc.StartTime)
		}
		res.MatchCtx, res.Diagnostics = mc, diag
	} else {
		reader := NewReplayReader(path, NewJSONFrameParser())
		reader.SetPhysics(opts.Physics)
		mc, fr, err := reader.ReadMatch()
		if err != nil {
			return nil, fmt.Errorf("reading replay: %w", err)
		}
		if mc.MatchID == "" {
			return nil, errors.New("replay has no match id")
		}
		if err := prepareMatch(mc.MatchID); err != nil {
			return nil, err
		}
		res.MatchCtx, frames = mc, fr
	}
	if res.MatchCtx == nil || res.MatchCtx.MatchID == "" {
		return nil, errors.New("replay has no match id")
	}
	res.Frames = len(frames)
	res.Summary = summarizeFrames(frames)

	result, err := opts.NewPipeline().ProcessMatch(ctx, res.MatchCtx, frames)
	if err != nil {
		return nil, err
	}
	res.Result = result

	// Persist telemetry and match context so reprocessing doesn't need replay
	// files (the raw profiler JSON of an .echoreplay was streamed to
	// match_ticks during parsing).
	fr, err := store.StoreTelemetryFramesWithRaw(ctx, res.MatchCtx.MatchID, frames, nil)
	if err != nil {
		res.TelemetryErr = err
	} else {
		res.Telemetry.Inserted, res.Telemetry.Ignored = fr.Inserted, fr.Ignored
	}
	if err := store.StoreMatchContext(ctx, res.MatchCtx, len(frames)); err != nil {
		res.ContextErr = err
	}
	res.Stored, res.AnalysisErr = StoreMatchAnalysis(ctx, store, res.MatchCtx, result, source, opts.Analysis)
	return res, nil
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
		if f.BlueScore != 0 || f.OrangeScore != 0 {
			s.HasScore = true
		}
	}
	if n := len(frames); n > 0 {
		s.BlueScore, s.OrangeScore = frames[n-1].BlueScore, frames[n-1].OrangeScore
	}
	return s
}
