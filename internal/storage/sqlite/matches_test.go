package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// TestMatches_ListAndLookup: the desktop history reads match_contexts rows
// newest first, and a single match's row round-trips its context.
func TestMatches_ListAndLookup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i, id := range []string{"m-old", "m-new"} {
		mc := &model.MatchContext{
			MatchID:         id,
			GameMode:        "Echo_Arena",
			StartTime:       time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC),
			PlayerIDs:       []string{"p1"},
			TeamAssignments: map[string]string{"p1": "blue"},
			PlayerNames:     map[string]string{"p1": "One"},
		}
		if err := s.StoreMatchContext(ctx, mc, 10*(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	backdate(t, s, "match_contexts", "ingested_at", "match_id = ?", time.Now().Add(-time.Hour), "m-old")

	list, err := s.ListMatches(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Context.MatchID != "m-new" || list[1].Context.MatchID != "m-old" {
		t.Fatalf("list = %+v", list)
	}
	if list[0].FrameCount != 20 || list[0].IngestedAt.IsZero() || list[0].Context.PlayerNames["p1"] != "One" {
		t.Errorf("row = %+v", list[0])
	}
	if list, err := s.ListMatches(ctx, 1); err != nil || len(list) != 1 {
		t.Errorf("limit: %v %v", list, err)
	}

	sm, err := s.GetStoredMatch(ctx, "m-old")
	if err != nil || sm.Context.GameMode != "Echo_Arena" || sm.FrameCount != 10 {
		t.Errorf("GetStoredMatch = %+v, %v", sm, err)
	}
	if _, err := s.GetStoredMatch(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing match: %v", err)
	}
}

// TestMatches_ScoresCasesFramesAndScore: per-match derived data is read back
// by match id, and the final team score comes from the last stored frame.
func TestMatches_ScoresCasesFramesAndScore(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const match = "m-1"

	frames := append(mkFrames("p1", 0, 5), mkFrames("p2", 0, 3)...)
	frames[len(frames)-1].BlueScore, frames[len(frames)-1].OrangeScore = 3, 1
	frames[len(frames)-1].FrameIndex = 99 // the last frame by index carries the final score
	if _, err := s.StoreTelemetryFrames(ctx, match, frames); err != nil {
		t.Fatal(err)
	}
	counts, err := s.GetMatchPlayerFrameCounts(ctx, match)
	if err != nil || counts["p1"] != 5 || counts["p2"] != 3 || len(counts) != 2 {
		t.Errorf("frame counts = %v, %v", counts, err)
	}
	blue, orange, ok, err := s.GetMatchFinalScore(ctx, match)
	if err != nil || !ok || blue != 3 || orange != 1 {
		t.Errorf("final score = %d-%d ok=%v err=%v", blue, orange, ok, err)
	}
	if _, _, ok, err := s.GetMatchFinalScore(ctx, "empty"); err != nil || ok {
		t.Errorf("empty match score ok=%v err=%v", ok, err)
	}

	// Two snapshots for p1 (the later wins), one for p2, one for another match.
	now := time.Now()
	for _, sc := range []struct {
		m     string
		p     string
		total float64
		at    time.Time
	}{
		{match, "p1", 10, now.Add(-time.Minute)}, {match, "p1", 42, now}, {match, "p2", 5, now}, {"m-2", "p1", 99, now},
	} {
		err := s.StoreMatchSuspicionScore(ctx, sc.m, model.SuspicionScore{PlayerID: sc.p, TotalScore: sc.total, EventCount: 1, SnapshotTime: sc.at})
		if err != nil {
			t.Fatal(err)
		}
	}
	scores, err := s.GetMatchScores(ctx, match)
	if err != nil || len(scores) != 2 || scores["p1"].TotalScore != 42 || scores["p2"].TotalScore != 5 {
		t.Errorf("match scores = %+v, %v", scores, err)
	}

	for _, rc := range []model.ReviewCase{
		{CaseID: "RC-m-1-p1", PlayerID: "p1", MatchID: match, SuspicionScore: 42, Severity: "medium", Status: model.CaseStatusPending, CreatedAt: now},
		{CaseID: "RC-m-1-p2", PlayerID: "p2", MatchID: match, SuspicionScore: 70, Severity: "high", Status: model.CaseStatusPending, CreatedAt: now},
		{CaseID: "RC-m-2-p1", PlayerID: "p1", MatchID: "m-2", SuspicionScore: 99, Severity: "critical", Status: model.CaseStatusPending, CreatedAt: now},
	} {
		if err := s.StoreReviewCase(ctx, rc); err != nil {
			t.Fatal(err)
		}
	}
	cases, err := s.GetReviewCasesByMatch(ctx, match)
	if err != nil || len(cases) != 2 || cases[0].CaseID != "RC-m-1-p2" || cases[1].CaseID != "RC-m-1-p1" {
		t.Errorf("cases = %+v, %v", cases, err)
	}
}
