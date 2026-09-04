// NEVR-Anticheat: Async cheat detection engine for Echo VR / Echo Arena.
// Analyzes profiler telemetry stored in a database. Does not run on game servers.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/evidence"
	"github.com/nevr-anticheat/nevr-anticheat/internal/logging"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/review"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

const appVersion = "0.1.0"

// dropMode is set when replay files or folders were passed instead of a
// command (files dropped onto nevr-ac.exe in Explorer); fatal paths then
// wait for Enter so the console window stays readable.
var dropMode bool

func main() {
	configPath := flag.String("config", "", "Path to TOML config file")
	verbose := flag.Bool("verbose", false, "Print the effective detector table to stderr at startup (also NEVR_AC_VERBOSE=1)")
	flag.BoolVar(verbose, "v", false, "Alias for --verbose")
	flag.Parse()
	startupVerbose = *verbose || os.Getenv("NEVR_AC_VERBOSE") == "1"

	args := flag.Args()
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}
	if isDropInvocation(args) {
		runDropMode(*configPath, args)
		return
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
	case "evidence-export":
		fs := flag.NewFlagSet("evidence-export", flag.ExitOnError)
		matchID := fs.String("match", "", "Export detections for this match instead of a review case")
		playerID := fs.String("player", "", "Player ID for --match")
		includeShadow := fs.Bool("include-shadow", false, "Include shadow events (match/player exports only)")
		before := fs.Int("before", 45, "Frames to include before each detection")
		after := fs.Int("after", 45, "Frames to include after each detection")
		format := fs.String("format", "auto", "Output format: auto, html, or json")
		force := fs.Bool("force", false, "Replace an existing output file")
		pos := parseSub(fs, sub)
		opts := evidenceExportOptions{
			matchID: *matchID, playerID: *playerID, includeShadow: *includeShadow,
			before: *before, after: *after, format: *format, force: *force,
		}
		if *matchID != "" {
			if len(pos) != 1 || *playerID == "" {
				fatalUsage("anticheat evidence-export --match <match-id> --player <player-id> <output.html|json> [--include-shadow]")
			}
			opts.outputPath = pos[0]
		} else {
			if len(pos) != 2 {
				fatalUsage("anticheat evidence-export <case-id> <output.html|json>")
			}
			opts.caseID, opts.outputPath = pos[0], pos[1]
		}
		runEvidenceExport(*configPath, opts)
	case "backup":
		if len(sub) != 1 {
			fatalUsage("anticheat backup <output.db>")
		}
		runBackup(*configPath, sub[0])
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
	case "observation-report":
		fs := flag.NewFlagSet("observation-report", flag.ExitOnError)
		since := fs.String("since", "", "Only matches newer than this window (e.g. 30d, 12h); default: all")
		asJSON := fs.Bool("json", false, "Print machine-readable JSON")
		if pos := parseSub(fs, sub); len(pos) != 0 {
			fatalUsage("anticheat observation-report [--since 30d] [--json]")
		}
		runObservationReport(*configPath, *since, *asJSON)
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
	if dropMode {
		pauseForEnter()
	}
	os.Exit(1)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	if dropMode {
		pauseForEnter()
	}
	os.Exit(1)
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "NEVR-Anticheat: Async cheat detection engine for Echo VR")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Usage: anticheat [--config <path>] [--verbose|-v] <command> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  analyze <file> [--force]   Analyze a replay file (stores telemetry + results; skips stored matches)")
	fmt.Fprintln(os.Stderr, "  batch <dir> [--force]      Batch analyze replays (stores telemetry + results; skips stored matches)")
	fmt.Fprintln(os.Stderr, "  flagged                    List pending review cases")
	fmt.Fprintln(os.Stderr, "  report <case-id>           Human-readable single-match case report with evidence")
	fmt.Fprintln(os.Stderr, "  evidence-export <case-id> <output.html|json>")
	fmt.Fprintln(os.Stderr, "                             Export an offline visual review bundle")
	fmt.Fprintln(os.Stderr, "  evidence-export --match <id> --player <id> <output> [--include-shadow]")
	fmt.Fprintln(os.Stderr, "                             Export match/player evidence without a scored case")
	fmt.Fprintln(os.Stderr, "  backup <output.db>         Create and verify a consistent SQLite snapshot")
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
	fmt.Fprintln(os.Stderr, "  observation-report [--since 30d] [--json]")
	fmt.Fprintln(os.Stderr, "                             Versioned shadow/scored event rates and evidence quality")
	fmt.Fprintln(os.Stderr, "  version                    Print version")
}

// app bundles what every command needs. The config-derived wiring (scorer,
// physics, level table, pipelines, analysis options) lives in replay.Engine
// so the desktop app and the CLI analyze replays identically.
type app struct {
	cfg    *config.Config
	store  *sqlite.Store
	engine *replay.Engine
	log    interface {
		Info(msg string, args ...any)
	}
}

// startupVerbose is set from --verbose / -v / NEVR_AC_VERBOSE=1. Config
// warnings are always logged (to stderr); the effective detector table is
// printed only when verbose so report output on stdout stays clean.
var startupVerbose bool

func openApp(configPath string) (*app, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	logger := logging.NewLogger(cfg.General.LogLevel, cfg.General.LogFormat)
	var table io.Writer
	if startupVerbose {
		table = os.Stderr
	}
	config.LogStartup(logger, cfg, table)
	store, err := sqlite.NewStore(cfg.General.DBPath)
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}
	return &app{cfg: cfg, store: store, engine: replay.NewEngine(cfg, store), log: logger}, nil
}

// scorerConfig is the [scoring] block as the scorer reads it (replay.Engine).
func (a *app) scorerConfig() scoring.ScorerConfig { return a.engine.ScorerConfig() }

// levels is the tier table shared by the in-match scorer, single-match review
// cases, cross-match aggregation and CLI output.
func (a *app) levels() model.LevelTable { return a.engine.Levels() }

// physics is the match physics built from the [physics] block.
func (a *app) physics() model.PhysicsConstants { return a.engine.Physics() }

// analysisOptions is what StoreMatchAnalysis needs to write cases the way
// the scorer scored them.
func (a *app) analysisOptions() replay.AnalysisOptions { return a.engine.AnalysisOptions() }

// newPipeline builds a fresh pipeline (own detectors, own scorer). Pipelines
// are not goroutine-safe; build one per worker.
func (a *app) newPipeline() *pipeline.Pipeline { return a.engine.NewPipeline() }

func (a *app) crossMatchConfig() sqlite.CrossMatchConfig { return a.engine.CrossMatchConfig() }

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

func runAnalyze(configPath, replayPath string, force bool) {
	a := mustOpen(configPath)
	defer a.store.Close()
	if err := analyzeReplay(context.Background(), a, replayPath, force); err != nil {
		fatal("Error: %v", err)
	}
}

// analyzeReplay is the analyze command over the engine: replay.AnalyzeFileAll
// parses the replay, and for every match it holds (a recording whose
// session id changes mid-file holds two) runs the pipeline and persists
// telemetry, context and derived outputs; the results are printed in the
// order they happened, one summary block per match. A match that is already
// stored is reported and left untouched unless force is set; with force the
// engine clears the previous analysis only after the match parsed
// completely, the pipeline ran and the source data was stored, so a
// truncated or corrupt replay never destroys the analysis it was meant to
// replace. A failure part-way through the file is returned after the
// matches finished before it were printed, and any persistence failure is
// returned, so the command exits non-zero.
func analyzeReplay(ctx context.Context, a *app, replayPath string, force bool) error {
	results, err := a.engine.AnalyzeFileAll(ctx, replayPath, force)
	printAnalyzeResults(results)
	if err != nil {
		return err
	}
	var errs []error
	for _, res := range results {
		if perr := res.PersistError(); perr != nil {
			errs = append(errs, fmt.Errorf("match %s: %w", res.MatchCtx.MatchID, perr))
		}
	}
	return errors.Join(errs...)
}

// printAnalyzeResults prints what AnalyzeFileAll did: the parse and the
// adapter diagnostics of the file once (.echoreplay; the report covers every
// match in it), then one block per match in file order.
func printAnalyzeResults(results []*replay.AnalyzeResult) {
	if len(results) == 0 {
		return
	}
	if diag := results[0].Diagnostics; diag != nil {
		// The adapter's count covers the whole file, including the matches
		// refused as already stored (their frames are not kept).
		fmt.Printf("Parsed %d player-frames from %s (%d lines rejected)\n", diag.FramesMapped, results[0].Path, diag.FramesRejected)
		fmt.Print(diag.FormatReport())
	}
	for i, res := range results {
		if i > 0 {
			fmt.Println()
		}
		printAnalyzeResult(res)
	}
}

// printAnalyzeResult prints what AnalyzeFileAll did for one match, in the
// order it happened: the refusal of a stored match, or the telemetry
// writes, the cleared analysis (--force), then the detection summary.
// Storage failures are not printed here: analyzeReplay returns them
// (AnalyzeResult.PersistError) so the command fails visibly instead of
// warning.
func printAnalyzeResult(res *replay.AnalyzeResult) {
	if res.AlreadyStored {
		fmt.Printf("Match %s is already stored; derived outputs left unchanged.\n", res.MatchCtx.MatchID)
		fmt.Println("Re-run with --force to replace its detection events and scores, or use reprocess-match.")
		return
	}
	if res.TelemetryErr == nil {
		fmt.Printf("Stored %d telemetry frames (%d already present)", res.Telemetry.Inserted, res.Telemetry.Ignored)
		if res.RawStored {
			fmt.Printf(", %d raw ticks (%d already present)", res.Telemetry.TicksInserted, res.Telemetry.TicksIgnored)
		}
		fmt.Println()
	}
	if res.Replaced {
		fmt.Printf("Cleared previous analysis for %s (%d events, %d score snapshots)\n",
			res.MatchCtx.MatchID, res.ClearedEvents, res.ClearedScores)
	}

	result := res.Result
	fmt.Printf("Match: %s\nFrames: %d processed, %d invalid\nDetections: %d (%d stored)\nReview cases: %d (%d stale closed)\nDuration: %v\n",
		result.MatchID, result.FramesProcessed, result.InvalidFrames,
		len(result.DetectionEvents), res.Stored.EventsStored, res.Stored.CasesStored, res.Stored.CasesClosed, result.Duration)
	printCountMap("Invalid frames by reason", result.InvalidFrameReasons)
	printCountMap("Sanitized frames by reason", result.SanitizedFrames)
	printPlayerScores(result.PlayerScores)
}

// printCountMap prints a reason -> count map sorted by count, largest first.
func printCountMap(title string, m map[string]int) {
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	fmt.Printf("%s:\n", title)
	for _, k := range keys {
		fmt.Printf("  %-32s %d\n", k, m[k])
	}
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
	if result.PersistFailed > 0 {
		fmt.Fprintf(os.Stderr, "Error: %d analyzed match(es) could not be persisted (see log); the store may be read-only, locked or full\n",
			result.PersistFailed)
	}

	// Post-batch cross-match aggregation: compute cumulative scores across all matches
	fmt.Println("\nRunning cross-match aggregation...")
	aggregated, cases := runCrossMatchAggregation(a)
	fmt.Printf("Cross-match: %d players aggregated, %d review cases\n", aggregated, cases)
	if result.Errors > 0 {
		fmt.Fprintf(os.Stderr, "Batch finished with %d error(s)\n", result.Errors)
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

type evidenceExportOptions struct {
	caseID        string
	matchID       string
	playerID      string
	outputPath    string
	format        string
	includeShadow bool
	before        int
	after         int
	force         bool
}

func runEvidenceExport(configPath string, opts evidenceExportOptions) {
	a := mustOpen(configPath)
	defer a.store.Close()
	bundle, abs, err := exportEvidence(context.Background(), a, opts)
	if err != nil {
		fatal("Error: %v", err)
	}
	fmt.Printf("Exported %d detection event(s), %d clip(s), and %d player-frame(s) to %s\n",
		len(bundle.DetectionEvents), len(bundle.Clips), len(bundle.Frames), abs)
}

func exportEvidence(ctx context.Context, a *app, opts evidenceExportOptions) (*evidence.ReplayBundle, string, error) {
	if opts.before < 0 || opts.after < 0 || opts.before > 900 || opts.after > 900 {
		return nil, "", fmt.Errorf("evidence frame window must be between 0 and 900 frames per side")
	}
	if strings.TrimSpace(opts.outputPath) == "" {
		return nil, "", fmt.Errorf("evidence output path is required")
	}
	if opts.caseID != "" && (opts.matchID != "" || opts.playerID != "") {
		return nil, "", fmt.Errorf("choose a review case or a match/player, not both")
	}
	if opts.includeShadow && opts.caseID != "" {
		return nil, "", fmt.Errorf("--include-shadow is available only with --match and --player")
	}

	exporter := evidence.NewExporter(a.store)
	exporter.SetWindow(opts.before, opts.after)
	var (
		bundle *evidence.ReplayBundle
		err    error
	)
	if opts.caseID != "" {
		bundle, err = exporter.ExportForCase(ctx, opts.caseID, nil)
	} else {
		bundle, err = exporter.ExportForMatchPlayer(ctx, opts.matchID, opts.playerID, opts.includeShadow, nil)
	}
	if err != nil {
		return nil, "", err
	}

	format := strings.ToLower(strings.TrimSpace(opts.format))
	if format == "" || format == "auto" {
		switch strings.ToLower(filepath.Ext(opts.outputPath)) {
		case ".html", ".htm":
			format = "html"
		case ".json":
			format = "json"
		default:
			return nil, "", fmt.Errorf("cannot infer evidence format from %q; use --format html or --format json", opts.outputPath)
		}
	}
	var data []byte
	if format == "html" {
		data, err = evidence.MarshalHTML(bundle)
	} else if format == "json" {
		data, err = evidence.MarshalBundle(bundle)
	} else {
		return nil, "", fmt.Errorf("unsupported evidence format %q (want html or json)", format)
	}
	if err != nil {
		return nil, "", err
	}
	abs, err := writeEvidenceFile(opts.outputPath, data, opts.force)
	if err != nil {
		return nil, "", err
	}
	return bundle, abs, nil
}

func writeEvidenceFile(path string, data []byte, force bool) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving evidence output: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return "", fmt.Errorf("creating evidence output directory: %w", err)
	}
	flags := os.O_WRONLY | os.O_CREATE
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(abs, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("evidence output already exists: %s (use --force to replace it)", abs)
		}
		return "", fmt.Errorf("creating evidence output: %w", err)
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(abs)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return "", fmt.Errorf("writing evidence output: %w", err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("syncing evidence output: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("closing evidence output: %w", err)
	}
	ok = true
	return abs, nil
}

func runBackup(configPath, outputPath string) {
	a := mustOpen(configPath)
	defer a.store.Close()
	abs, err := filepath.Abs(outputPath)
	if err != nil {
		fatal("Error: resolving backup path: %v", err)
	}
	if err := a.store.Backup(context.Background(), abs); err != nil {
		fatal("Error: %v", err)
	}
	if err := os.Chmod(abs, 0o600); err != nil {
		fatal("Backup was created but its permissions could not be restricted: %v", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		fatal("Backup was created but could not be inspected: %v", err)
	}
	fmt.Printf("Created verified database backup: %s (%d bytes)\n", abs, info.Size())
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

		// The review tier is high_risk (scoring.review_threshold) in the
		// same table the summary's Level was classified with.
		if levels.LevelFor(summary.DecayedScore).AtLeast(model.LevelHighRisk) {
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
	rc, err := recordVerdict(context.Background(), a, d)
	if err != nil {
		fatal("Error: %v", err)
	}
	fmt.Printf("Recorded %s for case %s by %s (case status -> %s)\n", d.Verdict, caseID, by, rc.Status)
	for _, fb := range feedback {
		fmt.Printf("  %-12s %s\n", fb.DetectorID, fb.Correct)
	}
}

// recordVerdict records a moderator decision through review.Queue.Decide so
// the case lifecycle applies: a case that is already decided or closed is
// refused instead of receiving a second decision that calibration would count
// as another reviewed case. Cross-match cases (XM-<player>) are not in the
// queue's case table; they are decided directly through the store, which
// applies the same decided/closed guard.
func recordVerdict(ctx context.Context, a *app, d model.ModeratorDecision) (model.ReviewCase, error) {
	q := review.NewQueue(a.store, logging.NewLogger(a.cfg.General.LogLevel, a.cfg.General.LogFormat))
	q.SetLevels(a.levels())
	rc, err := q.Decide(ctx, d)
	if err == nil {
		return rc, nil
	}
	if errors.Is(err, review.ErrInvalidTransition) {
		return rc, fmt.Errorf("case %s is %s and cannot receive another verdict (an appeal must reopen it first)", d.CaseID, rc.Status)
	}
	// Not a single-match case: try the cross-match table.
	if xm, xmErr := a.store.GetCrossMatchReviewCase(ctx, d.CaseID); xmErr == nil {
		if sErr := a.store.StoreModeratorDecision(ctx, d); sErr != nil {
			if errors.Is(sErr, sqlite.ErrCaseNotDecidable) {
				return rc, fmt.Errorf("case %s is %s and cannot receive another verdict", d.CaseID, xm.Status)
			}
			return rc, sErr
		}
		return model.ReviewCase{CaseID: xm.CaseID, PlayerID: xm.PlayerID, Status: model.CaseStatusDecided}, nil
	}
	return rc, err
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
		fmt.Println("DETECTOR CALIBRATION (all human labels)")
	} else {
		fmt.Printf("DETECTOR CALIBRATION (labels since %s)\n", since.UTC().Format(time.RFC3339))
	}
	fmt.Println("================================================================================")
	if len(rows) == 0 {
		fmt.Println("  No human labels recorded. Label observations in the desktop app or use `verdict <case-id> ...`.")
		return
	}
	fmt.Printf("  %-12s %9s %9s %9s %9s %6s %7s %7s %9s\n",
		"DETECTOR", "CONFIRMED", "FALSE_POS", "INCONCL", "NEED_DATA", "CASES", "EVENTS", "LABELS", "PRECISION")
	for _, c := range rows {
		prec := "n/a"
		if p, ok := c.Precision(); ok {
			prec = fmt.Sprintf("%.0f%%", p*100)
		}
		fmt.Printf("  %-12s %9d %9d %9d %9d %6d %7d %7d %9s\n",
			c.DetectorID, c.Confirmed, c.FalsePositive, c.Inconclusive, c.NeedsMoreData,
			c.CasesReviewed, c.EventsReviewed, c.DirectLabels, prec)
	}
	fmt.Println()
	fmt.Println("  Counts are per decided case in which the detector fired (shadow events included).")
	fmt.Println("  Explicit --detector feedback overrides the case verdict for that detector.")
	fmt.Println("  LABELS are direct detector verdicts recorded in the desktop app.")
}

type observationReport struct {
	GeneratedAt time.Time                         `json:"generated_at"`
	Since       *time.Time                        `json:"since,omitempty"`
	Stats       []sqlite.DetectorObservationStats `json:"stats"`
	Notice      string                            `json:"notice"`
}

func runObservationReport(configPath, sinceSpec string, asJSON bool) {
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
	stats, err := a.store.ComputeObservationStats(context.Background(), since)
	if err != nil {
		fatal("Error: %v", err)
	}
	if stats == nil {
		stats = []sqlite.DetectorObservationStats{}
	}
	const notice = "Observation counts are not detector validation; thresholds require labeled real telemetry and moderator verdicts."
	if asJSON {
		report := observationReport{GeneratedAt: time.Now().UTC(), Stats: stats, Notice: notice}
		if !since.IsZero() {
			u := since.UTC()
			report.Since = &u
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fatal("Error encoding observation report: %v", err)
		}
		return
	}

	fmt.Println("================================================================================")
	if since.IsZero() {
		fmt.Println("DETECTOR OBSERVATIONS (all stored events; split by detector version)")
	} else {
		fmt.Printf("DETECTOR OBSERVATIONS (matches since %s; split by detector version)\n", since.UTC().Format(time.RFC3339))
	}
	fmt.Println("================================================================================")
	if len(stats) == 0 {
		fmt.Println("  No detection events stored yet. Shadow deployment data will appear here.")
		fmt.Println()
		fmt.Println("  " + notice)
		return
	}
	fmt.Printf("  %-12s %-8s %7s %7s %7s %7s %7s %8s %8s %8s %8s\n",
		"DETECTOR", "VERSION", "EVENTS", "SHADOW", "SCORED", "MATCHES", "PLAYERS", "MAX/MATCH", "SEV P95", "CONF AVG", "CONF P05")
	for _, s := range stats {
		fmt.Printf("  %-12s %-8s %7d %7d %7d %7d %7d %8d %8.2f %8.2f %8.2f\n",
			s.DetectorID, s.DetectorVersion, s.Events, s.ShadowEvents, s.ScoredEvents,
			s.Matches, s.Players, s.MaxEventsMatch, s.P95Severity, s.MeanConfidence, s.P05Confidence)
	}
	fmt.Println()
	fmt.Println("  " + notice)
}

// reprocessMatchFromDB loads telemetry from the database and re-runs the
// detection pipeline. The previous detection events and per-match score
// snapshots are deleted only after the pipeline has run, immediately before
// the new outputs are stored, so a pipeline failure leaves the old analysis
// in place. Review cases are upserted under their deterministic ids and keep
// any moderator status; pending cases of players the new run no longer flags
// are closed as stale (replay.StoreMatchAnalysis).
func reprocessMatchFromDB(ctx context.Context, a *app, p *pipeline.Pipeline, matchID string) (*pipeline.MatchResult, error) {
	store := a.store
	matchCtx, err := store.GetMatchContext(ctx, matchID)
	if err != nil {
		return nil, fmt.Errorf("loading match context: %w", err)
	}
	// Reprocessing means "run under the current configuration", including
	// physics corrections such as the 18.9 m/s disc cap. Keeping constants
	// frozen in an old match context would make detector threshold changes
	// appear ineffective on historical data.
	matchCtx.Physics = a.physics()
	frames := []model.PlayerTelemetryFrame(nil)
	remapped := false
	if raw, remapErr := replay.RemapStoredTelemetry(ctx, store, matchCtx, a.physics()); remapErr == nil {
		matchCtx, frames, remapped = raw.Context, raw.Frames, true
		fmt.Printf("  Re-mapped %d raw ticks into %d current-schema player frames", raw.RawTicks, len(raw.Frames))
		if raw.MappingWarnings > 0 || raw.MappingErrors > 0 {
			fmt.Printf(" (%d mapping warnings, %d rejected player poses)", raw.MappingWarnings, raw.MappingErrors)
		}
		fmt.Println()
	} else if errors.Is(remapErr, replay.ErrNoRawTicks) {
		frames, err = store.GetMatchFrames(ctx, matchID)
		if err != nil {
			return nil, fmt.Errorf("loading frames: %w", err)
		}
	} else {
		return nil, remapErr
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no telemetry frames stored for match %s", matchID)
	}

	result, err := p.ProcessMatch(ctx, matchCtx, frames)
	if err != nil {
		return nil, err
	}
	// Only publish the regenerated normalized cache after the mapper and full
	// detection pipeline succeeded. ReplaceMatchTelemetryFrames is atomic and
	// never touches the immutable raw ticks.
	if remapped {
		if _, err := store.ReplaceMatchTelemetryFrames(ctx, matchID, frames); err != nil {
			return nil, fmt.Errorf("storing re-mapped telemetry: %w", err)
		}
	}
	if err := store.StoreMatchContext(ctx, matchCtx, len(frames)); err != nil {
		return nil, fmt.Errorf("storing current match context: %w", err)
	}

	deletedEvents, deletedScores, err := store.DeleteMatchAnalysis(ctx, matchID)
	if err != nil {
		return nil, fmt.Errorf("clearing old analysis: %w", err)
	}
	if deletedEvents > 0 || deletedScores > 0 {
		fmt.Printf("  Cleared %d old events and %d score snapshots for match %s\n", deletedEvents, deletedScores, matchID)
	}
	stored, err := replay.StoreMatchAnalysis(ctx, store, matchCtx, result, "reprocess", a.analysisOptions())
	if err != nil {
		return nil, err
	}
	if stored.CasesClosed > 0 {
		fmt.Printf("  Closed %d stale review case(s) for match %s\n", stored.CasesClosed, matchID)
	}
	// Refresh the human-facing document too. LoadMatchSummary reapplies the
	// new scores/events to an existing raw-derived match report; storing it
	// updates both its suspicion fields and the History "analyzed" timestamp.
	if summary, err := a.engine.LoadMatchSummary(ctx, matchCtx, result.PlayerScores, result.DetectionEvents); err == nil {
		if doc, marshalErr := json.Marshal(summary); marshalErr == nil {
			if storeErr := store.StoreMatchSummaryJSON(ctx, summary.Meta(), doc); storeErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: storing refreshed match summary: %v\n", storeErr)
			}
		}
	} else if !errors.Is(err, replay.ErrNoRawTicks) {
		fmt.Fprintf(os.Stderr, "Warning: refreshing match summary: %v\n", err)
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
func isEchoReplay(path string) bool { return replay.IsEchoReplay(path) }

// isDropInvocation reports whether the CLI was started with replay files or
// folders instead of a command, e.g. by dropping files onto nevr-ac.exe.
func isDropInvocation(args []string) bool {
	switch args[0] {
	case "version", "analyze", "batch", "flagged", "report", "cross-match-report",
		"player-history", "cross-match", "reprocess-match", "reprocess-player",
		"reprocess-timerange", "verdict", "calibration-report", "help", "-h", "--help":
		return false
	}
	for _, a := range args {
		if _, err := os.Stat(a); err != nil {
			return false
		}
	}
	return true
}

// runDropMode analyzes every dropped file (analyze) or folder (batch), prints
// the flagged summary and waits for Enter so an Explorer-launched window stays
// open. Without --config the database is kept next to the executable so
// results accumulate in one place regardless of where the replays live.
func runDropMode(configPath string, paths []string) {
	dropMode = true
	if configPath == "" {
		if exe, err := os.Executable(); err == nil {
			_ = os.Chdir(filepath.Dir(exe))
		}
	}
	fmt.Printf("NEVR-Anticheat %s: analyzing %d dropped item(s)\n\n", appVersion, len(paths))
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", p, err)
			continue
		}
		fmt.Printf("== %s ==\n", p)
		if info.IsDir() {
			runBatch(configPath, p, false)
		} else {
			runAnalyze(configPath, p, false)
		}
		fmt.Println()
	}
	fmt.Println("== Flagged players and pending cases ==")
	runFlagged(configPath)
	pauseForEnter()
}

func pauseForEnter() {
	fmt.Fprint(os.Stderr, "\nPress Enter to close...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
