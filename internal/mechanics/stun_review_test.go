package mechanics

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Synthetic data exercises retention/abstention contracts, not Echo contact
// legality or independently measured detector accuracy.
func stunFixture(frame int) (*model.MatchContext, map[string]*model.PlayerState) {
	players := make(map[string]*model.PlayerState)
	for i, id := range []string{"victim", "opponent-a", "opponent-b", "teammate"} {
		team := "orange"
		if id == "victim" || id == "teammate" {
			team = "blue"
		}
		head := model.Vec3{float64(i + 1), 2, 3}
		ps := &model.PlayerState{PlayerID: id, Team: team, LastFrameIdx: frame, LastTimestamp: 10 + float64(frame)*.05,
			IsStunnedKnown: true, StunsKnown: true, Position: model.Vec3{1, 1, 1}, HeadPosition: &head,
			LeftHand: model.Vec3{float64(i + 1), 1, 3}, RightHand: model.Vec3{float64(i + 1), 1, 4}}
		ps.Observation = &model.ObservationContext{Source: "echoreplay", SourceID: "private://synthetic?token=never-export",
			Authority: "client_reported", TimeBasis: "recorder_prefix", SessionID: "synthetic-session", FrameIndex: frame, Timestamp: ps.LastTimestamp}
		ps.IsStunned = id == "victim" && frame >= 2
		if id == "opponent-a" && frame >= 2 {
			ps.PrevStuns = 1
		}
		players[id] = ps
	}
	return &model.MatchContext{MatchID: "synthetic-match"}, players
}

func stunSequence(r *StunReviewer, from, through int, mutate func(int, map[string]*model.PlayerState)) []model.MechanicsAssessment {
	var out []model.MechanicsAssessment
	for frame := from; frame <= through; frame++ {
		mc, players := stunFixture(frame)
		if mutate != nil {
			mutate(frame, players)
		}
		out = append(out, r.Observe(mc, players, frame)...)
	}
	return out
}

func requireStunReview(t *testing.T, records []model.MechanicsAssessment, reason string) model.MechanicsAssessment {
	t.Helper()
	if len(records) != 1 {
		t.Fatalf("got %d reviews, want 1: %+v", len(records), records)
	}
	out := records[0]
	if err := out.Validate(); err != nil {
		t.Fatalf("invalid review: %v: %+v", err, out)
	}
	if out.Kind != model.MechanicsStunContact || out.Result != model.MechanicsInconclusive || out.Reason != reason {
		t.Fatalf("unexpected review classification: %+v", out)
	}
	if out.VerifiedEngineBuild != "" || out.ValidationReference != "" || out.GeometryDefinition != "" {
		t.Fatalf("unverified physics promoted to verified knowledge: %+v", out)
	}
	return out
}

func TestStunReviewRetainsOnsetAndCounterCandidateWithoutVerdict(t *testing.T) {
	r := NewStunReviewer()
	out := requireStunReview(t, stunSequence(r, 0, 8, nil), "stun_contact_geometry_unverified")
	if out.PlayerID != "victim" || out.FrameIndex != 2 || out.Timestamp != 10.1 || out.IntervalStart != 10.05 || out.IntervalEnd != 10.1 {
		t.Fatalf("lost original victim onset interval: %+v", out)
	}
	if out.RuleReference == nil || *out.RuleReference != *model.DefaultGameRuleReference() || out.RuleVersion != stunReviewVersion {
		t.Fatalf("missing reference profile/version: %+v", out)
	}
	if len(out.StunCandidates) != 1 || out.StunCandidates[0].PlayerID != "opponent-a" || out.StunCandidates[0].CounterBefore != 0 || out.StunCandidates[0].CounterAfter != 1 {
		t.Fatalf("lost raw counter transition: %+v", out.StunCandidates)
	}
	if len(out.RawSamples) != 4 || out.RawSamples[0].SampleRole != "stun_before" || out.RawSamples[1].SampleRole != "stun_onset" ||
		out.RawSamples[0].IsStunned == nil || *out.RawSamples[0].IsStunned || out.RawSamples[1].IsStunned == nil || !*out.RawSamples[1].IsStunned ||
		out.RawSamples[2].Stuns == nil || *out.RawSamples[2].Stuns != 0 || out.RawSamples[3].Stuns == nil || *out.RawSamples[3].Stuns != 1 {
		t.Fatalf("nullable false/zero or endpoint roles lost: %+v", out.RawSamples)
	}
	if out.Metrics["reference_left_hand_radius_m"] != .06 || out.Metrics["reference_head_radius_m"] != .15 || out.Metrics["reference_punch_offset_y_m"] != -.04 {
		t.Fatal("reference dimensions missing")
	}
	for key := range out.Metrics {
		if strings.Contains(key, "legal") || strings.Contains(key, "distance") {
			t.Fatalf("unverified geometric verdict metric: %s", key)
		}
	}
	if later := r.Flush("stun_end_of_stream"); len(later) != 0 {
		t.Fatal("mature review emitted twice")
	}
}

func TestStunReviewPreservesAllOpponentsNeverSelectsNearest(t *testing.T) {
	out := requireStunReview(t, stunSequence(NewStunReviewer(), 0, 8, func(frame int, ps map[string]*model.PlayerState) {
		ps["opponent-a"].LeftHand = model.Vec3{100, 100, 100}
		ps["opponent-b"].LeftHand = *ps["victim"].HeadPosition
		if frame >= 3 {
			ps["opponent-b"].PrevStuns = 1
		}
		if frame >= 2 {
			ps["teammate"].PrevStuns = 1
		}
	}), "stun_attribution_ambiguous")
	if len(out.StunCandidates) != 2 || out.StunCandidates[0].PlayerID != "opponent-a" || out.StunCandidates[1].PlayerID != "opponent-b" || out.Metrics["candidate_counter_increments"] != 2 {
		t.Fatalf("discarded a plausible opponent or included same-team count: %+v", out)
	}
}

func TestStunReviewRequiresExplicitFreshFalseToTrue(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(int, map[string]*model.PlayerState)
	}{
		{"missing-before", func(f int, ps map[string]*model.PlayerState) { ps["victim"].IsStunnedKnown = f != 1 }},
		{"missing-onset", func(f int, ps map[string]*model.PlayerState) { ps["victim"].IsStunnedKnown = f != 2 }},
		{"already-stunned-first-frame", func(_ int, ps map[string]*model.PlayerState) { ps["victim"].IsStunned = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewStunReviewer()
			out := stunSequence(r, 0, 8, tc.mutate)
			out = append(out, r.Flush("stun_end_of_stream")...)
			if len(out) != 0 {
				t.Fatalf("fabricated an onset: %+v", out)
			}
		})
	}
	out := requireStunReview(t, stunSequence(NewStunReviewer(), 0, 8, func(_ int, ps map[string]*model.PlayerState) {
		ps["opponent-a"].StunsKnown = false
	}), "stun_counter_evidence_unavailable")
	if len(out.StunCandidates) != 0 {
		t.Fatal("missing counter treated as observed zero")
	}
}

func TestStunReviewBoundariesFlushOnceWithoutBridging(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		mutate       func(*model.MatchContext, map[string]*model.PlayerState, *int)
	}{
		{"frame-gap", "stun_sample_gap", func(_ *model.MatchContext, ps map[string]*model.PlayerState, f *int) {
			*f = 4
			for _, p := range ps {
				p.LastFrameIdx = 4
				p.Observation.FrameIndex = 4
			}
		}},
		{"source", "stun_source_changed", func(_ *model.MatchContext, ps map[string]*model.PlayerState, _ *int) {
			for _, p := range ps {
				p.Observation.SourceID = "other-private-source"
			}
		}},
		{"epoch", "stun_source_changed", func(_ *model.MatchContext, ps map[string]*model.PlayerState, _ *int) {
			for _, p := range ps {
				p.Observation.SourceEpoch++
			}
		}},
		{"match", "stun_source_changed", func(mc *model.MatchContext, _ map[string]*model.PlayerState, _ *int) { mc.MatchID = "next-match" }},
		{"clock", "stun_clock_unavailable", func(_ *model.MatchContext, ps map[string]*model.PlayerState, _ *int) {
			for _, p := range ps {
				p.LastTimestamp = 10.1
				p.Observation.Timestamp = 10.1
			}
		}},
		{"missing-source", "stun_source_unavailable", func(_ *model.MatchContext, ps map[string]*model.PlayerState, _ *int) { ps["victim"].Observation = nil }},
		{"missing-player", "stun_roster_changed", func(_ *model.MatchContext, ps map[string]*model.PlayerState, _ *int) { delete(ps, "opponent-a") }},
		{"counter-reset", "stun_counter_reset", func(_ *model.MatchContext, ps map[string]*model.PlayerState, _ *int) { ps["opponent-a"].PrevStuns = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewStunReviewer()
			if out := stunSequence(r, 0, 2, nil); len(out) != 0 {
				t.Fatal("onset finalized before its window")
			}
			mc, ps := stunFixture(3)
			frame := 3
			tc.mutate(mc, ps, &frame)
			out := requireStunReview(t, r.Observe(mc, ps, frame), tc.reason)
			if out.FrameIndex != 2 || len(out.StunCandidates) != 1 {
				t.Fatalf("boundary lost prior onset/candidate: %+v", out)
			}
			if again := r.Flush("stun_end_of_stream"); len(again) != 0 {
				t.Fatal("flushed evidence emitted twice")
			}
		})
	}
	for _, reason := range []string{"stun_end_of_stream", "stun_inactive_phase", "stun_review_interrupted"} {
		r := NewStunReviewer()
		stunSequence(r, 0, 2, nil)
		request := reason
		if reason == "stun_review_interrupted" {
			request = "private://unsafe-report-text"
		}
		requireStunReview(t, r.Flush(request), reason)
		if again := r.Flush(request); len(again) != 0 {
			t.Fatal("flush is not idempotent")
		}
		if after := stunSequence(r, 3, 8, nil); len(after) != 0 {
			t.Fatal("flush bridged already-stunned baseline")
		}
	}
}

func TestStunReviewBeforeAfterWindowAndRepeatedCounts(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		incrementAt, expected int
		reason                string
	}{
		{"before-onset", 1, 1, "stun_contact_geometry_unverified"},
		{"after-onset", 5, 1, "stun_contact_geometry_unverified"},
		{"after-window", 9, 0, "stun_no_counter_candidate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := requireStunReview(t, stunSequence(NewStunReviewer(), 0, 10, func(f int, ps map[string]*model.PlayerState) {
				ps["opponent-a"].PrevStuns = 0
				if f >= tc.incrementAt {
					ps["opponent-a"].PrevStuns = 1
				}
			}), tc.reason)
			if len(out.StunCandidates) != tc.expected {
				t.Fatalf("wrong candidate window: %+v", out.StunCandidates)
			}
		})
	}
	out := requireStunReview(t, stunSequence(NewStunReviewer(), 0, 8, func(f int, ps map[string]*model.PlayerState) {
		if f >= 4 {
			ps["opponent-a"].PrevStuns = 2
		}
	}), "stun_attribution_ambiguous")
	if len(out.StunCandidates) != 1 || out.StunCandidates[0].CounterAfter != 2 || out.Metrics["candidate_counter_increments"] != 2 {
		t.Fatalf("multiple increments collapsed into a single known attacker: %+v", out)
	}
}

func TestStunReviewDetachedRawInputsAndPrivateSourceExcluded(t *testing.T) {
	r := NewStunReviewer()
	stunSequence(r, 0, 1, nil)
	mc, ps := stunFixture(2)
	if out := r.Observe(mc, ps, 2); len(out) != 0 {
		t.Fatal("premature review")
	}
	for _, p := range ps {
		*p.HeadPosition = model.Vec3{999, 999, 999}
		p.LeftHand = model.Vec3{999, 999, 999}
		p.PrevStuns = 999
		p.Observation.SourceID = "mutated-private-source"
	}
	out := requireStunReview(t, stunSequence(r, 3, 8, nil), "stun_contact_geometry_unverified")
	if out.RawSamples[1].HeadPosition[0] != 1 || out.RawSamples[3].Stuns == nil || *out.RawSamples[3].Stuns != 1 {
		t.Fatalf("caller mutated retained evidence: %+v", out.RawSamples)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private") || strings.Contains(string(data), "never-export") || strings.Contains(string(data), "source_id") {
		t.Fatalf("private source locator reached exported evidence: %s", data)
	}
	copy := out.Clone()
	*copy.RawSamples[0].IsStunned = true
	copy.RawSamples[1].HeadPosition[0] = 999
	copy.StunCandidates[0].CounterBefore = 999
	copy.RuleReference.ProfileID = "mutated"
	if *out.RawSamples[0].IsStunned || out.RawSamples[1].HeadPosition[0] != 1 || out.StunCandidates[0].CounterBefore != 0 || out.RuleReference.ProfileID == "mutated" {
		t.Fatal("cloned evidence aliases original")
	}
}

func TestStunReviewMissingPosesNeverUseBodyAsHead(t *testing.T) {
	out := requireStunReview(t, stunSequence(NewStunReviewer(), 0, 8, func(_ int, ps map[string]*model.PlayerState) {
		ps["victim"].HeadPosition = nil
		ps["opponent-a"].LeftHand = model.Vec3{}
		ps["opponent-a"].RightHand = model.Vec3{math.NaN(), 0, 0}
	}), "stun_contact_geometry_unverified")
	if out.RawSamples[0].HeadPosition != nil || out.RawSamples[0].PlayerPosition == nil || out.RawSamples[2].LeftHand != nil || out.RawSamples[2].RightHand != nil {
		t.Fatalf("lost missing-pose semantics: %+v", out.RawSamples)
	}
	if !strings.Contains(strings.Join(out.Limitations, " "), "no body-position substitute") {
		t.Fatal("missing pose limitation absent")
	}
}

func TestStunReviewBoundsAndExtremeCounters(t *testing.T) {
	r := NewStunReviewer()
	stunSequence(r, 0, 2, nil)
	mc, ps := stunFixture(3)
	for i := len(ps); i < 17; i++ {
		p := *ps["teammate"]
		p.PlayerID = fmt.Sprintf("synthetic-%d", i)
		ps[p.PlayerID] = &p
	}
	requireStunReview(t, r.Observe(mc, ps, 3), "stun_roster_unavailable")
	out := requireStunReview(t, stunSequence(NewStunReviewer(), 0, 8, func(f int, ps map[string]*model.PlayerState) {
		if f >= 2 {
			ps["opponent-a"].PrevStuns = int(^uint(0) >> 1)
			ps["opponent-b"].PrevStuns = int(^uint(0) >> 1)
		}
	}), "stun_attribution_ambiguous")
	if out.Metrics["candidate_counter_increments"] <= 0 || math.IsInf(out.Metrics["candidate_counter_increments"], 0) {
		t.Fatal("counter sum overflowed")
	}
	if len(out.RawSamples) > model.MaxMechanicsRawSamples || len(out.Limitations) > model.MaxMechanicsLimitations || len(out.Metrics) > model.MaxMechanicsMetrics {
		t.Fatal("unbounded evidence")
	}
}

func TestStunReviewDuplicateDispatchDoesNotEmitAgain(t *testing.T) {
	r := NewStunReviewer()
	stunSequence(r, 0, 2, nil)
	mc, ps := stunFixture(2)
	for i := 0; i < 3; i++ {
		if out := r.Observe(mc, ps, 2); len(out) != 0 {
			t.Fatal("duplicate dispatch emitted a review")
		}
	}
	requireStunReview(t, stunSequence(r, 3, 8, nil), "stun_contact_geometry_unverified")
}

func TestStunReviewCounterResetHiddenByMissingSampleIsNotAggregated(t *testing.T) {
	out := requireStunReview(t, stunSequence(NewStunReviewer(), 0, 8, func(f int, ps map[string]*model.PlayerState) {
		if f == 3 {
			ps["opponent-a"].StunsKnown = false
		}
		if f == 4 {
			ps["opponent-a"].PrevStuns = 0
		}
	}), "stun_counter_evidence_unavailable")
	if len(out.StunCandidates) != 1 || out.StunCandidates[0].FrameEnd != 2 || out.StunCandidates[0].CounterAfter != 1 {
		t.Fatalf("stitched counts across an unobserved counter epoch: %+v", out.StunCandidates)
	}
}

func TestStunReviewDenseCounterHistoryIsBoundedAndDisclosed(t *testing.T) {
	r := NewStunReviewer()
	var records []model.MechanicsAssessment
	for frame := 0; frame <= 280; frame++ {
		mc, ps := stunFixture(frame)
		for _, p := range ps {
			p.LastTimestamp = 10 + float64(frame)*.001
			p.Observation.Timestamp = p.LastTimestamp
		}
		ps["victim"].IsStunned = frame >= 20
		ps["opponent-a"].PrevStuns = frame
		records = append(records, r.Observe(mc, ps, frame)...)
		if len(r.recent) > stunCounterEdgeLimit || len(r.pending) > stunPlayerLimit {
			t.Fatal("internal collection exceeded its bounds")
		}
	}
	out := requireStunReview(t, records, "stun_evidence_limit")
	if !strings.Contains(strings.Join(out.Limitations, " "), "bounded history") || len(out.RawSamples) > model.MaxMechanicsRawSamples {
		t.Fatal("limited history was presented as complete")
	}
}

func TestStunReviewFlushOrderRetainsCanonicalOnsetFrames(t *testing.T) {
	r := NewStunReviewer()
	stunSequence(r, 0, 4, func(f int, ps map[string]*model.PlayerState) {
		ps["opponent-b"].IsStunned = f >= 3
		ps["teammate"].IsStunned = f >= 4
	})
	out := r.Flush("stun_end_of_stream")
	if len(out) != 3 {
		t.Fatalf("missing pending onsets: %+v", out)
	}
	for i, record := range out {
		if record.FrameIndex != i+2 || record.Result != model.MechanicsInconclusive {
			t.Fatalf("unordered or promoted pending onset: %+v", out)
		}
		if err := record.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
