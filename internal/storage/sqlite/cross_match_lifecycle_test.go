package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// aggregatePlayer is the production path of `anticheat crossmatch`: eligible
// events -> summary -> BuildCrossMatchReviewCase (id "XM-<player>") -> store.
func aggregatePlayer(t *testing.T, s *Store, player string) (CrossMatchStoreResult, bool) {
	t.Helper()
	rc := buildPlayerAggregate(t, s, player)
	if rc == nil {
		return CrossMatchStoreResult{}, false
	}
	res, err := s.StoreCrossMatchReviewCaseResult(context.Background(), *rc)
	if err != nil {
		t.Fatal(err)
	}
	return res, true
}

func buildPlayerAggregate(t *testing.T, s *Store, player string) *CrossMatchReviewCase {
	t.Helper()
	events, err := s.GetAllPlayerEvents(context.Background(), player)
	if err != nil {
		t.Fatal(err)
	}
	table := model.DefaultLevelTable().WithReviewThreshold(20)
	cfg := xmCfg(time.Now())
	cfg.Levels = table
	return BuildCrossMatchReviewCase(ComputePlayerCrossMatchSummary(events, nil, cfg), table)
}

func storeThrow(t *testing.T, s *Store, player, match string, frame int) model.DetectionEvent {
	t.Helper()
	ev := mkEvent("THROW_001", player, match, frame, 1, 1)
	mustStoreEvent(t, s, ev)
	return ev
}

func pendingFor(t *testing.T, s *Store, player string) []CrossMatchReviewCase {
	t.Helper()
	all, err := s.GetPendingCrossMatchReviewCases(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []CrossMatchReviewCase
	for _, rc := range all {
		if rc.PlayerID == player {
			out = append(out, rc)
		}
	}
	return out
}

// One "no" label closes the pending aggregate. That closed row is an audit
// record; it must not swallow every later aggregate for the player.
func TestOneNegativeLabelDoesNotFreezeThePlayersCrossMatchCase(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rejected := storeThrow(t, s, "p", "m01", 10)
	storeThrow(t, s, "p", "m01", 500) // m01 stays in scope after the rejection
	storeThrow(t, s, "p", "m02", 10)

	first, ok := aggregatePlayer(t, s, "p")
	if !ok || first.CaseID != "XM-p" || first.Outcome != CrossMatchCaseCreated || first.Status != CaseStatusPending {
		t.Fatalf("first aggregate: %+v %t", first, ok)
	}
	staleAggregate := buildPlayerAggregate(t, s, "p") // computed before the review below
	if _, err := s.StoreEventReview(ctx, rejected.EventID, "no", "legal throw", "r"); err != nil {
		t.Fatal(err)
	}
	audit, err := s.GetCrossMatchReviewCase(ctx, "XM-p")
	if err != nil || audit.Status != CaseStatusClosed || !strings.Contains(audit.Explanation, crossMatchReviewRevoked) {
		t.Fatalf("negative label did not supersede the pending case: %+v %v", audit, err)
	}

	for i := 3; i <= 22; i++ {
		storeThrow(t, s, "p", fmt.Sprintf("m%02d", i), 10)
	}
	next, ok := aggregatePlayer(t, s, "p")
	if !ok || next.Outcome != CrossMatchCaseNewCase || next.CaseID != "XM-p-r2" || next.SupersededCaseID != "XM-p" || next.Status != CaseStatusPending {
		t.Fatalf("20 new unreviewed matches did not open a new case: %+v %t", next, ok)
	}
	queue := pendingFor(t, s, "p")
	if len(queue) != 1 || queue[0].CaseID != "XM-p-r2" || queue[0].MatchCount != 22 || queue[0].Detectors["THROW_001"] != 22 ||
		!strings.Contains(queue[0].Explanation, "XM-p") || strings.Contains(queue[0].Explanation, crossMatchReviewRevoked) {
		t.Fatalf("pending queue after new evidence: %+v", queue)
	}
	if again, _ := s.GetCrossMatchReviewCase(ctx, "XM-p"); again.Status != CaseStatusClosed || again.MatchCount != audit.MatchCount ||
		again.DecayedScore != audit.DecayedScore || again.Explanation != audit.Explanation || again.Detectors["THROW_001"] != 3 {
		t.Fatalf("superseded audit record was rewritten: %+v", again)
	}

	// Re-running aggregation refreshes the open successor; it does not pile up cases.
	storeThrow(t, s, "p", "m23", 10)
	refreshed, _ := aggregatePlayer(t, s, "p")
	if refreshed.Outcome != CrossMatchCaseRefreshed || refreshed.CaseID != "XM-p-r2" {
		t.Fatalf("second run: %+v", refreshed)
	}
	if queue = pendingFor(t, s, "p"); len(queue) != 1 || queue[0].MatchCount != 23 || !strings.Contains(queue[0].Explanation, "Successor of case XM-p,") {
		t.Fatalf("refreshed successor: %+v", queue)
	}
	if n := countRows(t, s, "cross_match_review_cases", "player_id = 'p'"); n != 2 {
		t.Fatalf("case rows = %d, want audit record + current case", n)
	}

	// An aggregate computed BEFORE the review still contains the rejected event.
	// It is reported as ignored and changes nothing.
	ignored, err := s.StoreCrossMatchReviewCaseResult(ctx, *staleAggregate)
	if err != nil || ignored.Outcome != CrossMatchCaseIgnored || ignored.CaseID != "XM-p-r2" {
		t.Fatalf("stale aggregate: %+v %v", ignored, err)
	}
	if queue = pendingFor(t, s, "p"); len(queue) != 1 || queue[0].MatchCount != 23 {
		t.Fatalf("stale aggregate replaced the current case: %+v", queue)
	}
}

// A false_positive verdict covers the matches the moderator saw. Refreshing the
// decided row with a later scope would stretch that verdict over new matches,
// make their events ineligible, and hide the player for good.
func TestDecidedCaseKeepsItsScopeAndNewMatchesOpenASuccessor(t *testing.T) {
	for _, verdict := range []string{VerdictFalsePositive, VerdictConfirmedCheat} {
		t.Run(verdict, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			storeThrow(t, s, "p", "m1", 10)
			storeThrow(t, s, "p", "m2", 10)
			if res, ok := aggregatePlayer(t, s, "p"); !ok || res.CaseID != "XM-p" {
				t.Fatalf("fixture: %+v", res)
			}
			if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "XM-p", ModeratorID: "mod", Verdict: verdict}); err != nil {
				t.Fatal(err)
			}
			// Same evidence again: the decided case stays the player's case.
			if res, ok := aggregatePlayer(t, s, "p"); ok && res.Outcome != CrossMatchCaseRefreshed {
				t.Fatalf("unchanged scope after a verdict: %+v", res)
			}
			if got := pendingFor(t, s, "p"); len(got) != 0 {
				t.Fatalf("a decided case was re-queued without new evidence: %+v", got)
			}

			storeThrow(t, s, "p", "m3", 10)
			storeThrow(t, s, "p", "m4", 10)
			for run := 0; run < 2; run++ { // the second run proves the scope did not move
				res, ok := aggregatePlayer(t, s, "p")
				if !ok || res.CaseID != "XM-p-r2" || res.Status != CaseStatusPending {
					t.Fatalf("run %d with two new matches: %+v %t", run, res, ok)
				}
			}
			decided, err := s.GetCrossMatchReviewCase(ctx, "XM-p")
			if err != nil || decided.Status != CaseStatusDecided || strings.Join(decided.MatchIDs, ",") != "m1,m2" {
				t.Fatalf("verdict scope changed: %+v %v", decided, err)
			}
			events, err := s.GetAllPlayerEvents(ctx, "p")
			wantEligible := map[string]int{VerdictFalsePositive: 2, VerdictConfirmedCheat: 4}[verdict]
			if err != nil || len(events) != wantEligible {
				t.Fatalf("eligible events = %d, want %d (a verdict must not cover matches the moderator never saw)", len(events), wantEligible)
			}
			queue := pendingFor(t, s, "p")
			if len(queue) != 1 || queue[0].CaseID != "XM-p-r2" || queue[0].MatchCount != wantEligible {
				t.Fatalf("pending successor: %+v", queue)
			}
			// The successor is an ordinary case: it can be decided on its own.
			if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "XM-p-r2", ModeratorID: "mod", Verdict: VerdictInconclusive}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Successor ids are scoped to the player: "XM-A-r2" may already be the base
// case of a player literally named "A-r2".
func TestSuccessorCaseIDNeverCollidesWithAnotherPlayersCase(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, player := range []string{"A", "A-r2"} {
		storeThrow(t, s, player, "m1", 10)
		storeThrow(t, s, player, "m2", 10)
		if res, ok := aggregatePlayer(t, s, player); !ok || res.CaseID != "XM-"+player || res.Outcome != CrossMatchCaseCreated {
			t.Fatalf("fixture %s: %+v", player, res)
		}
	}
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "XM-A", ModeratorID: "mod", Verdict: VerdictConfirmedCheat}); err != nil {
		t.Fatal(err)
	}
	storeThrow(t, s, "A", "m3", 10)
	res, ok := aggregatePlayer(t, s, "A")
	if !ok || res.CaseID != "XM-A-r3" || res.Outcome != CrossMatchCaseNewCase {
		t.Fatalf("successor for A: %+v", res)
	}
	other, err := s.GetCrossMatchReviewCase(ctx, "XM-A-r2")
	if err != nil || other.PlayerID != "A-r2" || other.Status != CaseStatusPending || other.MatchCount != 2 {
		t.Fatalf("another player's case was touched: %+v %v", other, err)
	}
	if res, _ := aggregatePlayer(t, s, "A"); res.CaseID != "XM-A-r3" || res.Outcome != CrossMatchCaseRefreshed {
		t.Fatalf("second run for A: %+v", res)
	}
	cases, err := s.ListCrossMatchReviewCasesForPlayer(ctx, "A")
	if err != nil || len(cases) != 2 {
		t.Fatalf("cases for A: %+v %v", cases, err)
	}
}
