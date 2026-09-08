package sqlite

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
)

func TestScoringHistoryHonorsLatestHumanInvalidation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	event := mkEvent("MOV_001", "p", "m", 10, .9, .9)
	mustStoreEvent(t, s, event)
	assertEligible := func(want int) {
		t.Helper()
		for _, query := range []func() ([]model.DetectionEvent, error){
			func() ([]model.DetectionEvent, error) { return s.GetAllPlayerEvents(ctx, "p") },
			func() ([]model.DetectionEvent, error) { return s.GetPlayerHistoryEvents(ctx, "p", 20) },
		} {
			events, err := query()
			if err != nil || len(events) != want {
				t.Fatalf("eligible=%d want=%d err=%v", len(events), want, err)
			}
		}
	}
	assertEligible(1)
	if _, err := s.StoreEventReview(ctx, event.EventID, "no", "legal", "reviewer"); err != nil {
		t.Fatal(err)
	}
	assertEligible(0)
	// Same raw observation, same detector version, fresh derived ID on re-analysis:
	// a negative label remains relevant rather than being silently orphaned.
	if _, err := s.DeleteMatchEvents(ctx, "m"); err != nil {
		t.Fatal(err)
	}
	event.EventID = "reprocessed-event"
	mustStoreEvent(t, s, event)
	assertEligible(0)
	if _, err := s.StoreEventReview(ctx, event.EventID, "yes", "corrected review", "reviewer"); err != nil {
		t.Fatal(err)
	}
	assertEligible(1)
	if err := s.StoreReviewCase(ctx, model.ReviewCase{CaseID: "case", MatchID: "m", PlayerID: "p", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "case", ModeratorID: "reviewer", Verdict: model.VerdictFalsePositive, DecidedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	assertEligible(0)
	if err := s.UpdateReviewCaseStatus(ctx, "case", model.CaseStatusAppealed, "reviewer"); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "case", ModeratorID: "reviewer", Verdict: model.VerdictConfirmedCheat, DecidedAt: time.Now().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	assertEligible(1)
	if countRows(t, s, "detection_events", "") != 1 || countRows(t, s, "event_reviews", "") != 2 {
		t.Fatal("review invalidation deleted audit evidence")
	}
}

func TestScoringHistoryExcludesZeroWeightMetaAndScopedCrossMatchOverturn(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i, id := range []string{"MOV_001", "BIO_001", "PAT_003", "PAT_004"} {
		event := mkEvent(id, "p", "m", 10+i, 1, 1)
		if id == "BIO_001" {
			event.EnforcementWeight = 0
		}
		mustStoreEvent(t, s, event)
	}
	mustStoreEvent(t, s, mkEvent("MOV_001", "p", "other", 10, 1, 1))
	if err := s.StoreCrossMatchReviewCase(ctx, CrossMatchReviewCase{CaseID: "XM-p", PlayerID: "p", MatchIDs: []string{"m"}, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreModeratorDecision(ctx, model.ModeratorDecision{CaseID: "XM-p", ModeratorID: "r", Verdict: model.VerdictFalsePositive}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []func() ([]model.DetectionEvent, error){
		func() ([]model.DetectionEvent, error) { return s.GetAllPlayerEvents(ctx, "p") },
		func() ([]model.DetectionEvent, error) { return s.GetPlayerHistoryEvents(ctx, "p", 20) },
	} {
		events, err := query()
		if err != nil || len(events) != 1 || events[0].MatchID != "other" {
			t.Fatalf("scope/weight/meta leak: %+v %v", events, err)
		}
	}
}

func TestCrossMatchReevaluationUsesSharedScorerCooldownAndDiminishing(t *testing.T) {
	cfg := scoring.ScorerConfig{MaxSingleContribution: 15, MaxContribPerDetectorPerMatch: 2, SameCategoryDiminishing: .8, CooldownFrames: 300, CorrelationBonusCap: 15}
	events := []model.DetectionEvent{
		mkEvent("MOV_001", "p", "m", 0, 1, 1),
		mkEvent("MOV_001", "p", "m", 100, 1, 1), // retained incident, inside scoring cooldown
		mkEvent("MOV_002", "p", "m", 200, 1, 1), // same-category diminishing
		mkEvent("BIO_001", "p", "m", 300, 1, 1), // independent category bonus
		mkEvent("MOV_001", "p", "m", 1000, 1, 1),
		mkEvent("PAT_004", "p", "m", 2000, 1, 1), // derived, not new evidence
	}
	zero := mkEvent("STATE_002", "p", "zero-only", 3000, 1, 1)
	zero.EnforcementWeight = 0
	events = append(events, zero)
	scorer := scoring.NewSuspicionScorer(cfg)
	for _, event := range events {
		scorer.IngestEvent(event)
	}
	scorer.ApplyCorrelationBonus()
	want := scorer.GetScore("p")
	xcfg := CrossMatchConfig{MaxSingleContribution: cfg.MaxSingleContribution, MaxContribPerDetectorPerMatch: cfg.MaxContribPerDetectorPerMatch,
		SameCategoryDiminishing: cfg.SameCategoryDiminishing, CooldownFrames: cfg.CooldownFrames, CorrelationBonusCap: cfg.CorrelationBonusCap}
	got := ComputePlayerCrossMatchSummary(events, nil, xcfg)
	if math.Abs(got.DecayedScore-want.TotalScore) > 1e-9 || got.ScoredEvents != want.EventCount || got.DistinctMatches != 1 || got.ScoringBasis != "current_config_reevaluation" {
		t.Fatalf("cross-match invented accepted contributions: got=%+v want=%+v", got, want)
	}
	// A retry of the same immutable ID cannot inflate raw or scored counts.
	retry := append(append([]model.DetectionEvent(nil), events...), events[0])
	if repeated := ComputePlayerCrossMatchSummary(retry, nil, xcfg); repeated.TotalEvents != got.TotalEvents || repeated.DecayedScore != got.DecayedScore {
		t.Fatal("retry inflated cross-match summary")
	}
	historical := cfg
	historical.MaxSingleContribution = 1
	xcfg.ScoringByMatch = map[string]scoring.ScorerConfig{"m": historical}
	old := ComputePlayerCrossMatchSummary(events, nil, xcfg)
	if old.ScoringBasis != "provided_match_configuration" || old.DecayedScore >= got.DecayedScore {
		t.Fatalf("provided historical configuration ignored: %+v", old)
	}
}
