package sqlite

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func syntheticStunCoverage() map[string]*model.PlayerCoverage {
	profile := model.DefaultGameRuleProfile()
	behavior, _ := model.DetectorBehavior("THROW_003")
	before, after, zero, one := false, true, 0, 1
	pose := model.Vec3{1, 2, 3}
	log := model.NewMechanicsReviewLog()
	log.Add(model.MechanicsAssessment{
		Kind: model.MechanicsStunContact, Result: model.MechanicsInconclusive, Reason: "stun_end_of_stream",
		ReasonDescription: "Synthetic retained onset with unverified contact geometry.", RuleVersion: "synthetic-stun-v1",
		RuleReference: model.DefaultGameRuleReference(), EventID: "stun:synthetic-victim:11", PlayerID: "synthetic-victim",
		Source: "echoreplay", Authority: "client_reported", SessionID: "synthetic-session", TimeBasis: "recorder_prefix",
		CoordinateSpace: "arena_world", Object: "stun_contact", FrameIndex: 11, Timestamp: 1.1, IntervalStart: 1, IntervalEnd: 1.1,
		Metrics:        map[string]float64{"candidate_players": 1, "reference_head_radius_m": .15},
		Limitations:    []string{"A counter timing candidate does not establish an attacker or contact legality."},
		StunCandidates: []model.StunCandidate{{PlayerID: "synthetic-candidate", Team: "orange", FrameStart: 10, FrameEnd: 11, TimeStart: 1, TimeEnd: 1.1, CounterBefore: 0, CounterAfter: 1}},
		RawSamples: []model.MechanicsRawSample{
			{PlayerID: "synthetic-victim", FrameIndex: 10, Timestamp: 1, SampleRole: "stun_before", HeadPosition: &pose, IsStunned: &before, Stuns: &zero},
			{PlayerID: "synthetic-victim", FrameIndex: 11, Timestamp: 1.1, SampleRole: "stun_onset", HeadPosition: &pose, IsStunned: &after, Stuns: &zero},
			{PlayerID: "synthetic-candidate", FrameIndex: 10, Timestamp: 1, SampleRole: "stun_candidate_before", LeftHand: &pose, IsStunned: &before, Stuns: &zero},
			{PlayerID: "synthetic-candidate", FrameIndex: 11, Timestamp: 1.1, SampleRole: "stun_candidate_after", RightHand: &pose, IsStunned: &before, Stuns: &one},
		},
	})
	return map[string]*model.PlayerCoverage{"synthetic-victim": {
		Version: 1, Status: model.ReviewStatusInsufficientData, GameRuleProfile: &profile,
		DataHealth: &model.DataHealth{Version: 1, State: model.HealthDegraded, LastFrame: 11, Revision: 1, DegradedSamples: 12},
		Detectors: []model.DetectorCoverage{
			{DetectorID: "STATE_007", Enabled: false, ReviewOnlyDiagnostics: true, MechanicsReview: log},
			{DetectorID: "THROW_003", Enabled: true, Behavior: &behavior},
		},
	}}
}

func storedDetector(t *testing.T, player *model.PlayerCoverage, id string) *model.DetectorCoverage {
	t.Helper()
	for i := range player.Detectors {
		if player.Detectors[i].DetectorID == id {
			return &player.Detectors[i]
		}
	}
	t.Fatalf("missing detector %s", id)
	return nil
}

func TestGameRuleCoverageLiveMergeReopenPreservesReviewMetadataAndRawEvidence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "synthetic-rule-review.db")
	s := newTestStoreAt(t, path)
	incoming := syntheticStunCoverage()
	wantRecord := incoming["synthetic-victim"].Detectors[0].MechanicsReview.Records[0].Clone()
	for i := 0; i < 2; i++ {
		if err := s.MergeMatchCatchReviews(ctx, "synthetic-match", incoming); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.GetMatchAnalysisCoverage(ctx, "synthetic-match")
	if err != nil {
		t.Fatal(err)
	}
	// Caller-owned data must not be retained through pointers or reused slices.
	incoming["synthetic-victim"].GameRuleProfile.Facts[0].Value = "caller-mutated"
	incoming["synthetic-victim"].GameRuleProfile.Limitations[0] = "caller-mutated"
	incoming["synthetic-victim"].Detectors[1].Behavior.Label = "caller-mutated"
	*incoming["synthetic-victim"].Detectors[0].MechanicsReview.Records[0].RawSamples[0].IsStunned = true
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = newTestStoreAt(t, path)
	got, err := s.GetMatchAnalysisCoverage(ctx, "synthetic-match")
	if err != nil || !reflect.DeepEqual(before, got) {
		t.Fatalf("reopen changed retained metadata/evidence: %v", err)
	}
	p := got["synthetic-victim"]
	profile := model.DefaultGameRuleProfile()
	if !reflect.DeepEqual(p.GameRuleProfile, &profile) || p.Status != model.ReviewStatusInsufficientData || p.ValidFrames != 0 {
		t.Fatal("lost reference profile or fabricated live analysis coverage")
	}
	stun := storedDetector(t, p, "STATE_007")
	if stun.Enabled || !stun.ReviewOnlyDiagnostics || stun.MechanicsReview.Total != 1 || stun.MechanicsReview.Inconclusive != 1 ||
		!reflect.DeepEqual(stun.MechanicsReview.Records[0], wantRecord) {
		t.Fatalf("lost review-only status or raw evidence: %+v", stun)
	}
	behavior, _ := model.DetectorBehavior("THROW_003")
	if gotBehavior := storedDetector(t, p, "THROW_003").Behavior; gotBehavior == nil || *gotBehavior != behavior {
		t.Fatal("behavior descriptor lost or aliased")
	}
}

func TestGameRuleCoverageMetadataCannotRewindOrDisappearOnLegacyChunks(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	initial := syntheticStunCoverage()
	if err := s.MergeMatchCatchReviews(ctx, "synthetic-match", initial); err != nil {
		t.Fatal(err)
	}
	newer := syntheticStunCoverage()
	newer["synthetic-victim"].DataHealth.Revision = 2
	newer["synthetic-victim"].Detectors[1].Behavior.Label = "Synthetic newer descriptive wording"
	if err := s.MergeMatchCatchReviews(ctx, "synthetic-match", newer); err != nil {
		t.Fatal(err)
	}
	if err := s.MergeMatchCatchReviews(ctx, "synthetic-match", initial); err != nil {
		t.Fatal(err)
	}
	legacy := syntheticStunCoverage()
	legacy["synthetic-victim"].DataHealth.Revision = 3
	legacy["synthetic-victim"].GameRuleProfile = nil
	legacy["synthetic-victim"].Detectors[0].ReviewOnlyDiagnostics = false
	legacy["synthetic-victim"].Detectors[1].Behavior = nil
	if err := s.MergeMatchCatchReviews(ctx, "synthetic-match", legacy); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMatchAnalysisCoverage(ctx, "synthetic-match")
	if err != nil {
		t.Fatal(err)
	}
	p := got["synthetic-victim"]
	if p.GameRuleProfile == nil || p.DataHealth.Revision != 3 || !storedDetector(t, p, "STATE_007").ReviewOnlyDiagnostics ||
		storedDetector(t, p, "THROW_003").Behavior.Label != "Synthetic newer descriptive wording" {
		t.Fatalf("stale or missing metadata rewound persisted review context: %+v", p)
	}
}

func TestGameRuleCoverageRejectsForgedProfileAndReferenceAtomically(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	if err := s.MergeMatchCatchReviews(ctx, "synthetic-match", syntheticStunCoverage()); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetMatchAnalysisCoverage(ctx, "synthetic-match")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*model.PlayerCoverage){
		"profile-fact": func(p *model.PlayerCoverage) { p.GameRuleProfile.Facts[0].Value = "999" },
		"profile-authority": func(p *model.PlayerCoverage) {
			p.GameRuleProfile.RecordingConfiguration = "Verified engine configuration"
		},
		"record-reference": func(p *model.PlayerCoverage) {
			p.Detectors[0].MechanicsReview.Records[0].RuleReference.Applicability = "authoritative"
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := syntheticStunCoverage()
			mutate(bad["synthetic-victim"])
			if err := s.MergeMatchCatchReviews(ctx, "synthetic-match", bad); err == nil {
				t.Fatal("forged live reference accepted")
			}
			if _, err := s.WriteMatchAnalysis(ctx, MatchAnalysisWrite{MatchID: "synthetic-match", Coverage: bad, Replace: true}); err == nil {
				t.Fatal("forged offline reference accepted")
			}
			after, err := s.GetMatchAnalysisCoverage(ctx, "synthetic-match")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("rejected write changed retained analysis")
			}
		})
	}
}

func TestGameRuleCoverageReferenceOnlyChunkIsIdempotentAndPartial(t *testing.T) {
	s, ctx := newTestStore(t), context.Background()
	profile := model.DefaultGameRuleProfile()
	incoming := map[string]*model.PlayerCoverage{"synthetic-victim": {GameRuleProfile: &profile}}
	for i := 0; i < 2; i++ {
		if err := s.MergeMatchCatchReviews(ctx, "synthetic-match", incoming); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetMatchAnalysisCoverage(ctx, "synthetic-match")
	if err != nil {
		t.Fatal(err)
	}
	p := got["synthetic-victim"]
	if !reflect.DeepEqual(p.GameRuleProfile, &profile) || p.Status != model.ReviewStatusInsufficientData || p.ValidFrames != 0 || len(p.Detectors) != 0 || len(p.Limitations) != 1 {
		t.Fatal("reference-only metadata inflated coverage or duplicated limitations")
	}
}
