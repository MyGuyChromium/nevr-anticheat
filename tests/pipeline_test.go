package tests

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/throw"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

func newTestPipeline(detectors []detect.Detector) (*pipeline.Pipeline, *scoring.SuspicionScorer) {
	cfg := config.DefaultConfig()
	// Set all detectors to enforce mode for testing (default is shadow)
	for id, dc := range cfg.Detectors {
		dc.Mode = "enforce"
		cfg.Detectors[id] = dc
	}
	cfg.Scoring.ReviewThreshold = 15 // one detector firing once (15 points) reaches the review tier
	scorer := scoring.NewSuspicionScorer(scoring.ScorerConfig{
		MaxSingleContribution:         cfg.Scoring.MaxSingleContribution,
		MaxContribPerDetectorPerMatch: cfg.Scoring.MaxContribPerDetectorPerMatch,
		SameCategoryDiminishing:       cfg.Scoring.SameCategoryDiminishing,
		ReviewThreshold:               cfg.Scoring.ReviewThreshold,
		AutoEnforceThreshold:          cfg.Scoring.AutoEnforceThreshold,
		DecayHalfLifeHours:            cfg.Scoring.DecayHalfLifeHours,
		CooldownFrames:                cfg.Pipeline.CooldownFrames,
		CorrelationBonusCap:           cfg.Scoring.CorrelationBonusCap,
		Levels:                        cfg.Scoring.LevelTable(),
	})
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := pipeline.NewPipeline(cfg, detectors, scorer, logger)
	return p, scorer
}

func TestPipeline_CleanMatch_NoDetections(t *testing.T) {
	cfg := config.DefaultConfig()
	detectors := []detect.Detector{
		movement.NewMov001(cfg.GetDetectorConfig("MOV_001").Params),
		movement.NewMov002(cfg.GetDetectorConfig("MOV_002").Params),
	}
	p, scorer := newTestPipeline(detectors)

	matchCtx := testutil.NewMatchContext()
	frames := testutil.GenerateCleanFrames("player1", 120)

	result, err := p.ProcessMatch(context.Background(), matchCtx, frames)
	if err != nil {
		t.Fatalf("pipeline error: %v", err)
	}

	if len(result.DetectionEvents) > 0 {
		t.Errorf("expected no detections for clean match, got %d", len(result.DetectionEvents))
		for _, ev := range result.DetectionEvents {
			t.Logf("  detection: %s sev=%.2f conf=%.2f observed=%s",
				ev.DetectorID, ev.Severity, ev.Confidence, ev.ObservedValue)
		}
	}
	if result.FramesProcessed == 0 {
		t.Error("expected some frames to be processed")
	}

	score := scorer.GetScore("player1")
	if score.TotalScore > 0 {
		t.Errorf("expected zero score for clean match, got %.2f", score.TotalScore)
	}
}

func TestPipeline_SpeedHack_Detected(t *testing.T) {
	cfg := config.DefaultConfig()
	detectors := []detect.Detector{
		movement.NewMov001(cfg.GetDetectorConfig("MOV_001").Params),
	}
	p, scorer := newTestPipeline(detectors)

	matchCtx := testutil.NewMatchContext()

	// A sustained 75 m/s hack (hands riding on the body, so the frames are
	// internally consistent and every one passes the validator).
	frames := testutil.NewFrameBuilder("player1").WithStartPos(model.Vec3{2, 1.6, 0}).SpeedHackFrames(90, 75)

	result, err := p.ProcessMatch(context.Background(), matchCtx, frames)
	if err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	if result.InvalidFrames != 0 {
		t.Fatalf("%d frames rejected: %v", result.InvalidFrames, result.InvalidFrameReasons)
	}
	if len(result.DetectionEvents) != 2 { // one per full 30-frame window after warmup
		t.Errorf("expected 2 MOV_001 detections, got %d", len(result.DetectionEvents))
	}

	score := scorer.GetScore("player1")
	if score.TotalScore <= 0 {
		t.Errorf("expected positive score for speed hack, got %.2f", score.TotalScore)
	}
	t.Logf("speed hack: %d detections, score=%.2f", len(result.DetectionEvents), score.TotalScore)
}

func TestPipeline_HighPing_NoFalsePositive(t *testing.T) {
	cfg := config.DefaultConfig()
	detectors := []detect.Detector{
		throw.NewThrow001(cfg.GetDetectorConfig("THROW_001").Params),
		movement.NewMov001(cfg.GetDetectorConfig("MOV_001").Params),
	}
	p, scorer := newTestPipeline(detectors)

	matchCtx := testutil.NewMatchContext()

	// Fast (30 m/s) legitimate flight and legitimate throws at 200 ms ping
	// with jittered sample timing: enough motion for MOV_001 and enough
	// releases for THROW_001 to have something to judge.
	fb := testutil.NewFrameBuilder("player1").WithStartPos(model.Vec3{2, 1.6, 0}).WithPing(200)
	moving := fb.HighPingPlayer(200, 200)
	throws := fb.After(moving).EliteThrowSequence(6)
	frames := testutil.Concat(moving, throws)

	result, err := p.ProcessMatch(context.Background(), matchCtx, frames)
	if err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	if result.InvalidFrames != 0 || result.FramesProcessed != len(frames) {
		t.Fatalf("processed %d of %d, %d invalid", result.FramesProcessed, len(frames), result.InvalidFrames)
	}
	if len(result.DetectionEvents) != 0 {
		for _, ev := range result.DetectionEvents {
			t.Errorf("high-ping legit player flagged: %s sev=%.2f %s", ev.DetectorID, ev.Severity, ev.ObservedValue)
		}
	}
	if score := scorer.GetScore("player1"); score.TotalScore != 0 || scorer.ExceedsReviewThreshold("player1") {
		t.Errorf("high-ping clean player scored %.2f", score.TotalScore)
	}
}
