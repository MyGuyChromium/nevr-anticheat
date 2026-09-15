package flyagent

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

const (
	ClosedLoopCheckpointSchema = "nevr.fly.closed-loop-checkpoint/v1"
	EpisodeMetricsSchema       = "nevr.fly.episode-metrics/v1"
	EvaluationSummarySchema    = "nevr.fly.evaluation-summary/v1"
	ComparisonReportSchema     = "nevr.fly.comparison-report/v1"
	EpisodeSeedPlanSchema      = "nevr.fly.episode-seeds/v1"
	maxEvaluationEpisodes      = 256
	maxClosedLoopStateUpdates  = 200_000_000
)

// SyntheticEvaluationLimitation is attached to reports so synthetic objective
// measurements cannot be mistaken for detector accuracy, policy accuracy,
// biological validation, or evidence that an agent is ready for public play.
const SyntheticEvaluationLimitation = "Deterministic synthetic-arena objective metrics only; not detector or policy accuracy, biological validation, real-player performance, or public-match readiness."

// EpisodeSeedPlan provides non-overlapping reproducible seeds for optimization
// rollouts and held-out evaluation. This package does not update model weights;
// callers may use Arena.Reset/Step/Checkpoint to implement an optimizer.
type EpisodeSeedPlan struct {
	Schema          string   `json:"schema"`
	MasterSeed      uint64   `json:"master_seed"`
	TrainingSeeds   []uint64 `json:"training_seeds"`
	EvaluationSeeds []uint64 `json:"evaluation_seeds"`
}

// NewEpisodeSeedPlan derives bounded, disjoint seed sets from one master seed.
func NewEpisodeSeedPlan(masterSeed uint64, trainingEpisodes, evaluationEpisodes int) (EpisodeSeedPlan, error) {
	if trainingEpisodes < 0 || evaluationEpisodes < 1 || trainingEpisodes > maxEvaluationEpisodes || evaluationEpisodes > maxEvaluationEpisodes {
		return EpisodeSeedPlan{}, fmt.Errorf("training/evaluation episode counts must be 0..%d and 1..%d", maxEvaluationEpisodes, maxEvaluationEpisodes)
	}
	plan := EpisodeSeedPlan{Schema: EpisodeSeedPlanSchema, MasterSeed: masterSeed}
	rng := arenaRNG{state: masterSeed ^ 0xbb67ae8584caa73b}
	seen := make(map[uint64]struct{}, trainingEpisodes+evaluationEpisodes)
	nextUnique := func() uint64 {
		for {
			seed := rng.next()
			if _, exists := seen[seed]; !exists {
				seen[seed] = struct{}{}
				return seed
			}
		}
	}
	for range trainingEpisodes {
		plan.TrainingSeeds = append(plan.TrainingSeeds, nextUnique())
	}
	for range evaluationEpisodes {
		plan.EvaluationSeeds = append(plan.EvaluationSeeds, nextUnique())
	}
	return plan, nil
}

// ClosedLoopCheckpoint captures the simulator, rate-network state, and metric
// accumulator required to resume a rollout exactly.
type ClosedLoopCheckpoint struct {
	Schema              string          `json:"schema"`
	ModelDigest         string          `json:"model_digest"`
	Arena               ArenaCheckpoint `json:"arena"`
	NetworkState        []float64       `json:"network_state"`
	Actions             int             `json:"actions"`
	SyntheticReturn     float64         `json:"synthetic_objective_return"`
	InitialDiscDistance float64         `json:"initial_disc_distance"`
	BestDiscDistance    float64         `json:"best_disc_distance"`
	ConfidenceSum       float64         `json:"confidence_sum"`
	NeutralActions      int             `json:"neutral_actions"`
	PossessionSteps     int             `json:"possession_steps"`
	FirstPossessionStep int             `json:"first_possession_step"`
	DiscAcquisitions    int             `json:"disc_acquisitions"`
	DiscReleases        int             `json:"disc_releases"`
}

// EpisodeMetrics are descriptive measurements from one synthetic rollout.
// FirstPossessionStep is -1 if possession was never acquired.
type EpisodeMetrics struct {
	Schema                   string          `json:"schema"`
	Seed                     uint64          `json:"seed"`
	Dataset                  DatasetMetadata `json:"dataset"`
	TopologyDigest           string          `json:"topology_digest"`
	ModelDigest              string          `json:"model_digest"`
	Steps                    int             `json:"steps"`
	SyntheticObjectiveReturn float64         `json:"synthetic_objective_return"`
	InitialDiscDistance      float64         `json:"initial_disc_distance"`
	BestDiscDistance         float64         `json:"best_disc_distance"`
	FinalDiscDistance        float64         `json:"final_disc_distance"`
	MeanActionConfidence     float64         `json:"mean_action_confidence"`
	NeutralActions           int             `json:"neutral_actions"`
	PossessionSteps          int             `json:"possession_steps"`
	FirstPossessionStep      int             `json:"first_possession_step"`
	DiscAcquisitions         int             `json:"disc_acquisitions"`
	DiscReleases             int             `json:"disc_releases"`
	GoalsFor                 int             `json:"goals_for"`
	GoalsAgainst             int             `json:"goals_against"`
	Done                     bool            `json:"done"`
	TerminationReason        string          `json:"termination_reason,omitempty"`
}

// ClosedLoopRunner connects a frozen rate network to the deterministic Arena.
// Each call to Step computes an ActionIntent and applies it to the next state.
type ClosedLoopRunner struct {
	arena               *Arena
	network             *Network
	initialized         bool
	actions             int
	syntheticReturn     float64
	initialDiscDistance float64
	bestDiscDistance    float64
	confidenceSum       float64
	neutralActions      int
	possessionSteps     int
	firstPossessionStep int
	discAcquisitions    int
	discReleases        int
}

// ObservationPolicy is a caller-owned policy adapter. It may implement
// training or an alternative readout while reusing the same arena loop and
// metrics. Stateful policies are responsible for their own checkpoints.
type ObservationPolicy func(*Observation) (ActionIntent, error)

// NewClosedLoopRunner validates and compiles one topology for repeatable
// episodes. Arena integration intervals may not exceed the network gap limit.
func NewClosedLoopRunner(topology *Topology, dynamics Dynamics, arenaConfig ArenaConfig) (*ClosedLoopRunner, error) {
	if err := arenaConfig.validate(); err != nil {
		return nil, err
	}
	if err := ValidateAgentContract(topology); err != nil {
		return nil, fmt.Errorf("closed-loop topology: %w", err)
	}
	if _, err := closedLoopWork(topology, arenaConfig.MaxSteps, 1); err != nil {
		return nil, err
	}
	network, err := NewNetwork(topology, dynamics)
	if err != nil {
		return nil, fmt.Errorf("compile closed-loop topology: %w", err)
	}
	arena, err := NewArena(arenaConfig)
	if err != nil {
		return nil, err
	}
	if arenaConfig.StepSeconds > dynamics.MaxStep {
		return nil, errors.New("arena step exceeds network maximum step")
	}
	return &ClosedLoopRunner{arena: arena, network: network, firstPossessionStep: -1}, nil
}

// Reset clears recurrent state and starts the requested seeded episode.
func (r *ClosedLoopRunner) Reset(seed uint64) (*Observation, error) {
	if r == nil || r.arena == nil || r.network == nil {
		return nil, errors.New("closed-loop runner is nil")
	}
	r.network.Reset()
	observation, err := r.arena.Reset(seed)
	if err != nil {
		return nil, err
	}
	r.initialized = true
	r.actions = 0
	r.syntheticReturn = 0
	r.initialDiscDistance = observation.Disc.Distance
	r.bestDiscDistance = observation.Disc.Distance
	r.confidenceSum = 0
	r.neutralActions = 0
	r.possessionSteps = 0
	r.firstPossessionStep = -1
	r.discAcquisitions = 0
	r.discReleases = 0
	return observation, nil
}

// Step advances one complete sense-network-act-environment cycle.
func (r *ClosedLoopRunner) Step() (ActionIntent, *Observation, ArenaTransition, error) {
	return r.StepWithPolicy(func(observation *Observation) (ActionIntent, error) {
		if err := r.network.Step(observation.DeltaTime, observation.Currents); err != nil {
			return ActionIntent{}, err
		}
		return Decode(observation, r.network), nil
	})
}

// StepWithPolicy advances the same bounded episode through a caller-supplied
// observation-to-action callback. The built-in rate network is not advanced by
// this method unless the callback itself does so.
func (r *ClosedLoopRunner) StepWithPolicy(policy ObservationPolicy) (ActionIntent, *Observation, ArenaTransition, error) {
	if r == nil || !r.initialized {
		return ActionIntent{}, nil, ArenaTransition{}, errors.New("closed-loop runner has not been reset")
	}
	if policy == nil {
		return ActionIntent{}, nil, ArenaTransition{}, errors.New("closed-loop policy is nil")
	}
	observation, err := r.arena.Observation()
	if err != nil {
		return ActionIntent{}, nil, ArenaTransition{}, err
	}
	if !observation.GameActive {
		return ActionIntent{}, nil, ArenaTransition{}, errors.New("closed-loop episode is complete")
	}
	if reason := ControlBlockReason(observation); reason != "" {
		return ActionIntent{}, nil, ArenaTransition{}, fmt.Errorf("synthetic observation blocked: %s", reason)
	}
	action, err := policy(observation)
	if err != nil {
		return ActionIntent{}, nil, ArenaTransition{}, fmt.Errorf("closed-loop policy: %w", err)
	}
	next, transition, err := r.arena.Step(action)
	if err != nil {
		return ActionIntent{}, nil, ArenaTransition{}, err
	}
	r.actions++
	r.syntheticReturn += transition.SyntheticObjective
	r.confidenceSum += action.Confidence
	if action.NeutralReason != "" {
		r.neutralActions++
	}
	if transition.DistanceToDisc < r.bestDiscDistance {
		r.bestDiscDistance = transition.DistanceToDisc
	}
	if transition.HasDisc {
		r.possessionSteps++
	}
	if transition.AcquiredDisc {
		r.discAcquisitions++
		if r.firstPossessionStep < 0 {
			r.firstPossessionStep = transition.Step
		}
	}
	if transition.ReleasedDisc {
		r.discReleases++
	}
	return action, next, transition, nil
}

// Metrics returns a snapshot of accumulated descriptive episode metrics.
func (r *ClosedLoopRunner) Metrics() (EpisodeMetrics, error) {
	if r == nil || !r.initialized {
		return EpisodeMetrics{}, errors.New("closed-loop runner has not been reset")
	}
	meanConfidence := 0.0
	if r.actions > 0 {
		meanConfidence = r.confidenceSum / float64(r.actions)
	}
	return EpisodeMetrics{
		Schema: EpisodeMetricsSchema, Seed: r.arena.seed, Dataset: r.network.Dataset(),
		TopologyDigest: r.network.TopologyDigest(), ModelDigest: r.network.ModelDigest(), Steps: r.actions,
		SyntheticObjectiveReturn: r.syntheticReturn, InitialDiscDistance: r.initialDiscDistance,
		BestDiscDistance: r.bestDiscDistance, FinalDiscDistance: r.arena.discPos.Sub(r.arena.playerPos).Magnitude(),
		MeanActionConfidence: meanConfidence, NeutralActions: r.neutralActions,
		PossessionSteps: r.possessionSteps, FirstPossessionStep: r.firstPossessionStep,
		DiscAcquisitions: r.discAcquisitions, DiscReleases: r.discReleases,
		GoalsFor: r.arena.goalsFor, GoalsAgainst: r.arena.goalsAgainst,
		Done: r.arena.done, TerminationReason: r.arena.termination,
	}, nil
}

// Checkpoint captures the complete runner state without aliasing network data.
func (r *ClosedLoopRunner) Checkpoint() (ClosedLoopCheckpoint, error) {
	if r == nil || !r.initialized {
		return ClosedLoopCheckpoint{}, errors.New("closed-loop runner has not been reset")
	}
	return ClosedLoopCheckpoint{
		Schema: ClosedLoopCheckpointSchema, ModelDigest: r.network.ModelDigest(), Arena: r.arena.Checkpoint(),
		NetworkState: append([]float64(nil), r.network.state...), Actions: r.actions,
		SyntheticReturn: r.syntheticReturn, InitialDiscDistance: r.initialDiscDistance,
		BestDiscDistance: r.bestDiscDistance, ConfidenceSum: r.confidenceSum,
		NeutralActions: r.neutralActions, PossessionSteps: r.possessionSteps,
		FirstPossessionStep: r.firstPossessionStep, DiscAcquisitions: r.discAcquisitions,
		DiscReleases: r.discReleases,
	}, nil
}

// Restore resumes a checkpoint only on the exact same compiled model and arena
// configuration.
func (r *ClosedLoopRunner) Restore(checkpoint ClosedLoopCheckpoint) error {
	if r == nil || r.arena == nil || r.network == nil {
		return errors.New("closed-loop runner is nil")
	}
	if checkpoint.Schema != ClosedLoopCheckpointSchema {
		return fmt.Errorf("closed-loop checkpoint schema %q is unsupported", checkpoint.Schema)
	}
	if checkpoint.ModelDigest != r.network.ModelDigest() {
		return errors.New("closed-loop checkpoint model does not match")
	}
	if len(checkpoint.NetworkState) != len(r.network.state) {
		return errors.New("closed-loop checkpoint network size does not match")
	}
	for _, value := range checkpoint.NetworkState {
		if !finite(value) || value < 0 || value > 1 {
			return errors.New("closed-loop checkpoint network state is invalid")
		}
	}
	if checkpoint.Actions != checkpoint.Arena.Step || checkpoint.Actions < 0 ||
		checkpoint.NeutralActions < 0 || checkpoint.NeutralActions > checkpoint.Actions ||
		checkpoint.PossessionSteps < 0 || checkpoint.PossessionSteps > checkpoint.Actions ||
		checkpoint.DiscAcquisitions < 0 || checkpoint.DiscAcquisitions > checkpoint.Actions ||
		checkpoint.DiscReleases < 0 || checkpoint.DiscReleases > checkpoint.Actions ||
		checkpoint.FirstPossessionStep < -1 || checkpoint.FirstPossessionStep > checkpoint.Actions {
		return errors.New("closed-loop checkpoint counters are inconsistent")
	}
	for _, value := range []float64{checkpoint.SyntheticReturn, checkpoint.InitialDiscDistance, checkpoint.BestDiscDistance, checkpoint.ConfidenceSum} {
		if !finite(value) {
			return errors.New("closed-loop checkpoint metrics are non-finite")
		}
	}
	if checkpoint.InitialDiscDistance < 0 || checkpoint.BestDiscDistance < 0 || checkpoint.BestDiscDistance > checkpoint.InitialDiscDistance+1e-9 || checkpoint.ConfidenceSum < 0 || checkpoint.ConfidenceSum > float64(checkpoint.Actions)+1e-9 {
		return errors.New("closed-loop checkpoint metrics are inconsistent")
	}
	if err := r.arena.Restore(checkpoint.Arena); err != nil {
		return err
	}
	copy(r.network.state, checkpoint.NetworkState)
	clear(r.network.next)
	clear(r.network.drive)
	r.initialized = true
	r.actions = checkpoint.Actions
	r.syntheticReturn = checkpoint.SyntheticReturn
	r.initialDiscDistance = checkpoint.InitialDiscDistance
	r.bestDiscDistance = checkpoint.BestDiscDistance
	r.confidenceSum = checkpoint.ConfidenceSum
	r.neutralActions = checkpoint.NeutralActions
	r.possessionSteps = checkpoint.PossessionSteps
	r.firstPossessionStep = checkpoint.FirstPossessionStep
	r.discAcquisitions = checkpoint.DiscAcquisitions
	r.discReleases = checkpoint.DiscReleases
	return nil
}

// RunClosedLoopEpisode executes one bounded synthetic episode.
func RunClosedLoopEpisode(topology *Topology, dynamics Dynamics, arenaConfig ArenaConfig, seed uint64) (EpisodeMetrics, error) {
	runner, err := NewClosedLoopRunner(topology, dynamics, arenaConfig)
	if err != nil {
		return EpisodeMetrics{}, err
	}
	if _, err := runner.Reset(seed); err != nil {
		return EpisodeMetrics{}, err
	}
	for !runner.arena.done {
		if _, _, _, err := runner.Step(); err != nil {
			return EpisodeMetrics{}, err
		}
	}
	return runner.Metrics()
}

// EvaluationSummary aggregates a bounded list of seeded synthetic episodes.
type EvaluationSummary struct {
	Schema                       string           `json:"schema"`
	Label                        string           `json:"label"`
	Environment                  string           `json:"environment"`
	ArenaConfig                  ArenaConfig      `json:"arena_config"`
	ArenaConfigDigest            string           `json:"arena_config_digest"`
	Dynamics                     Dynamics         `json:"dynamics"`
	Dataset                      DatasetMetadata  `json:"dataset"`
	TopologyDigest               string           `json:"topology_digest"`
	ModelDigest                  string           `json:"model_digest"`
	PolicyAdapterSchema          string           `json:"policy_adapter_schema"`
	PolicyAdapterDigest          string           `json:"policy_adapter_digest"`
	Seeds                        []uint64         `json:"seeds"`
	Episodes                     []EpisodeMetrics `json:"episodes"`
	MeanSyntheticObjectiveReturn float64          `json:"mean_synthetic_objective_return"`
	MeanBestDiscDistance         float64          `json:"mean_best_disc_distance"`
	MeanFinalDiscDistance        float64          `json:"mean_final_disc_distance"`
	MeanActionConfidence         float64          `json:"mean_action_confidence"`
	PlannedStateUpdates          uint64           `json:"planned_state_updates"`
	EpisodesWithPossession       int              `json:"episodes_with_possession"`
	TotalDiscAcquisitions        int              `json:"total_disc_acquisitions"`
	TotalGoalsFor                int              `json:"total_goals_for"`
	TotalGoalsAgainst            int              `json:"total_goals_against"`
	TotalNeutralActions          int              `json:"total_neutral_actions"`
	Limitations                  []string         `json:"limitations"`
}

// EvaluateClosedLoop runs the same compiled topology over explicit episode
// seeds. It returns descriptive synthetic metrics, never an accuracy estimate.
func EvaluateClosedLoop(topology *Topology, dynamics Dynamics, arenaConfig ArenaConfig, seeds []uint64) (EvaluationSummary, error) {
	return evaluateClosedLoop("candidate", topology, dynamics, arenaConfig, seeds)
}

func evaluateClosedLoop(label string, topology *Topology, dynamics Dynamics, arenaConfig ArenaConfig, seeds []uint64) (EvaluationSummary, error) {
	if err := validateEvaluationSeeds(seeds); err != nil {
		return EvaluationSummary{}, err
	}
	if err := arenaConfig.validate(); err != nil {
		return EvaluationSummary{}, err
	}
	plannedWork, err := closedLoopWork(topology, arenaConfig.MaxSteps, len(seeds))
	if err != nil {
		return EvaluationSummary{}, err
	}
	runner, err := NewClosedLoopRunner(topology, dynamics, arenaConfig)
	if err != nil {
		return EvaluationSummary{}, err
	}
	adapterDigest, err := DigestPolicyAdapter(DefaultPolicyAdapter())
	if err != nil {
		return EvaluationSummary{}, err
	}
	summary := EvaluationSummary{
		Schema: EvaluationSummarySchema, Label: label, Environment: ArenaSourceKind,
		ArenaConfig: arenaConfig, ArenaConfigDigest: runner.arena.configDigest,
		Dynamics: dynamics, Dataset: runner.network.Dataset(),
		TopologyDigest: runner.network.TopologyDigest(), ModelDigest: runner.network.ModelDigest(),
		PolicyAdapterSchema: PolicyAdapterSchema, PolicyAdapterDigest: adapterDigest,
		Seeds: append([]uint64(nil), seeds...), PlannedStateUpdates: plannedWork,
		Limitations: []string{SyntheticEvaluationLimitation, "No learning or weight updates occur during evaluation."},
	}
	for _, seed := range seeds {
		if _, err := runner.Reset(seed); err != nil {
			return EvaluationSummary{}, err
		}
		for !runner.arena.done {
			if _, _, _, err := runner.Step(); err != nil {
				return EvaluationSummary{}, fmt.Errorf("seed %d: %w", seed, err)
			}
		}
		metrics, err := runner.Metrics()
		if err != nil {
			return EvaluationSummary{}, err
		}
		summary.Episodes = append(summary.Episodes, metrics)
		summary.MeanSyntheticObjectiveReturn += metrics.SyntheticObjectiveReturn
		summary.MeanBestDiscDistance += metrics.BestDiscDistance
		summary.MeanFinalDiscDistance += metrics.FinalDiscDistance
		summary.MeanActionConfidence += metrics.MeanActionConfidence
		if metrics.FirstPossessionStep >= 0 {
			summary.EpisodesWithPossession++
		}
		summary.TotalDiscAcquisitions += metrics.DiscAcquisitions
		summary.TotalGoalsFor += metrics.GoalsFor
		summary.TotalGoalsAgainst += metrics.GoalsAgainst
		summary.TotalNeutralActions += metrics.NeutralActions
	}
	denominator := float64(len(seeds))
	summary.MeanSyntheticObjectiveReturn /= denominator
	summary.MeanBestDiscDistance /= denominator
	summary.MeanFinalDiscDistance /= denominator
	summary.MeanActionConfidence /= denominator
	return summary, nil
}

func closedLoopWork(topology *Topology, steps, episodes int) (uint64, error) {
	if topology == nil || steps < 1 || episodes < 1 {
		return 0, errors.New("closed-loop work estimate requires a topology, steps, and episodes")
	}
	unitsPerStep := uint64(len(topology.Nodes)) + uint64(len(topology.Edges))
	stepCount := uint64(steps) * uint64(episodes)
	if unitsPerStep > maxClosedLoopStateUpdates/stepCount {
		return 0, fmt.Errorf("closed-loop rollout exceeds %d node/edge state updates; reduce topology, max_steps, or episode count", maxClosedLoopStateUpdates)
	}
	return unitsPerStep * stepCount, nil
}

func validateEvaluationSeeds(seeds []uint64) error {
	if len(seeds) == 0 || len(seeds) > maxEvaluationEpisodes {
		return fmt.Errorf("evaluation requires 1..%d seeds", maxEvaluationEpisodes)
	}
	seen := make(map[uint64]struct{}, len(seeds))
	for _, seed := range seeds {
		if _, duplicate := seen[seed]; duplicate {
			return fmt.Errorf("evaluation seed %d is duplicated", seed)
		}
		seen[seed] = struct{}{}
	}
	return nil
}

// ParameterParity records the controls applied to the shuffled baseline.
type ParameterParity struct {
	NodeCount                int  `json:"node_count"`
	BaselineNodeCount        int  `json:"baseline_node_count"`
	EdgeCount                int  `json:"edge_count"`
	BaselineEdgeCount        int  `json:"baseline_edge_count"`
	NodeIDsUnchanged         bool `json:"node_ids_unchanged"`
	BiasByNodeUnchanged      bool `json:"bias_by_node_unchanged"`
	ExactBiasMultiset        bool `json:"exact_bias_multiset"`
	ExactWeightMultiset      bool `json:"exact_weight_multiset"`
	InDegreeByNodeUnchanged  bool `json:"in_degree_by_node_unchanged"`
	OutDegreeByNodeUnchanged bool `json:"out_degree_by_node_unchanged"`
	InputBindingsUnchanged   bool `json:"input_bindings_unchanged"`
	OutputBindingsUnchanged  bool `json:"output_bindings_unchanged"`
	AgentContractValid       bool `json:"agent_contract_valid"`
	ConnectivityChanged      bool `json:"connectivity_changed"`
}

// PairedEpisodeDelta is candidate minus baseline for one shared seed. Distance
// deltas are descriptive; a negative best-distance delta means closer approach.
type PairedEpisodeDelta struct {
	Seed                     uint64  `json:"seed"`
	SyntheticObjectiveReturn float64 `json:"synthetic_objective_return"`
	BestDiscDistance         float64 `json:"best_disc_distance"`
	FinalDiscDistance        float64 `json:"final_disc_distance"`
	DiscAcquisitions         int     `json:"disc_acquisitions"`
	GoalsFor                 int     `json:"goals_for"`
}

// ComparisonReport compares a candidate with an exact-parameter-multiset,
// edge-weight-shuffled baseline on paired seeds.
type ComparisonReport struct {
	Schema                    string               `json:"schema"`
	ExperimentMode            string               `json:"experiment_mode"`
	BaselineSeed              uint64               `json:"baseline_seed"`
	BaselineMethod            string               `json:"baseline_method"`
	ProtectedEdges            int                  `json:"protected_path_edges,omitempty"`
	PolicyAdapter             PolicyAdapter        `json:"policy_adapter"`
	PolicyAdapterDigest       string               `json:"policy_adapter_digest"`
	PolicyAdapterReportDigest string               `json:"policy_adapter_report_digest,omitempty"`
	Candidate                 EvaluationSummary    `json:"candidate"`
	Baseline                  EvaluationSummary    `json:"baseline"`
	ParameterParity           ParameterParity      `json:"parameter_parity"`
	PairedDeltas              []PairedEpisodeDelta `json:"paired_deltas"`
	Limitations               []string             `json:"limitations"`
}

// CompareWithShuffledBaseline evaluates both graphs on identical seeded
// episodes. It makes no significance, generalization, or accuracy claim.
func CompareWithShuffledBaseline(topology *Topology, dynamics Dynamics, arenaConfig ArenaConfig, seeds []uint64, shuffleSeed uint64) (ComparisonReport, error) {
	baseline, err := ShuffledWeightBaseline(topology, shuffleSeed)
	if err != nil {
		return ComparisonReport{}, err
	}
	candidate, err := evaluateClosedLoop("candidate", topology, dynamics, arenaConfig, seeds)
	if err != nil {
		return ComparisonReport{}, err
	}
	shuffled, err := evaluateClosedLoop("shuffled_equal_parameter_baseline", baseline, dynamics, arenaConfig, seeds)
	if err != nil {
		return ComparisonReport{}, err
	}
	report := ComparisonReport{
		Schema: ComparisonReportSchema, ExperimentMode: "default_adapter_control", BaselineSeed: shuffleSeed,
		BaselineMethod: "edge_weight_permutation_fixed_connectivity",
		PolicyAdapter:  DefaultPolicyAdapter(), PolicyAdapterDigest: candidate.PolicyAdapterDigest,
		Candidate: candidate, Baseline: shuffled,
		ParameterParity: compareTopologyParameters(topology, baseline),
		Limitations: []string{
			SyntheticEvaluationLimitation,
			"Candidate and baseline use paired seeds, but no statistical significance or out-of-distribution generalization is claimed.",
			"The baseline shuffles edge weights while retaining graph connectivity; it is one control, not proof that topology caused any observed difference.",
		},
	}
	for i, episode := range candidate.Episodes {
		control := shuffled.Episodes[i]
		report.PairedDeltas = append(report.PairedDeltas, PairedEpisodeDelta{
			Seed:                     episode.Seed,
			SyntheticObjectiveReturn: episode.SyntheticObjectiveReturn - control.SyntheticObjectiveReturn,
			BestDiscDistance:         episode.BestDiscDistance - control.BestDiscDistance,
			FinalDiscDistance:        episode.FinalDiscDistance - control.FinalDiscDistance,
			DiscAcquisitions:         episode.DiscAcquisitions - control.DiscAcquisitions,
			GoalsFor:                 episode.GoalsFor - control.GoalsFor,
		})
	}
	if !report.ParameterParity.NodeIDsUnchanged || !report.ParameterParity.BiasByNodeUnchanged ||
		!report.ParameterParity.ExactBiasMultiset || !report.ParameterParity.ExactWeightMultiset ||
		!report.ParameterParity.InDegreeByNodeUnchanged || !report.ParameterParity.OutDegreeByNodeUnchanged ||
		!report.ParameterParity.InputBindingsUnchanged || !report.ParameterParity.OutputBindingsUnchanged ||
		!report.ParameterParity.AgentContractValid || report.ParameterParity.ConnectivityChanged {
		return ComparisonReport{}, errors.New("shuffled baseline failed equal-parameter audit")
	}
	return report, nil
}

// CompareWithRewiredBaseline evaluates the candidate against a deterministic
// degree-preserving endpoint permutation. ProtectedEdges reports how many
// candidate edges were retained solely to keep every required motor reachable.
func CompareWithRewiredBaseline(topology *Topology, dynamics Dynamics, arenaConfig ArenaConfig, seeds []uint64, shuffleSeed uint64) (ComparisonReport, error) {
	baseline, protectedEdges, err := shuffledConnectivityBaseline(topology, shuffleSeed)
	if err != nil {
		return ComparisonReport{}, err
	}
	candidate, err := evaluateClosedLoop("candidate", topology, dynamics, arenaConfig, seeds)
	if err != nil {
		return ComparisonReport{}, err
	}
	rewired, err := evaluateClosedLoop("contract_preserving_rewired_equal_parameter_baseline", baseline, dynamics, arenaConfig, seeds)
	if err != nil {
		return ComparisonReport{}, err
	}
	report := ComparisonReport{
		Schema: ComparisonReportSchema, ExperimentMode: "default_adapter_control", BaselineSeed: shuffleSeed,
		BaselineMethod: "degree_preserving_endpoint_permutation_with_protected_motor_paths",
		PolicyAdapter:  DefaultPolicyAdapter(), PolicyAdapterDigest: candidate.PolicyAdapterDigest,
		ProtectedEdges: protectedEdges, Candidate: candidate, Baseline: rewired,
		ParameterParity: compareTopologyParameters(topology, baseline),
		Limitations: []string{
			SyntheticEvaluationLimitation,
			"Candidate and baseline use paired seeds, but no statistical significance or out-of-distribution generalization is claimed.",
			"One deterministic shortest actionable path to every required motor is protected; this deliberately biases the rewired control toward interface viability.",
			"Degree-preserving endpoint permutation is one topology control, not proof that connectome wiring caused any observed difference.",
		},
	}
	for i, episode := range candidate.Episodes {
		control := rewired.Episodes[i]
		report.PairedDeltas = append(report.PairedDeltas, PairedEpisodeDelta{
			Seed:                     episode.Seed,
			SyntheticObjectiveReturn: episode.SyntheticObjectiveReturn - control.SyntheticObjectiveReturn,
			BestDiscDistance:         episode.BestDiscDistance - control.BestDiscDistance,
			FinalDiscDistance:        episode.FinalDiscDistance - control.FinalDiscDistance,
			DiscAcquisitions:         episode.DiscAcquisitions - control.DiscAcquisitions,
			GoalsFor:                 episode.GoalsFor - control.GoalsFor,
		})
	}
	parity := report.ParameterParity
	if parity.NodeCount != parity.BaselineNodeCount || parity.EdgeCount != parity.BaselineEdgeCount ||
		!parity.NodeIDsUnchanged || !parity.BiasByNodeUnchanged || !parity.ExactBiasMultiset ||
		!parity.ExactWeightMultiset || !parity.InDegreeByNodeUnchanged || !parity.OutDegreeByNodeUnchanged ||
		!parity.InputBindingsUnchanged || !parity.OutputBindingsUnchanged ||
		!parity.AgentContractValid || !parity.ConnectivityChanged {
		return ComparisonReport{}, errors.New("rewired baseline failed parity or reachability audit")
	}
	return report, nil
}

// ShuffledWeightBaseline keeps every node, edge, bias, binding, and exact edge
// weight, but deterministically permutes weights among existing edges. This is
// a matched engineering control, not a randomized biological connectome.
func ShuffledWeightBaseline(topology *Topology, seed uint64) (*Topology, error) {
	if err := ValidateAgentContract(topology); err != nil {
		return nil, fmt.Errorf("baseline source topology: %w", err)
	}
	if len(topology.Edges) < 2 {
		return nil, errors.New("shuffled baseline requires at least two edges")
	}
	baseline := cloneTopology(topology)
	// Source table order is not a topology parameter. Canonicalize edges before
	// assigning the seeded permutation so semantically identical input files
	// produce the same control graph.
	canonicalizeTopologyEdges(baseline.Edges)
	weights := make([]float64, len(baseline.Edges))
	allEqual := true
	for i, edge := range baseline.Edges {
		weights[i] = edge.Weight
		if i > 0 && edge.Weight != weights[0] {
			allEqual = false
		}
	}
	if allEqual {
		return nil, errors.New("shuffled weight baseline requires at least two distinct edge weights")
	}
	original := append([]float64(nil), weights...)
	rng := arenaRNG{state: seed ^ 0x3c6ef372fe94f82b}
	for i := len(weights) - 1; i > 0; i-- {
		j := rng.index(i + 1)
		weights[i], weights[j] = weights[j], weights[i]
	}
	if equalFloatSequence(original, weights) {
		copy(weights, append(original[1:], original[0]))
	}
	for i := range baseline.Edges {
		baseline.Edges[i].Weight = weights[i]
	}
	baseline.Dataset = DatasetMetadata{
		Name: topology.Dataset.Name + " shuffled-weight baseline", Version: topology.Dataset.Version,
		Source: topology.Dataset.Source, License: topology.Dataset.License,
		SourceManifestSHA256: topology.Dataset.SourceManifestSHA256, Synthetic: true,
		Derivation: fmt.Sprintf("deterministic edge-weight permutation seed=%d of topology %s; matched control, not biological data", seed, mustTopologyDigest(topology)),
	}
	if err := ValidateAgentContract(baseline); err != nil {
		return nil, fmt.Errorf("shuffled baseline contract: %w", err)
	}
	return baseline, nil
}

// ShuffledConnectivityBaseline rewires unprotected edge endpoints while
// preserving exact node/edge/weight/bias counts, every node's in/out degree,
// and the sensory/motor bindings. One deterministic path from an actionable
// input to each required motor population is retained so the control remains
// runnable; that constraint is reported as a baseline bias.
func ShuffledConnectivityBaseline(topology *Topology, seed uint64) (*Topology, error) {
	baseline, _, err := shuffledConnectivityBaseline(topology, seed)
	return baseline, err
}

func shuffledConnectivityBaseline(topology *Topology, seed uint64) (*Topology, int, error) {
	if err := ValidateAgentContract(topology); err != nil {
		return nil, 0, fmt.Errorf("rewired baseline source topology: %w", err)
	}
	original := cloneTopology(topology)
	canonicalizeTopologyEdges(original.Edges)
	protected, err := protectedAgentPathEdges(original)
	if err != nil {
		return nil, 0, err
	}
	mutable := make([]int, 0, len(original.Edges)-len(protected))
	for i := range original.Edges {
		if !protected[i] {
			mutable = append(mutable, i)
		}
	}
	if len(mutable) < 2 {
		return nil, 0, errors.New("rewired baseline has fewer than two non-protected edges")
	}
	originalConnectivity := topologyConnectivity(original)
	for mode := 0; mode < 3; mode++ {
		for attempt := 0; attempt < 32; attempt++ {
			candidate := cloneTopology(original)
			rng := arenaRNG{state: seed ^ 0xa54ff53a5f1d36f1 ^ uint64(mode+1)*0x9e3779b97f4a7c15 ^ uint64(attempt)}
			modeName := "post_endpoints"
			if mode == 0 || mode == 2 {
				posts := make([]string, len(mutable))
				for i, edgeIndex := range mutable {
					posts[i] = original.Edges[edgeIndex].Post
				}
				shuffleStrings(&rng, posts)
				for i, edgeIndex := range mutable {
					candidate.Edges[edgeIndex].Post = posts[i]
				}
			}
			if mode == 1 || mode == 2 {
				modeName = "pre_endpoints"
				if mode == 2 {
					modeName = "pre_and_post_endpoints"
				}
				pres := make([]string, len(mutable))
				for i, edgeIndex := range mutable {
					pres[i] = original.Edges[edgeIndex].Pre
				}
				shuffleStrings(&rng, pres)
				for i, edgeIndex := range mutable {
					candidate.Edges[edgeIndex].Pre = pres[i]
				}
			}
			if equalEndpoints(originalConnectivity, topologyConnectivity(candidate)) {
				continue
			}
			if err := ValidateAgentContract(candidate); err != nil {
				continue
			}
			candidate.Dataset = DatasetMetadata{
				Name: topology.Dataset.Name + " contract-preserving rewired baseline", Version: topology.Dataset.Version,
				Source: topology.Dataset.Source, License: topology.Dataset.License,
				SourceManifestSHA256: topology.Dataset.SourceManifestSHA256, Synthetic: true,
				Derivation: fmt.Sprintf("deterministic %s permutation seed=%d attempt=%d with %d shortest actionable-path edges retained from topology %s; matched control, not biological data", modeName, seed, attempt, len(protected), mustTopologyDigest(topology)),
			}
			parity := compareTopologyParameters(topology, candidate)
			if !parity.NodeIDsUnchanged || !parity.BiasByNodeUnchanged || !parity.ExactBiasMultiset ||
				!parity.ExactWeightMultiset || !parity.InDegreeByNodeUnchanged || !parity.OutDegreeByNodeUnchanged ||
				!parity.InputBindingsUnchanged || !parity.OutputBindingsUnchanged ||
				!parity.AgentContractValid || !parity.ConnectivityChanged {
				continue
			}
			return candidate, len(protected), nil
		}
	}
	return nil, len(protected), errors.New("could not produce a distinct contract-preserving rewired baseline in 96 deterministic attempts")
}

func protectedAgentPathEdges(topology *Topology) (map[int]bool, error) {
	sourceSet := make(map[string]bool)
	for name, members := range topology.Inputs {
		if _, actionable := actionableSensoryChannels[name]; actionable {
			for _, member := range members {
				sourceSet[member] = true
			}
		}
	}
	sources := make([]string, 0, len(sourceSet))
	for source := range sourceSet {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	if len(sources) == 0 {
		return nil, errors.New("rewired baseline has no actionable source nodes")
	}
	adjacency := make(map[string][]int)
	for i, edge := range topology.Edges {
		adjacency[edge.Pre] = append(adjacency[edge.Pre], i)
	}
	for pre := range adjacency {
		sort.SliceStable(adjacency[pre], func(i, j int) bool {
			left, right := topology.Edges[adjacency[pre][i]], topology.Edges[adjacency[pre][j]]
			if left.Post != right.Post {
				return left.Post < right.Post
			}
			return left.Weight < right.Weight
		})
	}
	reachable := make(map[string]bool, len(topology.Nodes))
	predecessor := make(map[string]int, len(topology.Nodes))
	queue := append([]string(nil), sources...)
	for _, source := range sources {
		reachable[source] = true
	}
	for len(queue) > 0 {
		pre := queue[0]
		queue = queue[1:]
		for _, edgeIndex := range adjacency[pre] {
			post := topology.Edges[edgeIndex].Post
			if reachable[post] {
				continue
			}
			reachable[post] = true
			predecessor[post] = edgeIndex
			queue = append(queue, post)
		}
	}
	protected := make(map[int]bool)
	for _, population := range requiredMotorPopulations {
		members := append([]string(nil), topology.Outputs[population]...)
		sort.Strings(members)
		target := ""
		for _, member := range members {
			if reachable[member] {
				target = member
				break
			}
		}
		if target == "" {
			return nil, fmt.Errorf("rewired baseline cannot protect a path to motor population %q", population)
		}
		for !sourceSet[target] {
			edgeIndex, ok := predecessor[target]
			if !ok {
				return nil, fmt.Errorf("rewired baseline lost predecessor for node %q", target)
			}
			protected[edgeIndex] = true
			target = topology.Edges[edgeIndex].Pre
		}
	}
	return protected, nil
}

func shuffleStrings(rng *arenaRNG, values []string) {
	for i := len(values) - 1; i > 0; i-- {
		j := rng.index(i + 1)
		values[i], values[j] = values[j], values[i]
	}
}

func canonicalizeTopologyEdges(edges []TopologyEdge) {
	sort.SliceStable(edges, func(i, j int) bool {
		left, right := edges[i], edges[j]
		if left.Post != right.Post {
			return left.Post < right.Post
		}
		if left.Pre != right.Pre {
			return left.Pre < right.Pre
		}
		return left.Weight < right.Weight
	})
}

func cloneTopology(topology *Topology) *Topology {
	clone := &Topology{
		Schema: topology.Schema, Dataset: topology.Dataset,
		Nodes: append([]TopologyNode(nil), topology.Nodes...), Edges: append([]TopologyEdge(nil), topology.Edges...),
		Inputs: make(map[string][]string, len(topology.Inputs)), Outputs: make(map[string][]string, len(topology.Outputs)),
	}
	for name, members := range topology.Inputs {
		clone.Inputs[name] = append([]string(nil), members...)
	}
	for name, members := range topology.Outputs {
		clone.Outputs[name] = append([]string(nil), members...)
	}
	return clone
}

func mustTopologyDigest(topology *Topology) string {
	digest, err := canonicalTopologyDigest(topology)
	if err != nil {
		return "unavailable"
	}
	return digest
}

func compareTopologyParameters(candidate, baseline *Topology) ParameterParity {
	parity := ParameterParity{}
	if candidate == nil || baseline == nil {
		return parity
	}
	parity.NodeCount = len(candidate.Nodes)
	parity.BaselineNodeCount = len(baseline.Nodes)
	parity.EdgeCount = len(candidate.Edges)
	parity.BaselineEdgeCount = len(baseline.Edges)
	candidateBiases := make([]float64, len(candidate.Nodes))
	baselineBiases := make([]float64, len(baseline.Nodes))
	candidateNodes := make(map[string]uint64, len(candidate.Nodes))
	baselineNodes := make(map[string]uint64, len(baseline.Nodes))
	for i, node := range candidate.Nodes {
		candidateBiases[i] = node.Bias
		candidateNodes[node.ID] = math.Float64bits(node.Bias)
	}
	for i, node := range baseline.Nodes {
		baselineBiases[i] = node.Bias
		baselineNodes[node.ID] = math.Float64bits(node.Bias)
	}
	candidateWeights := make([]float64, len(candidate.Edges))
	baselineWeights := make([]float64, len(baseline.Edges))
	for i, edge := range candidate.Edges {
		candidateWeights[i] = edge.Weight
	}
	for i, edge := range baseline.Edges {
		baselineWeights[i] = edge.Weight
	}
	sort.Float64s(candidateBiases)
	sort.Float64s(baselineBiases)
	sort.Float64s(candidateWeights)
	sort.Float64s(baselineWeights)
	parity.NodeIDsUnchanged = equalStringUint64MapKeys(candidateNodes, baselineNodes)
	parity.BiasByNodeUnchanged = equalStringUint64Maps(candidateNodes, baselineNodes)
	parity.ExactBiasMultiset = equalFloatSequence(candidateBiases, baselineBiases)
	parity.ExactWeightMultiset = equalFloatSequence(candidateWeights, baselineWeights)
	candidateIn, candidateOut := topologyDegrees(candidate)
	baselineIn, baselineOut := topologyDegrees(baseline)
	parity.InDegreeByNodeUnchanged = equalStringIntMaps(candidateIn, baselineIn)
	parity.OutDegreeByNodeUnchanged = equalStringIntMaps(candidateOut, baselineOut)
	parity.InputBindingsUnchanged = equalPopulations(candidate.Inputs, baseline.Inputs)
	parity.OutputBindingsUnchanged = equalPopulations(candidate.Outputs, baseline.Outputs)
	parity.AgentContractValid = ValidateAgentContract(baseline) == nil
	parity.ConnectivityChanged = !equalEndpoints(topologyConnectivity(candidate), topologyConnectivity(baseline))
	return parity
}

type topologyEndpoint struct{ pre, post string }

func topologyConnectivity(topology *Topology) []topologyEndpoint {
	if topology == nil {
		return nil
	}
	result := make([]topologyEndpoint, len(topology.Edges))
	for i, edge := range topology.Edges {
		result[i] = topologyEndpoint{pre: edge.Pre, post: edge.Post}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].post != result[j].post {
			return result[i].post < result[j].post
		}
		return result[i].pre < result[j].pre
	})
	return result
}

func equalEndpoints(left, right []topologyEndpoint) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func topologyDegrees(topology *Topology) (map[string]int, map[string]int) {
	in := make(map[string]int, len(topology.Nodes))
	out := make(map[string]int, len(topology.Nodes))
	for _, node := range topology.Nodes {
		in[node.ID] = 0
		out[node.ID] = 0
	}
	for _, edge := range topology.Edges {
		in[edge.Post]++
		out[edge.Pre]++
	}
	return in, out
}

func equalStringUint64MapKeys(left, right map[string]uint64) bool {
	if len(left) != len(right) {
		return false
	}
	for key := range left {
		if _, ok := right[key]; !ok {
			return false
		}
	}
	return true
}

func equalStringUint64Maps(left, right map[string]uint64) bool {
	if !equalStringUint64MapKeys(left, right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func equalStringIntMaps(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if other, ok := right[key]; !ok || other != value {
			return false
		}
	}
	return true
}

func equalFloatSequence(left, right []float64) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if math.Float64bits(left[i]) != math.Float64bits(right[i]) {
			return false
		}
	}
	return true
}

func equalPopulations(left, right map[string][]string) bool {
	if len(left) != len(right) {
		return false
	}
	for name, leftMembers := range left {
		rightMembers, ok := right[name]
		if !ok || len(leftMembers) != len(rightMembers) {
			return false
		}
		leftCopy := append([]string(nil), leftMembers...)
		rightCopy := append([]string(nil), rightMembers...)
		sort.Strings(leftCopy)
		sort.Strings(rightCopy)
		for i := range leftCopy {
			if leftCopy[i] != rightCopy[i] {
				return false
			}
		}
	}
	return true
}

func (r *arenaRNG) index(size int) int {
	if size <= 1 {
		return 0
	}
	bound := uint64(size)
	limit := ^uint64(0) - (^uint64(0) % bound)
	for {
		value := r.next()
		if value < limit {
			return int(value % bound) // #nosec G115 -- modulo result is below positive platform int size.
		}
	}
}
