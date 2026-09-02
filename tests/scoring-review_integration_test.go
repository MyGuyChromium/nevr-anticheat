package tests

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/enforce"
	"github.com/nevr-anticheat/nevr-anticheat/internal/evidence"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/review"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// Compile-time proof that the sqlite store satisfies the narrow interfaces
// the scoring/review/enforce packages depend on.
var (
	_ review.CaseStore     = (*sqlite.Store)(nil)
	_ evidence.ExportStore = (*sqlite.Store)(nil)
	_ enforce.ActionStore  = (*sqlite.Store)(nil)
)

func mkScoringEvent(det, player string, frame int) model.DetectionEvent {
	ev := testutil.MakeDetectionEvent(det, player, 0.9, 0.9)
	ev.MatchID = "match1"
	ev.FrameIndex = frame
	ev.FrameRangeStart = frame
	ev.FrameRangeEnd = frame
	ev.Timestamp = float64(frame) / 15.0
	ev.EnforcementWeight = 0.8
	return ev
}

// Regression for F50/F69: on the live path ProcessMatch runs once per batch
// with SetSkipReset(true) and calls ApplyCorrelationBonus every time. With
// no new events the score must not move.
func TestScoringReview_LiveBatchesDoNotInflateScore(t *testing.T) {
	p, scorer := newTestPipeline([]detect.Detector{})
	p.SetSkipReset(true)
	matchCtx := testutil.NewMatchContext()

	scorer.IngestEvent(mkScoringEvent("MOV_001", "player1", 100))
	scorer.IngestEvent(mkScoringEvent("BIO_002", "player1", 2000))

	first, err := p.ProcessMatch(context.Background(), matchCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := first.PlayerScores["player1"]
	if base.CorrelationBonus != 5 || math.Abs(base.TotalScore-(base.BaseScore+5)) > 1e-9 {
		t.Fatalf("expected a single +5 bonus after first batch, got %+v", base)
	}
	for i := 0; i < 25; i++ {
		res, err := p.ProcessMatch(context.Background(), matchCtx, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := res.PlayerScores["player1"]
		if got.TotalScore != base.TotalScore || got.EventCount != base.EventCount {
			t.Fatalf("batch %d: score drifted without new events: %.2f -> %.2f", i, base.TotalScore, got.TotalScore)
		}
	}
	// Returned maps must not alias the live scorer.
	first.PlayerScores["player1"].DetectorCounts["MOV_001"] = 99
	if scorer.GetScore("player1").DetectorCounts["MOV_001"] != 1 {
		t.Fatal("MatchResult.PlayerScores aliases scorer state")
	}
}

// End-to-end: pipeline result -> review cases in the real sqlite store ->
// `flagged`/`report` data -> replay bundle export.
func TestScoringReview_CasesReachStore(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	p, scorer := newTestPipeline([]detect.Detector{})
	p.SetSkipReset(true) // live-path semantics: keep the seeded scorer
	matchCtx := testutil.NewMatchContext()
	matchCtx.MatchID = "match1"
	matchCtx.PlayerIDs = []string{"player1", "player2"}

	// Inject events with weight 1.0 so player1 clears the default high_risk boundary.
	events := []model.DetectionEvent{
		mkScoringEvent("THROW_001", "player1", 100),
		mkScoringEvent("MOV_001", "player1", 1000),
		mkScoringEvent("BIO_001", "player1", 2000),
		mkScoringEvent("STATE_001", "player1", 3000),
		mkScoringEvent("PAT_001", "player1", 4000),
		mkScoringEvent("THROW_001", "player2", 100),
	}
	for i := range events {
		events[i].EnforcementWeight = 1.0
		scorer.IngestEvent(events[i])
		if err := store.StoreDetectionEvent(ctx, events[i]); err != nil {
			t.Fatal(err)
		}
	}
	result, err := p.ProcessMatch(ctx, matchCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result.DetectionEvents = events
	p1 := result.PlayerScores["player1"]
	if lvl := model.DefaultLevelTable().LevelFor(p1.TotalScore); !lvl.AtLeast(model.LevelHighRisk) {
		t.Fatalf("setup: player1 should be high_risk, got %s (%.1f)", lvl, p1.TotalScore)
	}

	frames := testutil.GenerateCleanFrames("player1", 4100)
	if _, err := store.StoreTelemetryFrames(ctx, "match1", frames); err != nil {
		t.Fatal(err)
	}

	cases, err := review.CreateCasesFromResult(ctx, store, matchCtx, result, model.DefaultLevelTable(),
		review.WithDetectorNames(map[string]string{"THROW_001": "Release Velocity Cap"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 || cases[0].PlayerID != "player1" {
		t.Fatalf("expected one case for player1, got %+v", cases)
	}
	result.ReviewCases = cases

	pending, err := store.GetPendingReviewCases(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("flagged should list the case: %v %+v", err, pending)
	}
	stored, err := store.GetReviewCase(ctx, cases[0].CaseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.DetectorsTriggered) != 5 || stored.DetectorsTriggered[0].DetectorName != "Release Velocity Cap" ||
		stored.DetectorsTriggered[0].FrameIndex != 100 || stored.DetectorsTriggered[0].EventID == "" {
		t.Fatalf("stored evidence lost locator fields: %+v", stored.DetectorsTriggered)
	}
	report := evidence.FormatReport(stored)
	if !strings.Contains(report, "Release Velocity Cap") || !strings.Contains(report, "frame 100") {
		t.Fatalf("report lacks locator data:\n%s", report)
	}

	bundle, err := evidence.NewExporter(store).ExportForCase(ctx, cases[0].CaseID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Clips) != 5 || len(bundle.Clips[0].Frames) != 91 {
		t.Fatalf("expected 5 clips of 91 frames, got %d clips, first has %d", len(bundle.Clips), len(bundle.Clips[0].Frames))
	}
	if _, err := json.Marshal(bundle); err != nil {
		t.Fatal(err)
	}

	// Enforcement engine persists a review_queue recommendation with full fields.
	eng := enforce.NewEngine(enforce.EngineConfig{Mode: enforce.ModeReview, CooldownDuration: time.Minute}, store, nil)
	action := eng.Evaluate(ctx, "player1", "match1", result.PlayerScores["player1"], events)
	if action == nil || action.ActionType != model.ActionReviewQueue || len(action.EvidenceIDs) != 5 || action.MatchIDs[0] != "match1" {
		t.Fatalf("engine action wrong: %+v", action)
	}
}
