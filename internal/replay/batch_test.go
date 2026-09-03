package replay

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func emptyPipeline() *pipeline.Pipeline {
	return pipeline.NewPipeline(config.DefaultConfig(), nil, scoring.NewSuspicionScorer(scoring.ScorerConfig{}), quietLogger())
}

func testStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.NewStore(filepath.Join(t.TempDir(), "batch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// writeLegacyReplay writes a minimal legacy JSON replay for matchID.
func writeLegacyReplay(t *testing.T, dir, name, matchID string) string {
	t.Helper()
	content := `{
  "header": {"MatchID": "` + matchID + `", "PlayerIDs": ["p1"], "Teams": {"p1": "blue"}},
  "frames": [
    {"Index": 0, "Timestamp": 0, "GamePhase": "playing",
     "Players": [{"player_id": "p1", "position": [1, 1.6, 2], "rotation": [0,0,0,1]}]},
    {"Index": 1, "Timestamp": 0.067, "GamePhase": "playing",
     "Players": [{"player_id": "p1", "position": [1, 1.6, 2.1], "rotation": [0,0,0,1]}]}
  ]
}`
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A store that cannot be written must make the run fail visibly: the match
// is counted as an error (PersistFailed), not as processed.
func TestBatchAnalyzer_PersistFailureIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeLegacyReplay(t, dir, "a.json", "M-A")
	writeLegacyReplay(t, dir, "b.json", "M-B")
	store := testStore(t)
	if _, err := store.DB().Exec("PRAGMA query_only = 1"); err != nil {
		t.Fatal(err)
	}
	ba := NewBatchAnalyzer(emptyPipeline(), store, func() FrameParser { return NewJSONFrameParser() }, 2, quietLogger())
	ba.SetPipelineFactory(emptyPipeline)
	res, err := ba.AnalyzeDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != 0 || res.PersistFailed != 2 || res.Errors != 2 {
		t.Fatalf("read-only store: %+v (want Processed 0, PersistFailed 2, Errors 2)", res)
	}
	if n, _ := store.GetStoredMatchCount(context.Background()); n != 0 {
		t.Errorf("stored matches = %d on a read-only store", n)
	}

	// Writable again: both matches persist and the counters agree with the store.
	if _, err := store.DB().Exec("PRAGMA query_only = 0"); err != nil {
		t.Fatal(err)
	}
	res, err = ba.AnalyzeDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != 2 || res.PersistFailed != 0 || res.Errors != 0 || res.FramesInserted != 4 {
		t.Fatalf("writable store: %+v", res)
	}
	if n, _ := store.GetStoredMatchCount(context.Background()); n != 2 {
		t.Errorf("stored matches = %d, want 2", n)
	}
}

func matchResult(matchID string, flagged map[string]float64) *pipeline.MatchResult {
	res := &pipeline.MatchResult{MatchID: matchID, PlayerScores: map[string]model.SuspicionScore{}}
	for pid, score := range flagged {
		res.PlayerScores[pid] = model.SuspicionScore{PlayerID: pid, TotalScore: score, EventCount: 1,
			MatchIDs: map[string]bool{matchID: true}}
		res.DetectionEvents = append(res.DetectionEvents, model.DetectionEvent{
			EventID: uuid.New().String(), DetectorID: "MOV_001", DetectorVersion: "1.0.0", MatchID: matchID, PlayerID: pid,
			FrameIndex: 10, Severity: 0.8, Confidence: 0.9, EnforcementWeight: 0.8, ObservedValue: "v",
			CausalKey: model.CausalKey{PlayerID: pid, FrameStart: 8, FrameEnd: 12, AnomalyType: "speed"},
		})
	}
	return res
}

// Re-analysis that no longer flags a player closes that player's pending
// case with a reason (never deletes it); flagging the player again reopens it.
func TestStoreMatchAnalysis_ClosesStaleCasesAndReopens(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	mc := &model.MatchContext{MatchID: "M1", PlayerIDs: []string{"p1", "p2"}}
	opts := AnalysisOptions{Logger: quietLogger()}

	first, err := StoreMatchAnalysis(ctx, store, mc, matchResult("M1", map[string]float64{"p1": 70, "p2": 70}), "initial", opts)
	if err != nil || first.CasesStored != 2 || first.CasesClosed != 0 {
		t.Fatalf("initial: %+v, %v", first, err)
	}

	// Threshold raised / detector fixed: p2 no longer scores.
	if _, _, err := store.DeleteMatchAnalysis(ctx, "M1"); err != nil {
		t.Fatal(err)
	}
	second, err := StoreMatchAnalysis(ctx, store, mc, matchResult("M1", map[string]float64{"p1": 70}), "reprocess", opts)
	if err != nil || second.CasesStored != 1 || second.CasesClosed != 1 {
		t.Fatalf("reprocess: %+v, %v", second, err)
	}
	p2, err := store.GetReviewCase(ctx, "RC-M1-p2")
	if err != nil {
		t.Fatalf("stale case was deleted: %v", err)
	}
	if p2.Status != model.CaseStatusClosed || !strings.Contains(p2.CloseReason, "reprocess") {
		t.Errorf("stale case = status %q reason %q", p2.Status, p2.CloseReason)
	}
	pending, _ := store.GetPendingReviewCases(ctx, 10)
	if len(pending) != 1 || pending[0].PlayerID != "p1" {
		t.Errorf("pending after reprocess = %+v", pending)
	}

	// p2 flagged again: the stale closure is undone, p1's case untouched.
	if _, _, err := store.DeleteMatchAnalysis(ctx, "M1"); err != nil {
		t.Fatal(err)
	}
	third, err := StoreMatchAnalysis(ctx, store, mc, matchResult("M1", map[string]float64{"p1": 70, "p2": 75}), "reprocess", opts)
	if err != nil || third.CasesStored != 2 || third.CasesClosed != 0 {
		t.Fatalf("re-flag: %+v, %v", third, err)
	}
	p2, _ = store.GetReviewCase(ctx, "RC-M1-p2")
	if p2.Status != model.CaseStatusPending || p2.CloseReason != "" || p2.SuspicionScore != 75 {
		t.Errorf("re-flagged case not reopened: %+v", p2)
	}

	// A moderator decision survives a reprocess that drops the player.
	if err := store.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "RC-M1-p1", ModeratorID: "mod", Verdict: sqlite.VerdictConfirmedCheat}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.DeleteMatchAnalysis(ctx, "M1"); err != nil {
		t.Fatal(err)
	}
	fourth, err := StoreMatchAnalysis(ctx, store, mc, matchResult("M1", nil), "reprocess", opts)
	if err != nil || fourth.CasesStored != 0 || fourth.CasesClosed != 1 {
		t.Fatalf("drop all: %+v, %v", fourth, err)
	}
	if p1, _ := store.GetReviewCase(ctx, "RC-M1-p1"); p1.Status != model.CaseStatusDecided {
		t.Errorf("decided case changed by reprocess: %+v", p1)
	}
}
