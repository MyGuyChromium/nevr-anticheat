package flyagent

import (
	"math"
	"strings"
	"testing"
)

func minimalTopology() *Topology {
	return &Topology{
		Schema:  TopologySchema,
		Dataset: DatasetMetadata{Name: "fixture", Version: "1", Synthetic: true},
		Nodes:   []TopologyNode{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Edges:   []TopologyEdge{{Pre: "a", Post: "b", Weight: 2}, {Pre: "b", Post: "c", Weight: 1}},
		Inputs:  map[string][]string{"stimulus": {"a"}},
		Outputs: map[string][]string{"response": {"b"}},
	}
}

func TestLoadTopologyStrictValidation(t *testing.T) {
	topology, err := LoadTopology(strings.NewReader(`{
		"schema":"nevr.fly.topology/v1",
		"dataset":{"name":"fixture","version":"1","source":"test","license":"test","synthetic":true},
		"nodes":[{"id":"a"},{"id":"b"}],
		"edges":[{"pre":"a","post":"b","weight":1}],
		"inputs":{"stimulus":["a"]},"outputs":{"response":["b"]}
	}`))
	if err != nil {
		t.Fatalf("LoadTopology: %v", err)
	}
	if topology.Dataset.Name != "fixture" {
		t.Fatalf("dataset name = %q", topology.Dataset.Name)
	}

	bad := []string{
		`{"schema":"wrong","dataset":{"name":"x","version":"1","synthetic":true},"nodes":[{"id":"a"}],"inputs":{"x":["a"]},"outputs":{"x":["a"]}}`,
		`{"schema":"nevr.fly.topology/v1","dataset":{"name":"x","version":"1","synthetic":true},"nodes":[{"id":"a"},{"id":"a"}],"inputs":{"x":["a"]},"outputs":{"x":["a"]}}`,
		`{"schema":"nevr.fly.topology/v1","dataset":{"name":"x","version":"1","synthetic":true},"nodes":[{"id":"a"}],"edges":[{"pre":"missing","post":"a","weight":1}],"inputs":{"x":["a"]},"outputs":{"x":["a"]}}`,
		`{"schema":"nevr.fly.topology/v1","dataset":{"name":"x","version":"1","synthetic":true},"nodes":[{"id":"a"}],"inputs":{"x":["a"]},"outputs":{"x":["a"]},"surprise":true}`,
		`{"schema":"nevr.fly.topology/v1","schema":"wrong","dataset":{"name":"x","version":"1","synthetic":true},"nodes":[{"id":"a"}],"inputs":{"x":["a"]},"outputs":{"x":["a"]}}`,
	}
	for i, document := range bad {
		if _, err := LoadTopology(strings.NewReader(document)); err == nil {
			t.Errorf("bad topology %d was accepted", i)
		}
	}
}

func TestNonSyntheticTopologyRequiresAttribution(t *testing.T) {
	topology := minimalTopology()
	topology.Dataset.Synthetic = false
	if err := topology.Validate(); err == nil || !strings.Contains(err.Error(), "source and license") {
		t.Fatalf("Validate error = %v", err)
	}
	topology.Dataset.Source = "https://example.invalid/source"
	topology.Dataset.License = "CC BY 4.0"
	if err := topology.Validate(); err == nil || !strings.Contains(err.Error(), "source_manifest_sha256") {
		t.Fatalf("Validate manifest error = %v", err)
	}
	topology.Dataset.SourceManifestSHA256 = strings.Repeat("a", 64)
	if err := topology.Validate(); err != nil {
		t.Fatalf("attributed topology: %v", err)
	}
}

func TestAgentContractRejectsInterfaceTypos(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAgentContract(topology); err != nil {
		t.Fatal(err)
	}
	delete(topology.Outputs, outputBrake)
	if err := ValidateAgentContract(topology); err == nil || !strings.Contains(err.Error(), outputBrake) {
		t.Fatalf("missing motor error = %v", err)
	}

	topology, err = DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	topology.Inputs = map[string][]string{"targte_ahead": {"sensor.target_ahead"}}
	if err := ValidateAgentContract(topology); err == nil || !strings.Contains(err.Error(), "no recognized sensory") {
		t.Fatalf("unknown sensor error = %v", err)
	}

	topology, err = DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	topology.Inputs = map[string][]string{"orientation_missing": {"sensor.target_ahead"}}
	if err := ValidateAgentContract(topology); err == nil || !strings.Contains(err.Error(), "no actionable sensory") {
		t.Fatalf("safety-only sensor error = %v", err)
	}

	topology, err = DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	topology.Edges = nil
	if err := ValidateAgentContract(topology); err == nil || !strings.Contains(err.Error(), "no actionable input path") {
		t.Fatalf("unreachable motor error = %v", err)
	}
}

func TestNetworkEdgeDirectionAndReset(t *testing.T) {
	network, err := NewNetwork(minimalTopology(), DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	if err := network.Step(0.1, map[string]float64{"stimulus": 1}); err != nil {
		t.Fatal(err)
	}
	if got := network.Output("response"); got != 0 {
		t.Fatalf("post-synaptic response happened in same step: %v", got)
	}
	if err := network.Step(0.1, nil); err != nil {
		t.Fatal(err)
	}
	if got := network.Output("response"); got <= 0 {
		t.Fatalf("pre node did not drive post node: %v", got)
	}
	network.Reset()
	if got := network.Output("response"); got != 0 {
		t.Fatalf("response after Reset = %v", got)
	}
	zeroDigest := network.StateDigest()
	network.Reset()
	if network.StateDigest() != zeroDigest {
		t.Fatal("zero-state digest is not deterministic")
	}
}

func TestNetworkStableAcrossEdgeOrder(t *testing.T) {
	a := &Topology{
		Schema: TopologySchema, Dataset: DatasetMetadata{Name: "ordering", Version: "1", Synthetic: true},
		Nodes: []TopologyNode{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "target"}},
		Edges: []TopologyEdge{
			{Pre: "a", Post: "target", Weight: 1e16},
			{Pre: "b", Post: "target", Weight: 1},
			{Pre: "c", Post: "target", Weight: 1e-16},
		},
		Inputs:  map[string][]string{"stimulus": {"a", "b", "c"}},
		Outputs: map[string][]string{"response": {"target"}},
	}
	b := &Topology{
		Schema: a.Schema, Dataset: a.Dataset,
		Nodes:   []TopologyNode{a.Nodes[3], a.Nodes[1], a.Nodes[0], a.Nodes[2]},
		Edges:   []TopologyEdge{a.Edges[2], a.Edges[0], a.Edges[1]},
		Inputs:  map[string][]string{"stimulus": {"c", "a", "b"}},
		Outputs: map[string][]string{"response": {"target"}},
	}
	first, err := NewNetwork(a, DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewNetwork(b, DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		currents := map[string]float64{"stimulus": float64(i%3) / 2}
		if err := first.Step(1.0/15.0, currents); err != nil {
			t.Fatal(err)
		}
		if err := second.Step(1.0/15.0, currents); err != nil {
			t.Fatal(err)
		}
	}
	if first.StateDigest() != second.StateDigest() {
		t.Fatalf("digest differs by source edge order: %s != %s", first.StateDigest(), second.StateDigest())
	}
	if first.TopologyDigest() != second.TopologyDigest() || first.ModelDigest() != second.ModelDigest() {
		t.Fatalf("canonical provenance differs: topology %s/%s model %s/%s", first.TopologyDigest(), second.TopologyDigest(), first.ModelDigest(), second.ModelDigest())
	}
}

func TestNetworkProvenanceChangesWithSemanticsAndIsCopied(t *testing.T) {
	base, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	network, err := NewNetwork(base, DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	originalTopology, originalModel, originalDataset := network.TopologyDigest(), network.ModelDigest(), network.Dataset()
	if len(originalTopology) != 64 || len(originalModel) != 64 || RateModelSchema == "" || DecoderPolicySchema == "" {
		t.Fatalf("provenance topology=%q model=%q rate=%q decoder=%q", originalTopology, originalModel, RateModelSchema, DecoderPolicySchema)
	}
	base.Dataset.Name = "mutated"
	base.Edges[0].Weight = 0.5
	if network.TopologyDigest() != originalTopology || network.ModelDigest() != originalModel || network.Dataset() != originalDataset {
		t.Fatal("compiled provenance changed after caller mutated topology")
	}

	changed, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	changed.Edges[0].Weight = 0.5
	changedNetwork, err := NewNetwork(changed, DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	if changedNetwork.TopologyDigest() == originalTopology || changedNetwork.ModelDigest() == originalModel {
		t.Fatal("edge change did not change topology/model digest")
	}
	changedBinding, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	changedBinding.Outputs[outputMoveLeft] = []string{"motor.move_right"}
	bindingNetwork, err := NewNetwork(changedBinding, DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	if bindingNetwork.TopologyDigest() == originalTopology || bindingNetwork.ModelDigest() == originalModel {
		t.Fatal("population binding change did not change topology/model digest")
	}

	dynamics := DefaultDynamics()
	dynamics.InputGain += 0.1
	dynamicsNetwork, err := NewNetwork(changed, dynamics)
	if err != nil {
		t.Fatal(err)
	}
	if dynamicsNetwork.TopologyDigest() != changedNetwork.TopologyDigest() || dynamicsNetwork.ModelDigest() == changedNetwork.ModelDigest() {
		t.Fatal("dynamics did not change only model digest")
	}
}

func TestNetworkGapResetsOnlyAboveMaximumStep(t *testing.T) {
	buildDriven := func(t *testing.T) *Network {
		t.Helper()
		network, err := NewNetwork(minimalTopology(), DefaultDynamics())
		if err != nil {
			t.Fatal(err)
		}
		if err := network.Step(0.1, map[string]float64{"stimulus": 1}); err != nil {
			t.Fatal(err)
		}
		if err := network.Step(0.1, nil); err != nil {
			t.Fatal(err)
		}
		if network.Output("response") == 0 {
			t.Fatal("fixture did not reach response")
		}
		return network
	}
	atLimit := buildDriven(t)
	if err := atLimit.Step(atLimit.Dynamics().MaxStep, nil); err != nil {
		t.Fatal(err)
	}
	if atLimit.Output("response") == 0 {
		t.Fatal("exact MaxStep unexpectedly reset state")
	}
	aboveLimit := buildDriven(t)
	gapDelta := math.Nextafter(aboveLimit.Dynamics().MaxStep, math.Inf(1))
	applied, reset, err := aboveLimit.AppliedDeltaTime(gapDelta)
	if err != nil || !reset || applied != NominalReplayStepSeconds {
		t.Fatalf("gap plan applied=%v reset=%t err=%v", applied, reset, err)
	}
	if err := aboveLimit.Step(gapDelta, nil); err != nil {
		t.Fatal(err)
	}
	if aboveLimit.Output("response") != 0 {
		t.Fatalf("telemetry gap retained stale response: %v", aboveLimit.Output("response"))
	}
}

func TestNetworkPreservesInhibitorySign(t *testing.T) {
	topology := &Topology{
		Schema:  TopologySchema,
		Dataset: DatasetMetadata{Name: "sign-fixture", Version: "1", Synthetic: true},
		Nodes:   []TopologyNode{{ID: "exc"}, {ID: "inh"}, {ID: "target"}},
		Edges:   []TopologyEdge{{Pre: "exc", Post: "target", Weight: 1}, {Pre: "inh", Post: "target", Weight: -1}},
		Inputs:  map[string][]string{"excite": {"exc"}, "inhibit": {"inh"}},
		Outputs: map[string][]string{"target": {"target"}},
	}
	network, err := NewNetwork(topology, DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	if err := network.Step(0.1, map[string]float64{"excite": 1}); err != nil {
		t.Fatal(err)
	}
	if err := network.Step(0.1, nil); err != nil {
		t.Fatal(err)
	}
	excitation := network.Output("target")
	if excitation <= 0 {
		t.Fatalf("excitatory response = %v", excitation)
	}

	network.Reset()
	if err := network.Step(0.1, map[string]float64{"inhibit": 1, "excite": 1}); err != nil {
		t.Fatal(err)
	}
	if err := network.Step(0.1, nil); err != nil {
		t.Fatal(err)
	}
	if cancelled := network.Output("target"); cancelled != 0 {
		t.Fatalf("balanced inhibitory response = %v, want 0", cancelled)
	}
}

func TestNetworkRejectsNonFiniteInputs(t *testing.T) {
	network, err := NewNetwork(minimalTopology(), DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	if err := network.Step(math.NaN(), nil); err == nil {
		t.Fatal("NaN dt was accepted")
	}
	if err := network.Step(0.1, map[string]float64{"stimulus": math.Inf(1)}); err == nil {
		t.Fatal("infinite current was accepted")
	}
}

func TestNetworkRejectsFiniteWeightOverflow(t *testing.T) {
	topology := &Topology{
		Schema: TopologySchema, Dataset: DatasetMetadata{Name: "overflow", Version: "1", Synthetic: true},
		Nodes:  []TopologyNode{{ID: "a"}, {ID: "b"}, {ID: "target"}},
		Edges:  []TopologyEdge{{Pre: "a", Post: "target", Weight: math.MaxFloat64}, {Pre: "b", Post: "target", Weight: math.MaxFloat64}},
		Inputs: map[string][]string{"stimulus": {"a"}}, Outputs: map[string][]string{"response": {"target"}},
	}
	if _, err := NewNetwork(topology, DefaultDynamics()); err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("overflow topology error = %v", err)
	}
}

func TestDemoTopologyIdentifiesItselfAsSynthetic(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	if !topology.Dataset.Synthetic || !strings.Contains(topology.Dataset.Derivation, "not MaleCNS") {
		t.Fatalf("demo provenance = %+v", topology.Dataset)
	}
}
