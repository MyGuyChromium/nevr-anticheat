package model

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestGameRuleReferenceIsNotMatchAuthority(t *testing.T) {
	p := DefaultGameRuleProfile()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Reference.Applicability != RuleReferenceOnly || len(p.Facts) != 16 {
		t.Fatal(p)
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var restored GameRuleProfile
	if err = json.Unmarshal(data, &restored); err != nil || restored.Validate() != nil {
		t.Fatal(err)
	}
	clone := p.Clone()
	clone.Facts[0].Value = "false"
	clone.Limitations[0] = "changed"
	if !reflect.DeepEqual(p, DefaultGameRuleProfile()) {
		t.Fatal("default profile aliases caller")
	}
	if clone.Validate() == nil {
		t.Fatal("mutated literals accepted under pinned identity")
	}
	for _, mutate := range []func(*GameRuleProfile){
		func(p *GameRuleProfile) { p.Reference.Applicability = "verified_match_config" },
		func(p *GameRuleProfile) { p.Reference.SourceRevision = "unverified" },
		func(p *GameRuleProfile) { p.BuildScope = "verified" },
		func(p *GameRuleProfile) { p.Facts = append(p.Facts, GameRuleFact{Key: "throw_cap", Value: "19.0"}) },
	} {
		bad := p.Clone()
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("forged profile accepted")
		}
	}
	if (*GameRuleProfile)(nil).Validate() != nil || (*RuleReference)(nil).Validate() != nil {
		t.Fatal("legacy absent reference rejected")
	}
}

func TestStunMechanicsOwnershipBoundsAndPersistence(t *testing.T) {
	pos, stunned, counter := Vec3{1, 2, 3}, false, 0
	r := mechanicsFixture()
	r.Kind = MechanicsStunContact
	r.PlayerID = "victim"
	r.RuleReference = DefaultGameRuleReference()
	r.StunCandidates = []StunCandidate{{PlayerID: "candidate", Team: "orange", FrameStart: 1, FrameEnd: 2, TimeStart: .05, TimeEnd: .1, CounterBefore: 0, CounterAfter: 1}}
	r.RawSamples = []MechanicsRawSample{{FrameIndex: 1, Timestamp: .05, SampleRole: "stun_before", PlayerID: "victim", HeadPosition: &pos, IsStunned: &stunned, Stuns: &counter}}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := r.Clone()
	clone.RuleReference.Applicability = "verified"
	clone.StunCandidates[0].PlayerID = "different"
	*clone.RawSamples[0].IsStunned = true
	*clone.RawSamples[0].Stuns = 5
	clone.RawSamples[0].HeadPosition[0] = 99
	if r.RuleReference.Applicability != RuleReferenceOnly || r.StunCandidates[0].PlayerID != "candidate" || *r.RawSamples[0].IsStunned || *r.RawSamples[0].Stuns != 0 || r.RawSamples[0].HeadPosition[0] != 1 {
		t.Fatal("new evidence aliases clone")
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var restored MechanicsAssessment
	if err = json.Unmarshal(data, &restored); err != nil || restored.Validate() != nil || !reflect.DeepEqual(r, restored) {
		t.Fatal("stun evidence changed in roundtrip", err)
	}
	for _, mutate := range []func(*MechanicsAssessment){
		func(r *MechanicsAssessment) { r.Result = MechanicsValidatedViolation },
		func(r *MechanicsAssessment) { r.Result = MechanicsAnomaly },
		func(r *MechanicsAssessment) { r.Result = MechanicsConsistent },
		func(r *MechanicsAssessment) { r.Kind = MechanicsGrabGeometry },
		func(r *MechanicsAssessment) { r.RuleReference.Applicability = "verified" },
		func(r *MechanicsAssessment) { r.StunCandidates = append(r.StunCandidates, r.StunCandidates[0]) },
		func(r *MechanicsAssessment) { r.StunCandidates[0].PlayerID = "victim" },
		func(r *MechanicsAssessment) { r.StunCandidates[0].TimeEnd = math.NaN() },
		func(r *MechanicsAssessment) { r.StunCandidates[0].TimeEnd = .05 },
		func(r *MechanicsAssessment) { r.StunCandidates[0].FrameEnd = 1 },
		func(r *MechanicsAssessment) { r.StunCandidates[0].CounterAfter = 0 },
		func(r *MechanicsAssessment) { *r.RawSamples[0].Stuns = -1 },
		func(r *MechanicsAssessment) { r.RawSamples[0].HeadPosition[0] = math.Inf(1) },
	} {
		bad := r.Clone()
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid stun evidence accepted")
		}
	}
}
