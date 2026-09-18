package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// relabelAcrossReanalysis reproduces what a re-analysis leaves behind: the same
// physical observation under a second event id, each carrying its own label
// row. It returns the second event id, which sorts after the first so it is the
// newest label when both are recorded within one second.
func relabelAcrossReanalysis(t *testing.T, s *server, base, firstVerdict, secondVerdict string) string {
	t.Helper()
	first := testLabEvent(teethMatchID)
	second := first
	second.EventID = first.EventID + "-REANALYZED"
	for _, step := range []struct {
		event   model.DetectionEvent
		verdict string
	}{{first, firstVerdict}, {second, secondVerdict}} {
		if err := s.engine.Store().StoreDetectionEvent(context.Background(), step.event); err != nil {
			t.Fatal(err)
		}
		if resp := postJSONTest(t, base+"/api/event/"+step.event.EventID+"/review", map[string]string{"verdict": step.verdict}, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("review %s status=%d", step.event.EventID, resp.StatusCode)
		}
	}
	reviews, err := s.engine.Store().ListEventReviews(context.Background(), time.Time{})
	if err != nil || len(reviews) != 2 || len(sqlite.LatestEventReviewPerObservation(reviews)) != 1 {
		t.Fatalf("test setup: want two label rows for one observation, got %d rows (%v)", len(reviews), err)
	}
	return second.EventID
}

type regressionLabReport struct {
	Total             int              `json:"total"`
	Passed            int              `json:"passed"`
	Failed            int              `json:"failed"`
	Excluded          int              `json:"excluded_unsure"`
	Items             []regressionItem `json:"items"`
	NoAnalysis        int              `json:"no_current_analysis"`
	NoAnalysisMatches int              `json:"no_current_analysis_matches"`
	NoAnalysisItems   []regressionItem `json:"no_current_analysis_items"`
	Superseded        int              `json:"superseded_labels"`
}

// Mutation: remove the LatestEventReviewPerObservation call in
// handleRegressionLab and the lab reports total=2 failed=2 for one observation.
func TestRegressionLabCountsAReanalysedObservationOnce(t *testing.T) {
	s, base := teethUploadFixture(t)
	relabelAcrossReanalysis(t, s, base, "no", "no")
	var lab regressionLabReport
	if resp := getJSON(t, base+"/api/lab/regression", &lab); resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if lab.Total != 1 || lab.Failed != 1 || lab.Passed != 0 || lab.Superseded != 1 || len(lab.Items) != 1 {
		t.Fatalf("one observation labelled across a re-analysis is one expectation: %+v", lab)
	}
	if lab.Items[0].AnalysisState != labAnalysisCurrent || lab.NoAnalysis != 0 {
		t.Fatalf("an analysed match was reported as having no analysis: %+v", lab)
	}
	// The player history counts reviews by the same rule (mutation: remove the
	// dedupe in handlePlayerHistory and reviewed_false becomes 2).
	var history struct {
		Matches []playerMatchHistory `json:"matches"`
	}
	if resp := getJSON(t, base+"/api/player/echovr%3A1001/history", &history); resp.StatusCode != http.StatusOK || len(history.Matches) != 1 {
		t.Fatalf("history status=%d %+v", resp.StatusCode, history)
	}
	if history.Matches[0].ReviewedFalse != 1 || history.Matches[0].ReviewedValid != 0 {
		t.Fatalf("player history counted one observation %d times", history.Matches[0].ReviewedFalse)
	}
}

// The newest verdict of the observation is the one that counts: a moderator who
// changes "False positive" to "Unsure" after a re-analysis withdraws the
// expectation. Mutation: keep the OLDEST row in the lab (or skip the dedupe) and
// the withdrawn "no" is still tested (total=1).
func TestRegressionLabUsesTheNewestVerdictOfAnObservation(t *testing.T) {
	s, base := teethUploadFixture(t)
	relabelAcrossReanalysis(t, s, base, "no", "uncertain")
	var lab regressionLabReport
	if resp := getJSON(t, base+"/api/lab/regression", &lab); resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if lab.Total != 0 || lab.Excluded != 1 || lab.Superseded != 1 {
		t.Fatalf("the superseded verdict is still being tested: %+v", lab)
	}
}

// Mutation: remove the LatestEventReviewPerObservation call in
// buildCalibrationDashboard and THROW_001 shows two direct labels and two
// legitimate opportunities for one observation.
func TestPromotionGateCountsAReanalysedObservationOnce(t *testing.T) {
	s, base := teethUploadFixture(t)
	relabelAcrossReanalysis(t, s, base, "no", "no")
	dashboard, err := s.buildCalibrationDashboard(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	metric, ok := detectorMetric(dashboard, "THROW_001")
	if !ok {
		t.Fatal("THROW_001 metric missing")
	}
	if metric.Overall.DirectLabels != 1 || metric.Overall.NegativeOpportunities != 1 || metric.Overall.FalsePositive+metric.Overall.TrueNegative != 1 {
		t.Fatalf("one re-labelled observation must be one gate sample: %+v", metric.Overall)
	}
	if dashboard.Samples != 1 {
		t.Fatalf("dashboard samples = %d, want 1", dashboard.Samples)
	}
}

// A label can arrive through the portable library before its recording does,
// and a stored match can lack any analysis. Neither says anything about the
// detector. Mutation: make matchAnalysisState always answer "analyzed" and the
// imported "no" labels pass while the imported "yes" label fails, exactly the
// old behaviour.
func TestRegressionLabSeparatesMissingAnalysisFromAQuietDetector(t *testing.T) {
	s, base := teethUploadFixture(t)
	ctx := context.Background()
	const absentMatch, bareMatch = "MATCH-NOT-ON-THIS-PC", "MATCH-STORED-NEVER-ANALYZED"
	if err := s.engine.Store().StoreMatchContext(ctx, &model.MatchContext{MatchID: bareMatch}, 0); err != nil {
		t.Fatal(err)
	}
	reviewedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	review := func(id, match, verdict string, frame int) sqlite.EventReview {
		return sqlite.EventReview{EventID: id, MatchID: match, PlayerID: "echovr:1001", DetectorID: "BIO_001", DetectorVersion: "1.0.0",
			FrameIndex: frame, Verdict: verdict, ReviewerID: "someone-else", ReviewedAt: reviewedAt}
	}
	lib := evidenceLibrary{Version: 2, Reviews: []sqlite.EventReview{
		review("imported-no", absentMatch, "no", 40),
		review("imported-yes", absentMatch, "yes", 400),
		review("bare-no", bareMatch, "no", 40),
		// The fixture match WAS analysed here and BIO_001 is silent on it, so
		// this one is a real, passing "legal play stays clear" expectation.
		review("quiet-no", teethMatchID, "no", 40),
	}}
	if resp := postAPI(t, base+"/api/library", lib, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("library import status=%d", resp.StatusCode)
	}
	events, err := s.engine.Store().GetMatchEvents(ctx, teethMatchID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.DetectorID == "BIO_001" {
			t.Fatal("test setup: BIO_001 fired on the fixture, pick a detector that stays quiet")
		}
	}

	var lab regressionLabReport
	if resp := getJSON(t, base+"/api/lab/regression", &lab); resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if lab.Total != 1 || lab.Passed != 1 || lab.Failed != 0 || len(lab.Items) != 1 ||
		lab.Items[0].MatchID != teethMatchID || lab.Items[0].AnalysisState != labAnalysisCurrent || lab.Items[0].AnalysisNote != "" {
		t.Fatalf("only the analysed match can be tested: %+v", lab)
	}
	if lab.NoAnalysis != 3 || lab.NoAnalysisMatches != 2 || len(lab.NoAnalysisItems) != 3 {
		t.Fatalf("labels without a current analysis: %+v", lab)
	}
	states := map[string]string{}
	for _, item := range lab.NoAnalysisItems {
		if item.Passed || item.Current != nil || item.AnalysisNote == "" {
			t.Fatalf("an untestable label was given a result: %+v", item)
		}
		if previous, seen := states[item.MatchID]; seen && previous != item.AnalysisState {
			t.Fatalf("match %s has two analysis states", item.MatchID)
		}
		states[item.MatchID] = item.AnalysisState
	}
	if states[absentMatch] != labAnalysisNotStored || states[bareMatch] != labAnalysisNeverRun {
		t.Fatalf("analysis states = %v", states)
	}
}
