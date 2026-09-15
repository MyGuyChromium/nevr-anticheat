package flyagent

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultPolicyAdapterPreservesOriginalDecode(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := NewNetwork(topology, DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	adaptedNetwork, err := NewNetwork(topology, DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	encoder, tick := testTick(false, GoalPositiveZ)
	observation, err := encoder.Encode(tick)
	if err != nil {
		t.Fatal(err)
	}
	adapter := DefaultPolicyAdapter()
	adaptedCurrents, err := adapter.AdaptCurrents(observation.Currents)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(adaptedCurrents, observation.Currents) {
		t.Fatal("default sensory adapter changed currents")
	}
	if err := baseline.Step(observation.DeltaTime, observation.Currents); err != nil {
		t.Fatal(err)
	}
	if err := adaptedNetwork.Step(observation.DeltaTime, adaptedCurrents); err != nil {
		t.Fatal(err)
	}
	want := Decode(observation, baseline)
	got, err := DecodeWithAdapter(observation, adaptedNetwork, adapter)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("adapted decode = %+v, want %+v", got, want)
	}
}

func TestPolicyAdapterGroupsAndBoundsSensoryCurrents(t *testing.T) {
	adapter := DefaultPolicyAdapter()
	adapter.TargetDirectionGain = 2
	adapter.TargetRangeGain = 0.5
	adapter.OpponentGain = 3
	adapter.TeammateGain = 0
	adapter.OpportunityGain = 4
	adapter.PossessionContextGain = 0.25
	currents := map[string]float64{
		"target_left": 0.3, "target_far": 0.8, "opponent_ahead": 0.2,
		"teammate_near": 0.9, "grab_opportunity": 0.4, "has_disc": 1,
		"phase_known": 1, "target_missing": 0.7, "opponent_missing": 0.8,
		"teammate_missing": 0.9, "orientation_missing": 1,
	}
	got, err := adapter.AdaptCurrents(currents)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"target_left": 0.6, "target_far": 0.4, "opponent_ahead": 0.6,
		"teammate_near": 0, "grab_opportunity": 1, "has_disc": 0.25,
		"phase_known": 1, "target_missing": 0.7, "opponent_missing": 0.8,
		"teammate_missing": 0.9, "orientation_missing": 1,
	}
	for name, expected := range want {
		if math.Abs(got[name]-expected) > 1e-12 {
			t.Errorf("current %s = %v, want %v", name, got[name], expected)
		}
	}
}

func TestPolicyAdapterRejectsInvalidParametersAndCurrents(t *testing.T) {
	adapter := DefaultPolicyAdapter()
	adapter.YawGain = math.NaN()
	if err := adapter.Validate(); err == nil || !strings.Contains(err.Error(), "yaw_gain") {
		t.Fatalf("gain error = %v", err)
	}
	adapter = DefaultPolicyAdapter()
	adapter.ReleaseThreshold = 1
	if err := adapter.Validate(); err == nil || !strings.Contains(err.Error(), "release_threshold") {
		t.Fatalf("threshold error = %v", err)
	}
	adapter = DefaultPolicyAdapter()
	if _, err := adapter.AdaptCurrents(map[string]float64{"target_left": math.Inf(1)}); err == nil {
		t.Fatal("non-finite current accepted")
	}
	if _, err := DecodeWithAdapter(nil, nil, PolicyAdapter{}); err == nil {
		t.Fatal("invalid adapter accepted")
	}
}
