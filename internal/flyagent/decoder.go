package flyagent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
)

const (
	ActionSchema      = "nevr.fly.action/v1"
	ActionTraceSchema = "nevr.fly.action-trace/v1"
)

// ActionIntent is an abstract, body-relative command. Translation uses Echo's
// local axes (+X left, +Y up, +Z forward). It is not a VR controller pose and
// no code in this package sends it to the game.
type ActionIntent struct {
	Schema        string     `json:"schema"`
	Translation   [3]float64 `json:"translation"`
	Yaw           float64    `json:"yaw"`
	LeftGrip      bool       `json:"left_grip"`
	RightGrip     bool       `json:"right_grip"`
	Release       bool       `json:"release"`
	Boost         bool       `json:"boost"`
	Brake         bool       `json:"brake"`
	Confidence    float64    `json:"confidence"`
	NeutralReason string     `json:"neutral_reason,omitempty"`
}

// ActionTraceRecord binds an intent to source provenance and a deterministic
// network-state fingerprint.
type ActionTraceRecord struct {
	Schema              string          `json:"schema"`
	Mode                ExecutionMode   `json:"mode"`
	ObservationSchema   string          `json:"observation_schema"`
	RateModelSchema     string          `json:"rate_model_schema"`
	DecoderPolicySchema string          `json:"decoder_policy_schema"`
	Sequence            uint64          `json:"sequence"`
	MatchID             string          `json:"match_id"`
	FrameIndex          int             `json:"frame_index"`
	Timestamp           float64         `json:"timestamp"`
	DeltaTime           float64         `json:"delta_time"`
	AppliedDeltaTime    float64         `json:"applied_delta_time"`
	SourceKind          string          `json:"source_kind"`
	SourceID            string          `json:"source_id,omitempty"`
	SourceAuthority     string          `json:"source_authority"`
	SourceTimeBasis     string          `json:"source_time_basis"`
	SourceEpoch         uint64          `json:"source_epoch,omitempty"`
	SourceEpochKnown    bool            `json:"source_epoch_known"`
	InputDigest         string          `json:"input_digest"`
	PlayerID            string          `json:"player_id"`
	AttackGoal          AttackGoal      `json:"attack_goal"`
	TargetKind          string          `json:"target_kind"`
	Target              RelativeEntity  `json:"target"`
	Action              ActionIntent    `json:"action"`
	ObservationDigest   string          `json:"observation_digest"`
	StateDigest         string          `json:"state_digest"`
	TopologyDigest      string          `json:"topology_digest"`
	ModelDigest         string          `json:"model_digest"`
	Dynamics            Dynamics        `json:"dynamics"`
	StateResetReason    string          `json:"state_reset_reason,omitempty"`
	Dataset             DatasetMetadata `json:"dataset"`
}

// Decode converts frozen population activity into bounded action intent.
func Decode(observation *Observation, network *Network) ActionIntent {
	if observation == nil || network == nil {
		return NeutralIntent("missing_observation_or_network")
	}
	if !network.agentCompatible() {
		return NeutralIntent("incompatible_topology")
	}
	if reason := ControlBlockReason(observation); reason != "" {
		return NeutralIntent(reason)
	}

	paired := func(positive, negative string) float64 {
		value := network.Output(positive) - network.Output(negative)
		if math.Abs(value) < 0.04 {
			return 0
		}
		return clamp(value, -1, 1)
	}
	intent := ActionIntent{Schema: ActionSchema}
	intent.Translation[0] = paired(outputMoveLeft, outputMoveRight)
	intent.Translation[1] = paired(outputMoveUp, outputMoveDown)
	intent.Translation[2] = paired(outputMoveForward, outputMoveBackward)
	intent.Yaw = paired(outputYawLeft, outputYawRight)
	intent.LeftGrip = network.Output(outputGripLeft) >= 0.55 && !observation.HasDisc
	intent.RightGrip = network.Output(outputGripRight) >= 0.55 && !observation.HasDisc
	intent.Release = network.Output(outputRelease) >= 0.55 && observation.HasDisc
	intent.Boost = network.Output(outputBoost) >= 0.55
	intent.Brake = network.Output(outputBrake) >= 0.55

	confidence := math.Abs(intent.Translation[0])
	for _, value := range []float64{math.Abs(intent.Translation[1]), math.Abs(intent.Translation[2]), math.Abs(intent.Yaw), network.Output(outputGripLeft), network.Output(outputGripRight), network.Output(outputRelease), network.Output(outputBoost), network.Output(outputBrake)} {
		confidence = math.Max(confidence, value)
	}
	intent.Confidence = clamp(confidence, 0, 1)
	return intent
}

func NeutralIntent(reason string) ActionIntent {
	return ActionIntent{Schema: ActionSchema, NeutralReason: reason}
}

// ControlBlockReason is the shared fail-closed gate for replay traces and any
// future interactive runner. Empty means the observation is safe to decode.
func ControlBlockReason(observation *Observation) string {
	if observation == nil {
		return "missing_observation"
	}
	if observation.Schema != ObservationSchema {
		return "unsupported_observation_schema"
	}
	if !observation.SourceEpochKnown {
		return "source_provenance_unavailable"
	}
	if !observation.PhaseKnown {
		return "phase_unavailable"
	}
	if !observation.GameActive {
		return "inactive_game_phase"
	}
	if !observation.OrientationKnown {
		return "orientation_unavailable"
	}
	if !observation.SelfVelocityKnown {
		return "self_velocity_unavailable"
	}
	if !observation.TeamKnown {
		return "team_unavailable"
	}
	if !observation.PossessionKnown {
		return "disc_possession_unknown"
	}
	if observation.PossessionConflict {
		return "disc_possession_conflict"
	}
	if !observation.Target.Available || !finite(observation.Target.Distance) ||
		(observation.HasDisc && observation.TargetKind != "goal") ||
		(!observation.HasDisc && observation.TargetKind != "disc") {
		return "target_unavailable"
	}
	if observation.Stunned {
		return "player_stunned"
	}
	return ""
}

// DigestObservation fingerprints the complete semantic input to a network
// step. encoding/json sorts string map keys, making Currents deterministic.
func DigestObservation(observation *Observation) (string, error) {
	if observation == nil {
		return "", fmt.Errorf("observation is nil")
	}
	blob, err := json.Marshal(observation)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(blob)
	return fmt.Sprintf("%x", digest), nil
}
