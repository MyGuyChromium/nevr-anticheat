package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestCalibration_JoinsDecisionsToDetectors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Case C1 (single match): THROW_001 (non-shadow) and BIO_003 (shadow) fired.
	mustStoreEvent(t, s, mkEvent("THROW_001", "P1", "M1", 1, 0.9, 0.9))
	shadow := mkEvent("BIO_003", "P1", "M1", 2, 0.5, 0.5)
	shadow.IsShadow = true
	mustStoreEvent(t, s, shadow)
	mustStoreEvent(t, s, mkEvent("BIO_003", "P1", "M1", 30, 0.5, 0.5))
	if err := s.StoreReviewCase(ctx, model.ReviewCase{CaseID: "C1", PlayerID: "P1", MatchID: "M1", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Cross-match case for P2 over M2+M3: THROW_001 in both.
	mustStoreEvent(t, s, mkEvent("THROW_001", "P2", "M2", 1, 0.9, 0.9))
	mustStoreEvent(t, s, mkEvent("THROW_001", "P2", "M3", 1, 0.9, 0.9))
	if err := s.StoreCrossMatchReviewCase(ctx, CrossMatchReviewCase{CaseID: "XM-P2", PlayerID: "P2", MatchIDs: []string{"M2", "M3"}, MatchCount: 2, Severity: "high", DecayedScore: 60, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	// C1: confirmed cheat overall, but BIO_003 specifically was wrong.
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "C1", ModeratorID: "m", Verdict: VerdictConfirmedCheat,
		DetectorFeedback: []model.DetectorVerdict{{DetectorID: "BIO_003", Correct: "no"}}}); err != nil {
		t.Fatal(err)
	}
	// XM-P2: false positive.
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "XM-P2", ModeratorID: "m", Verdict: VerdictFalsePositive}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.ComputeCalibration(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]DetectorCalibration{}
	for _, r := range rows {
		byID[r.DetectorID] = r
	}
	th := byID["THROW_001"]
	if th.Confirmed != 1 || th.FalsePositive != 1 || th.CasesReviewed != 2 || th.EventsReviewed != 3 {
		t.Errorf("THROW_001 = %+v", th)
	}
	if p, ok := th.Precision(); !ok || p != 0.5 {
		t.Errorf("THROW_001 precision = %v %v", p, ok)
	}
	bio := byID["BIO_003"]
	if bio.FalsePositive != 1 || bio.Confirmed != 0 || bio.FeedbackGiven != 1 || bio.EventsReviewed != 2 {
		t.Errorf("BIO_003 (feedback override, shadow included) = %+v", bio)
	}
	if rows[0].DetectorID != "BIO_003" || rows[1].DetectorID != "THROW_001" {
		t.Errorf("not sorted: %v %v", rows[0].DetectorID, rows[1].DetectorID)
	}
	if r, _ := s.ComputeCalibration(ctx, time.Now().Add(time.Hour)); len(r) != 0 {
		t.Error("since filter ignored")
	}

	fb, err := ParseDetectorFeedback([]string{"THROW_001=yes", " BIO_003 = No "})
	if err != nil || len(fb) != 2 || fb[1].Correct != "no" || fb[1].DetectorID != "BIO_003" {
		t.Errorf("ParseDetectorFeedback = %v, %v", fb, err)
	}
	if _, err := ParseDetectorFeedback([]string{"THROW_001=maybe"}); err == nil {
		t.Error("bad feedback accepted")
	}
}
