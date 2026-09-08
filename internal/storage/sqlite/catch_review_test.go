package sqlite

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func storedCatchChunk(start, end int) map[string]*model.PlayerCoverage {
	log := model.NewCatchReviewLog()
	for i := start; i < end; i++ {
		log.Add(model.CatchReviewRecord{FrameIndex: i, Timestamp: float64(i) / 15, Outcome: model.CatchReviewExcluded,
			Reason: "catch_direction_below_threshold", Confirmed: true, Metrics: map[string]float64{"duration_s": .2}})
	}
	return map[string]*model.PlayerCoverage{"P": {Version: 1, Detectors: []model.DetectorCoverage{{DetectorID: "STATE_008", Enabled: true, CatchReview: log}}}}
}

func storedCatchLog(t *testing.T, store *Store, matchID string) *model.CatchReviewLog {
	t.Helper()
	coverage, err := store.GetMatchAnalysisCoverage(context.Background(), matchID)
	if err != nil {
		t.Fatal(err)
	}
	if coverage["P"] != nil {
		for _, detector := range coverage["P"].Detectors {
			if detector.DetectorID == "STATE_008" {
				return detector.CatchReview
			}
		}
	}
	t.Fatal("missing persisted catch diagnostics")
	return nil
}

func TestCatchReviewStorageMergesLiveChunksWithBoundedCounts(t *testing.T) {
	for _, size := range []int{1, 3, 160} {
		store, ctx := newTestStore(t), context.Background()
		const total = 180
		for i := 0; i < total; i += size {
			chunk := storedCatchChunk(i, min(i+size, total))
			if err := store.MergeMatchCatchReviews(ctx, "M", chunk); err != nil {
				t.Fatal(err)
			}
			if err := store.MergeMatchCatchReviews(ctx, "M", chunk); err != nil {
				t.Fatal(err)
			}
		}
		got := storedCatchLog(t, store, "M")
		want := storedCatchChunk(0, total)["P"].Detectors[0].CatchReview
		if !reflect.DeepEqual(got, want) || got.Validate() != nil {
			t.Fatalf("chunk size %d changed log: %+v", size, got)
		}
		coverage, err := store.GetMatchAnalysisCoverage(ctx, "M")
		if err != nil {
			t.Fatal(err)
		}
		if coverage["P"].Status != model.ReviewStatusInsufficientData || len(coverage["P"].Limitations) == 0 || coverage["P"].ValidFrames != 0 {
			t.Fatal("catch-only live data claimed complete coverage")
		}
		for _, table := range []string{"detection_events", "suspicion_scores", "review_cases", "enforcement_actions"} {
			var count int
			if err := store.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
				t.Fatalf("diagnostics changed %s: count=%d err=%v", table, count, err)
			}
		}
	}
}

func TestCatchReviewStoragePreservesUnrelatedCoverageAndRejectsInvalid(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	other := model.DetectorCoverage{DetectorID: "THROW_001", Enabled: true, Status: "limited", CandidateFrames: 20, InputFrames: 3,
		Limitations: []string{"existing limitation"}, DecisionTrace: &model.DetectorDecisionTrace{Version: 1, InternalBranches: true}}
	catch := model.DetectorCoverage{DetectorID: "STATE_008", Enabled: true, Status: "limited", CandidateFrames: 20, InputFrames: 10}
	original := map[string]*model.PlayerCoverage{"P": {Version: 1, Status: "limited", ValidFrames: 20, RejectedFrames: 1,
		Limitations: []string{"original coverage"}, Detectors: []model.DetectorCoverage{other, catch}}}
	if _, err := store.WriteMatchAnalysis(ctx, MatchAnalysisWrite{MatchID: "M", Coverage: original}); err != nil {
		t.Fatal(err)
	}
	if err := store.MergeMatchCatchReviews(ctx, "M", storedCatchChunk(21, 24)); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetMatchAnalysisCoverage(ctx, "M")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["P"].Detectors[0], other) {
		t.Fatal("changed unrelated detector coverage")
	}
	got["P"].Detectors[1].CatchReview = nil
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("changed existing general/detector coverage: %+v", got)
	}
	var before string
	if err := store.db.QueryRow(`SELECT coverage_json FROM match_analysis_coverage WHERE match_id='M'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	bad := storedCatchChunk(24, 25)
	bad["P"].Detectors[0].CatchReview.Records[0].Metrics["duration_s"] = math.NaN()
	if err := store.MergeMatchCatchReviews(ctx, "M", bad); err == nil {
		t.Fatal("invalid diagnostic accepted")
	}
	var after string
	if err := store.db.QueryRow(`SELECT coverage_json FROM match_analysis_coverage WHERE match_id='M'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("failed merge changed stored coverage")
	}
	if _, err := store.db.Exec(`UPDATE match_analysis_coverage SET coverage_json='broken' WHERE match_id='M'`); err != nil {
		t.Fatal(err)
	}
	if err := store.MergeMatchCatchReviews(ctx, "M", storedCatchChunk(25, 26)); err == nil {
		t.Fatal("corrupt existing coverage silently replaced")
	}
}

func TestCatchReviewStorageOfflineRoundTripAndLegacyJSON(t *testing.T) {
	store, ctx := newTestStore(t), context.Background()
	coverage := storedCatchChunk(1, 5)
	if _, err := store.WriteMatchAnalysis(ctx, MatchAnalysisWrite{MatchID: "M", Coverage: coverage}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetMatchAnalysisCoverage(ctx, "M")
	if err != nil || !reflect.DeepEqual(got, coverage) {
		t.Fatalf("offline diagnostics changed: %v", err)
	}
	got["P"].Detectors[0].CatchReview.Records[0].Metrics["duration_s"] = 99
	if storedCatchLog(t, store, "M").Records[0].Metrics["duration_s"] != .2 {
		t.Fatal("loaded diagnostics retained mutable ownership")
	}
	legacy := `{"P":{"version":1,"status":"limited","detectors":[{"detector_id":"STATE_008","enabled":true}]}}`
	if _, err := store.db.Exec(`INSERT INTO match_analysis_coverage VALUES ('legacy',?)`, legacy); err != nil {
		t.Fatal(err)
	}
	old, err := store.GetMatchAnalysisCoverage(ctx, "legacy")
	if err != nil || old["P"].Detectors[0].CatchReview != nil {
		t.Fatalf("old JSON invented diagnostics: %v", err)
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatal(err)
	}
}
