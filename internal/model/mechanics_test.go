package model

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func mechanicsFixture() MechanicsAssessment {
	p := Vec3{1, 2, 3}
	return MechanicsAssessment{Kind: MechanicsGrabGeometry, Result: MechanicsInconclusive, Reason: "grab_geometry_unverified",
		RuleVersion: DefaultProjectRules().Version, FrameIndex: 2, Timestamp: .1, EventID: "test-grab", IntervalStart: .05, IntervalEnd: .1,
		Metrics: map[string]float64{"legal_limit_m": .25}, Limitations: []string{"Geometry unverified."},
		RawSamples: []MechanicsRawSample{{FrameIndex: 1, Timestamp: .05, DiscPosition: &p}}}
}

func TestMechanicsAssessmentDeepCopyRoundTrip(t *testing.T) {
	original := mechanicsFixture()
	copy := original.Clone()
	copy.Metrics["legal_limit_m"] = 9
	copy.Limitations[0] = "changed"
	copy.RawSamples[0].DiscPosition[0] = 99
	if !reflect.DeepEqual(original, mechanicsFixture()) {
		t.Fatal("clone aliases source")
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded MechanicsAssessment
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err = decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Fatal("evidence changed after JSON round trip")
	}
}

func TestMechanicsAssessmentBoundsAndFinite(t *testing.T) {
	for _, mutate := range []func(*MechanicsAssessment){
		func(r *MechanicsAssessment) { r.Metrics["legal_limit_m"] = math.NaN() },
		func(r *MechanicsAssessment) { r.RawSamples[0].DiscPosition[0] = math.Inf(1) },
		func(r *MechanicsAssessment) { r.RawSamples = make([]MechanicsRawSample, MaxMechanicsRawSamples+1) },
		func(r *MechanicsAssessment) { r.Limitations = make([]string, MaxMechanicsLimitations+1) },
		func(r *MechanicsAssessment) { r.IntervalEnd = .01 },
		func(r *MechanicsAssessment) { r.Result = "guilty" },
		func(r *MechanicsAssessment) { r.Reason = "<script>" },
		func(r *MechanicsAssessment) {
			r.Metrics = make(map[string]float64)
			for i := 0; i <= MaxMechanicsMetrics; i++ {
				r.Metrics[string(rune('a'+i))] = 1
			}
		},
	} {
		r := mechanicsFixture()
		mutate(&r)
		if r.Validate() == nil {
			t.Fatalf("accepted invalid evidence: %+v", r)
		}
	}
}

func TestMechanicsSupportingRawInputsCloneValidateAndRoundtrip(t *testing.T) {
	r := mechanicsFixture()
	speed, bounce, goal := 19.0, 0, Vec3{0, 0, 36}
	r.RawSamples[0].SampleRole = "supporting_release"
	r.RawSamples[0].EventID = "release:test"
	r.RawSamples[0].SampledSpeedMPS = &speed
	r.RawSamples[0].BounceCount = &bounce
	r.RawSamples[0].InferredReferenceGoal = &goal
	clone := r.Clone()
	*clone.RawSamples[0].SampledSpeedMPS = 0
	*clone.RawSamples[0].BounceCount = 99
	clone.RawSamples[0].InferredReferenceGoal[0] = 99
	if *r.RawSamples[0].SampledSpeedMPS != 19 || *r.RawSamples[0].BounceCount != 0 || r.RawSamples[0].InferredReferenceGoal[0] != 0 {
		t.Fatal("supporting raw fields alias clone")
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var restored MechanicsAssessment
	if err := json.Unmarshal(data, &restored); err != nil || restored.Validate() != nil || !reflect.DeepEqual(r, restored) {
		t.Fatal("supporting fields lost during JSON roundtrip")
	}
	for _, mutate := range []func(*MechanicsRawSample){
		func(s *MechanicsRawSample) { *s.SampledSpeedMPS = math.NaN() },
		func(s *MechanicsRawSample) { *s.BounceCount = -1 },
		func(s *MechanicsRawSample) { s.InferredReferenceGoal[0] = math.Inf(1) },
		func(s *MechanicsRawSample) { s.SampleRole = "verified_pocket" },
	} {
		bad := r.Clone()
		mutate(&bad.RawSamples[0])
		if bad.Validate() == nil {
			t.Fatal("malformed supporting input accepted")
		}
	}
	missing := mechanicsFixture()
	if missing.RawSamples[0].SampledSpeedMPS != nil || missing.RawSamples[0].InferredReferenceGoal != nil || missing.Validate() != nil {
		t.Fatal("legacy absence changed")
	}
}
