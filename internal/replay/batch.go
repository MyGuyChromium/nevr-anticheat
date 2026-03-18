package replay

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// BatchResult holds the results of batch analysis.
type BatchResult struct {
	TotalFiles     int           `json:"total_files"`
	Processed      int           `json:"processed"`
	Errors         int           `json:"errors"`
	FlaggedPlayers []string      `json:"flagged_players"`
	Duration       time.Duration `json:"duration"`
}

// BatchAnalyzer processes directories of replay files.
type BatchAnalyzer struct {
	pipeline *pipeline.Pipeline
	store    *sqlite.Store
	parser   func() FrameParser
	workers  int
	logger   *slog.Logger
	pipelineMu sync.Mutex
}

// NewBatchAnalyzer creates a new batch analyzer.
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
	return &BatchAnalyzer{
		pipeline: p,
		store:    store,
		parser:   parserFactory,
		workers:  workers,
		logger:   logger,
	}
}

// AnalyzeDirectory processes all replay files in a directory.
func (ba *BatchAnalyzer) AnalyzeDirectory(ctx context.Context, dir string) (*BatchResult, error) {
	start := time.Now()
	result := &BatchResult{}
	seenMatches := make(map[string]string) // match_id -> first file path

	// Find replay files
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		ext := filepath.Ext(path)
		if ext == ".echoreplay" || ext == ".json" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking directory: %w", err)
	}

	result.TotalFiles = len(files)
	if len(files) == 0 {
		return result, nil
	}

	// Process with worker pool
	fileCh := make(chan string, len(files))
	for _, f := range files {
		fileCh <- f
	}
	close(fileCh)

	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < ba.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range fileCh {
				select {
				case <-ctx.Done():
					return
				default:
				}

				ba.pipelineMu.Lock()
			matchResult, err := ba.analyzeFile(ctx, path, seenMatches)
			ba.pipelineMu.Unlock()
				mu.Lock()
				if err != nil {
					result.Errors++
					ba.logger.Warn("replay analysis failed", "path", path, "error", err)
				} else {
					result.Processed++
					for pid, score := range matchResult.PlayerScores {
						if score.ExceedsReview {
							result.FlaggedPlayers = append(result.FlaggedPlayers, pid)
						}
					}
				}
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	result.Duration = time.Since(start)
	return result, nil
}

func (ba *BatchAnalyzer) analyzeFile(ctx context.Context, path string, seenMatches map[string]string) (*pipeline.MatchResult, error) {
	var matchCtx *model.MatchContext
	var frames []model.PlayerTelemetryFrame
	var rawByFrame map[int]string // non-nil only for .echoreplay files

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".echoreplay" {
		// Use the EchoReplayParser which handles NDJSON/ZIP and captures raw profiler JSON.
		parser := adapter.NewEchoReplayParser()
		var diag *adapter.DiagnosticReport
		var err error
		matchCtx, frames, diag, err = parser.ParseFile(path)
		if err != nil {
			return nil, err
		}
		rawByFrame = parser.RawSessionByFrame()
		_ = diag // diagnostics available but not surfaced in batch mode
	} else {
		reader := NewReplayReader(path, ba.parser())
		var err error
		matchCtx, frames, err = reader.ReadMatch()
		if err != nil {
			return nil, err
		}
	}

	// Duplicate same-match suppression: if this match_id was already processed
	// in this batch run, skip pipeline/detection to avoid inflated detector counts.
	if firstFile, seen := seenMatches[matchCtx.MatchID]; seen {
		ba.logger.Info("skipping duplicate match in batch",
			"match_id", matchCtx.MatchID,
			"skipped_file", path,
			"first_file", firstFile,
		)
		return &pipeline.MatchResult{}, nil
	}
	seenMatches[matchCtx.MatchID] = path

	matchResult, err := ba.pipeline.ProcessMatch(ctx, matchCtx, frames)
	if err != nil {
		return nil, err
	}

	// Persist telemetry and match context for future reprocessing.
	// For .echoreplay sources, raw_json contains the original profiler API payload.
	// For legacy JSON sources, raw_json is NULL (the format IS the normalized schema).
	if len(rawByFrame) > 0 {
		rows := make([]sqlite.TelemetryFrameRow, len(frames))
		for i, f := range frames {
			rows[i] = sqlite.TelemetryFrameRow{Frame: f, RawJSON: rawByFrame[f.FrameIndex]}
		}
		if _, storeErr := ba.store.StoreTelemetryFrameRows(ctx, matchCtx.MatchID, rows); storeErr != nil {
			ba.logger.Warn("failed to store telemetry", "error", storeErr)
		}
	} else {
		if _, storeErr := ba.store.StoreTelemetryFrames(ctx, matchCtx.MatchID, frames); storeErr != nil {
			ba.logger.Warn("failed to store telemetry", "error", storeErr)
		}
	}
	_ = ba.store.StoreMatchContext(ctx, matchCtx, len(frames))

	// Store detection results
	for _, ev := range matchResult.DetectionEvents {
		if storeErr := ba.store.StoreDetectionEvent(ctx, ev); storeErr != nil {
			ba.logger.Warn("failed to store event", "error", storeErr)
		}
	}
	for _, score := range matchResult.PlayerScores {
		if storeErr := ba.store.StoreSuspicionScore(ctx, score); storeErr != nil {
			ba.logger.Warn("failed to store score", "error", storeErr)
		}
	}

	return matchResult, nil
}
