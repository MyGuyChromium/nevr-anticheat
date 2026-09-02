// NEVR-Anticheat: Async cheat detection engine for Echo VR / Echo Arena.
// Analyzes profiler telemetry stored in a database. Does not run on game servers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/bio"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/pattern"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/state"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/throw"
	"github.com/nevr-anticheat/nevr-anticheat/internal/evidence"
	"github.com/nevr-anticheat/nevr-anticheat/internal/logging"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

const appVersion = "0.1.0"

func main() {
	configPath := flag.String("config", "", "Path to TOML config file")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	sub := args[1:]
	switch args[0] {
	case "version":
		fmt.Printf("nevr-anticheat %s\n", appVersion)
		return
	case "analyze":
		fs := flag.NewFlagSet("analyze", flag.ExitOnError)
		force := fs.Bool("force", false, "Re-analyze a match that is already stored (replaces its events/scores)")
		pos := parseSub(fs, sub)
		if len(pos) < 1 {
			fatalUsage("anticheat analyze <replay-file> [--force]")
		}
		runAnalyze(*configPath, pos[0], *force)
	case "batch":
		fs := flag.NewFlagSet("batch", flag.ExitOnError)
		force := fs.Bool("force", false, "Re-analyze matches that are already stored")
		pos := parseSub(fs, sub)
		if len(pos) < 1 {
			fatalUsage("anticheat batch <directory> [--force]")
		}
		runBatch(*configPath, pos[0], *force)
	case "flagged":
		runFlagged(*configPath)
	case "report":
		if len(sub) < 1 {
			fatalUsage("anticheat report <case-id>")
		}
		runReport(*configPath, sub[0])
	case "player-history":
		if len(sub) < 1 {
			fatalUsage("anticheat player-history <player-id>")
		}
		runPlayerHistory(*configPath, sub[0])
	case "cross-match":
		runCrossMatchAnalysis(*configPath)
	case "cross-match-report":
		if len(sub) < 1 {
			fatalUsage("anticheat cross-match-report <case-id>")
		}
		runCrossMatchReport(*configPath, sub[0])
	case "reprocess-match":
		if len(sub) < 1 {
			fatalUsage("anticheat reprocess-match <match-id>")
		}
		runReprocessMatch(*configPath, sub[0])
	case "reprocess-player":
		if len(sub) < 1 {
			fatalUsage("anticheat reprocess-player <player-id>")
		}
		runReprocessPlayer(*configPath, sub[0])
	case "reprocess-timerange":
		if len(sub) < 2 {
			fatalUsage("anticheat reprocess-timerange <since-RFC3339> <until-RFC3339>   (range is [since, until))")
		}
		runReprocessTimeRange(*configPath, sub[0], sub[1])
	case "verdict":
		fs := flag.NewFlagSet("verdict", flag.ExitOnError)
		by := fs.String("by", "", "Moderator ID (required)")
		notes := fs.String("notes", "", "Free-form notes")
		action := fs.String("action", "", "Action taken (e.g. warn, temp_ban, none)")
		var detectors multiFlag
		fs.Var(&detectors, "detector", "Per-detector feedback DETECTOR_ID=yes|no|uncertain (repeatable)")
		pos := parseSub(fs, sub)
		if len(pos) < 2 || *by == "" {
			fatalUsage("anticheat verdict <case-id> <" + strings.Join(sqlite.ValidVerdicts(), "|") + "> --by <moderator> [--detector ID=yes|no|uncertain ...] [--notes ...] [--action ...]")
		}
		runVerdict(*configPath, pos[0], pos[1], *by, *notes, *action, detectors)
	case "calibration-report":
		fs := flag.NewFlagSet("calibration-report", flag.ExitOnError)
		since := fs.String("since", "", "Only decisions newer than this window (e.g. 30d, 12h); default: all")
		parseSub(fs, sub)
		runCalibrationReport(*configPath, *since)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

// parseSub parses flags that may appear before or after positional arguments
// and returns the positionals in order.
func parseSub(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			os.Exit(2)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func fatalUsage(usage string) {
	fmt.Fprintln(os.Stderr, "Usage: "+usage)
	os.Exit(1)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "NEVR-Anticheat: Async cheat detection engine for Echo VR")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Usage: anticheat [--config <path>] <command> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  analyze <file> [--force]   Analyze a replay file (stores telemetry + results; skips stored matches)")
	fmt.Fprintln(os.Stderr, "  batch <dir> [--force]      Batch analyze replays (stores telemetry + results; skips stored matches)")
	fmt.Fprintln(os.Stderr, "  flagged                    List pending review cases")
	fmt.Fprintln(os.Stderr, "  report <case-id>           Human-readable single-match case report with evidence")
	fmt.Fprintln(os.Stderr, "  player-history <id>        Show cross-match history for a player (from DB)")
	fmt.Fprintln(os.Stderr, "  cross-match                Run cross-match aggregation on stored data")
	fmt.Fprintln(os.Stderr, "  cross-match-report <id>    Inspect a cross-match review case with per-match evidence")
	fmt.Fprintln(os.Stderr, "  reprocess-match <id>       Re-run detection on stored telemetry for a match")
	fmt.Fprintln(os.Stderr, "  reprocess-player <id>      Re-run detection on stored telemetry for a player")
	fmt.Fprintln(os.Stderr, "  reprocess-timerange <a> <b> Re-run detection on matches with match time in [a, b) (RFC3339)")
	fmt.Fprintln(os.Stderr, "  verdict <case-id> <verdict> --by <mod> [--detector ID=yes|no|uncertain ...]")
	fmt.Fprintln(os.Stderr, "                             Record a moderator decision (verdict: "+strings.Join(sqlite.ValidVerdicts(), "|")+")")
	fmt.Fprintln(os.Stderr, "  calibration-report [--since 30d]")
	fmt.Fprintln(os.Stderr, "                             Per-detector confirmed/false-positive counts from moderator decisions")
	fmt.Fprintln(os.Stderr, "  version                    Print version")
}

// registerAllDetectors constructs and configures all 29 detectors from config.
// historyProvider may be nil if no database is available (PAT_003 will be inert).
func registerAllDetectors(cfg *config.Config, historyProvider pattern.HistoryProvider) []detect.Detector {
	type entry struct {
		id      string
		factory func(map[string]any) detect.Detector
	}
	catalog := []entry{
		{"THROW_001", func(p map[string]any) detect.Detector { return throw.NewThrow001(p) }},
		{"THROW_002", func(p map[string]any) detect.Detector { return throw.NewThrow002(p) }},
		{"THROW_003", func(p map[string]any) detect.Detector { return throw.NewThrow003(p) }},
		{"THROW_004", func(p map[string]any) detect.Detector { return throw.NewThrow004(p) }},
		{"THROW_005", func(p map[string]any) detect.Detector { return throw.NewThrow005(p) }},
		{"THROW_006", func(p map[string]any) detect.Detector { return throw.NewThrow006(p) }},
		{"THROW_007", func(p map[string]any) detect.Detector { return throw.NewThrow007(p) }},
		{"THROW_008", func(p map[string]any) detect.Detector { return throw.NewThrow008(p) }},
		{"BIO_001", func(p map[string]any) detect.Detector { return bio.NewBio001(p) }},
		{"BIO_002", func(p map[string]any) detect.Detector { return bio.NewBio002(p) }},
		{"BIO_003", func(p map[string]any) detect.Detector { return bio.NewBio003(p) }},
		{"BIO_004", func(p map[string]any) detect.Detector { return bio.NewBio004(p) }},
		{"MOV_001", func(p map[string]any) detect.Detector { return movement.NewMov001(p) }},
		{"MOV_002", func(p map[string]any) detect.Detector { return movement.NewMov002(p) }},
		{"MOV_003", func(p map[string]any) detect.Detector { return movement.NewMov003(p) }},
		{"MOV_004", func(p map[string]any) detect.Detector { return movement.NewMov004(p) }},
		{"MOV_005", func(p map[string]any) detect.Detector { return movement.NewMov005(p) }},
		{"STATE_001", func(p map[string]any) detect.Detector { return state.NewState001(p) }},
		{"STATE_002", func(p map[string]any) detect.Detector { return state.NewState002(p) }},
		{"STATE_003", func(p map[string]any) detect.Detector { return state.NewState003(p) }},
		{"STATE_004", func(p map[string]any) detect.Detector { return state.NewState004(p) }},
		{"STATE_005", func(p map[string]any) detect.Detector { return state.NewState005(p) }},
		{"STATE_006", func(p map[string]any) detect.Detector { return state.NewState006(p) }},
		{"STATE_007", func(p map[string]any) detect.Detector { return state.NewState007(p) }},
		{"PAT_001", func(p map[string]any) detect.Detector { return pattern.NewPat001(p) }},
		{"PAT_002", func(p map[string]any) detect.Detector { return pattern.NewPat002(p) }},
		{"PAT_003", func(p map[string]any) detect.Detector { return pattern.NewPat003(p) }},
		{"PAT_004", func(p map[string]any) detect.Detector { return pattern.NewPat004(p) }},
		{"PAT_005", func(p map[string]any) detect.Detector { return pattern.NewPat005(p) }},
	}

	var detectors []detect.Detector
	for _, e := range catalog {
		dc := cfg.GetDetectorConfig(e.id)
		if !dc.Enabled {
			continue
		}
		// Copy params so injecting the history provider never mutates shared config.
		params := make(map[string]any, len(dc.Params)+1)
		for k, v := range dc.Params {
			params[k] = v
		}
		// Inject history provider for PAT_003 (cross-match consistency)
		if e.id == "PAT_003" && historyProvider != nil {
			params["history_provider"] = historyProvider
		}
		detectors = append(detectors, e.factory(params))
	}
	return detectors
}

// app bundles what every command needs.
type app struct {
	cfg   *config.Config
	store *sqlite.Store
	hp    *sqlite.StoreHistoryProvider
	log   interface {
		Info(msg string, args ...any)
	}
}

func openApp(configPath string) (*app, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	logger := logging.NewLogger(cfg.General.LogLevel, cfg.General.LogFormat)
	store, err := sqlite.NewStore(cfg.General.DBPath)
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}
	return &app{cfg: cfg, store: store, hp: sqlite.NewStoreHistoryProvider(store), log: logger}, nil
}

// scorerConfig is the single place the [scoring] block is turned into scorer
// parameters; levels() derives the tier table every command classifies with.
func (a *app) scorerConfig() scoring.ScorerConfig {
	cfg := a.cfg
	return scoring.ScorerConfig{
		MaxSingleContribution:         cfg.Scoring.MaxSingleContribution,
		MaxContribPerDetectorPerMatch: cfg.Scoring.MaxContribPerDetectorPerMatch,
		SameCategoryDiminishing:       cfg.Scoring.SameCategoryDiminishing,
		ReviewThreshold:               cfg.Scoring.ReviewThreshold,
		AutoEnforceThreshold:          cfg.Scoring.AutoEnforceThreshold,
		DecayHalfLifeHours:            cfg.Scoring.DecayHalfLifeHours,
		CooldownFrames:                cfg.Pipeline.CooldownFrames,
		CorrelationBonusCap:           cfg.Scoring.CorrelationBonusCap,
	}
}

// levels is the tier table shared by the in-match scorer, single-match review
// cases, cross-match aggregation and CLI output (contract 7: one source of
// truth, review_threshold mapped onto high_risk).
func (a *app) levels() model.LevelTable {
	return a.scorerConfig().EffectiveLevels()
}

// physics is the match physics built from the [physics] block.
func (a *app) physics() model.PhysicsConstants {
	return pipeline.PhysicsFromConfig(a.cfg)
}

// analysisOptions is what StoreMatchAnalysis needs to write cases the way
// the scorer scored them.
func (a *app) analysisOptions() replay.AnalysisOptions {
	return replay.AnalysisOptions{
		Levels:        a.levels(),
		DetectorNames: detectorNames(),
		Logger:        logging.NewLogger(a.cfg.General.LogLevel, a.cfg.General.LogFormat),
	}
}

// detectorNames maps detector ID -> name from the process-wide catalog.
func detectorNames() map[string]string {
	names := make(map[string]string)
	for _, d := range detect.Catalog() {
		names[d.ID] = d.Name
	}
	return names
}

// newPipeline builds a fresh pipeline (own detectors, own scorer). Pipelines
// are not goroutine-safe; build one per worker.
func (a *app) newPipeline() *pipeline.Pipeline {
	cfg := a.cfg
	detectors := registerAllDetectors(cfg, a.hp)
	scorer := scoring.NewSuspicionScorer(a.scorerConfig())
	logger := logging.NewLogger(cfg.General.LogLevel, cfg.General.LogFormat)
	return pipeline.NewPipeline(cfg, detectors, scorer, logger)
}

func (a *app) crossMatchConfig() sqlite.CrossMatchConfig {
	return sqlite.CrossMatchConfig{
		DecayHalfLifeHours:            a.cfg.Scoring.DecayHalfLifeHours,
		MaxSingleContribution:         a.cfg.Scoring.MaxSingleContribution,
		MaxContribPerDetectorPerMatch: a.cfg.Scoring.MaxContribPerDetectorPerMatch,
		Levels:                        a.levels(),
	}
}

func mustOpen(configPath string) *app {
	a, err := openApp(configPath)
	if err != nil {
		fatal("Error: %v", err)
	}
	return a
}

func sortedPlayerIDs(scores map[string]model.SuspicionScore) []string {
	ids := make([]string, 0, len(scores))
	for pid := range scores {
		ids = append(ids, pid)
	}
	sort.Strings(ids)
	return ids
}

func printPlayerScores(scores map[string]model.SuspicionScore) {
	for _, pid := range sortedPlayerIDs(scores) {
		score := scores[pid]
		if score.EventCount > 0 {
			fmt.Printf("  Player %s: score=%.1f level=%s events=%d\n",
				pid, score.TotalScore, score.Level(), score.EventCount)
		}
	}
}

// errMatchAlreadyStored aborts a streaming parse when the match is already in
// the store and --force was not given.
var errMatchAlreadyStored = errors.New("match already stored")

// rawTickFlushEvery bounds how many raw session payloads analyze keeps in
// memory before writing them to match_ticks.
const rawTickFlushEvery = 500

func runAnalyze(configPath, replayPath string, force bool) {
	a := mustOpen(configPath)
	defer a.store.Close()
	ctx := context.Background()

	// Auto-detect format: .echoreplay (NDJSON/ZIP) vs legacy JSON replay
	var matchCtx *model.MatchContext
	var frames []model.PlayerTelemetryFrame
	var err error
	source := "initial"
	var tel sqlite.TelemetryStoreResult // accumulated telemetry writes
	rawStored := false

	// prepareMatch runs once the match id is known (first tick for a
	// streamed .echoreplay, after reading for a legacy replay): skip a stored
	// match unless --force, in which case the previous analysis is cleared.
	prepareMatch := func(matchID string) error {
		exists, err := a.store.HasMatch(ctx, matchID)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if !force {
			return errMatchAlreadyStored
		}
		source = "reprocess"
		ev, sc, err := a.store.DeleteMatchAnalysis(ctx, matchID)
		if err != nil {
			return fmt.Errorf("clearing previous analysis: %w", err)
		}
		fmt.Printf("Cleared previous analysis for %s (%d events, %d score snapshots)\n", matchID, ev, sc)
		return nil
	}

	if isEchoReplay(replayPath) {
		// Stream the replay so the raw profiler payloads are written to
		// match_ticks (once per frame index) in bounded chunks instead of
		// being held for the whole file. Frames are still accumulated once:
		// ProcessMatch needs the complete match.
		parser := adapter.NewEchoReplayParser()
		parser.SetPhysics(a.physics())
		matchID := ""
		pending := make(map[int]string)
		flush := func() error {
			if len(pending) == 0 {
				return nil
			}
			res, err := a.store.StoreTelemetryFramesWithRaw(ctx, matchID, nil, pending)
			if err != nil {
				return err
			}
			tel.TicksInserted += res.TicksInserted
			tel.TicksIgnored += res.TicksIgnored
			rawStored = true
			pending = make(map[int]string)
			return nil
		}
		mc, diag, err := parser.ParseFileStream(replayPath, func(tick *adapter.ParsedTick) error {
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
			if len(pending) >= rawTickFlushEvery {
				return flush()
			}
			return nil
		})
		if errors.Is(err, errMatchAlreadyStored) {
			fmt.Printf("Match %s is already stored; derived outputs left unchanged.\n", matchID)
			fmt.Println("Re-run with --force to replace its detection events and scores, or use reprocess-match.")
			return
		}
		if err != nil {
			fatal("Error reading echoreplay: %v", err)
		}
		if err := flush(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to store raw ticks: %v\n", err)
		}
		matchCtx = mc
		fmt.Printf("Parsed %d player-frames from %s (%d lines rejected)\n",
			len(frames), replayPath, diag.FramesRejected)
		fmt.Print(diag.FormatReport())
	} else {
		reader := replay.NewReplayReader(replayPath, replay.NewJSONFrameParser())
		reader.SetPhysics(a.physics())
		matchCtx, frames, err = reader.ReadMatch()
		if err != nil {
			fatal("Error reading replay: %v", err)
		}
		if matchCtx.MatchID == "" {
			fatal("Error: replay has no match id")
		}
		if err := prepareMatch(matchCtx.MatchID); errors.Is(err, errMatchAlreadyStored) {
			fmt.Printf("Match %s is already stored; derived outputs left unchanged.\n", matchCtx.MatchID)
			fmt.Println("Re-run with --force to replace its detection events and scores, or use reprocess-match.")
			return
		} else if err != nil {
			fatal("Error: %v", err)
		}
	}
	if matchCtx == nil || matchCtx.MatchID == "" {
		fatal("Error: replay has no match id")
	}

	p := a.newPipeline()
	result, err := p.ProcessMatch(ctx, matchCtx, frames)
	if err != nil {
		fatal("Error: %v", err)
	}

	// Persist telemetry and match context so reprocessing doesn't need replay
	// files (the raw profiler JSON of an .echoreplay was streamed to
	// match_ticks during parsing).
	fr, storeErr := a.store.StoreTelemetryFramesWithRaw(ctx, matchCtx.MatchID, frames, nil)
	if storeErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to store telemetry: %v\n", storeErr)
	} else {
		tel.Inserted, tel.Ignored = fr.Inserted, fr.Ignored
		fmt.Printf("Stored %d telemetry frames (%d already present)", tel.Inserted, tel.Ignored)
		if rawStored {
			fmt.Printf(", %d raw ticks (%d already present)", tel.TicksInserted, tel.TicksIgnored)
		}
		fmt.Println()
	}
	if err := a.store.StoreMatchContext(ctx, matchCtx, len(frames)); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to store match context: %v\n", err)
	}
	stored, err := replay.StoreMatchAnalysis(ctx, a.store, matchCtx, result, source, a.analysisOptions())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	fmt.Printf("Match: %s\nFrames: %d processed, %d invalid\nDetections: %d (%d stored)\nReview cases: %d\nDuration: %v\n",
		result.MatchID, result.FramesProcessed, result.InvalidFrames,
		len(result.DetectionEvents), stored.EventsStored, stored.CasesStored, result.Duration)
	printPlayerScores(result.PlayerScores)
}

func runBatch(configPath, dir string, force bool) {
	a := mustOpen(configPath)
	defer a.store.Close()

	analyzer := replay.NewBatchAnalyzer(a.newPipeline(), a.store,
		func() replay.FrameParser { return replay.NewJSONFrameParser() },
		a.cfg.General.MaxWorkers, logging.NewLogger(a.cfg.General.LogLevel, a.cfg.General.LogFormat))
	analyzer.SetPipelineFactory(a.newPipeline)
	analyzer.SetForce(force)
	analyzer.SetAnalysisOptions(a.analysisOptions())
	analyzer.SetPhysics(a.physics())
	result, err := analyzer.AnalyzeDirectory(context.Background(), dir)
	if err != nil {
		fatal("Error: %v", err)
	}
	fmt.Printf("Batch: %d/%d processed, %d skipped, %d errors, %d non-replay files ignored, %v\n",
		result.Processed, result.TotalFiles, result.Skipped, result.Errors, result.IgnoredFiles, result.Duration)
	fmt.Printf("Stored: %d telemetry frames (%d already present), %d detection events, %d players flagged\n",
		result.FramesInserted, result.FramesIgnored, result.EventsStored, len(result.FlaggedPlayers))

	// Post-batch cross-match aggregation: compute cumulative scores across all matches
	fmt.Println("\nRunning cross-match aggregation...")
	aggregated, cases := runCrossMatchAggregation(a)
	fmt.Printf("Cross-match: %d players aggregated, %d review cases\n", aggregated, cases)
	if result.Errors > 0 {
		os.Exit(1)
	}
}

func runFlagged(configPath string) {
	a := mustOpen(configPath)
	defer a.store.Close()
	ctx := context.Background()

	cases, err := a.store.GetPendingReviewCases(ctx, 100)
	if err != nil {
		fatal("Error: %v", err)
	}
	xmCases, err := a.store.GetPendingCrossMatchReviewCases(ctx, 100)
	if err != nil {
		fatal("Error loading cross-match cases: %v", err)
	}

	if len(cases) == 0 && len(xmCases) == 0 {
		fmt.Println("No flagged players.")
		return
	}

	if len(xmCases) > 0 {
		fmt.Printf("CROSS-MATCH CASES (%d)  (decayed score is 0-100, same scale as a match score)\n", len(xmCases))
		for _, rc := range xmCases {
			fmt.Printf("  %-30s %-20s decayed=%.1f raw=%.1f matches=%d %s\n",
				rc.CaseID, rc.PlayerID, rc.DecayedScore, rc.CumulativeScore, rc.MatchCount, rc.Severity)
		}
		fmt.Println()
	}

	if len(cases) > 0 {
		fmt.Printf("SINGLE-MATCH CASES (%d)\n", len(cases))
		for _, rc := range cases {
			fmt.Printf("  %-30s %-20s score=%.1f %s\n", rc.CaseID, rc.PlayerID, rc.SuspicionScore, rc.Severity)
		}
	}
}

func runReport(configPath, caseID string) {
	a := mustOpen(configPath)
	defer a.store.Close()
	ctx := context.Background()

	rc, err := a.store.GetReviewCase(ctx, caseID)
	if err != nil {
		if errors.Is(err, sqlite.ErrNotFound) || strings.Contains(err.Error(), "no rows") {
			fatal("Error: no single-match review case %q (cross-match cases use cross-match-report)", caseID)
		}
		fatal("Error: %v", err)
	}
	fmt.Print(evidence.FormatReport(rc))

	events, err := a.store.GetMatchPlayerEvents(ctx, rc.MatchID, rc.PlayerID)
	if err != nil {
		fatal("Error loading events: %v", err)
	}
	printEvidenceSection(events)
	printDecisions(a, caseID)
}

// printEvidenceSection prints every stored event with its typed evidence so a
// moderator can see the measurements behind the case, not just the summary strings.
func printEvidenceSection(events []model.DetectionEvent) {
	fmt.Println("================================================================================")
	fmt.Printf("STORED EVENTS AND EVIDENCE (%d)\n", len(events))
	fmt.Println("================================================================================")
	if len(events) == 0 {
		fmt.Println("  (no detection events stored for this player in this match)")
		return
	}
	for _, ev := range events {
		shadow := ""
		if ev.IsShadow {
			shadow = " [shadow]"
		}
		fmt.Printf("\n  %s v%s%s  frame=%d (%d-%d) t=%.2fs  sev=%.2f conf=%.2f weight=%.2f\n",
			ev.DetectorID, ev.DetectorVersion, shadow, ev.FrameIndex, ev.FrameRangeStart, ev.FrameRangeEnd,
			ev.Timestamp, ev.Severity, ev.Confidence, ev.EnforcementWeight)
		fmt.Printf("    observed: %s\n    expected: %s\n", ev.ObservedValue, ev.ExpectedRange)
		if ev.CausalKey.AnomalyType != "" {
			fmt.Printf("    causal:   %s\n", ev.CausalKey.Key())
		}
		if ev.Evidence == nil {
			fmt.Println("    evidence: (none stored)")
			continue
		}
		b, err := json.Marshal(ev.Evidence)
		if err != nil {
			fmt.Printf("    evidence: %s (unprintable: %v)\n", ev.Evidence.EvidenceType(), err)
			continue
		}
		fmt.Printf("    evidence[%s]: %s\n", ev.Evidence.EvidenceType(), b)
	}
}

func printDecisions(a *app, caseID string) {
	decisions, err := a.store.GetCaseDecisions(context.Background(), caseID)
	if err != nil || len(decisions) == 0 {
		return
	}
	fmt.Println("================================================================================")
	fmt.Printf("MODERATOR DECISIONS (%d)\n", len(decisions))
	fmt.Println("================================================================================")
	for _, d := range decisions {
		fmt.Printf("  %s  %-16s by %s", d.DecidedAt.UTC().Format(time.RFC3339), d.Verdict, d.ModeratorID)
		if d.ActionTaken != "" {
			fmt.Printf("  action=%s", d.ActionTaken)
		}
		fmt.Println()
		for _, fb := range d.DetectorFeedback {
			fmt.Printf("      %-12s %s\n", fb.DetectorID, fb.Correct)
		}
		if d.Notes != "" {
			fmt.Printf("      notes: %s\n", d.Notes)
		}
	}
}

func runPlayerHistory(configPath, playerID string) {
	a := mustOpen(configPath)
	defer a.store.Close()
	ctx := context.Background()

	events, err := a.store.GetAllPlayerEvents(ctx, playerID)
	if err != nil {
		fatal("Error: %v", err)
	}
	if len(events) == 0 {
		fmt.Printf("No non-shadow detection events found for player %s\n", playerID)
		return
	}
	summary, err := computeSummary(ctx, a, events)
	if err != nil {
		fatal("Error: %v", err)
	}

	fmt.Printf("================================================================================\n")
	fmt.Printf("           CROSS-MATCH PLAYER HISTORY: %s\n", playerID)
	fmt.Printf("================================================================================\n\n")
	fmt.Printf("  Total Events:       %d (%d scored after per-detector-per-match cap of %d)\n",
		summary.TotalEvents, summary.ScoredEvents, a.cfg.Scoring.MaxContribPerDetectorPerMatch)
	fmt.Printf("  Distinct Matches:   %d\n", summary.DistinctMatches)
	fmt.Printf("  Distinct Detectors: %d\n", summary.DistinctDetectors)
	fmt.Printf("  Raw Points:         %.1f (per-event cap %.0f, no decay)\n", summary.CumulativeScore, a.cfg.Scoring.MaxSingleContribution)
	fmt.Printf("  Decayed Score:      %.1f / 100 (half-life: %.0fh, anchored on match time)\n", summary.DecayedScore, a.cfg.Scoring.DecayHalfLifeHours)
	if summary.AnchorFallbacks > 0 {
		fmt.Printf("  Note:               %d events had no recorded match start; decay for them used storage time\n", summary.AnchorFallbacks)
	}
	fmt.Printf("  Avg Severity:       %.3f\n", summary.AvgSeverity)
	fmt.Printf("  Avg Confidence:     %.3f\n", summary.AvgConfidence)
	fmt.Printf("  Level:              %s\n\n", summary.Level)

	fmt.Printf("  BY DETECTOR:\n")
	for _, det := range sortedKeys(summary.ByDetector) {
		fmt.Printf("    %-15s %d events  %.1f pts\n", det, summary.ByDetector[det], summary.PointsByDetector[det])
	}
	fmt.Printf("\n  BY MATCH:\n")
	for _, mid := range summary.MatchIDs {
		fmt.Printf("    %-40s %d events\n", mid, summary.ByMatch[mid])
	}

	if sc, err := a.store.GetPlayerScore(ctx, playerID); err == nil {
		fmt.Printf("\n  Latest per-match snapshot: score=%.1f level=%s at %s\n",
			sc.TotalScore, sc.Level(), sc.SnapshotTime.UTC().Format(time.RFC3339))
	}
	fmt.Println()
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// computeSummary looks up the wall-clock start of every match the events
// belong to and runs the capped, decayed cross-match aggregation.
func computeSummary(ctx context.Context, a *app, events []model.DetectionEvent) (sqlite.PlayerCrossMatchSummary, error) {
	seen := make(map[string]bool)
	var matchIDs []string
	for _, ev := range events {
		if !seen[ev.MatchID] {
			seen[ev.MatchID] = true
			matchIDs = append(matchIDs, ev.MatchID)
		}
	}
	starts, err := a.store.GetMatchStartTimes(ctx, matchIDs)
	if err != nil {
		return sqlite.PlayerCrossMatchSummary{}, fmt.Errorf("loading match start times: %w", err)
	}
	return sqlite.ComputePlayerCrossMatchSummary(events, starts, a.crossMatchConfig()), nil
}

func runCrossMatchAnalysis(configPath string) {
	a := mustOpen(configPath)
	defer a.store.Close()

	aggregated, cases := runCrossMatchAggregation(a)
	fmt.Printf("Cross-match aggregation complete: %d players analyzed, %d review cases created/refreshed\n", aggregated, cases)
}

// runCrossMatchAggregation queries all stored non-shadow events, computes
// per-player cross-match summaries (same caps as the in-match scorer, decay
// anchored on match time), stores cross-match score snapshots, and creates or
// refreshes cross-match review cases for players at or above the review
// threshold. Only players with events in at least
// scoring.min_matches_for_cross_match (floor 2) matches are aggregated.
func runCrossMatchAggregation(a *app) (int, int) {
	ctx := context.Background()
	levels := a.levels()
	reviewThreshold := levels.HighRisk // scoring.review_threshold mapped onto high_risk
	minMatches := a.cfg.Scoring.MinMatchesForCrossMatch
	if minMatches < 2 {
		minMatches = 2
	}

	// Get all players with events in the last 90 days
	since := time.Now().Add(-90 * 24 * time.Hour)
	players, err := a.store.GetDistinctPlayersWithEvents(ctx, since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting players: %v\n", err)
		return 0, 0
	}

	playerCount := 0
	caseCount := 0
	for _, pid := range players {
		events, err := a.store.GetAllPlayerEvents(ctx, pid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading events for %s: %v\n", pid, err)
			continue
		}
		if len(events) == 0 {
			continue
		}
		summary, err := computeSummary(ctx, a, events)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error aggregating %s: %v\n", pid, err)
			continue
		}
		if summary.DistinctMatches < minMatches {
			continue
		}
		if err := a.store.StoreCrossMatchScore(ctx, summary); err != nil {
			fmt.Fprintf(os.Stderr, "Error storing cross-match score for %s: %v\n", pid, err)
			continue
		}
		playerCount++

		if summary.DecayedScore >= reviewThreshold {
			fmt.Printf("  [%s] %s: decayed=%.1f raw=%.1f matches=%d events=%d\n",
				summary.Level, pid, summary.DecayedScore, summary.CumulativeScore,
				summary.DistinctMatches, summary.TotalEvents)
		}
		if rc := sqlite.BuildCrossMatchReviewCase(summary, levels); rc != nil {
			if err := a.store.StoreCrossMatchReviewCase(ctx, *rc); err != nil {
				fmt.Fprintf(os.Stderr, "Error storing cross-match case for %s: %v\n", pid, err)
			} else {
				caseCount++
			}
		}
	}
	return playerCount, caseCount
}

func runCrossMatchReport(configPath, caseID string) {
	a := mustOpen(configPath)
	defer a.store.Close()
	ctx := context.Background()

	rc, err := a.store.GetCrossMatchReviewCase(ctx, caseID)
	if err != nil {
		fatal("Error: %v", err)
	}

	fmt.Printf("================================================================================\n")
	fmt.Printf("              CROSS-MATCH REVIEW CASE: %s\n", rc.CaseID)
	fmt.Printf("================================================================================\n\n")
	fmt.Printf("  Player:           %s\n", rc.PlayerID)
	fmt.Printf("  Status:           %s\n", rc.Status)
	fmt.Printf("  Severity:         %s\n", rc.Severity)
	fmt.Printf("  Matches:          %d\n", rc.MatchCount)
	fmt.Printf("  Raw Points:       %.1f (capped per event, no decay)\n", rc.CumulativeScore)
	fmt.Printf("  Decayed Score:    %.1f / 100\n", rc.DecayedScore)
	fmt.Printf("  Created:          %s\n", rc.CreatedAt.UTC().Format(time.RFC3339))
	if !rc.UpdatedAt.IsZero() {
		fmt.Printf("  Updated:          %s\n", rc.UpdatedAt.UTC().Format(time.RFC3339))
	}
	fmt.Println()

	fmt.Printf("  DETECTORS:\n")
	for _, det := range sortedKeys(rc.Detectors) {
		fmt.Printf("    %-15s %d events\n", det, rc.Detectors[det])
	}

	fmt.Printf("\n  EXPLANATION:\n    %s\n", rc.Explanation)

	for _, mid := range rc.MatchIDs {
		fmt.Printf("\n--------------------------------------------------------------------------------\n")
		fmt.Printf("MATCH %s\n", mid)
		if mc, err := a.store.GetMatchContext(ctx, mid); err == nil {
			fmt.Printf("  %s / %s  start=%s  source=%s\n", mc.GameMode, mc.Map,
				mc.StartTime.UTC().Format(time.RFC3339), mc.Source)
		}
		events, err := a.store.GetMatchPlayerEvents(ctx, mid, rc.PlayerID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error loading events: %v\n", err)
			continue
		}
		printEvidenceSection(events)
	}
	printDecisions(a, caseID)
	fmt.Printf("================================================================================\n")
}

func runVerdict(configPath, caseID, verdict, by, notes, action string, detectorSpecs []string) {
	feedback, err := sqlite.ParseDetectorFeedback(detectorSpecs)
	if err != nil {
		fatal("Error: %v", err)
	}
	a := mustOpen(configPath)
	defer a.store.Close()

	d := model.ModeratorDecision{
		CaseID:           caseID,
		ModeratorID:      by,
		Verdict:          strings.ToLower(verdict),
		ActionTaken:      action,
		Notes:            notes,
		DecidedAt:        time.Now(),
		DetectorFeedback: feedback,
	}
	if err := a.store.StoreModeratorDecision(context.Background(), d); err != nil {
		fatal("Error: %v", err)
	}
	fmt.Printf("Recorded %s for case %s by %s (case status -> decided)\n", d.Verdict, caseID, by)
	for _, fb := range feedback {
		fmt.Printf("  %-12s %s\n", fb.DetectorID, fb.Correct)
	}
}

// parseWindow parses "30d", "12h", "90m" or any time.ParseDuration string.
func parseWindow(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid window %q", s)
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	}
	return time.ParseDuration(s)
}

func runCalibrationReport(configPath, sinceSpec string) {
	var since time.Time
	if sinceSpec != "" {
		window, err := parseWindow(sinceSpec)
		if err != nil {
			fatal("Error: %v", err)
		}
		since = time.Now().Add(-window)
	}
	a := mustOpen(configPath)
	defer a.store.Close()

	rows, err := a.store.ComputeCalibration(context.Background(), since)
	if err != nil {
		fatal("Error: %v", err)
	}
	fmt.Println("================================================================================")
	if since.IsZero() {
		fmt.Println("DETECTOR CALIBRATION (all moderator decisions)")
	} else {
		fmt.Printf("DETECTOR CALIBRATION (decisions since %s)\n", since.UTC().Format(time.RFC3339))
	}
	fmt.Println("================================================================================")
	if len(rows) == 0 {
		fmt.Println("  No moderator decisions recorded. Use `verdict <case-id> ...` to label cases.")
		return
	}
	fmt.Printf("  %-12s %9s %9s %9s %9s %6s %7s %9s\n",
		"DETECTOR", "CONFIRMED", "FALSE_POS", "INCONCL", "NEED_DATA", "CASES", "EVENTS", "PRECISION")
	for _, c := range rows {
		prec := "n/a"
		if p, ok := c.Precision(); ok {
			prec = fmt.Sprintf("%.0f%%", p*100)
		}
		fmt.Printf("  %-12s %9d %9d %9d %9d %6d %7d %9s\n",
			c.DetectorID, c.Confirmed, c.FalsePositive, c.Inconclusive, c.NeedsMoreData,
			c.CasesReviewed, c.EventsReviewed, prec)
	}
	fmt.Println()
	fmt.Println("  Counts are per decided case in which the detector fired (shadow events included).")
	fmt.Println("  Explicit --detector feedback overrides the case verdict for that detector.")
}

// reprocessMatchFromDB loads telemetry from the database and re-runs the detection pipeline.
// It deletes existing detection events and per-match score snapshots for this
// match first so reprocessing is idempotent (review cases are upserted under
// their deterministic ids and keep any moderator status).
func reprocessMatchFromDB(ctx context.Context, a *app, p *pipeline.Pipeline, matchID string) (*pipeline.MatchResult, error) {
	store := a.store
	matchCtx, err := store.GetMatchContext(ctx, matchID)
	if err != nil {
		return nil, fmt.Errorf("loading match context: %w", err)
	}
	if matchCtx.Physics == (model.PhysicsConstants{}) {
		// Context stored without physics (older rows, synthesized contexts):
		// analyze under the configured constants, as the original run did.
		matchCtx.Physics = a.physics()
	}
	frames, err := store.GetMatchFrames(ctx, matchID)
	if err != nil {
		return nil, fmt.Errorf("loading frames: %w", err)
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no telemetry frames stored for match %s", matchID)
	}

	deletedEvents, deletedScores, err := store.DeleteMatchAnalysis(ctx, matchID)
	if err != nil {
		return nil, fmt.Errorf("clearing old analysis: %w", err)
	}
	if deletedEvents > 0 || deletedScores > 0 {
		fmt.Printf("  Cleared %d old events and %d score snapshots for match %s\n", deletedEvents, deletedScores, matchID)
	}

	result, err := p.ProcessMatch(ctx, matchCtx, frames)
	if err != nil {
		return nil, err
	}
	if _, err := replay.StoreMatchAnalysis(ctx, store, matchCtx, result, "reprocess", a.analysisOptions()); err != nil {
		return nil, err
	}
	return result, nil
}

func runReprocessMatch(configPath, matchID string) {
	a := mustOpen(configPath)
	defer a.store.Close()

	result, err := reprocessMatchFromDB(context.Background(), a, a.newPipeline(), matchID)
	if err != nil {
		fatal("Error: %v", err)
	}
	fmt.Printf("Reprocessed match %s: %d frames, %d detections, %v\n",
		matchID, result.FramesProcessed, len(result.DetectionEvents), result.Duration)
	printPlayerScores(result.PlayerScores)
}

// reprocessMany reprocesses each match in order and returns the IDs that failed.
func reprocessMany(ctx context.Context, a *app, matchIDs []string, verbose bool) (totalDetections int, failed []string) {
	p := a.newPipeline()
	for _, matchID := range matchIDs {
		result, err := reprocessMatchFromDB(ctx, a, p, matchID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Match %s: error: %v\n", matchID, err)
			failed = append(failed, matchID)
			continue
		}
		detections := len(result.DetectionEvents)
		totalDetections += detections
		if verbose || detections > 0 {
			fmt.Printf("  Match %s: %d frames, %d detections\n", matchID, result.FramesProcessed, detections)
		}
	}
	return totalDetections, failed
}

func finishReprocess(matchIDs []string, totalDetections int, failed []string) {
	fmt.Printf("Total: %d matches reprocessed, %d failed, %d detections\n",
		len(matchIDs)-len(failed), len(failed), totalDetections)
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "Failed matches: %s\n", strings.Join(failed, ", "))
		os.Exit(1)
	}
}

func runReprocessPlayer(configPath, playerID string) {
	a := mustOpen(configPath)
	defer a.store.Close()
	ctx := context.Background()

	matchIDs, err := a.store.GetPlayerMatchIDs(ctx, playerID, 50)
	if err != nil {
		fatal("Error loading player matches: %v", err)
	}
	if len(matchIDs) == 0 {
		fmt.Printf("No stored telemetry found for player %s\n", playerID)
		os.Exit(1)
	}

	fmt.Printf("Reprocessing %d matches for player %s...\n", len(matchIDs), playerID)
	total, failed := reprocessMany(ctx, a, matchIDs, true)
	finishReprocess(matchIDs, total, failed)
}

func runReprocessTimeRange(configPath, sinceStr, untilStr string) {
	since, err := time.Parse(time.RFC3339, sinceStr)
	if err != nil {
		fatal("Invalid since time (use RFC3339, e.g. 2026-01-01T00:00:00Z): %v", err)
	}
	until, err := time.Parse(time.RFC3339, untilStr)
	if err != nil {
		fatal("Invalid until time (use RFC3339, e.g. 2026-03-01T00:00:00Z): %v", err)
	}
	if !until.After(since) {
		fatal("Invalid range: until must be after since")
	}
	a := mustOpen(configPath)
	defer a.store.Close()
	ctx := context.Background()

	matchIDs, err := a.store.GetMatchIDsByTimeRange(ctx, since, until)
	if err != nil {
		fatal("Error querying matches: %v", err)
	}
	if len(matchIDs) == 0 {
		fmt.Printf("No matches with match time in [%s, %s).\n",
			since.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339))
		return
	}

	fmt.Printf("Reprocessing %d matches with match time in [%s, %s)...\n", len(matchIDs),
		since.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339))
	total, failed := reprocessMany(ctx, a, matchIDs, false)
	finishReprocess(matchIDs, total, failed)
}

// isEchoReplay returns true if the file path looks like an Echo VR replay.
func isEchoReplay(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".echoreplay"
}
