package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func importFixtureOpportunity(id string, at time.Time) CalibrationOpportunity {
	return CalibrationOpportunity{OpportunityID: id, MatchID: "M1", PlayerID: "P1", DetectorID: "THROW_001",
		Kind: OpportunityThrow, FrameStart: 10, FrameEnd: 20, GroundTruth: GroundTruthPositive,
		Comment: "video proof", ReviewerID: "local", ReviewedAt: at}
}

// A library export that is older than the local database (your own earlier
// export, or another reviewer's copy) must never replace a newer local verdict.
func TestLibraryImportNeverOverwritesANewerLocalReview(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	stale := now.Add(-90 * 24 * time.Hour)
	if err := s.StoreMatchContext(ctx, &model.MatchContext{MatchID: "M1", PlayerIDs: []string{"P1"}}, 4); err != nil {
		t.Fatal(err)
	}
	event := mkEvent("THROW_001", "P1", "M1", 40, 0.9, 0.9)
	mustStoreEvent(t, s, event)

	if _, err := s.StoreMatchLabel(ctx, "M1", MatchLabelConfirmedCheat, "video proof", "local", "v1", "cfg"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreEventReview(ctx, event.EventID, "yes", "video proof", "local"); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportCalibrationOpportunity(ctx, importFixtureOpportunity("O1", now)); err != nil {
		t.Fatal(err)
	}

	staleLabel := MatchLabel{MatchID: "M1", Label: MatchLabelKnownClean, Comment: "stale", ReviewerID: "other", ReviewedAt: stale}
	staleReview := EventReview{EventID: event.EventID, MatchID: "M1", PlayerID: "P1", DetectorID: "THROW_001", Verdict: "no", Comment: "stale", ReviewerID: "other", ReviewedAt: stale}
	staleOpportunity := importFixtureOpportunity("O1", stale)
	staleOpportunity.GroundTruth, staleOpportunity.Comment = GroundTruthNegative, "stale"
	undated := staleLabel
	undated.ReviewedAt = time.Time{}
	for name, err := range map[string]error{
		"label":         s.ImportMatchLabel(ctx, staleLabel),
		"undated label": s.ImportMatchLabel(ctx, undated),
		"review":        s.ImportEventReview(ctx, staleReview),
		"opportunity":   s.ImportCalibrationOpportunity(ctx, staleOpportunity),
	} {
		if !errors.Is(err, ErrImportKeptLocal) {
			t.Errorf("%s: older import error = %v, want ErrImportKeptLocal", name, err)
		}
	}

	label, _, err := s.GetMatchLabel(ctx, "M1")
	if err != nil || label.Label != MatchLabelConfirmedCheat || label.Comment != "video proof" || label.ReviewerID != "local" || label.ReviewedAt.Before(now.Add(-time.Minute)) {
		t.Fatalf("older import replaced the local match label: %+v %v", label, err)
	}
	reviews, err := s.GetEventReviewsByMatch(ctx, "M1")
	if err != nil || reviews[event.EventID].Verdict != "yes" || reviews[event.EventID].Comment != "video proof" || reviews[event.EventID].ReviewerID != "local" {
		t.Fatalf("older import replaced the local event review: %+v %v", reviews, err)
	}
	opportunities, err := s.ListCalibrationOpportunities(ctx, "M1", "")
	if err != nil || len(opportunities) != 1 || opportunities[0].GroundTruth != GroundTruthPositive || opportunities[0].Comment != "video proof" {
		t.Fatalf("older import replaced the local ground-truth window: %+v %v", opportunities, err)
	}

	// Last writer wins: a strictly newer review does replace the local one.
	future := now.Add(time.Hour)
	newerLabel, newerReview, newerOpportunity := staleLabel, staleReview, staleOpportunity
	newerLabel.ReviewedAt, newerReview.ReviewedAt, newerOpportunity.ReviewedAt = future, future, future
	if err := s.ImportMatchLabel(ctx, newerLabel); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportEventReview(ctx, newerReview); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportCalibrationOpportunity(ctx, newerOpportunity); err != nil {
		t.Fatal(err)
	}
	label, _, _ = s.GetMatchLabel(ctx, "M1")
	reviews, _ = s.GetEventReviewsByMatch(ctx, "M1")
	opportunities, _ = s.ListCalibrationOpportunities(ctx, "M1", "")
	if label.Label != MatchLabelKnownClean || !label.ReviewedAt.Equal(future) || reviews[event.EventID].Verdict != "no" || opportunities[0].GroundTruth != GroundTruthNegative {
		t.Fatalf("newer import was not applied: %+v %+v %+v", label, reviews[event.EventID], opportunities)
	}
}

// Export followed by import of the same library (database restore, machine
// move, merge with an overlapping copy) adds no information, so it must not
// spend the held-out cohort: quarantine never clears.
func TestReimportingAnUnchangedLibraryDoesNotQuarantineHeldOutMatches(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	matches := []StoredMatch{splitMatch("H-LABEL", "PL"), splitMatch("H-EVENT", "PE"), splitMatch("H-OPP", "PO")}
	if _, err := s.ReconcileCalibrationSplits(ctx, matches, map[string]string{"H-LABEL": "holdout", "H-EVENT": "validation", "H-OPP": "holdout"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreMatchContext(ctx, &model.MatchContext{MatchID: "H-LABEL", PlayerIDs: []string{"PL"}}, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreMatchLabel(ctx, "H-LABEL", MatchLabelKnownClean, "scrim", "local", "v1", "cfg"); err != nil {
		t.Fatal(err)
	}
	event := mkEvent("THROW_001", "PE", "H-EVENT", 40, 0.9, 0.9)
	mustStoreEvent(t, s, event)
	if _, err := s.StoreEventReview(ctx, event.EventID, "yes", "seen", "local"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO calibration_opportunities
		(opportunity_id, match_id, player_id, detector_id, opportunity_kind, frame_start, frame_end, ground_truth, reviewer_id, reviewed_at)
		VALUES ('O-H', 'H-OPP', 'PO', 'THROW_001', 'throw', 10, 20, 'negative', 'local', ?)`, fmtDBTime(time.Now())); err != nil {
		t.Fatal(err)
	}

	// What the library export endpoint serialises, imported straight back.
	labels, err := s.GetMatchLabels(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviews, err := s.ListEventReviews(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	opportunities, err := s.ListCalibrationOpportunities(ctx, "", "")
	if err != nil || len(labels) != 1 || len(reviews) != 1 || len(opportunities) != 1 {
		t.Fatalf("fixture export: %d %d %d %v", len(labels), len(reviews), len(opportunities), err)
	}
	for round := 0; round < 2; round++ {
		for _, label := range labels {
			if err := s.ImportMatchLabel(ctx, label); err != nil {
				t.Fatal(err)
			}
		}
		for _, review := range reviews {
			if err := s.ImportEventReview(ctx, review); err != nil {
				t.Fatal(err)
			}
		}
		for _, opportunity := range opportunities {
			if err := s.ImportCalibrationOpportunity(ctx, opportunity); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := s.ReconcileCalibrationSplits(ctx, matches, nil)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"H-LABEL": "holdout", "H-EVENT": "validation", "H-OPP": "holdout"} {
		if got[id].Split != want || got[id].Quarantined {
			t.Errorf("%s after re-importing its own unchanged evidence: %+v", id, got[id])
		}
	}
	if countRows(t, s, "match_labels", "") != 1 || countRows(t, s, "event_reviews", "") != 1 || countRows(t, s, "calibration_opportunities", "") != 1 {
		t.Fatal("idempotent import changed the row counts")
	}

	// A rejected import exposes nothing either...
	older := labels["H-LABEL"]
	older.Label, older.ReviewedAt = MatchLabelSuspected, older.ReviewedAt.Add(-time.Hour)
	if err := s.ImportMatchLabel(ctx, older); !errors.Is(err, ErrImportKeptLocal) {
		t.Fatalf("older label: %v", err)
	}
	overlapping := opportunities[0]
	overlapping.OpportunityID = "O-OTHER"
	if err := s.ImportCalibrationOpportunity(ctx, overlapping); err == nil {
		t.Fatal("overlapping window accepted")
	}
	got, _ = s.ReconcileCalibrationSplits(ctx, matches, nil)
	if got["H-LABEL"].Quarantined || got["H-OPP"].Quarantined {
		t.Fatalf("rejected imports quarantined held-out matches: %+v", got)
	}
	// ...while an import that really changes held-out evidence still does.
	newer := labels["H-LABEL"]
	newer.Label, newer.ReviewedAt = MatchLabelSuspected, newer.ReviewedAt.Add(time.Hour)
	if err := s.ImportMatchLabel(ctx, newer); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ReconcileCalibrationSplits(ctx, matches, nil)
	if !got["H-LABEL"].Quarantined || got["H-LABEL"].Split != "holdout" {
		t.Fatalf("changed held-out label was laundered: %+v", got["H-LABEL"])
	}
}

// The same verdict has the same side effects however it entered the database:
// an imported "no" supersedes the pending aggregate like a local "no" does,
// unless a newer local verdict for the same observation already overrides it.
func TestImportedNegativeReviewSupersedesPendingAggregateLikeALocalOne(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	event := mkEvent("MOV_001", "p", "m", 10, 1, 1)
	mustStoreEvent(t, s, event)
	imported := EventReview{EventID: "imported-" + event.EventID, MatchID: "m", PlayerID: "p", DetectorID: event.DetectorID,
		DetectorVersion: event.DetectorVersion, FrameIndex: event.FrameIndex, Timestamp: event.Timestamp,
		ObservedValue: event.ObservedValue, ExpectedRange: event.ExpectedRange,
		Verdict: "no", ReviewerID: "other", ReviewedAt: time.Now().Add(-time.Hour)}

	if err := s.StoreCrossMatchReviewCase(ctx, pendingAggregate("XM-p", "p", "m", "second")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreEventReview(ctx, event.EventID, "yes", "reconsidered with video", "local"); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportEventReview(ctx, imported); err != nil {
		t.Fatal(err)
	}
	if rc, _ := s.GetCrossMatchReviewCase(ctx, "XM-p"); rc.Status != CaseStatusPending {
		t.Fatalf("an older imported 'no' overrode the newer local verdict for the same observation: %+v", rc)
	}

	imported.EventID, imported.ReviewedAt = "imported-newer", time.Now().Add(time.Hour)
	if err := s.ImportEventReview(ctx, imported); err != nil {
		t.Fatal(err)
	}
	rc, err := s.GetCrossMatchReviewCase(ctx, "XM-p")
	if err != nil || rc.Status != CaseStatusClosed || rc.DecayedScore != 75 {
		t.Fatalf("imported 'no' left the recommendation pending or rewrote its audit totals: %+v %v", rc, err)
	}
}
