package flyagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestTrainPolicyAdapterIsDeterministicAndHeldOut(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	arenaConfig := DefaultArenaConfig()
	arenaConfig.MaxSteps = 40
	config := DefaultAdapterTrainingConfig()
	config.TrainingEpisodes = 2
	config.EvaluationEpisodes = 2
	config.Passes = 1

	first, err := TrainPolicyAdapter(topology, DefaultDynamics(), arenaConfig, config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := TrainPolicyAdapter(topology, DefaultDynamics(), arenaConfig, config)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("identical adapter training inputs were not byte-deterministic")
	}
	if !first.FrozenConnectomeEdges || first.TrainableParameterCount != policyAdapterTrainableParameters || len(first.Attempts) != 2*policyAdapterTrainableParameters {
		t.Fatalf("training audit = %+v", first)
	}
	trainingSeeds := make(map[uint64]struct{}, len(first.SeedPlan.TrainingSeeds))
	for _, seed := range first.SeedPlan.TrainingSeeds {
		trainingSeeds[seed] = struct{}{}
	}
	for _, seed := range first.SeedPlan.EvaluationSeeds {
		if _, reused := trainingSeeds[seed]; reused {
			t.Fatalf("evaluation seed %d was reused for training", seed)
		}
	}
	if first.TrainedTraining.MeanSyntheticObjectiveReturn+1e-12 < first.InitialTraining.MeanSyntheticObjectiveReturn {
		t.Fatalf("training objective regressed: initial=%v trained=%v", first.InitialTraining.MeanSyntheticObjectiveReturn, first.TrainedTraining.MeanSyntheticObjectiveReturn)
	}
	if first.TopologyDigest != first.InitialTraining.TopologyDigest || first.TopologyDigest != first.TrainedTraining.TopologyDigest || first.TopologyDigest != first.HeldOutEvaluation.TopologyDigest {
		t.Fatal("adapter training changed or misbound the topology digest")
	}
	if first.SeedPlan.Schema != EpisodeSeedPlanSchema || first.Schema != AdapterTrainingReportSchema || reflect.DeepEqual(first.Limitations, []string{}) {
		t.Fatalf("training report metadata = %+v", first)
	}
	loaded, digest, err := LoadAdapterTrainingReportWithDigest(bytes.NewReader(firstJSON))
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(firstJSON)
	if digest != fmt.Sprintf("%x", wantDigest[:]) {
		t.Fatalf("loaded report digest=%q want=%x", digest, wantDigest)
	}
	if !reflect.DeepEqual(*loaded, first) {
		t.Fatal("strictly loaded training report differs from source")
	}
	_, capability, err := LoadVerifiedPolicyAdapter(bytes.NewReader(firstJSON), topology, DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	network, err := NewNetwork(topology, DefaultDynamics())
	if err != nil {
		t.Fatal(err)
	}
	boundAdapter, boundReportDigest, err := capability.Bind(network)
	if err != nil || boundAdapter != first.TrainedAdapter || boundReportDigest != digest {
		t.Fatalf("verified adapter binding adapter=%+v report=%q err=%v", boundAdapter, boundReportDigest, err)
	}
}

type endlessZeroReader struct{}

func (endlessZeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

func TestLoadAdapterTrainingReportBoundsBytesBeforeDecode(t *testing.T) {
	reader := io.LimitReader(endlessZeroReader{}, maxAdapterTrainingReportBytes+1)
	if _, _, err := LoadAdapterTrainingReportWithDigest(reader); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize report error = %v", err)
	}
}

func TestEvaluatePolicyAdapterRejectsInvalidInputs(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	arenaConfig := DefaultArenaConfig()
	arenaConfig.MaxSteps = 2
	adapter := DefaultPolicyAdapter()
	adapter.TargetDirectionGain = math.NaN()
	if _, err := EvaluatePolicyAdapter("test", topology, DefaultDynamics(), arenaConfig, []uint64{1}, adapter); err == nil {
		t.Fatal("invalid policy adapter accepted")
	}
	if _, err := EvaluatePolicyAdapter("", topology, DefaultDynamics(), arenaConfig, []uint64{1}, DefaultPolicyAdapter()); err == nil {
		t.Fatal("empty evaluation label accepted")
	}
}

func TestComparePolicyAdapterWithShuffledBaselineUsesSameAdapterAndParity(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	arenaConfig := DefaultArenaConfig()
	arenaConfig.MaxSteps = 8
	adapter := DefaultPolicyAdapter()
	adapter.TargetDirectionGain = 1.4
	report, err := ComparePolicyAdapterWithShuffledBaseline(topology, DefaultDynamics(), arenaConfig, []uint64{2, 3}, 99, adapter)
	if err != nil {
		t.Fatal(err)
	}
	if report.BaselineMethod != "edge_weight_permutation_fixed_connectivity" || len(report.PairedDeltas) != 2 || !report.ParameterParity.ExactWeightMultiset ||
		!report.ParameterParity.InputBindingsUnchanged || !report.ParameterParity.OutputBindingsUnchanged {
		t.Fatalf("trained comparison report = %+v", report)
	}
	if report.Candidate.Label != "bounded_adapter_candidate" || report.Baseline.Label != "bounded_adapter_on_weight_permuted_baseline" {
		t.Fatalf("comparison labels = %q / %q", report.Candidate.Label, report.Baseline.Label)
	}
}

func TestComparePolicyAdapterWithRewiredBaselineUsesSameAdapterAndParity(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	arenaConfig := DefaultArenaConfig()
	arenaConfig.MaxSteps = 8
	report, err := ComparePolicyAdapterWithRewiredBaseline(topology, DefaultDynamics(), arenaConfig, []uint64{2, 3}, 99, DefaultPolicyAdapter())
	if err != nil {
		t.Fatal(err)
	}
	if report.BaselineMethod != "degree_preserving_endpoint_permutation_with_protected_motor_paths" ||
		len(report.PairedDeltas) != 2 || !report.ParameterParity.ConnectivityChanged ||
		!report.ParameterParity.InDegreeByNodeUnchanged || !report.ParameterParity.OutDegreeByNodeUnchanged ||
		!report.ParameterParity.AgentContractValid || report.ProtectedEdges == 0 {
		t.Fatalf("trained rewired comparison report = %+v", report)
	}
}

func TestAdapterTrainingWorkFailsBeforeUnboundedRollout(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultAdapterTrainingConfig()
	config.TrainingEpisodes = maxAdapterTrainingEpisodes
	config.EvaluationEpisodes = maxAdapterTrainingEpisodes
	config.Passes = maxAdapterTrainingPasses
	if _, err := adapterTrainingWork(topology, 10_000, config); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("work bound error = %v", err)
	}
}

func TestLoadAdapterTrainingReportRejectsTamperingAndUnknownFields(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	arenaConfig := DefaultArenaConfig()
	arenaConfig.MaxSteps = 2
	config := DefaultAdapterTrainingConfig()
	config.TrainingEpisodes, config.EvaluationEpisodes, config.Passes = 1, 1, 1
	report, err := TrainPolicyAdapter(topology, DefaultDynamics(), arenaConfig, config)
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(document, []byte(`"schema":`), []byte(`"unknown":true,"schema":`), 1)
	if _, err := LoadAdapterTrainingReport(bytes.NewReader(unknown)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
	caseVariant := bytes.Replace(document, []byte(`"schema":`), []byte(`"SCHEMA":`), 1)
	if _, err := LoadAdapterTrainingReport(bytes.NewReader(caseVariant)); err == nil || !strings.Contains(err.Error(), "non-canonical JSON field") {
		t.Fatalf("case-variant field error = %v", err)
	}
	report.FrozenConnectomeEdges = false
	tampered, _ := json.Marshal(report)
	if _, err := LoadAdapterTrainingReport(bytes.NewReader(tampered)); err == nil || !strings.Contains(err.Error(), "frozen-edge") {
		t.Fatalf("tampered boundary error = %v", err)
	}
}

func TestVerifyAdapterTrainingReportRequiresExactDeterministicReproduction(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	arenaConfig := DefaultArenaConfig()
	arenaConfig.MaxSteps = 2
	config := DefaultAdapterTrainingConfig()
	config.TrainingEpisodes, config.EvaluationEpisodes, config.Passes = 1, 1, 1
	report, err := TrainPolicyAdapter(topology, DefaultDynamics(), arenaConfig, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAdapterTrainingReport(topology, DefaultDynamics(), &report); err != nil {
		t.Fatalf("reproducible report rejected: %v", err)
	}
	report.Limitations = append(report.Limitations, "forged but structurally valid annotation")
	if err := report.Validate(); err != nil {
		t.Fatalf("structurally valid fixture rejected too early: %v", err)
	}
	if err := VerifyAdapterTrainingReport(topology, DefaultDynamics(), &report); err == nil || !strings.Contains(err.Error(), "deterministic reproduction") {
		t.Fatalf("non-reproduced report error = %v", err)
	}
}

func TestAdapterTrainingReportRequiresCanonicalLimitation(t *testing.T) {
	topology, err := DemoTopology()
	if err != nil {
		t.Fatal(err)
	}
	arenaConfig := DefaultArenaConfig()
	arenaConfig.MaxSteps = 2
	config := DefaultAdapterTrainingConfig()
	config.TrainingEpisodes, config.EvaluationEpisodes, config.Passes = 1, 1, 1
	report, err := TrainPolicyAdapter(topology, DefaultDynamics(), arenaConfig, config)
	if err != nil {
		t.Fatal(err)
	}
	report.Limitations = []string{"synthetic-ish"}
	if err := report.Validate(); err == nil || !strings.Contains(err.Error(), "canonical synthetic-evaluation limitation") {
		t.Fatalf("missing limitation error = %v", err)
	}
}
