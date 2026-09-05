package tests

import (
	"context"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/review"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// TestReviewCases_AnalyzeVerdictCalibration is the moderator loop end to end
// on the offline path: analyze a synthetic match with a speed hacker ->
// StoreMatchAnalysis creates ONE review case under review.CaseID ->
// `verdict` (Store.StoreModeratorDecision) marks it decided ->
// `calibration-report` (Store.ComputeCalibration) counts the detector ->
// a reprocess refreshes the case without duplicating it or reopening it.
func TestReviewCases_AnalyzeVerdictCalibration(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p, scorer := newTestPipeline([]detect.Detector{movement.NewMov001(nil)})
	mc := matchContextForPlayer("player1")
	mc.MatchID = "match-e2e"
	frames := player1().SpeedHackFrames(120, 75)
	result, err := p.ProcessMatch(ctx, mc, frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.DetectionEvents) == 0 {
		t.Fatal("setup: speed hack produced no MOV_001 events")
	}
	levels := scorer.Levels()
	if !levels.LevelFor(result.PlayerScores["player1"].TotalScore).AtLeast(model.LevelHighRisk) {
		t.Fatalf("setup: player1 should reach the review tier: %.1f", result.PlayerScores["player1"].TotalScore)
	}

	opts := replay.AnalysisOptions{Levels: levels, DetectorNames: map[string]string{"MOV_001": "Speed Cap"}}
	stored, err := replay.StoreMatchAnalysis(ctx, store, mc, result, "initial", opts)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CasesStored != 1 || len(result.ReviewCases) != 1 || stored.FlaggedPlayers[0] != "player1" {
		t.Fatalf("stored=%+v cases=%d", stored, len(result.ReviewCases))
	}
	caseID := review.CaseID(mc.MatchID, "player1")
	if result.ReviewCases[0].CaseID != caseID {
		t.Fatalf("case id %q, want %q", result.ReviewCases[0].CaseID, caseID)
	}
	rc, err := store.GetReviewCase(ctx, caseID)
	if err != nil {
		t.Fatalf("case not in store: %v", err)
	}
	if rc.Status != model.CaseStatusPending || rc.DetectorsTriggered[0].DetectorName != "Speed Cap" {
		t.Errorf("stored case = %+v", rc)
	}
	pending, _ := store.GetPendingReviewCases(ctx, 10)
	if len(pending) != 1 {
		t.Fatalf("flagged should list one case, got %d", len(pending))
	}

	// `verdict <case-id> confirmed_cheat --by mod` is StoreModeratorDecision.
	if err := store.StoreModeratorDecision(ctx, model.ModeratorDecision{
		CaseID: caseID, ModeratorID: "mod", Verdict: sqlite.VerdictConfirmedCheat,
		DetectorFeedback: []model.DetectorVerdict{{DetectorID: "MOV_001", Correct: "yes"}},
	}); err != nil {
		t.Fatalf("verdict: %v", err)
	}
	rc, _ = store.GetReviewCase(ctx, caseID)
	if rc.Status != model.CaseStatusDecided {
		t.Errorf("verdict did not decide the case: %s", rc.Status)
	}

	// `calibration-report` is ComputeCalibration.
	rows, err := store.ComputeCalibration(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range rows {
		if c.DetectorID == "MOV_001" {
			found = true
			if c.Confirmed != 1 || c.CasesReviewed != 1 || c.EventsReviewed == 0 {
				t.Errorf("calibration row = %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("calibration report lacks MOV_001: %+v", rows)
	}

	// Reprocess: same case id, still one row, moderator status preserved.
	result2, err := p.ProcessMatch(ctx, mc, frames)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replay.ReplaceMatchAnalysis(ctx, store, mc, result2, "reprocess", opts); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM review_cases WHERE match_id = ?`, mc.MatchID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reprocess duplicated review cases: %d rows", n)
	}
	rc, _ = store.GetReviewCase(ctx, caseID)
	if rc.Status != model.CaseStatusDecided {
		t.Errorf("reprocess reopened a decided case: %s", rc.Status)
	}

	// Zero-score players leave no snapshot on the offline path.
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM suspicion_scores WHERE match_id = ? AND total_score <= 0`, mc.MatchID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("zero-score snapshots written: %d", n)
	}
}
