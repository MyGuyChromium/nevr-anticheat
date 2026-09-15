// Command fly-agent-readiness verifies the locally available MaleCNS artifact,
// optional deterministically reproduced adapter, and paired synthetic controls.
// It reports—not bypasses—the external gates that still block public play.
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
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/flyagent"
)

const readinessReportSchema = "nevr.fly.readiness/v1"

type readinessReport struct {
	Schema           string                            `json:"schema"`
	CheckedAt        time.Time                         `json:"checked_at"`
	Artifact         *flyagent.MaleCNSArtifactEvidence `json:"artifact"`
	TopologyDigest   string                            `json:"topology_digest"`
	ModelDigest      string                            `json:"model_digest"`
	Policy           policyReadiness                   `json:"policy"`
	ControlsExecuted bool                              `json:"controls_executed"`
	Controls         []controlReadiness                `json:"controls"`
	Execution        executionReadiness                `json:"execution"`
	PublicMatchReady bool                              `json:"public_match_ready"`
	Limitations      []string                          `json:"limitations"`
}

type policyReadiness struct {
	Mode                                string `json:"mode"`
	AdapterSchema                       string `json:"adapter_schema"`
	AdapterDigest                       string `json:"adapter_digest"`
	TrainingReportDigest                string `json:"training_report_digest,omitempty"`
	TrainingDeterministicallyReproduced bool   `json:"training_deterministically_reproduced"`
}

type controlReadiness struct {
	Method                  string  `json:"method"`
	BaselineSeed            uint64  `json:"baseline_seed"`
	PairedEpisodes          int     `json:"paired_episodes"`
	MeanObjectiveDelta      float64 `json:"mean_synthetic_objective_delta"`
	ConnectivityChanged     bool    `json:"connectivity_changed"`
	ProtectedPathEdges      int     `json:"protected_path_edges,omitempty"`
	ParameterParityVerified bool    `json:"parameter_parity_verified"`
}

type executionReadiness struct {
	ReplayAuthorized                   bool     `json:"replay_authorized"`
	PrivateDryRunImplemented           bool     `json:"private_dry_run_implemented"`
	PrivateRuntimeVerifiedThisRun      bool     `json:"private_runtime_verified_this_run"`
	PrivateActuationAvailable          bool     `json:"private_actuation_available"`
	PublicAuthorizationGateImplemented bool     `json:"public_authorization_gate_implemented"`
	PublicAuthorizationVerified        bool     `json:"public_authorization_verified"`
	SupportedControllerIntegrated      bool     `json:"supported_controller_integrated"`
	PublicActuationAuthorized          bool     `json:"public_actuation_authorized"`
	PublicBlockers                     []string `json:"public_blockers"`
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("fly-agent-readiness", flag.ContinueOnError)
	flags.SetOutput(stderr)
	buildManifestPath := flags.String("build-manifest", "", "verified MaleCNS compiler build manifest (required)")
	adapterReportPath := flags.String("adapter-report", "", "optional training report to reproduce and bind")
	controls := flags.Bool("controls", true, "run both paired synthetic controls (use --controls=false to skip)")
	baselineSeed := flags.Uint64("baseline-seed", 0x73687566666c65, "weight-permutation and rewiring seed")
	outputPath := flags.String("output", "-", "readiness JSON path, or - for stdout (existing files are never replaced)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*buildManifestPath) == "" {
		fmt.Fprintln(stderr, "usage: fly-agent-readiness --build-manifest FILE [--adapter-report FILE] [--controls=true|false] [--baseline-seed N] [--output FILE]")
		return 2
	}
	if err := preflightReadinessOutput(*outputPath); err != nil {
		fmt.Fprintf(stderr, "fly-agent-readiness: output: %v\n", err)
		return 1
	}

	topology, artifact, err := flyagent.LoadVerifiedMaleCNSArtifact(*buildManifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-readiness: MaleCNS artifact: %v\n", err)
		return 1
	}
	dynamics := flyagent.DefaultDynamics()
	network, err := flyagent.NewNetwork(topology, dynamics)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-readiness: compile topology: %v\n", err)
		return 1
	}
	defaultAdapter := flyagent.DefaultPolicyAdapter()
	adapterDigest, err := flyagent.DigestPolicyAdapter(defaultAdapter)
	if err != nil {
		fmt.Fprintf(stderr, "fly-agent-readiness: default adapter: %v\n", err)
		return 1
	}
	policy := policyReadiness{Mode: "default_untrained", AdapterSchema: flyagent.PolicyAdapterSchema, AdapterDigest: adapterDigest}
	arenaConfig := flyagent.DefaultArenaConfig()
	arenaConfig.MaxSteps = 10
	seeds := []uint64{0x72656164696e6573}
	var capability flyagent.VerifiedPolicyAdapter
	if strings.TrimSpace(*adapterReportPath) != "" {
		report, verified, err := loadReadinessAdapter(*adapterReportPath, topology, dynamics)
		if err != nil {
			fmt.Fprintf(stderr, "fly-agent-readiness: adapter report: %v\n", err)
			return 1
		}
		_, reportDigest, err := verified.Bind(network)
		if err != nil {
			fmt.Fprintf(stderr, "fly-agent-readiness: adapter capability: %v\n", err)
			return 1
		}
		capability = verified
		policy = policyReadiness{
			Mode: "verified_trained", AdapterSchema: flyagent.PolicyAdapterSchema,
			AdapterDigest: report.TrainedAdapterDigest, TrainingReportDigest: reportDigest,
			TrainingDeterministicallyReproduced: true,
		}
		arenaConfig = report.HeldOutEvaluation.ArenaConfig
		seeds = append([]uint64(nil), report.SeedPlan.EvaluationSeeds...)
	}

	controlEvidence := make([]controlReadiness, 0, 2)
	if *controls {
		var weight, rewired flyagent.ComparisonReport
		if capability.Verified() {
			weight, err = flyagent.CompareVerifiedPolicyAdapterWithShuffledBaseline(topology, dynamics, arenaConfig, seeds, *baselineSeed, capability)
			if err == nil {
				rewired, err = flyagent.CompareVerifiedPolicyAdapterWithRewiredBaseline(topology, dynamics, arenaConfig, seeds, *baselineSeed, capability)
			}
		} else {
			weight, err = flyagent.CompareWithShuffledBaseline(topology, dynamics, arenaConfig, seeds, *baselineSeed)
			if err == nil {
				rewired, err = flyagent.CompareWithRewiredBaseline(topology, dynamics, arenaConfig, seeds, *baselineSeed)
			}
		}
		if err != nil {
			fmt.Fprintf(stderr, "fly-agent-readiness: synthetic controls: %v\n", err)
			return 1
		}
		controlEvidence = []controlReadiness{compactControl(weight), compactControl(rewired)}
	}

	replayAuthorized := flyagent.AuthorizeExecution(flyagent.ModeReplay, false) == nil
	privateActuationAvailable := flyagent.AuthorizeExecution(flyagent.ModePrivateLive, true) == nil
	publicActuationAvailable := flyagent.AuthorizeExecution(flyagent.ModePublicLive, true) == nil
	report := readinessReport{
		Schema: readinessReportSchema, CheckedAt: time.Now().UTC(), Artifact: artifact,
		TopologyDigest: network.TopologyDigest(), ModelDigest: network.ModelDigest(),
		Policy: policy, ControlsExecuted: *controls, Controls: controlEvidence, PublicMatchReady: false,
		Execution: executionReadiness{
			ReplayAuthorized: replayAuthorized, PrivateDryRunImplemented: true,
			PrivateRuntimeVerifiedThisRun: false, PrivateActuationAvailable: privateActuationAvailable,
			PublicAuthorizationGateImplemented: true, PublicAuthorizationVerified: false,
			SupportedControllerIntegrated: false, PublicActuationAuthorized: publicActuationAvailable,
			PublicBlockers: []string{
				"No Echo-compatible runtime or live /session evidence was supplied to this readiness run.",
				"No documented, supported controller/bot actuation API is integrated.",
				"No signed community-server operator grant or sanctioned bot slot was supplied.",
				"No supervised private-session validation or held-out real-match evaluation has been completed.",
			},
		},
		Limitations: []string{
			flyagent.SyntheticEvaluationLimitation,
			"Artifact hashes verify local byte consistency, not publisher signatures or biological correctness.",
			"This command never joins matchmaking, sends controller input, or changes the public execution denial.",
		},
	}
	if !*controls {
		report.Limitations = append(report.Limitations, "Paired synthetic controls were explicitly skipped for this readiness run.")
	}
	if !replayAuthorized || privateActuationAvailable || publicActuationAvailable {
		fmt.Fprintln(stderr, "fly-agent-readiness: execution policy is inconsistent with the expected deny-by-default boundary")
		return 1
	}
	if err := writeReadinessReport(*outputPath, stdout, report); err != nil {
		fmt.Fprintf(stderr, "fly-agent-readiness: output: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "fly-agent-readiness: verified %d nodes/%d edges; public_match_ready=false (%d external blockers)\n",
		artifact.TopologyNodes, artifact.TopologyEdges, len(report.Execution.PublicBlockers))
	return 0
}

func loadReadinessAdapter(path string, topology *flyagent.Topology, dynamics flyagent.Dynamics) (*flyagent.AdapterTrainingReport, flyagent.VerifiedPolicyAdapter, error) {
	file, err := os.Open(path) // #nosec G304 -- explicit operator-provided training artifact.
	if err != nil {
		return nil, flyagent.VerifiedPolicyAdapter{}, err
	}
	defer file.Close()
	return flyagent.LoadVerifiedPolicyAdapter(file, topology, dynamics)
}

func compactControl(report flyagent.ComparisonReport) controlReadiness {
	mean := 0.0
	for _, delta := range report.PairedDeltas {
		mean += delta.SyntheticObjectiveReturn
	}
	if len(report.PairedDeltas) > 0 {
		mean /= float64(len(report.PairedDeltas))
	}
	parity := report.ParameterParity
	parityVerified := parity.NodeCount == parity.BaselineNodeCount && parity.EdgeCount == parity.BaselineEdgeCount &&
		parity.NodeIDsUnchanged && parity.BiasByNodeUnchanged && parity.ExactBiasMultiset && parity.ExactWeightMultiset &&
		parity.InDegreeByNodeUnchanged && parity.OutDegreeByNodeUnchanged && parity.InputBindingsUnchanged &&
		parity.OutputBindingsUnchanged && parity.AgentContractValid
	return controlReadiness{
		Method: report.BaselineMethod, BaselineSeed: report.BaselineSeed,
		PairedEpisodes: len(report.PairedDeltas), MeanObjectiveDelta: mean,
		ConnectivityChanged: parity.ConnectivityChanged, ProtectedPathEdges: report.ProtectedEdges,
		ParameterParityVerified: parityVerified,
	}
}

func preflightReadinessOutput(path string) error {
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
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("inspect output directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("output parent is not a directory")
	}
	return nil
}

func writeReadinessReport(path string, stdout io.Writer, report readinessReport) error {
	if path == "-" {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- explicit exclusive output.
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	encodeErr := encoder.Encode(report)
	closeErr := file.Close()
	if encodeErr != nil || closeErr != nil {
		return errors.Join(encodeErr, closeErr, os.Remove(path))
	}
	return nil
}
