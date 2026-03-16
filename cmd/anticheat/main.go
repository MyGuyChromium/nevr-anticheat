// NEVR-Anticheat: Server-side anticheat for Echo VR / Echo Arena.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/bio"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/pattern"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/state"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/throw"
	"github.com/nevr-anticheat/nevr-anticheat/internal/evidence"
	"github.com/nevr-anticheat/nevr-anticheat/internal/logging"
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

	switch args[0] {
	case "version":
		fmt.Printf("nevr-anticheat %s\n", appVersion)
		return
	case "analyze":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: anticheat analyze <replay-file>")
			os.Exit(1)
		}
		runAnalyze(*configPath, args[1])
	case "batch":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: anticheat batch <directory>")
			os.Exit(1)
		}
		runBatch(*configPath, args[1])
	case "flagged":
		runFlagged(*configPath)
	case "report":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: anticheat report <case-id>")
			os.Exit(1)
		}
		runReport(*configPath, args[1])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "NEVR-Anticheat: Server-side anticheat for Echo VR")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Usage: anticheat [--config <path>] <command> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  analyze <file>     Analyze a single replay file")
	fmt.Fprintln(os.Stderr, "  batch <dir>        Batch analyze all replays in directory")
	fmt.Fprintln(os.Stderr, "  flagged            List all flagged players")
	fmt.Fprintln(os.Stderr, "  report <case-id>   Generate human-readable report")
	fmt.Fprintln(os.Stderr, "  version            Print version")
}

// registerAllDetectors constructs and configures all 29 detectors from config.
func registerAllDetectors(cfg *config.Config) []detect.Detector {
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
		params := dc.Params
		if params == nil {
			params = make(map[string]any)
		}
		d := e.factory(params)
		detectors = append(detectors, d)
	}
	return detectors
}

func buildPipeline(configPath string) (*config.Config, *pipeline.Pipeline, *scoring.SuspicionScorer, *sqlite.Store, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("loading config: %w", err)
	}
	logger := logging.NewLogger(cfg.General.LogLevel, cfg.General.LogFormat)
	store, err := sqlite.NewStore(cfg.General.DBPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("opening store: %w", err)
	}
	detectors := registerAllDetectors(cfg)
	scorer := scoring.NewSuspicionScorer(scoring.ScorerConfig{
		MaxSingleContribution:         cfg.Scoring.MaxSingleContribution,
		MaxContribPerDetectorPerMatch: cfg.Scoring.MaxContribPerDetectorPerMatch,
		SameCategoryDiminishing:       cfg.Scoring.SameCategoryDiminishing,
		ReviewThreshold:               cfg.Scoring.ReviewThreshold,
		AutoEnforceThreshold:          cfg.Scoring.AutoEnforceThreshold,
		DecayHalfLifeHours:            cfg.Scoring.DecayHalfLifeHours,
		CooldownFrames:                cfg.Pipeline.CooldownFrames,
		CorrelationBonusCap:           cfg.Scoring.CorrelationBonusCap,
	})
	p := pipeline.NewPipeline(cfg, detectors, scorer, logger)
	logger.Info("pipeline ready", "detectors", len(detectors))
	return cfg, p, scorer, store, nil
}

func runAnalyze(configPath, replayPath string) {
	_, p, _, store, err := buildPipeline(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Auto-detect format: .echoreplay (NDJSON/ZIP) vs legacy JSON replay
	var matchCtx *model.MatchContext
	var frames []model.PlayerTelemetryFrame

	if isEchoReplay(replayPath) {
		parser := adapter.NewEchoReplayParser()
		var diag *adapter.DiagnosticReport
		matchCtx, frames, diag, err = parser.ParseFile(replayPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading echoreplay: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Parsed %d player-frames from %s (%d rejected)\n",
			len(frames), replayPath, diag.FramesRejected)
	} else {
		reader := replay.NewReplayReader(replayPath, replay.NewJSONFrameParser())
		matchCtx, frames, err = reader.ReadMatch()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading replay: %v\n", err)
			os.Exit(1)
		}
	}
	result, err := p.ProcessMatch(context.Background(), matchCtx, frames)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	ctx := context.Background()
	for _, ev := range result.DetectionEvents {
		_ = store.StoreDetectionEvent(ctx, ev)
	}
	for _, score := range result.PlayerScores {
		_ = store.StoreSuspicionScore(ctx, score)
	}
	fmt.Printf("Match: %s\nFrames: %d processed, %d invalid\nDetections: %d\nDuration: %v\n",
		result.MatchID, result.FramesProcessed, result.InvalidFrames,
		len(result.DetectionEvents), result.Duration)
	for pid, score := range result.PlayerScores {
		if score.EventCount > 0 {
			fmt.Printf("  Player %s: score=%.1f level=%s events=%d\n",
				pid, score.TotalScore, score.Level(), score.EventCount)
		}
	}
}

func runBatch(configPath, dir string) {
	cfg, p, _, store, err := buildPipeline(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()
	analyzer := replay.NewBatchAnalyzer(p, store,
		func() replay.FrameParser { return replay.NewJSONFrameParser() },
		cfg.General.MaxWorkers, logging.NewLogger("info", "text"))
	result, err := analyzer.AnalyzeDirectory(context.Background(), dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Batch: %d/%d processed, %d errors, %d flagged, %v\n",
		result.Processed, result.TotalFiles, result.Errors, len(result.FlaggedPlayers), result.Duration)
}

func runFlagged(configPath string) {
	_, _, _, store, err := buildPipeline(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()
	cases, err := store.GetPendingReviewCases(context.Background(), 100)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(cases) == 0 {
		fmt.Println("No flagged players.")
		return
	}
	for _, rc := range cases {
		fmt.Printf("%-30s %-15s score=%.1f %s\n", rc.CaseID, rc.PlayerID, rc.SuspicionScore, rc.Severity)
	}
}

func runReport(configPath, caseID string) {
	_, _, _, store, err := buildPipeline(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()
	rc, err := store.GetReviewCase(context.Background(), caseID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(evidence.FormatReport(rc))
}

// isEchoReplay returns true if the file path looks like an Echo VR replay.
func isEchoReplay(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".echoreplay"
}
