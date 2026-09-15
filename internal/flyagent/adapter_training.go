package flyagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"slices"
	"strings"
)

const (
	AdapterTrainingReportSchema      = "nevr.fly.adapter-training/v1"
	AdapterTrainingAttemptSchema     = "nevr.fly.adapter-training-attempt/v1"
	maxAdapterTrainingPasses         = 8
	maxAdapterTrainingEpisodes       = 32
	maxAdapterTrainingStateUpdates   = 250_000_000
	policyAdapterTrainableParameters = 12
	maxAdapterTrainingReportBytes    = 32 << 20
)

// AdapterTrainingConfig bounds a deterministic coordinate search around the
// frozen connectome. Training and held-out evaluation seeds are derived from
// MasterSeed and never overlap.
type AdapterTrainingConfig struct {
	MasterSeed          uint64  `json:"master_seed"`
	TrainingEpisodes    int     `json:"training_episodes"`
	EvaluationEpisodes  int     `json:"evaluation_episodes"`
	Passes              int     `json:"passes"`
	InitialStepFraction float64 `json:"initial_step_fraction"`
}

func DefaultAdapterTrainingConfig() AdapterTrainingConfig {
	return AdapterTrainingConfig{
		MasterSeed: 0x4d616c65434e53, TrainingEpisodes: 4,
		EvaluationEpisodes: 4, Passes: 2, InitialStepFraction: 0.1,
	}
}

// AdapterTrainingAttempt records every bounded candidate considered by the
// coordinate search. Score is the mean synthetic objective on training seeds.
type AdapterTrainingAttempt struct {
	Schema         string  `json:"schema"`
	Pass           int     `json:"pass"`
	Parameter      string  `json:"parameter"`
	Direction      int     `json:"direction"`
	Value          float64 `json:"value"`
	Step           float64 `json:"step"`
	Score          float64 `json:"score"`
	Accepted       bool    `json:"accepted"`
	SkippedAtBound bool    `json:"skipped_at_bound,omitempty"`
}

// AdapterTrainingReport is a reproducible optimization artifact. Its metrics
// describe only the small deterministic arena and make no public-play claim.
type AdapterTrainingReport struct {
	Schema                  string                   `json:"schema"`
	Config                  AdapterTrainingConfig    `json:"config"`
	SeedPlan                EpisodeSeedPlan          `json:"seed_plan"`
	TopologyDigest          string                   `json:"topology_digest"`
	ModelDigest             string                   `json:"model_digest"`
	TopologyNodeCount       int                      `json:"topology_node_count"`
	TopologyEdgeCount       int                      `json:"topology_edge_count"`
	FrozenConnectomeEdges   bool                     `json:"frozen_connectome_edges"`
	TrainableParameterCount int                      `json:"trainable_parameter_count"`
	PlannedStateUpdates     uint64                   `json:"planned_state_updates"`
	InitialAdapter          PolicyAdapter            `json:"initial_adapter"`
	InitialAdapterDigest    string                   `json:"initial_adapter_digest"`
	TrainedAdapter          PolicyAdapter            `json:"trained_adapter"`
	TrainedAdapterDigest    string                   `json:"trained_adapter_digest"`
	InitialTraining         EvaluationSummary        `json:"initial_training"`
	TrainedTraining         EvaluationSummary        `json:"trained_training"`
	HeldOutEvaluation       EvaluationSummary        `json:"held_out_evaluation"`
	Attempts                []AdapterTrainingAttempt `json:"attempts"`
	Limitations             []string                 `json:"limitations"`
}

// VerifiedPolicyAdapter is an immutable capability created only by loading the
// exact report bytes and reproducing their bounded training run. It binds one
// adapter to one topology, dynamics model, and report artifact.
type VerifiedPolicyAdapter struct {
	adapter           PolicyAdapter
	adapterDigest     string
	reportDigest      string
	topologyDigest    string
	modelDigest       string
	evaluationSeeds   []uint64
	arenaConfigDigest string
	verified          bool
}

func (v VerifiedPolicyAdapter) Verified() bool        { return v.verified }
func (v VerifiedPolicyAdapter) AdapterDigest() string { return v.adapterDigest }
func (v VerifiedPolicyAdapter) ReportDigest() string  { return v.reportDigest }

// Bind returns copies of the adapter and exact report digest only for the
// compiled network against which the capability was verified.
func (v VerifiedPolicyAdapter) Bind(network *Network) (PolicyAdapter, string, error) {
	if !v.verified || network == nil || v.topologyDigest != network.TopologyDigest() || v.modelDigest != network.ModelDigest() {
		return PolicyAdapter{}, "", errors.New("verified policy adapter is not bound to this network")
	}
	digest, err := DigestPolicyAdapter(v.adapter)
	if err != nil || digest != v.adapterDigest || !canonicalSHA256(v.reportDigest) {
		return PolicyAdapter{}, "", errors.New("verified policy adapter capability is inconsistent")
	}
	return v.adapter, v.reportDigest, nil
}

// LoadVerifiedPolicyAdapter hashes the exact bounded bytes it decodes, then
// reproduces the report's full deterministic training run before issuing a
// network-bound capability.
func LoadVerifiedPolicyAdapter(r io.Reader, topology *Topology, dynamics Dynamics) (*AdapterTrainingReport, VerifiedPolicyAdapter, error) {
	report, reportDigest, err := LoadAdapterTrainingReportWithDigest(r)
	if err != nil {
		return nil, VerifiedPolicyAdapter{}, err
	}
	if err := VerifyAdapterTrainingReport(topology, dynamics, report); err != nil {
		return nil, VerifiedPolicyAdapter{}, err
	}
	network, err := NewNetwork(topology, dynamics)
	if err != nil {
		return nil, VerifiedPolicyAdapter{}, err
	}
	return report, VerifiedPolicyAdapter{
		adapter: report.TrainedAdapter, adapterDigest: report.TrainedAdapterDigest,
		reportDigest: reportDigest, topologyDigest: network.TopologyDigest(),
		modelDigest: network.ModelDigest(), evaluationSeeds: append([]uint64(nil), report.SeedPlan.EvaluationSeeds...),
		arenaConfigDigest: report.HeldOutEvaluation.ArenaConfigDigest, verified: true,
	}, nil
}

// LoadAdapterTrainingReport strictly decodes and structurally validates a
// bounded training artifact. Structural validation alone does not prove that
// its claimed scores were produced by this trainer; consumers must call
// VerifyAdapterTrainingReport with the exact topology and dynamics.
func LoadAdapterTrainingReport(r io.Reader) (*AdapterTrainingReport, error) {
	report, _, err := LoadAdapterTrainingReportWithDigest(r)
	return report, err
}

// LoadAdapterTrainingReportWithDigest returns the SHA-256 of the exact bounded
// bytes decoded from r, allowing an action trace to bind to the consumed
// artifact without reopening a mutable pathname.
func LoadAdapterTrainingReportWithDigest(r io.Reader) (*AdapterTrainingReport, string, error) {
	if r == nil {
		return nil, "", errors.New("adapter training report reader is nil")
	}
	limited := &io.LimitedReader{R: r, N: maxAdapterTrainingReportBytes + 1}
	document, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", fmt.Errorf("read adapter training report: %w", err)
	}
	if len(document) > maxAdapterTrainingReportBytes {
		return nil, "", fmt.Errorf("adapter training report exceeds %d bytes", maxAdapterTrainingReportBytes)
	}
	if err := rejectDuplicateJSONKeys(document); err != nil {
		return nil, "", fmt.Errorf("decode adapter training report: %w", err)
	}
	var report AdapterTrainingReport
	if err := rejectNonExactJSONFields(document, &report); err != nil {
		return nil, "", fmt.Errorf("decode adapter training report: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return nil, "", fmt.Errorf("decode adapter training report: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, "", errors.New("decode adapter training report: multiple JSON values")
		}
		return nil, "", fmt.Errorf("decode adapter training report trailer: %w", err)
	}
	if err := report.Validate(); err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(document)
	return &report, fmt.Sprintf("%x", digest[:]), nil
}

// Validate checks the internal bindings and accounting of a training report.
// A caller must additionally compare TopologyDigest/ModelDigest with the exact
// network it is about to run.
func (r AdapterTrainingReport) Validate() error {
	if r.Schema != AdapterTrainingReportSchema {
		return fmt.Errorf("adapter training report schema %q is unsupported", r.Schema)
	}
	if err := validateAdapterTrainingConfig(r.Config); err != nil {
		return err
	}
	expectedSeedPlan, err := NewEpisodeSeedPlan(r.Config.MasterSeed, r.Config.TrainingEpisodes, r.Config.EvaluationEpisodes)
	if err != nil || r.SeedPlan.Schema != expectedSeedPlan.Schema || r.SeedPlan.MasterSeed != expectedSeedPlan.MasterSeed ||
		!slices.Equal(r.SeedPlan.TrainingSeeds, expectedSeedPlan.TrainingSeeds) ||
		!slices.Equal(r.SeedPlan.EvaluationSeeds, expectedSeedPlan.EvaluationSeeds) {
		return errors.New("adapter training report seed plan does not match its configuration")
	}
	if err := validateEvaluationSeeds(r.SeedPlan.TrainingSeeds); err != nil {
		return fmt.Errorf("adapter training seeds: %w", err)
	}
	if err := validateEvaluationSeeds(r.SeedPlan.EvaluationSeeds); err != nil {
		return fmt.Errorf("adapter evaluation seeds: %w", err)
	}
	seen := make(map[uint64]struct{}, len(r.SeedPlan.TrainingSeeds))
	for _, seed := range r.SeedPlan.TrainingSeeds {
		seen[seed] = struct{}{}
	}
	for _, seed := range r.SeedPlan.EvaluationSeeds {
		if _, reused := seen[seed]; reused {
			return fmt.Errorf("adapter evaluation seed %d was used for training", seed)
		}
	}
	if !canonicalSHA256(r.TopologyDigest) || !canonicalSHA256(r.ModelDigest) {
		return errors.New("adapter training report requires canonical topology and model SHA-256 digests")
	}
	if r.TopologyNodeCount < 1 || r.TopologyNodeCount > maxTopologyNodes ||
		r.TopologyEdgeCount < 1 || r.TopologyEdgeCount > maxTopologyEdges {
		return errors.New("adapter training report topology counts are outside supported bounds")
	}
	if !r.FrozenConnectomeEdges || r.TrainableParameterCount != policyAdapterTrainableParameters || r.PlannedStateUpdates == 0 {
		return errors.New("adapter training report does not attest the expected frozen-edge parameter boundary")
	}
	if r.InitialAdapter != DefaultPolicyAdapter() {
		return errors.New("adapter training report initial adapter is not the canonical default")
	}
	initialAdapterDigest, err := DigestPolicyAdapter(r.InitialAdapter)
	if err != nil || r.InitialAdapterDigest != initialAdapterDigest || !canonicalSHA256(r.InitialAdapterDigest) {
		return errors.New("adapter training report initial adapter digest is inconsistent")
	}
	if err := r.TrainedAdapter.Validate(); err != nil {
		return fmt.Errorf("trained policy adapter: %w", err)
	}
	trainedAdapterDigest, err := DigestPolicyAdapter(r.TrainedAdapter)
	if err != nil || r.TrainedAdapterDigest != trainedAdapterDigest || !canonicalSHA256(r.TrainedAdapterDigest) {
		return errors.New("adapter training report trained adapter digest is inconsistent")
	}
	if err := validateBoundEvaluationSummary(r.InitialTraining, "initial_training", r.TopologyDigest, r.ModelDigest, r.InitialAdapterDigest, r.SeedPlan.TrainingSeeds, r.TopologyNodeCount, r.TopologyEdgeCount); err != nil {
		return fmt.Errorf("initial training summary: %w", err)
	}
	if err := validateBoundEvaluationSummary(r.TrainedTraining, "trained_training", r.TopologyDigest, r.ModelDigest, r.TrainedAdapterDigest, r.SeedPlan.TrainingSeeds, r.TopologyNodeCount, r.TopologyEdgeCount); err != nil {
		return fmt.Errorf("trained training summary: %w", err)
	}
	if err := validateBoundEvaluationSummary(r.HeldOutEvaluation, "held_out_evaluation", r.TopologyDigest, r.ModelDigest, r.TrainedAdapterDigest, r.SeedPlan.EvaluationSeeds, r.TopologyNodeCount, r.TopologyEdgeCount); err != nil {
		return fmt.Errorf("held-out evaluation summary: %w", err)
	}
	if r.InitialTraining.ArenaConfig != r.TrainedTraining.ArenaConfig || r.InitialTraining.ArenaConfig != r.HeldOutEvaluation.ArenaConfig ||
		r.InitialTraining.Dynamics != r.TrainedTraining.Dynamics || r.InitialTraining.Dynamics != r.HeldOutEvaluation.Dynamics ||
		r.InitialTraining.Dataset != r.TrainedTraining.Dataset || r.InitialTraining.Dataset != r.HeldOutEvaluation.Dataset {
		return errors.New("adapter training report evaluation environments are inconsistent")
	}
	trainingEvaluations := uint64(1 + 2*policyAdapterTrainableParameters*r.Config.Passes) // #nosec G115 -- validated passes in 1..8 make this positive and tiny.
	if r.InitialTraining.PlannedStateUpdates > maxAdapterTrainingStateUpdates/trainingEvaluations ||
		r.InitialTraining.PlannedStateUpdates*trainingEvaluations > maxAdapterTrainingStateUpdates-r.HeldOutEvaluation.PlannedStateUpdates {
		return fmt.Errorf("adapter training report exceeds %d planned state updates", maxAdapterTrainingStateUpdates)
	}
	expectedPlannedWork := r.InitialTraining.PlannedStateUpdates*trainingEvaluations + r.HeldOutEvaluation.PlannedStateUpdates
	if r.TrainedTraining.PlannedStateUpdates != r.InitialTraining.PlannedStateUpdates || r.PlannedStateUpdates != expectedPlannedWork {
		return errors.New("adapter training report planned work accounting is inconsistent")
	}
	if r.TrainedTraining.MeanSyntheticObjectiveReturn+1e-12 < r.InitialTraining.MeanSyntheticObjectiveReturn {
		return errors.New("adapter training report regresses its training objective")
	}
	if len(r.Attempts) != r.Config.Passes*policyAdapterTrainableParameters*2 {
		return errors.New("adapter training report attempt count is inconsistent")
	}
	currentAdapter := r.InitialAdapter
	currentScore := r.InitialTraining.MeanSyntheticObjectiveReturn
	attemptIndex := 0
	for pass := 1; pass <= r.Config.Passes; pass++ {
		stepFraction := r.Config.InitialStepFraction * math.Pow(0.5, float64(pass-1))
		for _, parameter := range adapterParameters() {
			baseValue := parameter.get(currentAdapter)
			bestScore := currentScore
			bestDirectionIndex := -1
			var candidates [2]PolicyAdapter
			for directionIndex, direction := range []int{1, -1} {
				attempt := r.Attempts[attemptIndex+directionIndex]
				expectedValue := clamp(baseValue+float64(direction)*stepFraction*(parameter.maximum-parameter.minimum), parameter.minimum, parameter.maximum)
				expectedStep := math.Abs(expectedValue - baseValue)
				expectedSkipped := expectedValue == baseValue
				if attempt.Schema != AdapterTrainingAttemptSchema || attempt.Pass != pass || attempt.Parameter != parameter.name ||
					attempt.Direction != direction || !nearlyEqual(attempt.Value, expectedValue) || !nearlyEqual(attempt.Step, expectedStep) ||
					attempt.SkippedAtBound != expectedSkipped || !finite(attempt.Score) ||
					(expectedSkipped && (!nearlyEqual(attempt.Score, currentScore) || attempt.Accepted)) {
					return errors.New("adapter training report optimization trajectory is inconsistent")
				}
				candidate := currentAdapter
				parameter.set(&candidate, expectedValue)
				candidates[directionIndex] = candidate
				if !expectedSkipped && attempt.Score > bestScore+1e-12 {
					bestScore = attempt.Score
					bestDirectionIndex = directionIndex
				}
			}
			for directionIndex := range 2 {
				if r.Attempts[attemptIndex+directionIndex].Accepted != (directionIndex == bestDirectionIndex) {
					return errors.New("adapter training report accepted-attempt trajectory is inconsistent")
				}
			}
			if bestDirectionIndex >= 0 {
				currentAdapter = candidates[bestDirectionIndex]
				currentScore = bestScore
			}
			attemptIndex += 2
		}
	}
	if currentAdapter != r.TrainedAdapter || !nearlyEqual(currentScore, r.TrainedTraining.MeanSyntheticObjectiveReturn) {
		return errors.New("adapter training report final optimizer state is inconsistent")
	}
	if !slices.Contains(r.Limitations, SyntheticEvaluationLimitation) {
		return errors.New("adapter training report omits the canonical synthetic-evaluation limitation")
	}
	return nil
}

// VerifyAdapterTrainingReport replays the complete deterministic, bounded
// coordinate search and requires the claimed artifact to match exactly. This
// verifies reproducibility under this implementation; it is not a signature,
// independent biological validation, or evidence of Echo VR skill.
func VerifyAdapterTrainingReport(topology *Topology, dynamics Dynamics, claimed *AdapterTrainingReport) error {
	if claimed == nil {
		return errors.New("adapter training report is nil")
	}
	if err := claimed.Validate(); err != nil {
		return err
	}
	network, err := NewNetwork(topology, dynamics)
	if err != nil {
		return fmt.Errorf("compile adapter verification topology: %w", err)
	}
	if claimed.TopologyDigest != network.TopologyDigest() || claimed.ModelDigest != network.ModelDigest() ||
		claimed.TopologyNodeCount != len(topology.Nodes) || claimed.TopologyEdgeCount != len(topology.Edges) {
		return errors.New("adapter training report is bound to a different topology or dynamics")
	}
	reproduced, err := TrainPolicyAdapter(topology, dynamics, claimed.InitialTraining.ArenaConfig, claimed.Config)
	if err != nil {
		return fmt.Errorf("reproduce adapter training report: %w", err)
	}
	if !reflect.DeepEqual(reproduced, *claimed) {
		return errors.New("adapter training report does not match deterministic reproduction")
	}
	return nil
}

func validateBoundEvaluationSummary(summary EvaluationSummary, label, topologyDigest, modelDigest, adapterDigest string, seeds []uint64, nodeCount, edgeCount int) error {
	if summary.Schema != EvaluationSummarySchema || summary.Label != label || summary.TopologyDigest != topologyDigest || summary.ModelDigest != modelDigest ||
		summary.PolicyAdapterSchema != PolicyAdapterSchema || summary.PolicyAdapterDigest != adapterDigest || !canonicalSHA256(summary.PolicyAdapterDigest) ||
		summary.Environment != ArenaSourceKind || !slices.Equal(summary.Seeds, seeds) || len(summary.Episodes) != len(seeds) ||
		!slices.Contains(summary.Limitations, SyntheticEvaluationLimitation) {
		return errors.New("evaluation identity, seeds, episodes, or limitations are inconsistent")
	}
	if err := summary.ArenaConfig.validate(); err != nil {
		return fmt.Errorf("evaluation arena config: %w", err)
	}
	arenaDigest, err := arenaConfigDigest(summary.ArenaConfig)
	if err != nil || arenaDigest != summary.ArenaConfigDigest || !canonicalSHA256(summary.ArenaConfigDigest) {
		return errors.New("evaluation arena configuration digest is inconsistent")
	}
	if !finite(summary.Dynamics.TauSeconds) || summary.Dynamics.TauSeconds <= 0 ||
		!finite(summary.Dynamics.InputGain) || summary.Dynamics.InputGain < 0 ||
		!finite(summary.Dynamics.RecurrentGain) || summary.Dynamics.RecurrentGain < 0 ||
		!finite(summary.Dynamics.MaxStep) || summary.Dynamics.MaxStep <= 0 {
		return errors.New("evaluation dynamics are invalid")
	}
	expectedModelDigest, err := canonicalModelDigest(topologyDigest, summary.Dynamics)
	if err != nil || expectedModelDigest != modelDigest {
		return errors.New("evaluation dynamics do not match the model digest")
	}
	unitsPerStep := uint64(nodeCount) + uint64(edgeCount)                                    // #nosec G115 -- both counts were validated positive and within topology limits.
	expectedWork := unitsPerStep * uint64(summary.ArenaConfig.MaxSteps) * uint64(len(seeds)) // #nosec G115 -- arena and seed validators bound both positive values.
	if unitsPerStep == 0 || expectedWork > maxClosedLoopStateUpdates || summary.PlannedStateUpdates != expectedWork {
		return errors.New("evaluation planned work is inconsistent")
	}
	if strings.TrimSpace(summary.Dataset.Name) == "" {
		return errors.New("evaluation dataset identity is empty")
	}
	var objective, bestDistance, finalDistance, confidence float64
	possessions, acquisitions, goalsFor, goalsAgainst, neutral := 0, 0, 0, 0, 0
	for i, episode := range summary.Episodes {
		if episode.Schema != EpisodeMetricsSchema || episode.Seed != seeds[i] || episode.Dataset != summary.Dataset || episode.TopologyDigest != topologyDigest || episode.ModelDigest != modelDigest ||
			!finite(episode.SyntheticObjectiveReturn) || !finite(episode.BestDiscDistance) || !finite(episode.FinalDiscDistance) ||
			!finite(episode.InitialDiscDistance) || !finite(episode.MeanActionConfidence) || episode.MeanActionConfidence < 0 || episode.MeanActionConfidence > 1 ||
			episode.InitialDiscDistance < 0 || episode.BestDiscDistance < 0 || episode.BestDiscDistance > episode.InitialDiscDistance+1e-9 || episode.FinalDiscDistance < 0 ||
			episode.Steps < 1 || episode.Steps > summary.ArenaConfig.MaxSteps || episode.NeutralActions < 0 || episode.NeutralActions > episode.Steps ||
			episode.PossessionSteps < 0 || episode.PossessionSteps > episode.Steps || episode.DiscAcquisitions < 0 || episode.DiscAcquisitions > episode.Steps ||
			episode.DiscReleases < 0 || episode.DiscReleases > episode.Steps || episode.GoalsFor < 0 || episode.GoalsFor > 1 || episode.GoalsAgainst < 0 || episode.GoalsAgainst > 1 ||
			!episode.Done || !validEpisodeTermination(episode, summary.ArenaConfig.MaxSteps) ||
			(episode.DiscAcquisitions == 0 && episode.FirstPossessionStep != -1) ||
			(episode.DiscAcquisitions > 0 && (episode.FirstPossessionStep < 1 || episode.FirstPossessionStep > episode.Steps)) {
			return errors.New("evaluation contains invalid episode metrics")
		}
		objective += episode.SyntheticObjectiveReturn
		bestDistance += episode.BestDiscDistance
		finalDistance += episode.FinalDiscDistance
		confidence += episode.MeanActionConfidence
		if episode.FirstPossessionStep >= 0 {
			possessions++
		}
		acquisitions += episode.DiscAcquisitions
		goalsFor += episode.GoalsFor
		goalsAgainst += episode.GoalsAgainst
		neutral += episode.NeutralActions
	}
	denominator := float64(len(seeds))
	valuesMatch := nearlyEqual(summary.MeanSyntheticObjectiveReturn, objective/denominator) &&
		nearlyEqual(summary.MeanBestDiscDistance, bestDistance/denominator) &&
		nearlyEqual(summary.MeanFinalDiscDistance, finalDistance/denominator) &&
		nearlyEqual(summary.MeanActionConfidence, confidence/denominator)
	if !valuesMatch || summary.EpisodesWithPossession != possessions || summary.TotalDiscAcquisitions != acquisitions ||
		summary.TotalGoalsFor != goalsFor || summary.TotalGoalsAgainst != goalsAgainst || summary.TotalNeutralActions != neutral {
		return errors.New("evaluation aggregate metrics do not match episodes")
	}
	return nil
}

func validEpisodeTermination(episode EpisodeMetrics, maxSteps int) bool {
	switch episode.TerminationReason {
	case "attack_goal":
		return episode.GoalsFor == 1 && episode.GoalsAgainst == 0
	case "defended_goal":
		return episode.GoalsFor == 0 && episode.GoalsAgainst == 1
	case "step_limit":
		return episode.Steps == maxSteps && episode.GoalsFor == 0 && episode.GoalsAgainst == 0
	default:
		return false
	}
}

func canonicalSHA256(value string) bool { return isSHA256(value) && value == strings.ToLower(value) }

func nearlyEqual(left, right float64) bool {
	return finite(left) && finite(right) && math.Abs(left-right) <= 1e-12*math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
}

// TrainPolicyAdapter tunes only the bounded PolicyAdapter fields. Connectome
// nodes, edges, signs, weights, populations and dynamics remain unchanged.
func TrainPolicyAdapter(topology *Topology, dynamics Dynamics, arenaConfig ArenaConfig, config AdapterTrainingConfig) (AdapterTrainingReport, error) {
	if err := validateAdapterTrainingConfig(config); err != nil {
		return AdapterTrainingReport{}, err
	}
	if err := ValidateAgentContract(topology); err != nil {
		return AdapterTrainingReport{}, fmt.Errorf("adapter training topology: %w", err)
	}
	if err := arenaConfig.validate(); err != nil {
		return AdapterTrainingReport{}, err
	}
	seedPlan, err := NewEpisodeSeedPlan(config.MasterSeed, config.TrainingEpisodes, config.EvaluationEpisodes)
	if err != nil {
		return AdapterTrainingReport{}, err
	}
	plannedWork, err := adapterTrainingWork(topology, arenaConfig.MaxSteps, config)
	if err != nil {
		return AdapterTrainingReport{}, err
	}
	network, err := NewNetwork(topology, dynamics)
	if err != nil {
		return AdapterTrainingReport{}, err
	}
	initial := DefaultPolicyAdapter()
	initialAdapterDigest, err := DigestPolicyAdapter(initial)
	if err != nil {
		return AdapterTrainingReport{}, err
	}
	initialTraining, err := EvaluatePolicyAdapter("initial_training", topology, dynamics, arenaConfig, seedPlan.TrainingSeeds, initial)
	if err != nil {
		return AdapterTrainingReport{}, err
	}
	current := initial
	currentSummary := initialTraining
	currentScore := initialTraining.MeanSyntheticObjectiveReturn
	attempts := make([]AdapterTrainingAttempt, 0, config.Passes*policyAdapterTrainableParameters*2)

	for pass := 0; pass < config.Passes; pass++ {
		stepFraction := config.InitialStepFraction * math.Pow(0.5, float64(pass))
		for _, parameter := range adapterParameters() {
			baseValue := parameter.get(current)
			passAttempts := make([]AdapterTrainingAttempt, 0, 2)
			bestAdapter := current
			bestSummary := currentSummary
			bestScore := currentScore
			bestAttempt := -1
			for _, direction := range []int{1, -1} {
				delta := stepFraction * (parameter.maximum - parameter.minimum)
				value := clamp(baseValue+float64(direction)*delta, parameter.minimum, parameter.maximum)
				attempt := AdapterTrainingAttempt{
					Schema: AdapterTrainingAttemptSchema, Pass: pass + 1,
					Parameter: parameter.name, Direction: direction, Value: value, Step: math.Abs(value - baseValue),
				}
				if value == baseValue {
					attempt.Score = currentScore
					attempt.SkippedAtBound = true
					passAttempts = append(passAttempts, attempt)
					continue
				}
				candidate := current
				parameter.set(&candidate, value)
				summary, err := EvaluatePolicyAdapter("training_candidate", topology, dynamics, arenaConfig, seedPlan.TrainingSeeds, candidate)
				if err != nil {
					return AdapterTrainingReport{}, fmt.Errorf("train pass %d parameter %s: %w", pass+1, parameter.name, err)
				}
				attempt.Score = summary.MeanSyntheticObjectiveReturn
				passAttempts = append(passAttempts, attempt)
				if attempt.Score > bestScore+1e-12 {
					bestAdapter, bestSummary, bestScore = candidate, summary, attempt.Score
					bestAttempt = len(passAttempts) - 1
				}
			}
			if bestAttempt >= 0 {
				passAttempts[bestAttempt].Accepted = true
				current, currentSummary, currentScore = bestAdapter, bestSummary, bestScore
			}
			attempts = append(attempts, passAttempts...)
		}
	}
	heldOut, err := EvaluatePolicyAdapter("held_out_evaluation", topology, dynamics, arenaConfig, seedPlan.EvaluationSeeds, current)
	if err != nil {
		return AdapterTrainingReport{}, err
	}
	currentSummary.Label = "trained_training"
	trainedAdapterDigest, err := DigestPolicyAdapter(current)
	if err != nil {
		return AdapterTrainingReport{}, err
	}
	return AdapterTrainingReport{
		Schema: AdapterTrainingReportSchema, Config: config, SeedPlan: seedPlan,
		TopologyDigest: network.TopologyDigest(), ModelDigest: network.ModelDigest(),
		TopologyNodeCount: len(topology.Nodes), TopologyEdgeCount: len(topology.Edges),
		FrozenConnectomeEdges: true, TrainableParameterCount: policyAdapterTrainableParameters,
		PlannedStateUpdates: plannedWork, InitialAdapter: initial, InitialAdapterDigest: initialAdapterDigest,
		TrainedAdapter: current, TrainedAdapterDigest: trainedAdapterDigest,
		InitialTraining: initialTraining, TrainedTraining: currentSummary,
		HeldOutEvaluation: heldOut, Attempts: attempts,
		Limitations: []string{
			SyntheticEvaluationLimitation,
			"Coordinate search tunes only bounded sensory gains and motor readout gains/thresholds; connectome edges remain frozen.",
			"Held-out synthetic seeds reduce direct seed reuse but do not establish transfer to Echo VR or human play.",
		},
	}, nil
}

// EvaluatePolicyAdapter runs a fixed adapter on explicit seeds while reusing
// the closed-loop arena's metrics and safety validation.
func EvaluatePolicyAdapter(label string, topology *Topology, dynamics Dynamics, arenaConfig ArenaConfig, seeds []uint64, adapter PolicyAdapter) (EvaluationSummary, error) {
	if strings.TrimSpace(label) == "" || len(label) > 128 {
		return EvaluationSummary{}, errors.New("policy adapter evaluation label is required and must be at most 128 bytes")
	}
	if err := adapter.Validate(); err != nil {
		return EvaluationSummary{}, err
	}
	if err := validateEvaluationSeeds(seeds); err != nil {
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
	adapterDigest, err := DigestPolicyAdapter(adapter)
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
		Limitations: []string{
			SyntheticEvaluationLimitation,
			"A bounded policy adapter is active; connectome topology and edge weights are unchanged.",
		},
	}
	for _, seed := range seeds {
		if _, err := runner.Reset(seed); err != nil {
			return EvaluationSummary{}, err
		}
		done := false
		for !done {
			_, _, transition, err := runner.StepWithPolicy(func(observation *Observation) (ActionIntent, error) {
				currents, err := adapter.AdaptCurrents(observation.Currents)
				if err != nil {
					return ActionIntent{}, err
				}
				if err := runner.network.Step(observation.DeltaTime, currents); err != nil {
					return ActionIntent{}, err
				}
				return DecodeWithAdapter(observation, runner.network, adapter)
			})
			if err != nil {
				return EvaluationSummary{}, fmt.Errorf("seed %d: %w", seed, err)
			}
			done = transition.Done
		}
		metrics, err := runner.Metrics()
		if err != nil {
			return EvaluationSummary{}, err
		}
		appendEvaluationMetrics(&summary, metrics)
	}
	finishEvaluationSummary(&summary)
	return summary, nil
}

// ComparePolicyAdapterWithShuffledBaseline applies the exact same bounded
// adapter to the candidate and equal-parameter weight control.
func ComparePolicyAdapterWithShuffledBaseline(topology *Topology, dynamics Dynamics, arenaConfig ArenaConfig, seeds []uint64, shuffleSeed uint64, adapter PolicyAdapter) (ComparisonReport, error) {
	baseline, err := ShuffledWeightBaseline(topology, shuffleSeed)
	if err != nil {
		return ComparisonReport{}, err
	}
	candidate, err := EvaluatePolicyAdapter("bounded_adapter_candidate", topology, dynamics, arenaConfig, seeds, adapter)
	if err != nil {
		return ComparisonReport{}, err
	}
	shuffled, err := EvaluatePolicyAdapter("bounded_adapter_on_weight_permuted_baseline", baseline, dynamics, arenaConfig, seeds, adapter)
	if err != nil {
		return ComparisonReport{}, err
	}
	report := ComparisonReport{
		Schema: ComparisonReportSchema, ExperimentMode: "bounded_adapter_control", BaselineSeed: shuffleSeed,
		BaselineMethod: "edge_weight_permutation_fixed_connectivity",
		PolicyAdapter:  adapter, PolicyAdapterDigest: candidate.PolicyAdapterDigest,
		Candidate: candidate, Baseline: shuffled,
		ParameterParity: compareTopologyParameters(topology, baseline),
		Limitations: []string{
			SyntheticEvaluationLimitation,
			"The identical bounded sensory/readout adapter is applied to both graphs on paired caller-supplied seeds.",
			"The control permutes weights over fixed edges; it is not proof that connectome topology caused a measured difference.",
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
	parity := report.ParameterParity
	if parity.NodeCount != parity.BaselineNodeCount || parity.EdgeCount != parity.BaselineEdgeCount ||
		!parity.NodeIDsUnchanged || !parity.BiasByNodeUnchanged || !parity.ExactBiasMultiset ||
		!parity.ExactWeightMultiset || !parity.InDegreeByNodeUnchanged || !parity.OutDegreeByNodeUnchanged ||
		!parity.InputBindingsUnchanged || !parity.OutputBindingsUnchanged || !parity.AgentContractValid || parity.ConnectivityChanged {
		return ComparisonReport{}, errors.New("trained-adapter shuffled baseline failed equal-parameter audit")
	}
	return report, nil
}

// ComparePolicyAdapterWithRewiredBaseline applies one adapter to the candidate
// and the degree-preserving, contract-preserving connectivity control.
func ComparePolicyAdapterWithRewiredBaseline(topology *Topology, dynamics Dynamics, arenaConfig ArenaConfig, seeds []uint64, shuffleSeed uint64, adapter PolicyAdapter) (ComparisonReport, error) {
	baseline, protectedEdges, err := shuffledConnectivityBaseline(topology, shuffleSeed)
	if err != nil {
		return ComparisonReport{}, err
	}
	candidate, err := EvaluatePolicyAdapter("bounded_adapter_candidate", topology, dynamics, arenaConfig, seeds, adapter)
	if err != nil {
		return ComparisonReport{}, err
	}
	rewired, err := EvaluatePolicyAdapter("bounded_adapter_on_contract_preserving_rewired_baseline", baseline, dynamics, arenaConfig, seeds, adapter)
	if err != nil {
		return ComparisonReport{}, err
	}
	report := ComparisonReport{
		Schema: ComparisonReportSchema, ExperimentMode: "bounded_adapter_control", BaselineSeed: shuffleSeed,
		BaselineMethod: "degree_preserving_endpoint_permutation_with_protected_motor_paths",
		ProtectedEdges: protectedEdges, PolicyAdapter: adapter, PolicyAdapterDigest: candidate.PolicyAdapterDigest,
		Candidate: candidate, Baseline: rewired,
		ParameterParity: compareTopologyParameters(topology, baseline),
		Limitations: []string{
			SyntheticEvaluationLimitation,
			"The identical bounded sensory/readout adapter is applied to both graphs on paired caller-supplied seeds.",
			"One deterministic shortest actionable path to every required motor is protected; this deliberately biases the control toward interface viability.",
			"Degree-preserving endpoint permutation is one topology control, not proof that connectome wiring caused a measured difference.",
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
		!parity.InputBindingsUnchanged || !parity.OutputBindingsUnchanged || !parity.AgentContractValid || !parity.ConnectivityChanged {
		return ComparisonReport{}, errors.New("trained-adapter rewired baseline failed parity or reachability audit")
	}
	return report, nil
}

// CompareVerifiedPolicyAdapterWithShuffledBaseline binds a comparison to the
// exact deterministically reproduced training artifact that issued capability.
func CompareVerifiedPolicyAdapterWithShuffledBaseline(topology *Topology, dynamics Dynamics, arenaConfig ArenaConfig, seeds []uint64, shuffleSeed uint64, capability VerifiedPolicyAdapter) (ComparisonReport, error) {
	if err := capability.verifyHeldOutBinding(arenaConfig, seeds); err != nil {
		return ComparisonReport{}, err
	}
	network, err := NewNetwork(topology, dynamics)
	if err != nil {
		return ComparisonReport{}, err
	}
	adapter, reportDigest, err := capability.Bind(network)
	if err != nil {
		return ComparisonReport{}, err
	}
	report, err := ComparePolicyAdapterWithShuffledBaseline(topology, dynamics, arenaConfig, seeds, shuffleSeed, adapter)
	if err != nil {
		return ComparisonReport{}, err
	}
	report.ExperimentMode = "verified_trained_adapter_control"
	report.PolicyAdapterReportDigest = reportDigest
	report.Limitations = append(report.Limitations, "Adapter and paired seeds were reproduced from the exact report's declared held-out evaluation split.")
	return report, nil
}

// CompareVerifiedPolicyAdapterWithRewiredBaseline is the corresponding
// degree-preserving connectivity control for a verified trained adapter.
func CompareVerifiedPolicyAdapterWithRewiredBaseline(topology *Topology, dynamics Dynamics, arenaConfig ArenaConfig, seeds []uint64, shuffleSeed uint64, capability VerifiedPolicyAdapter) (ComparisonReport, error) {
	if err := capability.verifyHeldOutBinding(arenaConfig, seeds); err != nil {
		return ComparisonReport{}, err
	}
	network, err := NewNetwork(topology, dynamics)
	if err != nil {
		return ComparisonReport{}, err
	}
	adapter, reportDigest, err := capability.Bind(network)
	if err != nil {
		return ComparisonReport{}, err
	}
	report, err := ComparePolicyAdapterWithRewiredBaseline(topology, dynamics, arenaConfig, seeds, shuffleSeed, adapter)
	if err != nil {
		return ComparisonReport{}, err
	}
	report.ExperimentMode = "verified_trained_adapter_control"
	report.PolicyAdapterReportDigest = reportDigest
	report.Limitations = append(report.Limitations, "Adapter and paired seeds were reproduced from the exact report's declared held-out evaluation split.")
	return report, nil
}

func (v VerifiedPolicyAdapter) verifyHeldOutBinding(arenaConfig ArenaConfig, seeds []uint64) error {
	if !v.verified || !slices.Equal(seeds, v.evaluationSeeds) {
		return errors.New("verified policy adapter control seeds do not match its held-out report split")
	}
	digest, err := arenaConfigDigest(arenaConfig)
	if err != nil || digest != v.arenaConfigDigest {
		return errors.New("verified policy adapter control arena does not match its held-out report configuration")
	}
	return nil
}

func appendEvaluationMetrics(summary *EvaluationSummary, metrics EpisodeMetrics) {
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

func finishEvaluationSummary(summary *EvaluationSummary) {
	denominator := float64(len(summary.Episodes))
	summary.MeanSyntheticObjectiveReturn /= denominator
	summary.MeanBestDiscDistance /= denominator
	summary.MeanFinalDiscDistance /= denominator
	summary.MeanActionConfidence /= denominator
}

func validateAdapterTrainingConfig(config AdapterTrainingConfig) error {
	if config.TrainingEpisodes < 1 || config.TrainingEpisodes > maxAdapterTrainingEpisodes ||
		config.EvaluationEpisodes < 1 || config.EvaluationEpisodes > maxAdapterTrainingEpisodes {
		return fmt.Errorf("adapter training and evaluation episodes must each be in 1..%d", maxAdapterTrainingEpisodes)
	}
	if config.Passes < 1 || config.Passes > maxAdapterTrainingPasses {
		return fmt.Errorf("adapter training passes must be in 1..%d", maxAdapterTrainingPasses)
	}
	if !finite(config.InitialStepFraction) || config.InitialStepFraction <= 0 || config.InitialStepFraction > 0.5 {
		return errors.New("adapter training initial_step_fraction must be finite and in (0, 0.5]")
	}
	return nil
}

func adapterTrainingWork(topology *Topology, steps int, config AdapterTrainingConfig) (uint64, error) {
	if topology == nil || steps < 1 {
		return 0, errors.New("adapter training work estimate requires a topology and positive step count")
	}
	// Initial training evaluation, two candidates per parameter/pass, and one
	// final held-out evaluation. Bound-skipped candidates make actual work lower.
	trainingEvaluations := 1 + 2*policyAdapterTrainableParameters*config.Passes
	episodes := uint64(trainingEvaluations*config.TrainingEpisodes + config.EvaluationEpisodes) // #nosec G115 -- validated positive counts are bounded by 8 passes and 32 episodes.
	unitsPerStep := uint64(len(topology.Nodes)) + uint64(len(topology.Edges))
	if unitsPerStep == 0 || uint64(steps) > maxAdapterTrainingStateUpdates/unitsPerStep {
		return 0, fmt.Errorf("adapter training exceeds %d node/edge state updates", maxAdapterTrainingStateUpdates)
	}
	perEpisode := unitsPerStep * uint64(steps)
	if episodes > maxAdapterTrainingStateUpdates/perEpisode {
		return 0, fmt.Errorf("adapter training exceeds %d node/edge state updates; reduce topology, max_steps, episodes, or passes", maxAdapterTrainingStateUpdates)
	}
	return perEpisode * episodes, nil
}

type adapterParameter struct {
	name             string
	minimum, maximum float64
	get              func(PolicyAdapter) float64
	set              func(*PolicyAdapter, float64)
}

func adapterParameters() []adapterParameter {
	return []adapterParameter{
		{"target_direction_gain", 0, 4, func(a PolicyAdapter) float64 { return a.TargetDirectionGain }, func(a *PolicyAdapter, v float64) { a.TargetDirectionGain = v }},
		{"target_range_gain", 0, 4, func(a PolicyAdapter) float64 { return a.TargetRangeGain }, func(a *PolicyAdapter, v float64) { a.TargetRangeGain = v }},
		{"opponent_gain", 0, 4, func(a PolicyAdapter) float64 { return a.OpponentGain }, func(a *PolicyAdapter, v float64) { a.OpponentGain = v }},
		{"teammate_gain", 0, 4, func(a PolicyAdapter) float64 { return a.TeammateGain }, func(a *PolicyAdapter, v float64) { a.TeammateGain = v }},
		{"opportunity_gain", 0, 4, func(a PolicyAdapter) float64 { return a.OpportunityGain }, func(a *PolicyAdapter, v float64) { a.OpportunityGain = v }},
		{"possession_context_gain", 0, 4, func(a PolicyAdapter) float64 { return a.PossessionContextGain }, func(a *PolicyAdapter, v float64) { a.PossessionContextGain = v }},
		{"translation_gain", 0, 4, func(a PolicyAdapter) float64 { return a.TranslationGain }, func(a *PolicyAdapter, v float64) { a.TranslationGain = v }},
		{"yaw_gain", 0, 4, func(a PolicyAdapter) float64 { return a.YawGain }, func(a *PolicyAdapter, v float64) { a.YawGain = v }},
		{"grip_threshold", 0.05, 0.95, func(a PolicyAdapter) float64 { return a.GripThreshold }, func(a *PolicyAdapter, v float64) { a.GripThreshold = v }},
		{"release_threshold", 0.05, 0.95, func(a PolicyAdapter) float64 { return a.ReleaseThreshold }, func(a *PolicyAdapter, v float64) { a.ReleaseThreshold = v }},
		{"boost_threshold", 0.05, 0.95, func(a PolicyAdapter) float64 { return a.BoostThreshold }, func(a *PolicyAdapter, v float64) { a.BoostThreshold = v }},
		{"brake_threshold", 0.05, 0.95, func(a PolicyAdapter) float64 { return a.BrakeThreshold }, func(a *PolicyAdapter, v float64) { a.BrakeThreshold = v }},
	}
}
