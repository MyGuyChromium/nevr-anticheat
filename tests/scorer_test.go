package tests

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

func scorerConfigFromCfg(cfg *config.Config) scoring.ScorerConfig {
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

func newTestScorer() *scoring.SuspicionScorer {
	cfg := config.DefaultConfig()
	return scoring.NewSuspicionScorer(scorerConfigFromCfg(cfg))
}

func TestScorer_SingleEvent(t *testing.T) {
	scorer := newTestScorer()
	evt := testutil.MakeDetectionEvent("THROW_001", "player1", 0.9, 0.95)
	evt.MatchID = "match1"
	evt.EnforcementWeight = 0.8

	result := scorer.IngestEvent(evt)
	if result.EventCount == 0 {
		t.Fatal("expected event to be processed")
	}

	score := scorer.GetScore("player1")
	if score.TotalScore <= 0 {
		t.Errorf("expected positive score, got %.4f", score.TotalScore)
	}
	if score.EventCount != 1 {
		t.Errorf("expected 1 event, got %d", score.EventCount)
	}
}

func TestScorer_CooldownDedup(t *testing.T) {
	scorer := newTestScorer()

	evt1 := testutil.MakeDetectionEvent("THROW_001", "player1", 0.9, 0.9)
	evt1.MatchID = "match1"
	evt1.FrameIndex = 100
	evt1.EnforcementWeight = 0.8

	evt2 := testutil.MakeDetectionEvent("THROW_001", "player1", 0.9, 0.9)
	evt2.MatchID = "match1"
	evt2.FrameIndex = 150 // within 300-frame cooldown
	evt2.EnforcementWeight = 0.8

	before := scorer.GetScore("player1").EventCount
	scorer.IngestEvent(evt1)
	after1 := scorer.GetScore("player1").EventCount
	ok1 := after1 > before

	scorer.IngestEvent(evt2)
	after2 := scorer.GetScore("player1").EventCount
	ok2 := after2 > after1

	if !ok1 {
		t.Error("first event should be processed")
	}
	if ok2 {
		t.Error("second event should be deduplicated by cooldown")
	}

	score := scorer.GetScore("player1")
	if score.EventCount != 1 {
		t.Errorf("expected 1 event after dedup, got %d", score.EventCount)
	}
}

func TestScorer_PerDetectorCap(t *testing.T) {
	cfg := config.DefaultConfig()
	scorer := scoring.NewSuspicionScorer(scorerConfigFromCfg(cfg))

	for i := 0; i < 10; i++ {
		evt := testutil.MakeDetectionEvent("THROW_001", "player1", 0.5, 0.5)
		evt.MatchID = "match1"
		evt.FrameIndex = i * 1000 // far apart to avoid cooldown
		evt.EnforcementWeight = 0.8
		scorer.IngestEvent(evt)
	}

	score := scorer.GetScore("player1")
	if score.EventCount > cfg.Scoring.MaxContribPerDetectorPerMatch {
		t.Errorf("expected at most %d events from per-detector cap, got %d",
			cfg.Scoring.MaxContribPerDetectorPerMatch, score.EventCount)
	}
}

func TestScorer_SameCategoryDiminishing(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Scoring.MaxContribPerDetectorPerMatch = 100
	scorer := scoring.NewSuspicionScorer(scorerConfigFromCfg(cfg))

	detectors := []string{"THROW_001", "THROW_002", "THROW_003"}
	var contributions []float64

	for i, did := range detectors {
		evt := model.DetectionEvent{
			EventID:           model.NewEventID(),
			DetectorID:        did,
			PlayerID:          "player1",
			MatchID:           "match1",
			FrameIndex:        (i + 1) * 1000,
			Severity:          0.7,
			Confidence:        0.9,
			EnforcementWeight: 0.8,
		}
		prevScore := scorer.GetScore("player1").TotalScore
		scorer.IngestEvent(evt)
		newScore := scorer.GetScore("player1").TotalScore
		contributions = append(contributions, newScore-prevScore)
	}

	// Each subsequent same-category event should contribute less
	for i := 1; i < len(contributions); i++ {
		if contributions[i] >= contributions[i-1] {
			t.Errorf("expected diminishing returns: event %d contributed %.4f >= event %d contributed %.4f",
				i, contributions[i], i-1, contributions[i-1])
		}
	}
}

func TestScorer_ThresholdCheck(t *testing.T) {
	cfg := config.DefaultConfig()
	scorer := scoring.NewSuspicionScorer(scorerConfigFromCfg(cfg))

	if scorer.ExceedsReviewThreshold("player1") {
		t.Error("expected to be below review threshold initially")
	}

	// Add events from different categories to maximize score
	events := []struct {
		detector string
		category string
	}{
		{"THROW_001", "throw"},
		{"MOV_001", "movement"},
		{"BIO_001", "bio"},
		{"STATE_006", "state"},
		{"PAT_001", "pattern"},
	}
	for i, e := range events {
		evt := model.DetectionEvent{
			EventID:           model.NewEventID(),
			DetectorID:        e.detector,
			PlayerID:          "player1",
			MatchID:           "match1",
			FrameIndex:        (i + 1) * 1000,
			Severity:          1.0,
			Confidence:        1.0,
			EnforcementWeight: 1.0,
		}
		scorer.IngestEvent(evt)
	}

	score := scorer.GetScore("player1")
	t.Logf("total score: %.4f, review threshold: %.2f", score.TotalScore, cfg.Scoring.ReviewThreshold)

	if !scorer.ExceedsReviewThreshold("player1") {
		t.Errorf("expected to exceed review threshold (%.2f) after 5 max events, got %.4f",
			cfg.Scoring.ReviewThreshold, score.TotalScore)
	}
}

func TestScorer_Reset(t *testing.T) {
	scorer := newTestScorer()

	evt := testutil.MakeDetectionEvent("THROW_001", "player1", 0.8, 0.9)
	evt.MatchID = "match1"
	evt.EnforcementWeight = 0.8
	scorer.IngestEvent(evt)

	if scorer.GetScore("player1").TotalScore <= 0 {
		t.Fatal("expected positive score before reset")
	}

	scorer.Reset()

	score := scorer.GetScore("player1")
	if score.TotalScore != 0 {
		t.Errorf("expected zero score after reset, got %.4f", score.TotalScore)
	}
	if score.EventCount != 0 {
		t.Errorf("expected zero events after reset, got %d", score.EventCount)
	}
}
