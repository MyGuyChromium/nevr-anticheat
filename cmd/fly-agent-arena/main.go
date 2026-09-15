// Command fly-agent-arena trains and evaluates bounded sensory/readout
// adapters in the deterministic synthetic zero-gravity arena.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/flyagent"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("fly-agent-arena", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String("mode", "train", "experiment: train, compare, or compare-trained")
	topologyPath := flags.String("topology", "", "bounded topology JSON; empty uses the synthetic plumbing circuit")
	outputPath := flags.String("output", "-", "JSON report path, or - for stdout (existing files are never replaced)")
	adapterReportPath := flags.String("adapter-report", "", "training report required by compare-trained")
	baseline := flags.String("baseline", "weight", "comparison control: weight or rewired")
	masterSeed := flags.Uint64("master-seed", 0x4d616c65434e53, "master seed for disjoint training/evaluation episodes")
	baselineSeed := flags.Uint64("baseline-seed", 0x73687566666c65, "weight-permutation or rewiring control seed")
	trainingEpisodes := flags.Int("training-episodes", 4, "training episodes (train mode)")
	evaluationEpisodes := flags.Int("evaluation-episodes", 4, "held-out or comparison episodes")
	passes := flags.Int("passes", 2, "bounded coordinate-search passes (train mode)")
	stepFraction := flags.Float64("step-fraction", 0.1, "initial fraction of each adapter parameter range per search step")
	maxSteps := flags.Int("max-steps", 120, "maximum steps per synthetic episode")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "fly-agent-arena: unexpected positional arguments")
		return 2
	}
	if *mode != "train" && *mode != "compare" && *mode != "compare-trained" {
		fmt.Fprintf(stderr, "fly-agent-arena: unsupported --mode %q (want train, compare, or compare-trained)\n", *mode)
		return 2
	}
	explicit := make(map[string]bool)
	flags.Visit(func(flag *flag.Flag) { explicit[flag.Name] = true })
	allowedByMode := map[string]map[string]bool{
		"train": {
			"mode": true, "topology": true, "output": true, "master-seed": true,
			"training-episodes": true, "evaluation-episodes": true, "passes": true,
			"step-fraction": true, "max-steps": true,
		},
		"compare": {
			"mode": true, "topology": true, "output": true, "baseline": true,
			"master-seed": true, "baseline-seed": true, "evaluation-episodes": true, "max-steps": true,
		},
		"compare-trained": {
			"mode": true, "topology": true, "output": true, "adapter-report": true,
			"baseline": true, "baseline-seed": true, "max-steps": true,
		},
	}
	for name := range explicit {
		if !allowedByMode[*mode][name] {
			fmt.Fprintf(stderr, "fly-agent-arena: --%s has no effect in --mode %s\n", name, *mode)
			return 2
		}
	}
	if *mode != "train" && *baseline != "weight" && *baseline != "rewired" {
		fmt.Fprintf(stderr, "fly-agent-arena: unsupported --baseline %q (want weight or rewired)\n", *baseline)
		return 2
	}
	if err := preflightReportOutput(*outputPath); err != nil {
		fmt.Fprintf(stderr, "fly-agent-arena: output: %v\n", err)
		return 1
	}

	topology, err := readTopology(*topologyPath)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-arena: %v\n", err)
		return 1
	}
	if err := flyagent.ValidateAgentContract(topology); err != nil {
		fmt.Fprintf(stderr, "fly-agent-arena: incompatible topology: %v\n", err)
		return 1
	}
	fmt.Fprintln(stderr, "fly-agent-arena: synthetic-environment results are plumbing metrics, not Echo VR readiness")
	if topology.Dataset.Synthetic {
		fmt.Fprintln(stderr, "fly-agent-arena: the selected topology is also synthetic")
	}
	arenaConfig := flyagent.DefaultArenaConfig()
	arenaConfig.MaxSteps = *maxSteps
	dynamics := flyagent.DefaultDynamics()

	var report any
	switch *mode {
	case "train":
		if strings.TrimSpace(*adapterReportPath) != "" {
			fmt.Fprintln(stderr, "fly-agent-arena: --adapter-report is only valid with --mode compare-trained")
			return 2
		}
		trainingConfig := flyagent.AdapterTrainingConfig{
			MasterSeed: *masterSeed, TrainingEpisodes: *trainingEpisodes,
			EvaluationEpisodes: *evaluationEpisodes, Passes: *passes,
			InitialStepFraction: *stepFraction,
		}
		trained, err := flyagent.TrainPolicyAdapter(topology, dynamics, arenaConfig, trainingConfig)
		if err != nil {
			fmt.Fprintf(stderr, "fly-agent-arena: train: %v\n", err)
			return 1
		}
		report = trained
		fmt.Fprintf(stderr, "fly-agent-arena: trained %d bounded adapter parameters; training objective %.6f -> %.6f, held-out %.6f\n",
			trained.TrainableParameterCount, trained.InitialTraining.MeanSyntheticObjectiveReturn,
			trained.TrainedTraining.MeanSyntheticObjectiveReturn, trained.HeldOutEvaluation.MeanSyntheticObjectiveReturn)
	case "compare":
		if strings.TrimSpace(*adapterReportPath) != "" {
			fmt.Fprintln(stderr, "fly-agent-arena: --adapter-report is only valid with --mode compare-trained")
			return 2
		}
		seedPlan, err := flyagent.NewEpisodeSeedPlan(*masterSeed, 0, *evaluationEpisodes)
		if err != nil {
			fmt.Fprintf(stderr, "fly-agent-arena: seed plan: %v\n", err)
			return 1
		}
		comparison, err := compareBaseline(*baseline, topology, dynamics, arenaConfig, seedPlan.EvaluationSeeds, *baselineSeed)
		if err != nil {
			fmt.Fprintf(stderr, "fly-agent-arena: compare: %v\n", err)
			return 1
		}
		report = comparison
		fmt.Fprintf(stderr, "fly-agent-arena: compared candidate with --baseline=%s control over %d paired synthetic episodes\n", *baseline, len(seedPlan.EvaluationSeeds))
	case "compare-trained":
		if strings.TrimSpace(*adapterReportPath) == "" {
			fmt.Fprintln(stderr, "fly-agent-arena: --adapter-report is required with --mode compare-trained")
			return 2
		}
		trainingReport, capability, err := readAdapterTrainingReport(*adapterReportPath, topology, dynamics)
		if err != nil {
			fmt.Fprintf(stderr, "fly-agent-arena: adapter report: %v\n", err)
			return 1
		}
		arenaDigest, err := flyagent.DigestArenaConfig(arenaConfig)
		if err != nil {
			fmt.Fprintf(stderr, "fly-agent-arena: arena configuration: %v\n", err)
			return 1
		}
		if trainingReport.HeldOutEvaluation.ArenaConfigDigest != arenaDigest {
			fmt.Fprintln(stderr, "fly-agent-arena: --max-steps does not reproduce the adapter report arena configuration")
			return 1
		}
		comparison, err := compareTrainedBaseline(
			*baseline, topology, dynamics, arenaConfig, trainingReport.SeedPlan.EvaluationSeeds,
			*baselineSeed, capability)
		if err != nil {
			fmt.Fprintf(stderr, "fly-agent-arena: compare trained adapter: %v\n", err)
			return 1
		}
		report = comparison
		fmt.Fprintf(stderr, "fly-agent-arena: compared trained adapter on candidate and --baseline=%s control over %d held-out episodes\n", *baseline, len(trainingReport.SeedPlan.EvaluationSeeds))
	}

	output, finish, err := reportOutput(*outputPath, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-arena: output: %v\n", err)
		return 1
	}
	finished := false
	defer func() {
		if !finished {
			_ = finish(false)
		}
	}()
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(stderr, "fly-agent-arena: encode report: %v\n", err)
		return 1
	}
	if err := finish(true); err != nil {
		fmt.Fprintf(stderr, "fly-agent-arena: finalize report: %v\n", err)
		return 1
	}
	finished = true
	return 0
}

func preflightReportOutput(path string) error {
	if path == "-" {
		return nil
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("output path is empty")
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("output already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("inspect output directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("output parent is not a directory: %s", parent)
	}
	return nil
}

func compareBaseline(method string, topology *flyagent.Topology, dynamics flyagent.Dynamics, arenaConfig flyagent.ArenaConfig, seeds []uint64, baselineSeed uint64) (flyagent.ComparisonReport, error) {
	switch method {
	case "weight":
		return flyagent.CompareWithShuffledBaseline(topology, dynamics, arenaConfig, seeds, baselineSeed)
	case "rewired":
		return flyagent.CompareWithRewiredBaseline(topology, dynamics, arenaConfig, seeds, baselineSeed)
	default:
		return flyagent.ComparisonReport{}, fmt.Errorf("unsupported --baseline %q (want weight or rewired)", method)
	}
}

func compareTrainedBaseline(method string, topology *flyagent.Topology, dynamics flyagent.Dynamics, arenaConfig flyagent.ArenaConfig, seeds []uint64, baselineSeed uint64, capability flyagent.VerifiedPolicyAdapter) (flyagent.ComparisonReport, error) {
	switch method {
	case "weight":
		return flyagent.CompareVerifiedPolicyAdapterWithShuffledBaseline(topology, dynamics, arenaConfig, seeds, baselineSeed, capability)
	case "rewired":
		return flyagent.CompareVerifiedPolicyAdapterWithRewiredBaseline(topology, dynamics, arenaConfig, seeds, baselineSeed, capability)
	default:
		return flyagent.ComparisonReport{}, fmt.Errorf("unsupported --baseline %q (want weight or rewired)", method)
	}
}

func readTopology(path string) (*flyagent.Topology, error) {
	if strings.TrimSpace(path) == "" {
		return flyagent.DemoTopology()
	}
	file, err := os.Open(path) // #nosec G304 -- explicit operator-provided research topology.
	if err != nil {
		return nil, fmt.Errorf("open topology: %w", err)
	}
	defer file.Close()
	topology, err := flyagent.LoadTopology(file)
	if err != nil {
		return nil, fmt.Errorf("load topology: %w", err)
	}
	return topology, nil
}

func readAdapterTrainingReport(path string, topology *flyagent.Topology, dynamics flyagent.Dynamics) (*flyagent.AdapterTrainingReport, flyagent.VerifiedPolicyAdapter, error) {
	file, err := os.Open(path) // #nosec G304 -- explicit operator-provided training artifact.
	if err != nil {
		return nil, flyagent.VerifiedPolicyAdapter{}, err
	}
	defer file.Close()
	return flyagent.LoadVerifiedPolicyAdapter(file, topology, dynamics)
}

func reportOutput(path string, stdout io.Writer) (io.Writer, func(bool) error, error) {
	if path == "-" {
		return stdout, func(bool) error { return nil }, nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- explicit exclusive output.
	if err != nil {
		return nil, func(bool) error { return nil }, err
	}
	return file, func(success bool) error {
		closeErr := file.Close()
		if !success || closeErr != nil {
			return errors.Join(closeErr, os.Remove(path))
		}
		return nil
	}, nil
}
