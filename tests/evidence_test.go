package tests

import (
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/evidence"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

func TestFormatter_ReportGeneration(t *testing.T) {
	rc := model.ReviewCase{
		CaseID:            "RC-test-001",
		PlayerID:          "player1",
		MatchID:           "test-match-001",
		Severity:          "high",
		SuspicionScore:    72.5,
		RecommendedAction: "review_only",
		Explanation:       "Multiple throw anomalies detected",
		Status:            "pending",
		CreatedAt:         time.Now(),
		DetectorsTriggered: []model.TriggeredDetector{
			{
				DetectorID: "THROW_001",
				Severity:   "high",
				Confidence: 0.92,
				Details:    "Release velocity 25.0 m/s exceeds cap 20.0 m/s",
			},
			{
				DetectorID: "MOV_001",
				Severity:   "medium",
				Confidence: 0.75,
				Details:    "Sustained speed 60.0 exceeds max 55.0",
			},
		},
	}

	report := evidence.FormatReport(rc)

	if report == "" {
		t.Fatal("expected non-empty report")
	}

	// Check key fields are present
	checks := []string{
		"RC-test-001",
		"player1",
		"test-match-001",
		"HIGH",
		"72.5",
		"THROW_001",
		"MOV_001",
	}
	for _, check := range checks {
		if !strings.Contains(report, check) {
			t.Errorf("report missing expected content %q", check)
		}
	}

	t.Logf("Generated report (%d bytes):\n%s", len(report), report)
}

func TestBuilder_ReviewCase(t *testing.T) {
	builder := evidence.NewBuilder()

	matchCtx := testutil.NewMatchContext()
	score := model.SuspicionScore{
		PlayerID:   "player1",
		TotalScore: 45.0,
		EventCount: 3,
	}
	score.Init()
	score.ScoreByDetector["THROW_001"] = 25.0
	score.ScoreByDetector["MOV_001"] = 20.0

	events := []model.DetectionEvent{
		testutil.MakeDetectionEvent("THROW_001", "player1", 0.9, 0.85),
		testutil.MakeDetectionEvent("MOV_001", "player1", 0.7, 0.8),
		testutil.MakeDetectionEvent("THROW_001", "player2", 0.6, 0.7), // different player
	}

	rc := builder.Build("player1", matchCtx, score, events)

	if rc.CaseID == "" {
		t.Error("expected non-empty case ID")
	}
	if rc.PlayerID != "player1" {
		t.Errorf("expected player1, got %s", rc.PlayerID)
	}
	if rc.MatchID != matchCtx.MatchID {
		t.Errorf("expected match %s, got %s", matchCtx.MatchID, rc.MatchID)
	}
	if rc.SuspicionScore != 45.0 {
		t.Errorf("expected score 45.0, got %.1f", rc.SuspicionScore)
	}
	if rc.Status != "pending" {
		t.Errorf("expected pending status, got %s", rc.Status)
	}

	// Should only include player1's events (2 of 3)
	if len(rc.DetectorsTriggered) != 2 {
		t.Errorf("expected 2 triggered detectors (player1 only), got %d", len(rc.DetectorsTriggered))
	}

	t.Logf("review case: id=%s player=%s score=%.1f detectors=%d",
		rc.CaseID, rc.PlayerID, rc.SuspicionScore, len(rc.DetectorsTriggered))
}
