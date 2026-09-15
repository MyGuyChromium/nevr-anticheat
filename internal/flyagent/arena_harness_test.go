package flyagent

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func demoRunner(t *testing.T, steps int) (*ClosedLoopRunner, ArenaConfig) {
	t.Helper()
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultArenaConfig()
	config.MaxSteps = steps
	runner, err := NewClosedLoopRunner(topology, DefaultDynamics(), config)
	if err != nil {
		t.Fatal(err)
	}
	return runner, config
}

func TestClosedLoopPolicyCallbackUsesSharedEpisodeLoop(t *testing.T) {
	runner, _ := demoRunner(t, 3)
	initial, err := runner.Reset(17)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	policy := ObservationPolicy(func(observation *Observation) (ActionIntent, error) {
		if reason := ControlBlockReason(observation); reason != "" {
			t.Fatalf("callback received blocked observation: %s", reason)
		}
		if observation.Sequence != uint64(calls) {
			t.Fatalf("callback sequence=%d want=%d", observation.Sequence, calls)
		}
		calls++
		return arenaAction([3]float64{0, 0, 1}, 0), nil
	})
	var final *Observation
	for calls < 3 {
		_, next, _, err := runner.StepWithPolicy(policy)
		if err != nil {
			t.Fatal(err)
		}
		final = next
	}
	if final == nil || final.SelfPosition == initial.SelfPosition || final.GameActive {
		t.Fatalf("policy did not advance to terminal observation: %+v", final)
	}
	metrics, err := runner.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Steps != 3 || !metrics.Done || metrics.TerminationReason != "step_limit" || metrics.MeanActionConfidence != 0.5 {
		t.Fatalf("callback metrics = %+v", metrics)
	}
	if _, _, _, err := runner.StepWithPolicy(nil); err == nil {
		t.Fatal("nil policy was accepted")
	}

	second, _ := demoRunner(t, 3)
	if _, err := second.Reset(17); err != nil {
		t.Fatal(err)
	}
	before, err := second.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("training adapter failed")
	if _, _, _, err := second.StepWithPolicy(func(*Observation) (ActionIntent, error) { return ActionIntent{}, wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("policy error = %v", err)
	}
	after, err := second.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("failed policy callback advanced episode state")
	}
}

func TestClosedLoopCheckpointRestoresNetworkArenaAndMetrics(t *testing.T) {
	runner, config := demoRunner(t, 30)
	if _, err := runner.Reset(99); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		if _, _, _, err := runner.Step(); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint, err := runner.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip ClosedLoopCheckpoint
	if err := json.Unmarshal(blob, &roundTrip); err != nil {
		t.Fatal(err)
	}

	type result struct {
		Action      ActionIntent
		Observation *Observation
		Transition  ArenaTransition
	}
	runSuffix := func() []result {
		var results []result
		for range 5 {
			action, observation, transition, err := runner.Step()
			if err != nil {
				t.Fatal(err)
			}
			results = append(results, result{action, observation, transition})
		}
		return results
	}
	want := runSuffix()
	wantMetrics, err := runner.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Restore(roundTrip); err != nil {
		t.Fatal(err)
	}
	got := runSuffix()
	gotMetrics, err := runner.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotMetrics, wantMetrics) {
		t.Fatalf("closed loop diverged after restore\n got=%#v %+v\nwant=%#v %+v", got, gotMetrics, want, wantMetrics)
	}

	// Returned checkpoint storage must not alias the live recurrent state.
	copyCheckpoint, err := runner.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	live := runner.network.state[0]
	copyCheckpoint.NetworkState[0] = 1 - copyCheckpoint.NetworkState[0]
	if runner.network.state[0] != live {
		t.Fatal("checkpoint aliases live network state")
	}

	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	dynamics := DefaultDynamics()
	dynamics.InputGain += 0.1
	different, err := NewClosedLoopRunner(topology, dynamics, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := different.Restore(roundTrip); err == nil {
		t.Fatal("checkpoint restored into a different model")
	}
}

func TestRunClosedLoopEpisodeIsBounded(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultArenaConfig()
	config.MaxSteps = 12
	metrics, err := RunClosedLoopEpisode(topology, DefaultDynamics(), config, 77)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Schema != EpisodeMetricsSchema || metrics.Seed != 77 || !metrics.Done || metrics.Steps < 1 || metrics.Steps > config.MaxSteps || metrics.TopologyDigest == "" || metrics.ModelDigest == "" {
		t.Fatalf("episode metrics = %+v", metrics)
	}
}

func TestShuffledBaselineParityAndDeterministicComparison(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	originalDigest, err := canonicalTopologyDigest(topology)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ShuffledWeightBaseline(topology, 1234)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ShuffledWeightBaseline(topology, 1234)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same shuffle seed produced different baselines")
	}
	reordered := cloneTopology(topology)
	for left, right := 0, len(reordered.Edges)-1; left < right; left, right = left+1, right-1 {
		reordered.Edges[left], reordered.Edges[right] = reordered.Edges[right], reordered.Edges[left]
	}
	reorderedBaseline, err := ShuffledWeightBaseline(reordered, 1234)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := canonicalTopologyDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	reorderedDigest, err := canonicalTopologyDigest(reorderedBaseline)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != reorderedDigest {
		t.Fatal("baseline depends on semantically irrelevant source edge order")
	}
	afterDigest, err := canonicalTopologyDigest(topology)
	if err != nil {
		t.Fatal(err)
	}
	if afterDigest != originalDigest {
		t.Fatal("baseline construction mutated candidate topology")
	}
	parity := compareTopologyParameters(topology, first)
	if !parity.NodeIDsUnchanged || !parity.BiasByNodeUnchanged || !parity.ExactBiasMultiset || !parity.ExactWeightMultiset ||
		!parity.InDegreeByNodeUnchanged || !parity.OutDegreeByNodeUnchanged || !parity.InputBindingsUnchanged ||
		!parity.OutputBindingsUnchanged || !parity.AgentContractValid || parity.ConnectivityChanged ||
		parity.NodeCount != len(topology.Nodes) || parity.BaselineNodeCount != len(topology.Nodes) ||
		parity.EdgeCount != len(topology.Edges) || parity.BaselineEdgeCount != len(topology.Edges) {
		t.Fatalf("baseline parity = %+v", parity)
	}
	if !first.Dataset.Synthetic || !strings.Contains(first.Dataset.Derivation, "not biological data") {
		t.Fatalf("baseline provenance = %+v", first.Dataset)
	}
	changed := false
	for i := range topology.Edges {
		changed = changed || topology.Edges[i].Weight != first.Edges[i].Weight
	}
	if !changed {
		t.Fatal("baseline weights were not shuffled")
	}

	config := DefaultArenaConfig()
	config.MaxSteps = 36
	seeds := []uint64{4, 8, 15}
	report, err := CompareWithShuffledBaseline(topology, DefaultDynamics(), config, seeds, 1234)
	if err != nil {
		t.Fatal(err)
	}
	again, err := CompareWithShuffledBaseline(topology, DefaultDynamics(), config, seeds, 1234)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, again) {
		t.Fatal("comparison report is not reproducible")
	}
	if report.Schema != ComparisonReportSchema || report.BaselineMethod != "edge_weight_permutation_fixed_connectivity" || len(report.PairedDeltas) != len(seeds) || !report.ParameterParity.ExactWeightMultiset || report.ParameterParity.ConnectivityChanged || report.Candidate.TopologyDigest == report.Baseline.TopologyDigest {
		t.Fatalf("comparison report = %+v", report)
	}
	if report.Candidate.Environment != ArenaSourceKind || report.Candidate.ArenaConfig != config || report.Candidate.ArenaConfigDigest == "" || report.Candidate.Dynamics != DefaultDynamics() {
		t.Fatalf("comparison omitted reproducibility settings: %+v", report.Candidate)
	}
	wantWork := uint64(len(topology.Nodes)+len(topology.Edges)) * uint64(config.MaxSteps) * uint64(len(seeds))
	if report.Candidate.PlannedStateUpdates != wantWork || report.Baseline.PlannedStateUpdates != wantWork {
		t.Fatalf("planned work candidate=%d baseline=%d want=%d", report.Candidate.PlannedStateUpdates, report.Baseline.PlannedStateUpdates, wantWork)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "not detector or policy accuracy") || !strings.Contains(string(encoded), "no statistical significance") {
		t.Fatalf("report omitted limitations: %s", encoded)
	}
	for i, delta := range report.PairedDeltas {
		if delta.Seed != seeds[i] {
			t.Fatalf("paired delta %d seed=%d want=%d", i, delta.Seed, seeds[i])
		}
	}

	equalWeights, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	for i := range equalWeights.Edges {
		equalWeights.Edges[i].Weight = 1
	}
	if _, err := ShuffledWeightBaseline(equalWeights, 1); err == nil {
		t.Fatal("uninformative all-equal weight shuffle was accepted")
	}
}

func TestRewiredBaselinePreservesDegreesParametersAndReachability(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	rewired, protected, err := shuffledConnectivityBaseline(topology, 8080)
	if err != nil {
		t.Fatal(err)
	}
	again, againProtected, err := shuffledConnectivityBaseline(topology, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if protected < 1 || protected != againProtected || !reflect.DeepEqual(rewired, again) {
		t.Fatalf("rewired baseline is not deterministic/protected: edges=%d/%d", protected, againProtected)
	}
	parity := compareTopologyParameters(topology, rewired)
	if parity.NodeCount != parity.BaselineNodeCount || parity.EdgeCount != parity.BaselineEdgeCount ||
		!parity.NodeIDsUnchanged || !parity.BiasByNodeUnchanged || !parity.ExactBiasMultiset ||
		!parity.ExactWeightMultiset || !parity.InDegreeByNodeUnchanged || !parity.OutDegreeByNodeUnchanged ||
		!parity.InputBindingsUnchanged || !parity.OutputBindingsUnchanged ||
		!parity.AgentContractValid || !parity.ConnectivityChanged {
		t.Fatalf("rewired parity/reachability = %+v", parity)
	}
	if !rewired.Dataset.Synthetic || !strings.Contains(rewired.Dataset.Derivation, "shortest actionable-path edges retained") {
		t.Fatalf("rewired provenance = %+v", rewired.Dataset)
	}

	reordered := cloneTopology(topology)
	for left, right := 0, len(reordered.Edges)-1; left < right; left, right = left+1, right-1 {
		reordered.Edges[left], reordered.Edges[right] = reordered.Edges[right], reordered.Edges[left]
	}
	reorderedBaseline, err := ShuffledConnectivityBaseline(reordered, 8080)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := canonicalTopologyDigest(rewired)
	if err != nil {
		t.Fatal(err)
	}
	reorderedDigest, err := canonicalTopologyDigest(reorderedBaseline)
	if err != nil {
		t.Fatal(err)
	}
	if digest != reorderedDigest {
		t.Fatal("rewired baseline depends on source edge ordering")
	}

	config := DefaultArenaConfig()
	config.MaxSteps = 24
	report, err := CompareWithRewiredBaseline(topology, DefaultDynamics(), config, []uint64{2, 3}, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if report.BaselineMethod != "degree_preserving_endpoint_permutation_with_protected_motor_paths" || report.ProtectedEdges != protected ||
		!report.ParameterParity.ConnectivityChanged || !report.ParameterParity.AgentContractValid || len(report.PairedDeltas) != 2 {
		t.Fatalf("rewired report = %+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "biases the rewired control") || !strings.Contains(string(encoded), "not proof") {
		t.Fatalf("rewired report omitted design limitations: %s", encoded)
	}

	// A graph with no mutable edges still satisfies the agent interface, but
	// cannot form an informative topology control. Construction must fail
	// explicitly instead of returning the unchanged candidate as a baseline.
	minimal := &Topology{
		Schema:  TopologySchema,
		Dataset: DatasetMetadata{Name: "minimal", Version: "1", Synthetic: true},
		Nodes:   []TopologyNode{{ID: "shared"}},
		Inputs:  map[string][]string{"target_ahead": {"shared"}},
		Outputs: make(map[string][]string, len(requiredMotorPopulations)),
	}
	for _, population := range requiredMotorPopulations {
		minimal.Outputs[population] = []string{"shared"}
	}
	if err := ValidateAgentContract(minimal); err != nil {
		t.Fatalf("minimal control fixture violates the agent contract: %v", err)
	}
	if _, err := ShuffledConnectivityBaseline(minimal, 8080); err == nil || !strings.Contains(err.Error(), "fewer than two non-protected edges") {
		t.Fatalf("uninformative rewiring did not fail explicitly: %v", err)
	}
}

func TestEpisodeSeedPlanAndEvaluationBounds(t *testing.T) {
	plan, err := NewEpisodeSeedPlan(123, 4, 3)
	if err != nil {
		t.Fatal(err)
	}
	again, err := NewEpisodeSeedPlan(123, 4, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, again) || plan.Schema != EpisodeSeedPlanSchema || len(plan.TrainingSeeds) != 4 || len(plan.EvaluationSeeds) != 3 {
		t.Fatalf("seed plan = %+v", plan)
	}
	seen := make(map[uint64]bool)
	for _, seed := range append(append([]uint64(nil), plan.TrainingSeeds...), plan.EvaluationSeeds...) {
		if seen[seed] {
			t.Fatalf("seed %d repeated across split", seed)
		}
		seen[seed] = true
	}
	if _, err := NewEpisodeSeedPlan(1, 0, 0); err == nil {
		t.Fatal("empty evaluation split was accepted")
	}

	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluateClosedLoop(topology, DefaultDynamics(), DefaultArenaConfig(), []uint64{7, 7}); err == nil {
		t.Fatal("duplicate evaluation seeds were accepted")
	}
	oversized := cloneTopology(topology)
	for i := 0; i < 1_000; i++ {
		oversized.Nodes = append(oversized.Nodes, TopologyNode{ID: "work"})
	}
	if _, err := closedLoopWork(oversized, 10_000, maxEvaluationEpisodes); err == nil {
		t.Fatal("closed-loop work budget was not enforced")
	}
}
