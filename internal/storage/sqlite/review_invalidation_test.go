package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func pendingAggregate(id, player string, matches ...string) CrossMatchReviewCase {
	return CrossMatchReviewCase{CaseID: id, PlayerID: player, MatchIDs: matches,
		MatchCount: len(matches), CumulativeScore: 80, DecayedScore: 75,
		Detectors: map[string]int{"MOV_001": 4}, Explanation: "original evidence window",
		Status: CaseStatusPending, CreatedAt: time.Now()}
}

func TestNegativeEventReviewRevokesPendingAggregateWithoutRewritingAudit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	event := mkEvent("MOV_001", "p", "m", 10, 1, 1)
	mustStoreEvent(t, s, event)
	for _, rc := range []CrossMatchReviewCase{pendingAggregate("XM-p", "p", "m", "second"), pendingAggregate("unrelated-match", "p", "other"), pendingAggregate("unrelated-player", "other", "m")} {
		if err := s.StoreCrossMatchReviewCase(ctx, rc); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.StoreMatchSuspicionScore(ctx, "m", model.SuspicionScore{PlayerID: "p", TotalScore: 75, SnapshotTime: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, verdict := range []string{"yes", "uncertain"} {
		if _, err := s.StoreEventReview(ctx, event.EventID, verdict, "", "r"); err != nil {
			t.Fatal(err)
		}
		if rc, _ := s.GetCrossMatchReviewCase(ctx, "XM-p"); rc.Status != CaseStatusPending {
			t.Fatal("nonnegative review revoked pending case")
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := s.StoreEventReview(ctx, event.EventID, "no", "legal", "r"); err != nil {
			t.Fatal(err)
		}
	}
	rc, err := s.GetCrossMatchReviewCase(ctx, "XM-p")
	if err != nil || rc.Status != CaseStatusClosed || rc.DecayedScore != 75 || rc.CumulativeScore != 80 || len(rc.MatchIDs) != 2 || strings.Count(rc.Explanation, crossMatchReviewRevoked) != 1 {
		t.Fatalf("revocation lost history or was not idempotent: %+v %v", rc, err)
	}
	// A stale computed result cannot reopen or overwrite the superseded snapshot.
	refresh := pendingAggregate("XM-p", "p", "m")
	refresh.DecayedScore = 99
	if err := s.StoreCrossMatchReviewCase(ctx, refresh); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetCrossMatchReviewCase(ctx, "XM-p")
	if again.Explanation != rc.Explanation || again.DecayedScore != 75 || len(again.MatchIDs) != 2 {
		t.Fatalf("refresh rewrote revoked audit: %+v", again)
	}
	pending, err := s.GetPendingCrossMatchReviewCases(ctx, 10)
	if err != nil || len(pending) != 2 || countRows(t, s, "detection_events", "") != 1 || countRows(t, s, "suspicion_scores", "") != 1 || countRows(t, s, "event_reviews", "") != 1 {
		t.Fatalf("wrong scope or deleted history: pending=%+v err=%v", pending, err)
	}
}

func TestNegativeModeratorReviewRevokesOverlappingPendingAggregate(t *testing.T) {
	for _, tc := range []struct {
		name, verdict string
		feedback      []model.DetectorVerdict
		closed        bool
	}{
		{"false-positive", VerdictFalsePositive, nil, true},
		{"detector-no", VerdictInconclusive, []model.DetectorVerdict{{DetectorID: "MOV_001", Correct: "no"}}, true},
		{"uncertain", VerdictInconclusive, []model.DetectorVerdict{{DetectorID: "MOV_001", Correct: "uncertain"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			if err := s.StoreReviewCase(ctx, model.ReviewCase{CaseID: "single", PlayerID: "p", MatchID: "m", Status: CaseStatusPending}); err != nil {
				t.Fatal(err)
			}
			if err := s.StoreCrossMatchReviewCase(ctx, pendingAggregate("XM-p", "p", "m")); err != nil {
				t.Fatal(err)
			}
			if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "single", ModeratorID: "r", Verdict: tc.verdict, DetectorFeedback: tc.feedback}); err != nil {
				t.Fatal(err)
			}
			rc, err := s.GetCrossMatchReviewCase(ctx, "XM-p")
			if err != nil || (rc.Status == CaseStatusClosed) != tc.closed || countRows(t, s, "moderator_decisions", "") != 1 {
				t.Fatalf("decision/case invalidation mismatch: %+v %v", rc, err)
			}
			// Also covers a candidate first inserted after a review was committed.
			if err := s.StoreCrossMatchReviewCase(ctx, pendingAggregate("late-first-insert", "p", "m")); err != nil {
				t.Fatal(err)
			}
			late, _ := s.GetCrossMatchReviewCase(ctx, "late-first-insert")
			if (late.Status == CaseStatusClosed) != tc.closed {
				t.Fatalf("stale first insert bypassed current review: %+v", late)
			}
		})
	}
}

func TestAggregateFirstInsertRequiresCurrentReviewReconsideration(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	event := mkEvent("MOV_001", "p", "m", 10, 1, 1)
	mustStoreEvent(t, s, event)
	if _, err := s.StoreEventReview(ctx, event.EventID, "no", "", "r"); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreCrossMatchReviewCase(ctx, pendingAggregate("stale", "p", "m")); err != nil {
		t.Fatal(err)
	}
	if rc, _ := s.GetCrossMatchReviewCase(ctx, "stale"); rc.Status != CaseStatusClosed {
		t.Fatal("first insert ignored negative review")
	}
	if _, err := s.StoreEventReview(ctx, event.EventID, "yes", "explicit reconsideration", "r"); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreCrossMatchReviewCase(ctx, pendingAggregate("reconsidered", "p", "m")); err != nil {
		t.Fatal(err)
	}
	if rc, _ := s.GetCrossMatchReviewCase(ctx, "reconsidered"); rc.Status != CaseStatusPending {
		t.Fatal("latest positive reconsideration was ignored")
	}
	if rc, _ := s.GetCrossMatchReviewCase(ctx, "stale"); rc.Status != CaseStatusClosed {
		t.Fatal("reconsideration silently reopened closed historical case")
	}
}

func TestReviewInvalidationFailureRollsBackHumanLabelAndDecision(t *testing.T) {
	for _, direct := range []bool{false, true} {
		s := newTestStore(t)
		ctx := context.Background()
		event := mkEvent("MOV_001", "p", "m", 10, 1, 1)
		mustStoreEvent(t, s, event)
		if err := s.StoreReviewCase(ctx, model.ReviewCase{CaseID: "single", PlayerID: "p", MatchID: "m", Status: CaseStatusPending}); err != nil {
			t.Fatal(err)
		}
		if err := s.StoreCrossMatchReviewCase(ctx, pendingAggregate("XM-p", "p", "m")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_case_invalidation BEFORE UPDATE ON cross_match_review_cases BEGIN SELECT RAISE(ABORT, 'invalidation failed'); END`); err != nil {
			t.Fatal(err)
		}
		var err error
		if direct {
			_, err = s.StoreEventReview(ctx, event.EventID, "no", "", "r")
		} else {
			err = s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "single", ModeratorID: "r", Verdict: VerdictFalsePositive})
		}
		if err == nil || countRows(t, s, "event_reviews", "") != 0 || countRows(t, s, "moderator_decisions", "") != 0 {
			t.Fatal("review committed while invalidation failed")
		}
		if rc, _ := s.GetReviewCase(ctx, "single"); rc.Status != CaseStatusPending {
			t.Fatal("failed review left a decided case")
		}
	}
}
