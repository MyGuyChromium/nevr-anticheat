package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
)

func TestPausedPlayspaceHistoryIsPreservedButExcludedFromNewScoring(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i, mid := range []string{"older-wrist", "newer-playspace"} {
		mc := &model.MatchContext{MatchID: mid, StartTime: time.Unix(1700000000, 0).Add(time.Duration(i) * time.Hour)}
		if err := s.StoreMatchContext(ctx, mc, 0); err != nil {
			t.Fatal(err)
		}
	}
	wrist := mkEvent("THROW_003", "mixed", "older-wrist", 10, .9, .9)
	mustStoreEvent(t, s, wrist)
	for _, player := range []string{"mixed", "paused-only"} {
		for i, id := range []string{"MOV_006", "PAT_005"} {
			event := mkEvent(id, player, "newer-playspace", 20+i, .99, .99)
			mustStoreEvent(t, s, event)
			// A previously positive review is not authority to undo the pause.
			if _, err := s.StoreEventReview(ctx, event.EventID, "yes", "synthetic historical label", "test-reviewer"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, query := range map[string]func(string) ([]model.DetectionEvent, error){
		"aggregate": func(player string) ([]model.DetectionEvent, error) { return s.GetAllPlayerEvents(ctx, player) },
		"history":   func(player string) ([]model.DetectionEvent, error) { return s.GetPlayerHistoryEvents(ctx, player, 1) },
		"provider": func(player string) ([]model.DetectionEvent, error) {
			return NewStoreHistoryProvider(s).GetPlayerDetections(player, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			events, err := query("mixed")
			if err != nil || len(events) != 1 || events[0].EventID != wrist.EventID {
				t.Fatalf("paused matches displaced eligible history or earned points: %+v %v", events, err)
			}
			events, err = query("paused-only")
			if err != nil || len(events) != 0 {
				t.Fatalf("paused-only history remained eligible: %+v %v", events, err)
			}
		})
	}
	players, err := s.GetDistinctPlayersWithEvents(ctx, time.Time{})
	if err != nil || !reflect.DeepEqual(players, []string{"mixed"}) {
		t.Fatalf("aggregate discovery included a paused-only player: %v %v", players, err)
	}
	// Pausing is a read-time eligibility rule, not a destructive migration.
	archived, err := s.GetMatchEvents(ctx, "newer-playspace")
	if err != nil || len(archived) != 4 {
		t.Fatalf("pause deleted historical findings: %+v %v", archived, err)
	}
	for _, event := range archived {
		if !model.IsPlayspaceDetector(event.DetectorID) || event.IsShadow || event.EnforcementWeight != .8 || event.Confidence != .99 {
			t.Fatalf("pause rewrote original evidence or scoring metadata: %+v", event)
		}
	}
	if countRows(t, s, "detection_events", "") != 5 || countRows(t, s, "event_reviews", "") != 4 {
		t.Fatal("pause modified persisted evidence or review records")
	}
}

func TestCrossMatchDirectInputCannotRescorePausedPlayspacing(t *testing.T) {
	wrist := mkEvent("THROW_003", "p", "wrist-match", 10, .9, .9)
	paused := []model.DetectionEvent{
		mkEvent("MOV_006", "p", "playspace-match", 20, .99, .99),
		mkEvent("PAT_005", "p", "playspace-match", 30, .99, .99),
	}
	input := append([]model.DetectionEvent{wrist}, paused...)
	before := append([]model.DetectionEvent(nil), input...)
	for _, historical := range []bool{false, true} {
		cfg := CrossMatchConfig{MaxSingleContribution: 15, CorrelationBonusCap: 15, Now: time.Unix(1700000000, 0)}
		if historical {
			cfg.ScoringByMatch = map[string]scoring.ScorerConfig{
				"wrist-match":     {MaxSingleContribution: 15, CorrelationBonusCap: 15},
				"playspace-match": {MaxSingleContribution: 15, CorrelationBonusCap: 15},
			}
		}
		want := ComputePlayerCrossMatchSummary([]model.DetectionEvent{wrist}, nil, cfg)
		got := ComputePlayerCrossMatchSummary(input, nil, cfg)
		if !reflect.DeepEqual(got, want) || got.ScoredEvents != 1 || got.DecayedScore <= 0 {
			t.Fatalf("paused evidence changed direct aggregate (historical=%v): got=%+v want=%+v", historical, got, want)
		}
		got = ComputePlayerCrossMatchSummary(paused, nil, cfg)
		if got.TotalEvents != 0 || got.ScoredEvents != 0 || got.DecayedScore != 0 || got.DistinctMatches != 0 || len(got.ByDetector) != 0 {
			t.Fatalf("paused-only direct input earned a new finding (historical=%v): %+v", historical, got)
		}
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("aggregate pause mutated caller-owned historical records")
	}
}
