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

	// Create frames where player moves impossibly fast.
	// Start at (1,1,1) to avoid zero-position rejection.
	frames := make([]model.PlayerTelemetryFrame, 60)
	for i := 0; i < 60; i++ {
		ts := float64(i) * 0.067
		// Oscillate on Z axis (long arena axis) at impossible speed.
		// At 0.067s per frame, moving 5m per frame = ~75 m/s (well over 55 limit).
		z := 5.0 + float64(i%6)*5.0 // oscillates 5-30m on Z
		pos := model.Vec3{1.0, 1.0, z}
		frames[i] = model.PlayerTelemetryFrame{
			PlayerID:          "player1",
			FrameIndex:        i,
			Timestamp:         ts,
			DeltaTime:         0.067,
			Position:          pos,
			Rotation:          model.QuatIdentity(),
			LeftHandPosition:  model.Vec3{pos[0] - 0.3, 0.3, 0.2},
			RightHandPosition: model.Vec3{pos[0] + 0.3, 0.3, -0.2},
			LeftHandRotation:  model.QuatIdentity(),
			RightHandRotation: model.QuatIdentity(),
			GamePhase:         "playing",
		}
	}

	result, err := p.ProcessMatch(context.Background(), matchCtx, frames)
	if err != nil {
		t.Fatalf("pipeline error: %v", err)
	}

	if len(result.DetectionEvents) == 0 {
		t.Error("expected speed hack detections, got none")
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

	// Create frames with high ping but otherwise clean gameplay
	frames := testutil.GenerateCleanFrames("player1", 120)
	for i := range frames {
		frames[i].EstimatedPingMs = 200.0 // high ping
	}

	result, err := p.ProcessMatch(context.Background(), matchCtx, frames)
	if err != nil {
		t.Fatalf("pipeline error: %v", err)
	}

	// Should have very few or no detections despite high ping
	score := scorer.GetScore("player1")
	t.Logf("high ping test: %d detections, score=%.2f", len(result.DetectionEvents), score.TotalScore)

	// The score should be low - not exceeding review threshold
	if scorer.ExceedsReviewThreshold("player1") {
		t.Errorf("high-ping clean player should not exceed review threshold, score=%.2f",
			score.TotalScore)
	}
}
