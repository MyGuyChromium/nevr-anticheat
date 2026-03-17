// NEVR-Anticheat: Server-side anticheat for Echo VR / Echo Arena.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"time"

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
	case "player-history":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: anticheat player-history <player-id>")
			os.Exit(1)
		}
		runPlayerHistory(*configPath, args[1])
	case "cross-match":
		runCrossMatchAnalysis(*configPath)
	case "reprocess-match":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: anticheat reprocess-match <match-id>")
			os.Exit(1)
		}
		runReprocessMatch(*configPath, args[1])
	case "reprocess-player":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: anticheat reprocess-player <player-id>")
			os.Exit(1)
		}
		runReprocessPlayer(*configPath, args[1])
	case "reprocess-timerange":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: anticheat reprocess-timerange <since-RFC3339> <until-RFC3339>")
			os.Exit(1)
		}
		runReprocessTimeRange(*configPath, args[1], args[2])
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
	fmt.Fprintln(os.Stderr, "  analyze <file>             Analyze a replay file (stores telemetry + results)")
	fmt.Fprintln(os.Stderr, "  batch <dir>                Batch analyze replays (stores telemetry + results)")
	fmt.Fprintln(os.Stderr, "  flagged                    List all flagged players")
	fmt.Fprintln(os.Stderr, "  report <case-id>           Generate human-readable report")
	fmt.Fprintln(os.Stderr, "  player-history <id>        Show cross-match history for a player (from DB)")
	fmt.Fprintln(os.Stderr, "  cross-match                Run cross-match aggregation on stored data")
	fmt.Fprintln(os.Stderr, "  reprocess-match <id>       Re-run detection on stored telemetry for a match")
	fmt.Fprintln(os.Stderr, "  reprocess-player <id>      Re-run detection on stored telemetry for a player")
	fmt.Fprintln(os.Stderr, "  reprocess-timerange <a> <b> Re-run detection on matches in time range (RFC3339)")
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
		params := dc.Params
		if params == nil {
			params = make(map[string]any)
		}
		// Inject history provider for PAT_003 (cross-match consistency)
		if e.id == "PAT_003" && historyProvider != nil {
			params["history_provider"] = historyProvider
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
	// Run v2+ migrations (telemetry_frames, match_contexts, cross_match_review_cases)
	if err := sqlite.RunMigrationsV2(store.DB(), logger); err != nil {
		logger.Warn("migration warning", "error", err)
	}
	// Wire up the history provider so PAT_003 can query cross-match data from the database.
	historyProvider := sqlite.NewStoreHistoryProvider(store)
	detectors := registerAllDetectors(cfg, historyProvider)
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

	// Persist raw telemetry and match context so reprocessing doesn't need replay files
	if stored, err := store.StoreTelemetryFrames(ctx, matchCtx.MatchID, frames); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to store telemetry: %v\n", err)
	} else {
		fmt.Printf("Stored %d telemetry frames to database\n", stored)
	}
	_ = store.StoreMatchContext(ctx, matchCtx, len(frames))

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

	// Post-batch cross-match aggregation: compute cumulative scores across all matches
	fmt.Println("\nRunning cross-match aggregation...")
	aggregated, cases := runCrossMatchAggregation(store, cfg.Scoring.DecayHalfLifeHours, cfg.Scoring.ReviewThreshold)
	fmt.Printf("Cross-match: %d players aggregated, %d review cases\n", aggregated, cases)
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

func runPlayerHistory(configPath, playerID string) {
	cfg, _, _, store, err := buildPipeline(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	ctx := context.Background()
	events, err := store.GetAllPlayerEvents(ctx, playerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(events) == 0 {
		fmt.Printf("No detection events found for player %s\n", playerID)
		return
	}

	summary := sqlite.ComputePlayerCrossMatchSummary(events, cfg.Scoring.DecayHalfLifeHours)

	fmt.Printf("================================================================================\n")
	fmt.Printf("           CROSS-MATCH PLAYER HISTORY: %s\n", playerID)
	fmt.Printf("================================================================================\n\n")
	fmt.Printf("  Total Events:       %d\n", summary.TotalEvents)
	fmt.Printf("  Distinct Matches:   %d\n", summary.DistinctMatches)
	fmt.Printf("  Distinct Detectors: %d\n", summary.DistinctDetectors)
	fmt.Printf("  Cumulative Score:   %.1f (raw, no decay)\n", summary.CumulativeScore)
	fmt.Printf("  Decayed Score:      %.1f (half-life: %.0fh)\n", summary.DecayedScore, cfg.Scoring.DecayHalfLifeHours)
	fmt.Printf("  Avg Severity:       %.3f\n", summary.AvgSeverity)
	fmt.Printf("  Avg Confidence:     %.3f\n", summary.AvgConfidence)
	fmt.Printf("  Level:              %s\n\n", summary.Level)

	fmt.Printf("  BY DETECTOR:\n")
	for det, count := range summary.ByDetector {
		fmt.Printf("    %-15s %d events\n", det, count)
	}
	fmt.Printf("\n  BY MATCH:\n")
	for matchID, count := range summary.ByMatch {
		fmt.Printf("    %-40s %d events\n", matchID, count)
	}
	fmt.Println()
}

func runCrossMatchAnalysis(configPath string) {
	cfg, _, _, store, err := buildPipeline(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	aggregated, cases := runCrossMatchAggregation(store, cfg.Scoring.DecayHalfLifeHours, cfg.Scoring.ReviewThreshold)
	fmt.Printf("Cross-match aggregation complete: %d players analyzed, %d review cases created\n", aggregated, cases)
}

// runCrossMatchAggregation queries all stored events, computes per-player cross-match
// summaries with time decay, stores cumulative score snapshots, and generates
// cross-match review cases for players exceeding the review threshold.
func runCrossMatchAggregation(store *sqlite.Store, decayHalfLifeHours, reviewThreshold float64) (int, int) {
	ctx := context.Background()

	// Get all players with events in the last 90 days
	since := time.Now().Add(-90 * 24 * time.Hour)
	players, err := store.GetDistinctPlayersWithEvents(ctx, since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting players: %v\n", err)
		return 0, 0
	}

	playerCount := 0
	caseCount := 0
	for _, pid := range players {
		events, err := store.GetAllPlayerEvents(ctx, pid)
		if err != nil {
			continue
		}
		if len(events) == 0 {
			continue
		}

		summary := sqlite.ComputePlayerCrossMatchSummary(events, decayHalfLifeHours)

		// Only store if there are events across multiple matches
		if summary.DistinctMatches >= 2 {
			if err := store.StoreCrossMatchScore(ctx, summary); err != nil {
				fmt.Fprintf(os.Stderr, "Error storing cross-match score for %s: %v\n", pid, err)
				continue
			}

			if summary.DecayedScore >= 40 {
				fmt.Printf("  [%s] %s: decayed=%.1f raw=%.1f matches=%d events=%d\n",
					summary.Level, pid, summary.DecayedScore, summary.CumulativeScore,
					summary.DistinctMatches, summary.TotalEvents)
			}

			// Generate cross-match review case if threshold exceeded
			if rc := sqlite.BuildCrossMatchReviewCase(summary, reviewThreshold); rc != nil {
				if err := store.StoreCrossMatchReviewCase(ctx, *rc); err != nil {
					fmt.Fprintf(os.Stderr, "Error storing cross-match case for %s: %v\n", pid, err)
				} else {
					caseCount++
				}
			}
			playerCount++
		}
	}
	return playerCount, caseCount
}

// reprocessMatchFromDB loads telemetry from the database and re-runs the detection pipeline.
// It deletes existing detection events for this match first to prevent duplication.
func reprocessMatchFromDB(ctx context.Context, store *sqlite.Store, p *pipeline.Pipeline, matchID string) (*pipeline.MatchResult, error) {
	matchCtx, err := store.GetMatchContext(ctx, matchID)
	if err != nil {
		return nil, fmt.Errorf("loading match context: %w", err)
	}
	frames, err := store.GetMatchFrames(ctx, matchID)
	if err != nil {
		return nil, fmt.Errorf("loading frames: %w", err)
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no telemetry frames stored for match %s", matchID)
	}

	// Delete old detection events for this match to prevent duplication on reprocessing.
	if deleted, err := store.DeleteMatchEvents(ctx, matchID); err != nil {
		return nil, fmt.Errorf("clearing old events: %w", err)
	} else if deleted > 0 {
		fmt.Printf("  Cleared %d old events for match %s\n", deleted, matchID)
	}

	result, err := p.ProcessMatch(ctx, matchCtx, frames)
	if err != nil {
		return nil, err
	}

	// Store fresh detection results
	for _, ev := range result.DetectionEvents {
		_ = store.StoreDetectionEvent(ctx, ev)
	}
	for _, score := range result.PlayerScores {
		_ = store.StoreSuspicionScore(ctx, score)
	}
	return result, nil
}

func runReprocessMatch(configPath, matchID string) {
	_, p, _, store, err := buildPipeline(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	ctx := context.Background()
	result, err := reprocessMatchFromDB(ctx, store, p, matchID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Reprocessed match %s: %d frames, %d detections, %v\n",
		matchID, result.FramesProcessed, len(result.DetectionEvents), result.Duration)
	for pid, score := range result.PlayerScores {
		if score.EventCount > 0 {
			fmt.Printf("  Player %s: score=%.1f level=%s events=%d\n",
				pid, score.TotalScore, score.Level(), score.EventCount)
		}
	}
}

func runReprocessPlayer(configPath, playerID string) {
	_, p, _, store, err := buildPipeline(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	ctx := context.Background()
	matchFrames, err := store.GetPlayerFrames(ctx, playerID, 50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading player frames: %v\n", err)
		os.Exit(1)
	}
	if len(matchFrames) == 0 {
		fmt.Printf("No stored telemetry found for player %s\n", playerID)
		os.Exit(1)
	}

	fmt.Printf("Reprocessing %d matches for player %s...\n", len(matchFrames), playerID)
	totalDetections := 0
	for matchID := range matchFrames {
		result, err := reprocessMatchFromDB(ctx, store, p, matchID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Match %s: error: %v\n", matchID, err)
			continue
		}
		detections := len(result.DetectionEvents)
		totalDetections += detections
		fmt.Printf("  Match %s: %d frames, %d detections\n", matchID, result.FramesProcessed, detections)
	}
	fmt.Printf("Total: %d matches reprocessed, %d detections\n", len(matchFrames), totalDetections)
}

func runReprocessTimeRange(configPath, sinceStr, untilStr string) {
	_, p, _, store, err := buildPipeline(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	since, err := time.Parse(time.RFC3339, sinceStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid since time (use RFC3339): %v\n", err)
		os.Exit(1)
	}
	until, err := time.Parse(time.RFC3339, untilStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid until time (use RFC3339): %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	matchIDs, err := store.GetMatchIDsByTimeRange(ctx, since, until)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error querying matches: %v\n", err)
		os.Exit(1)
	}
	if len(matchIDs) == 0 {
		fmt.Println("No matches found in the given time range.")
		return
	}

	fmt.Printf("Reprocessing %d matches from %s to %s...\n", len(matchIDs), sinceStr, untilStr)
	totalDetections := 0
	for _, matchID := range matchIDs {
		result, err := reprocessMatchFromDB(ctx, store, p, matchID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Match %s: error: %v\n", matchID, err)
			continue
		}
		detections := len(result.DetectionEvents)
		totalDetections += detections
		if detections > 0 {
			fmt.Printf("  Match %s: %d frames, %d detections\n", matchID, result.FramesProcessed, detections)
		}
	}
	fmt.Printf("Total: %d matches reprocessed, %d detections\n", len(matchIDs), totalDetections)
}

// isEchoReplay returns true if the file path looks like an Echo VR replay.
func isEchoReplay(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".echoreplay"
}
