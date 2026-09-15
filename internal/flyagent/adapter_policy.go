package flyagent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

const PolicyAdapterSchema = "nevr.fly.policy-adapter/v1"

// PolicyAdapter is the deliberately small trainable surface around a frozen
// connectome. It scales semantic sensory groups and motor readouts; it cannot
// add, remove, redirect, or reweight connectome edges.
type PolicyAdapter struct {
	Schema                string  `json:"schema"`
	TargetDirectionGain   float64 `json:"target_direction_gain"`
	TargetRangeGain       float64 `json:"target_range_gain"`
	OpponentGain          float64 `json:"opponent_gain"`
	TeammateGain          float64 `json:"teammate_gain"`
	OpportunityGain       float64 `json:"opportunity_gain"`
	PossessionContextGain float64 `json:"possession_context_gain"`
	TranslationGain       float64 `json:"translation_gain"`
	YawGain               float64 `json:"yaw_gain"`
	GripThreshold         float64 `json:"grip_threshold"`
	ReleaseThreshold      float64 `json:"release_threshold"`
	BoostThreshold        float64 `json:"boost_threshold"`
	BrakeThreshold        float64 `json:"brake_threshold"`
}

// DefaultPolicyAdapter preserves the original encoder/decoder behavior.
func DefaultPolicyAdapter() PolicyAdapter {
	return PolicyAdapter{
		Schema:              PolicyAdapterSchema,
		TargetDirectionGain: 1, TargetRangeGain: 1, OpponentGain: 1,
		TeammateGain: 1, OpportunityGain: 1, PossessionContextGain: 1,
		TranslationGain: 1, YawGain: 1,
		GripThreshold: 0.55, ReleaseThreshold: 0.55,
		BoostThreshold: 0.55, BrakeThreshold: 0.55,
	}
}

// DigestPolicyAdapter fingerprints the complete bounded adapter so action
// traces can distinguish default and trained readouts on the same network.
func DigestPolicyAdapter(adapter PolicyAdapter) (string, error) {
	if err := adapter.Validate(); err != nil {
		return "", err
	}
	document, err := json.Marshal(adapter)
	if err != nil {
		return "", fmt.Errorf("encode policy adapter: %w", err)
	}
	digest := sha256.Sum256(document)
	return fmt.Sprintf("%x", digest[:]), nil
}

// Validate keeps the learnable surface finite and bounded. Thresholds cannot
// be trained to permanently-on or permanently-off endpoints.
func (a PolicyAdapter) Validate() error {
	if a.Schema != PolicyAdapterSchema {
		return fmt.Errorf("policy adapter schema %q is unsupported", a.Schema)
	}
	gains := map[string]float64{
		"target_direction_gain":   a.TargetDirectionGain,
		"target_range_gain":       a.TargetRangeGain,
		"opponent_gain":           a.OpponentGain,
		"teammate_gain":           a.TeammateGain,
		"opportunity_gain":        a.OpportunityGain,
		"possession_context_gain": a.PossessionContextGain,
		"translation_gain":        a.TranslationGain,
		"yaw_gain":                a.YawGain,
	}
	for name, value := range gains {
		if !finite(value) || value < 0 || value > 4 {
			return fmt.Errorf("policy adapter %s must be finite and in [0, 4]", name)
		}
	}
	thresholds := map[string]float64{
		"grip_threshold": a.GripThreshold, "release_threshold": a.ReleaseThreshold,
		"boost_threshold": a.BoostThreshold, "brake_threshold": a.BrakeThreshold,
	}
	for name, value := range thresholds {
		if !finite(value) || value < 0.05 || value > 0.95 {
			return fmt.Errorf("policy adapter %s must be finite and in [0.05, 0.95]", name)
		}
	}
	return nil
}

// AdaptCurrents applies only the sensory side of the adapter. Safety and
// provenance channels are copied unchanged; ControlBlockReason remains the
// authority for whether an observation may produce an action.
func (a PolicyAdapter) AdaptCurrents(currents map[string]float64) (map[string]float64, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	if currents == nil {
		return nil, errors.New("sensory currents are nil")
	}
	adapted := make(map[string]float64, len(currents))
	for name, value := range currents {
		if !finite(value) {
			return nil, fmt.Errorf("sensory current %q is non-finite", name)
		}
		gain := a.sensoryGain(name)
		adapted[name] = clamp(value*gain, 0, 1)
	}
	return adapted, nil
}

func (a PolicyAdapter) sensoryGain(name string) float64 {
	// Missingness is evidence about observation validity, never a trainable
	// game signal. In particular, zero gains must not erase absent entities.
	if strings.HasSuffix(name, "_missing") {
		return 1
	}
	switch {
	case strings.HasPrefix(name, "target_") && (strings.HasSuffix(name, "_near") || strings.HasSuffix(name, "_far")):
		return a.TargetRangeGain
	case strings.HasPrefix(name, "target_") && name != "target_is_disc" && name != "target_is_goal" && name != "target_missing":
		return a.TargetDirectionGain
	case strings.HasPrefix(name, "opponent_") || name == "evade_left" || name == "evade_right":
		return a.OpponentGain
	case strings.HasPrefix(name, "teammate_"):
		return a.TeammateGain
	case name == "grab_opportunity" || name == "throw_opportunity" ||
		name == "boost_opportunity" || name == "brake_opportunity" || name == "disc_approaching":
		return a.OpportunityGain
	case name == "has_disc" || name == "disc_free" || name == "target_is_disc" ||
		name == "target_is_goal" || name == "shield":
		return a.PossessionContextGain
	default:
		return 1
	}
}

// DecodeWithAdapter applies bounded readout gains and thresholds to a frozen
// network. Its default adapter is behaviorally equivalent to Decode.
func DecodeWithAdapter(observation *Observation, network *Network, adapter PolicyAdapter) (ActionIntent, error) {
	if err := adapter.Validate(); err != nil {
		return NeutralIntent("invalid_policy_adapter"), err
	}
	if observation == nil || network == nil {
		return NeutralIntent("missing_observation_or_network"), nil
	}
	if !network.agentCompatible() {
		return NeutralIntent("incompatible_topology"), nil
	}
	if reason := ControlBlockReason(observation); reason != "" {
		return NeutralIntent(reason), nil
	}

	paired := func(positive, negative string, gain float64) float64 {
		value := (network.Output(positive) - network.Output(negative)) * gain
		if math.Abs(value) < 0.04 {
			return 0
		}
		return clamp(value, -1, 1)
	}
	intent := ActionIntent{Schema: ActionSchema}
	intent.Translation[0] = paired(outputMoveLeft, outputMoveRight, adapter.TranslationGain)
	intent.Translation[1] = paired(outputMoveUp, outputMoveDown, adapter.TranslationGain)
	intent.Translation[2] = paired(outputMoveForward, outputMoveBackward, adapter.TranslationGain)
	intent.Yaw = paired(outputYawLeft, outputYawRight, adapter.YawGain)
	intent.LeftGrip = network.Output(outputGripLeft) >= adapter.GripThreshold && !observation.HasDisc
	intent.RightGrip = network.Output(outputGripRight) >= adapter.GripThreshold && !observation.HasDisc
	intent.Release = network.Output(outputRelease) >= adapter.ReleaseThreshold && observation.HasDisc
	intent.Boost = network.Output(outputBoost) >= adapter.BoostThreshold
	intent.Brake = network.Output(outputBrake) >= adapter.BrakeThreshold

	confidence := math.Abs(intent.Translation[0])
	for _, value := range []float64{
		math.Abs(intent.Translation[1]), math.Abs(intent.Translation[2]), math.Abs(intent.Yaw),
		network.Output(outputGripLeft), network.Output(outputGripRight), network.Output(outputRelease),
		network.Output(outputBoost), network.Output(outputBrake),
	} {
		confidence = math.Max(confidence, value)
	}
	intent.Confidence = clamp(confidence, 0, 1)
	return intent, nil
}
