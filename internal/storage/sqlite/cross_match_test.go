package sqlite

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func xmCfg(now time.Time) CrossMatchConfig {
	return CrossMatchConfig{
		DecayHalfLifeHours:            168,
		MaxSingleContribution:         15,
		MaxContribPerDetectorPerMatch: 2,
		Now:                           now,
	}
}

func TestCrossMatch_CapsMirrorScorer(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	start := now // no decay
	starts := map[string]time.Time{"M1": start, "M2": start}

	// One THROW_001 event alone: sev*conf*weight*100 = 0.9*0.85*0.8*100 = 61.2 -> capped to 15.
	single := []model.DetectionEvent{mkEvent("THROW_001", "P1", "M1", 10, 0.9, 0.85)}
	s := ComputePlayerCrossMatchSummary(single, starts, xmCfg(now))
	if s.CumulativeScore != 15 || s.DecayedScore != 15 {
		t.Fatalf("single event = raw %.1f decayed %.1f, want 15/15", s.CumulativeScore, s.DecayedScore)
	}
	if s.Level != model.LevelClean {
		t.Errorf("one capped event must not be action-worthy: %s", s.Level)
	}

	// 100 BIO_003 events in one match (pipeline rate limit) count at most 2.
	var flood []model.DetectionEvent
	for i := 0; i < 100; i++ {
		flood = append(flood, mkEvent("BIO_003", "P1", "M1", i*3, 0.5, 0.6))
	}
	s = ComputePlayerCrossMatchSummary(flood, starts, xmCfg(now))
	want := 2 * math.Min(0.5*0.6*0.8*100, 15) // 2 x 24 -> 2 x 15
	if s.ScoredEvents != 2 || math.Abs(s.CumulativeScore-want) > 1e-9 {
		t.Fatalf("flood scored=%d raw=%.1f, want 2 / %.1f", s.ScoredEvents, s.CumulativeScore, want)
	}
	if s.TotalEvents != 100 || s.ByDetector["BIO_003"] != 100 {
		t.Errorf("counts must still reflect all events: %+v", s)
	}

	// Same detector in a second match counts again (per detector PER MATCH).
	both := append(flood, mkEvent("BIO_003", "P1", "M2", 1, 0.5, 0.6))
	s = ComputePlayerCrossMatchSummary(both, starts, xmCfg(now))
	if s.ScoredEvents != 3 || s.DistinctMatches != 2 {
		t.Fatalf("two matches: scored=%d matches=%d", s.ScoredEvents, s.DistinctMatches)
	}
	if s.MatchIDs[0] != "M1" || s.MatchIDs[1] != "M2" {
		t.Errorf("match ids not sorted: %v", s.MatchIDs)
	}

	// Total is capped at 100 so Level/thresholds are on the match scale.
	var many []model.DetectionEvent
	for i := 0; i < 20; i++ {
		many = append(many, mkEvent("THROW_001", "P1", "M"+string(rune('A'+i)), 1, 1, 1))
	}
	allStarts := map[string]time.Time{}
	for _, e := range many {
		allStarts[e.MatchID] = start
	}
	s = ComputePlayerCrossMatchSummary(many, allStarts, xmCfg(now))
	if s.DecayedScore != 100 || s.CumulativeScore != 300 {
		t.Fatalf("cap: decayed=%.1f raw=%.1f", s.DecayedScore, s.CumulativeScore)
	}
	if s.Level != model.LevelActionWorthy {
		t.Errorf("level = %s", s.Level)
	}
	// Per-detector/category points are uncapped, like the scorer's maps.
	if s.PointsByCategory["throw"] != 300 || s.PointsByDetector["THROW_001"] != 300 {
		t.Errorf("points maps = %v %v", s.PointsByCategory, s.PointsByDetector)
	}
	// Deterministic regardless of input order.
	rev := make([]model.DetectionEvent, len(many))
	for i := range many {
		rev[len(many)-1-i] = many[i]
	}
	s2 := ComputePlayerCrossMatchSummary(rev, allStarts, xmCfg(now))
	if s2.DecayedScore != s.DecayedScore || s2.CumulativeScore != s.CumulativeScore {
		t.Error("order-dependent result")
	}
}

func TestCrossMatch_DecayAnchoredOnMatchTimeNotStorageTime(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	cfg := xmCfg(now)
	// Match played exactly one half-life ago; event 100s into the match.
	matchStart := now.Add(-168 * time.Hour).Add(-100 * time.Second)
	ev := mkEvent("THROW_001", "P1", "M1", 1500, 1, 1)
	ev.Timestamp = 100
	ev.StoredAt = now // freshly (re)inserted by reprocessing

	s := ComputePlayerCrossMatchSummary([]model.DetectionEvent{ev}, map[string]time.Time{"M1": matchStart}, cfg)
	if math.Abs(s.DecayedScore-7.5) > 1e-9 {
		t.Fatalf("decayed = %.4f, want 7.5 (15 points, one half-life since match time)", s.DecayedScore)
	}
	if s.AnchorFallbacks != 0 {
		t.Errorf("fallbacks = %d", s.AnchorFallbacks)
	}

	// Storage time is irrelevant when the match start is known.
	ev.StoredAt = now.Add(-1000 * time.Hour)
	s2 := ComputePlayerCrossMatchSummary([]model.DetectionEvent{ev}, map[string]time.Time{"M1": matchStart}, cfg)
	if s2.DecayedScore != s.DecayedScore {
		t.Errorf("storage time changed the decay: %.4f vs %.4f", s2.DecayedScore, s.DecayedScore)
	}

	// Without a match start the storage time is the only clock, and it is reported.
	ev.StoredAt = now.Add(-336 * time.Hour)
	s3 := ComputePlayerCrossMatchSummary([]model.DetectionEvent{ev}, nil, cfg)
	if math.Abs(s3.DecayedScore-3.75) > 1e-9 || s3.AnchorFallbacks != 1 {
		t.Errorf("fallback decay = %.4f fallbacks=%d, want 3.75 / 1", s3.DecayedScore, s3.AnchorFallbacks)
	}

	// Empty input.
	e := ComputePlayerCrossMatchSummary(nil, nil, cfg)
	if e.Level != model.LevelClean || e.MatchIDs == nil {
		t.Errorf("empty summary = %+v", e)
	}
}

func TestCrossMatch_ReviewCaseTiersAndThreshold(t *testing.T) {
	for _, tc := range []struct {
		score    float64
		severity string
	}{
		{15, "low"}, {39.9, "low"}, {40, "medium"}, {60, "high"}, {80, "critical"}, {95, "critical"},
	} {
		if got := SeverityForScore(tc.score); got != tc.severity {
			t.Errorf("SeverityForScore(%.1f) = %s, want %s", tc.score, got, tc.severity)
		}
	}
	sum := PlayerCrossMatchSummary{PlayerID: "P1", DecayedScore: 59, CumulativeScore: 200, DistinctMatches: 3, MatchIDs: []string{"a", "b", "c"}}
	if BuildCrossMatchReviewCase(sum, 60) != nil {
		t.Error("case created below threshold")
	}
	rc := BuildCrossMatchReviewCase(sum, 40)
	if rc == nil || rc.Severity != "medium" || rc.CaseID != "XM-P1" || rc.Status != CaseStatusPending {
		t.Errorf("case = %+v", rc)
	}
}

// End-to-end through the store: reprocessing (delete + reinsert with fresh
// created_at) must not change the decayed score.
func TestCrossMatch_ReprocessDoesNotResetDecay(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	matchStart := now.Add(-336 * time.Hour) // two half-lives ago
	if err := s.StoreMatchContext(ctx, &model.MatchContext{MatchID: "M1", StartTime: matchStart, PlayerIDs: []string{"P1"}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreMatchContext(ctx, &model.MatchContext{MatchID: "M2", StartTime: matchStart, PlayerIDs: []string{"P1"}}, 1); err != nil {
		t.Fatal(err)
	}
	orig := []model.DetectionEvent{mkEvent("THROW_001", "P1", "M1", 1, 1, 1), mkEvent("THROW_001", "P1", "M2", 1, 1, 1)}
	for _, e := range orig {
		e.StoredAt = matchStart
		mustStoreEvent(t, s, e)
	}
	compute := func() PlayerCrossMatchSummary {
		evs, err := s.GetAllPlayerEvents(ctx, "P1")
		if err != nil {
			t.Fatal(err)
		}
		starts, err := s.GetMatchStartTimes(ctx, []string{"M1", "M2"})
		if err != nil {
			t.Fatal(err)
		}
		return ComputePlayerCrossMatchSummary(evs, starts, xmCfg(now))
	}
	before := compute()
	if math.Abs(before.DecayedScore-7.5) > 1e-3 { // 2 x 15 x 0.25 (second-precision start time)
		t.Fatalf("before = %.4f, want 7.5", before.DecayedScore)
	}

	// Reprocess: events deleted and re-inserted now.
	for _, mid := range []string{"M1", "M2"} {
		if _, _, err := s.DeleteMatchAnalysis(ctx, mid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StoreDetectionEvents(ctx, []model.DetectionEvent{
		mkEvent("THROW_001", "P1", "M1", 1, 1, 1), mkEvent("THROW_001", "P1", "M2", 1, 1, 1)}, "reprocess"); err != nil {
		t.Fatal(err)
	}
	after := compute()
	if math.Abs(after.DecayedScore-before.DecayedScore) > 1e-6 {
		t.Fatalf("reprocessing reset decay: %.4f -> %.4f", before.DecayedScore, after.DecayedScore)
	}

	// Shadow events never reach aggregation.
	sh := mkEvent("PAT_001", "P1", "M1", 9, 1, 1)
	sh.IsShadow = true
	mustStoreEvent(t, s, sh)
	if compute().TotalEvents != 2 {
		t.Error("shadow event leaked into cross-match aggregation")
	}
	players, _ := s.GetDistinctPlayersWithEvents(ctx, now.Add(-time.Hour))
	if len(players) != 1 || players[0] != "P1" {
		t.Errorf("distinct players = %v", players)
	}
}
