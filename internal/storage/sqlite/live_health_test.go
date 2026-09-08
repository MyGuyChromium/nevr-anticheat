package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestLiveHealthSnapshotsReplaceNotSumAndFailureCannotRecoverViaMerge(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	incoming := map[string]*model.PlayerCoverage{"p": {DataHealth: &model.DataHealth{Version: 1, State: model.HealthDegraded, Reasons: []string{"tracking_missing"}, LastFrame: 20, HealthySamples: 10, DegradedSamples: 11}}}
	for i := 0; i < 2; i++ {
		if err := s.MergeMatchCatchReviews(ctx, "m", incoming); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetMatchAnalysisCoverage(ctx, "m")
	if err != nil || got["p"].DataHealth.HealthySamples != 10 || got["p"].DataHealth.DegradedSamples != 11 || got["p"].Status != model.ReviewStatusInsufficientData {
		t.Fatalf("health snapshot summed or overclaimed: %+v %v", got, err)
	}
	incoming["p"].DataHealth.LastFrame = 30
	incoming["p"].DataHealth.HealthySamples = 20
	if err := s.MergeMatchCatchReviews(ctx, "m", incoming); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkLiveAnalysisIncomplete(ctx, "m", []string{"p"}); err != nil {
		t.Fatal(err)
	}
	incoming["p"].DataHealth.LastFrame = 40
	incoming["p"].DataHealth.State = model.HealthHealthy
	incoming["p"].DataHealth.Reasons = nil
	if err := s.MergeMatchCatchReviews(ctx, "m", incoming); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetMatchAnalysisCoverage(ctx, "m")
	health := got["p"].DataHealth
	if err != nil || health.State != model.HealthBlind || health.HealthySamples != 20 || health.DegradedSamples != 11 || len(health.Reasons) != 2 || health.Reasons[0] != "tracking_missing" {
		t.Fatalf("persistence failure lost reasons/counters or recovered without re-analysis: %+v %v", health, err)
	}
}

func TestLiveHealthSameFrameRevisionAndStaleCapability(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	makeSnapshot := func(revision uint64, state, rule string) map[string]*model.PlayerCoverage {
		return map[string]*model.PlayerCoverage{"p": {DataHealth: &model.DataHealth{Version: 1, LastFrame: 10, Revision: revision, State: state, HealthySamples: 10}, Detectors: []model.DetectorCoverage{{DetectorID: "MOV_001", Enabled: true, Capability: &model.DetectorCapability{DetectorID: "MOV_001", Version: rule, Rule: &model.RuleDefinition{Version: rule, Tests: []string{"regression"}, ConfiguredParameters: json.RawMessage(`{"limit":12}`)}}}}}}
	}
	old := makeSnapshot(1, model.HealthHealthy, "old")
	if err := s.MergeMatchCatchReviews(ctx, "m", old); err != nil {
		t.Fatal(err)
	}
	newer := makeSnapshot(2, model.HealthBlind, "new")
	if err := s.MergeMatchCatchReviews(ctx, "m", newer); err != nil {
		t.Fatal(err)
	}
	newer["p"].Detectors[0].Capability.Rule.ConfiguredParameters[0] = 'x'
	newer["p"].Detectors[0].Capability.Rule.Tests[0] = "mutated"
	if err := s.MergeMatchCatchReviews(ctx, "m", old); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMatchAnalysisCoverage(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	p := got["p"]
	if p.DataHealth.State != model.HealthBlind || p.DataHealth.Revision != 2 || p.Detectors[0].Capability.Version != "new" || p.Detectors[0].Capability.Rule.Tests[0] != "regression" || string(p.Detectors[0].Capability.Rule.ConfiguredParameters) != `{"limit":12}` {
		t.Fatalf("stale/aliased snapshot overwrote health or rule: %+v", p)
	}
}
