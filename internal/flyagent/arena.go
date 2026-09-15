package flyagent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	ArenaCheckpointSchema = "nevr.fly.arena-checkpoint/v1"
	ArenaSourceKind       = "synthetic_zero_g_arena"
)

// ArenaConfig defines a deliberately small deterministic zero-gravity task.
// It is an engineering environment for closed-loop plumbing and comparative
// rollouts, not an Echo VR mechanics emulator.
type ArenaConfig struct {
	StepSeconds        float64    `json:"step_seconds"`
	MaxSteps           int        `json:"max_steps"`
	HalfExtents        model.Vec3 `json:"half_extents"`
	PlayerAcceleration float64    `json:"player_acceleration"`
	BoostMultiplier    float64    `json:"boost_multiplier"`
	PlayerDrag         float64    `json:"player_drag"`
	BrakeDrag          float64    `json:"brake_drag"`
	MaxPlayerSpeed     float64    `json:"max_player_speed"`
	YawRate            float64    `json:"yaw_rate"`
	DiscDrag           float64    `json:"disc_drag"`
	MaxDiscSpeed       float64    `json:"max_disc_speed"`
	WallRestitution    float64    `json:"wall_restitution"`
	GrabRadius         float64    `json:"grab_radius"`
	HoldOffset         float64    `json:"hold_offset"`
	ThrowSpeed         float64    `json:"throw_speed"`
	GoalHalfWidth      float64    `json:"goal_half_width"`
	GoalHalfHeight     float64    `json:"goal_half_height"`
	ProgressScale      float64    `json:"progress_scale"`
	ControlCost        float64    `json:"control_cost"`
	PickupBonus        float64    `json:"pickup_bonus"`
	GoalBonus          float64    `json:"goal_bonus"`
	ConcedePenalty     float64    `json:"concede_penalty"`
}

// DefaultArenaConfig is intentionally modest enough for fast, repeatable test
// and development rollouts.
func DefaultArenaConfig() ArenaConfig {
	return ArenaConfig{
		StepSeconds:        NominalReplayStepSeconds,
		MaxSteps:           300,
		HalfExtents:        model.Vec3{12, 8, 36},
		PlayerAcceleration: 12,
		BoostMultiplier:    1.75,
		PlayerDrag:         0.15,
		BrakeDrag:          4,
		MaxPlayerSpeed:     12,
		YawRate:            math.Pi,
		DiscDrag:           0.02,
		MaxDiscSpeed:       25,
		WallRestitution:    0.85,
		GrabRadius:         1.2,
		HoldOffset:         0.8,
		ThrowSpeed:         14,
		GoalHalfWidth:      2.8,
		GoalHalfHeight:     2.2,
		ProgressScale:      0.1,
		ControlCost:        0.002,
		PickupBonus:        1,
		GoalBonus:          10,
		ConcedePenalty:     5,
	}
}

func (c ArenaConfig) validate() error {
	if !finite(c.StepSeconds) || c.StepSeconds <= 0 || c.StepSeconds > 0.25 {
		return errors.New("arena step_seconds must be finite and in (0, 0.25]")
	}
	if c.MaxSteps < 1 || c.MaxSteps > 10_000 {
		return errors.New("arena max_steps must be in 1..10000")
	}
	for i, extent := range c.HalfExtents {
		if !finite(extent) || extent < 2 || extent > 1_000 {
			return fmt.Errorf("arena half extent %d must be finite and in [2, 1000]", i)
		}
	}
	positive := []struct {
		name  string
		value float64
	}{
		{"player_acceleration", c.PlayerAcceleration},
		{"boost_multiplier", c.BoostMultiplier},
		{"max_player_speed", c.MaxPlayerSpeed},
		{"yaw_rate", c.YawRate},
		{"max_disc_speed", c.MaxDiscSpeed},
		{"grab_radius", c.GrabRadius},
		{"hold_offset", c.HoldOffset},
		{"throw_speed", c.ThrowSpeed},
		{"goal_half_width", c.GoalHalfWidth},
		{"goal_half_height", c.GoalHalfHeight},
	}
	for _, field := range positive {
		if !finite(field.value) || field.value <= 0 || field.value > 1_000 {
			return fmt.Errorf("arena %s must be finite and in (0, 1000]", field.name)
		}
	}
	nonnegative := []struct {
		name  string
		value float64
	}{
		{"player_drag", c.PlayerDrag},
		{"brake_drag", c.BrakeDrag},
		{"disc_drag", c.DiscDrag},
		{"progress_scale", c.ProgressScale},
		{"control_cost", c.ControlCost},
		{"pickup_bonus", c.PickupBonus},
		{"goal_bonus", c.GoalBonus},
		{"concede_penalty", c.ConcedePenalty},
	}
	for _, field := range nonnegative {
		if !finite(field.value) || field.value < 0 || field.value > 1_000 {
			return fmt.Errorf("arena %s must be finite and in [0, 1000]", field.name)
		}
	}
	if !finite(c.WallRestitution) || c.WallRestitution < 0 || c.WallRestitution > 1 {
		return errors.New("arena wall_restitution must be in [0, 1]")
	}
	if c.BoostMultiplier < 1 {
		return errors.New("arena boost_multiplier must be at least 1")
	}
	if c.GoalHalfWidth >= c.HalfExtents[0] || c.GoalHalfHeight >= c.HalfExtents[1] {
		return errors.New("arena goal opening must fit strictly inside the x/y walls")
	}
	if c.GrabRadius >= min(c.HalfExtents[0], c.HalfExtents[1], c.HalfExtents[2]) || c.HoldOffset >= c.HalfExtents[2] {
		return errors.New("arena grab radius and hold offset must fit inside the arena")
	}
	minExtent := min(c.HalfExtents[0], c.HalfExtents[1], c.HalfExtents[2])
	if c.MaxPlayerSpeed*c.StepSeconds > 4*minExtent || c.MaxDiscSpeed*c.StepSeconds > 4*minExtent {
		return errors.New("arena speed and step permit too many wall crossings per tick")
	}
	return nil
}

func arenaConfigDigest(config ArenaConfig) (string, error) {
	blob, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(blob)
	return fmt.Sprintf("%x", digest), nil
}

// ArenaCheckpoint is a complete, JSON-serializable environment snapshot. RNG
// state is retained even though the default task currently randomizes only at
// reset, so later seeded respawn rules cannot silently break resumability.
type ArenaCheckpoint struct {
	Schema            string     `json:"schema"`
	ConfigDigest      string     `json:"config_digest"`
	Seed              uint64     `json:"seed"`
	RNGState          uint64     `json:"rng_state"`
	Step              int        `json:"step"`
	PlayerPosition    model.Vec3 `json:"player_position"`
	PlayerVelocity    model.Vec3 `json:"player_velocity"`
	PlayerYaw         float64    `json:"player_yaw"`
	DiscPosition      model.Vec3 `json:"disc_position"`
	DiscVelocity      model.Vec3 `json:"disc_velocity"`
	HasDisc           bool       `json:"has_disc"`
	Done              bool       `json:"done"`
	TerminationReason string     `json:"termination_reason,omitempty"`
	GoalsFor          int        `json:"goals_for"`
	GoalsAgainst      int        `json:"goals_against"`
}

// ArenaTransition describes synthetic task events and a shaped objective. The
// objective is useful for optimization experiments but is not policy accuracy.
type ArenaTransition struct {
	Step                 int     `json:"step"`
	SyntheticObjective   float64 `json:"synthetic_objective"`
	DistanceToDisc       float64 `json:"distance_to_disc"`
	DistanceToAttackGoal float64 `json:"distance_to_attack_goal"`
	HasDisc              bool    `json:"has_disc"`
	AcquiredDisc         bool    `json:"acquired_disc"`
	ReleasedDisc         bool    `json:"released_disc"`
	ScoredFor            bool    `json:"scored_for"`
	ScoredAgainst        bool    `json:"scored_against"`
	Done                 bool    `json:"done"`
	TerminationReason    string  `json:"termination_reason,omitempty"`
}

// Arena is a deterministic, bounded zero-gravity environment. It contains no
// live game integration and does not model human players or network effects.
type Arena struct {
	config       ArenaConfig
	configDigest string
	initialized  bool
	seed         uint64
	rng          arenaRNG
	step         int
	playerPos    model.Vec3
	playerVel    model.Vec3
	playerYaw    float64
	discPos      model.Vec3
	discVel      model.Vec3
	hasDisc      bool
	done         bool
	termination  string
	goalsFor     int
	goalsAgainst int
}

// NewArena validates and constructs an uninitialized arena. Call Reset before
// asking for an observation or taking a step.
func NewArena(config ArenaConfig) (*Arena, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	digest, err := arenaConfigDigest(config)
	if err != nil {
		return nil, fmt.Errorf("digest arena config: %w", err)
	}
	return &Arena{config: config, configDigest: digest}, nil
}

// Reset begins an episode whose complete initial state is determined by seed.
func (a *Arena) Reset(seed uint64) (*Observation, error) {
	if a == nil {
		return nil, errors.New("arena is nil")
	}
	a.initialized = true
	a.seed = seed
	a.rng = arenaRNG{state: seed ^ 0x6a09e667f3bcc909}
	a.step = 0
	a.done = false
	a.termination = ""
	a.goalsFor = 0
	a.goalsAgainst = 0
	a.hasDisc = false
	a.playerPos = model.Vec3{
		a.rng.signed(min(2.5, a.config.HalfExtents[0]*0.6)),
		a.rng.signed(min(1.5, a.config.HalfExtents[1]*0.6)),
		-a.config.HalfExtents[2]*0.45 + a.rng.signed(min(1.5, a.config.HalfExtents[2]*0.25)),
	}
	a.playerVel = model.Vec3{}
	a.playerYaw = a.rng.signed(0.3)
	a.discPos = model.Vec3{
		a.rng.signed(min(5, a.config.HalfExtents[0]*0.55)),
		a.rng.signed(min(3, a.config.HalfExtents[1]*0.55)),
		a.rng.signed(min(4, a.config.HalfExtents[2]*0.2)),
	}
	a.discVel = model.Vec3{a.rng.signed(0.35), a.rng.signed(0.35), a.rng.signed(0.35)}
	return a.Observation()
}

// Observation returns a fresh semantic observation of the current state.
func (a *Arena) Observation() (*Observation, error) {
	if a == nil || !a.initialized {
		return nil, errors.New("arena has not been reset")
	}
	if !a.finiteState() {
		return nil, errors.New("arena state is non-finite")
	}
	rotation := yawRotation(a.playerYaw)
	toLocal := func(world model.Vec3) model.Vec3 { return rotation.Conjugate().Rotate(world) }
	entity := func(position, velocity model.Vec3, velocityKnown bool) RelativeEntity {
		relative := position.Sub(a.playerPos)
		relativeVelocity := model.Vec3{}
		if velocityKnown {
			relativeVelocity = velocity.Sub(a.playerVel)
		}
		return RelativeEntity{
			Available: true, VelocityKnown: velocityKnown,
			Relative: toLocal(relative), Velocity: toLocal(relativeVelocity), Distance: relative.Magnitude(),
		}
	}
	disc := entity(a.discPos, a.discVel, true)
	goalPosition := model.Vec3{0, 0, a.config.HalfExtents[2]}
	goal := entity(goalPosition, model.Vec3{}, true)
	target, targetKind := disc, "disc"
	if a.hasDisc {
		target, targetKind = goal, "goal"
	}
	currents := make(map[string]float64, 48)
	encodeDirections(currents, "target", target, true)
	encodeDirections(currents, "opponent", RelativeEntity{}, true)
	encodeDirections(currents, "teammate", RelativeEntity{}, true)
	currents["game_active"] = boolCurrent(!a.done)
	currents["phase_known"] = 1
	currents["orientation_known"] = 1
	currents["orientation_missing"] = 0
	currents["self_velocity_known"] = 1
	currents["self_velocity_missing"] = 0
	currents["team_known"] = 1
	currents["possession_known"] = 1
	currents["possession_conflict"] = 0
	currents["has_disc"] = boolCurrent(a.hasDisc)
	currents["disc_free"] = boolCurrent(!a.hasDisc)
	currents["stunned"] = 0
	currents["shield"] = 0
	currents["target_is_disc"] = boolCurrent(targetKind == "disc")
	currents["target_is_goal"] = boolCurrent(targetKind == "goal")
	currents["disc_missing"] = 0
	currents["opponent_missing"] = 1
	currents["teammate_missing"] = 1
	currents["disc_approaching"] = approachingCurrent(disc)
	currents["opponent_closing"] = 0
	currents["grab_opportunity"] = grabOpportunity(disc, !a.hasDisc, false)
	currents["throw_opportunity"] = throwOpportunity(a.hasDisc, false, goal)
	currents["boost_opportunity"] = currents["target_far"] * currents["target_ahead"]
	currents["brake_opportunity"] = 0
	currents["evade_left"] = 0
	currents["evade_right"] = 0
	phase := "playing"
	if a.done {
		phase = "complete"
	}
	matchID := fmt.Sprintf("synthetic-zero-g-%016x", a.seed)
	return &Observation{
		Schema: ObservationSchema, Sequence: uint64(a.step), MatchID: matchID, // #nosec G115 -- arena step is maintained in 0..validated MaxSteps.
		FrameIndex: a.step, Timestamp: float64(a.step) * a.config.StepSeconds, DeltaTime: a.config.StepSeconds,
		SourceKind: ArenaSourceKind, SourceID: matchID, SourceAuthority: "simulated",
		SourceTimeBasis: "fixed_step", SourceEpoch: a.seed, SourceEpochKnown: true,
		PlayerID: "synthetic-agent", Team: "blue", TeamKnown: true,
		GamePhase: phase, PhaseKnown: true, GameActive: !a.done,
		OrientationKnown: true, SelfVelocityKnown: true, PossessionKnown: true,
		AttackGoal: GoalPositiveZ, SelfPosition: a.playerPos, SelfVelocity: a.playerVel,
		HasDisc: a.hasDisc, TargetKind: targetKind, Target: target, Disc: disc,
		Opponent: RelativeEntity{}, Teammate: RelativeEntity{}, Currents: currents,
	}, nil
}

// Step applies one abstract body-relative action and returns the resulting
// observation. No gravity term is present in either body integration.
func (a *Arena) Step(action ActionIntent) (*Observation, ArenaTransition, error) {
	if a == nil || !a.initialized {
		return nil, ArenaTransition{}, errors.New("arena has not been reset")
	}
	if a.done {
		return nil, ArenaTransition{}, errors.New("arena episode is complete")
	}
	if err := validateArenaAction(action); err != nil {
		return nil, ArenaTransition{}, err
	}
	previousTargetDistance := a.discPos.Sub(a.playerPos).Magnitude()
	previouslyHeld := a.hasDisc
	if previouslyHeld {
		previousTargetDistance = model.Vec3{0, 0, a.config.HalfExtents[2]}.Sub(a.playerPos).Magnitude()
	}

	dt := a.config.StepSeconds
	a.playerYaw = normalizeAngle(a.playerYaw + action.Yaw*a.config.YawRate*dt)
	localInput := model.Vec3(action.Translation)
	if magnitude := localInput.Magnitude(); magnitude > 1 {
		localInput = localInput.Scale(1 / magnitude)
	}
	acceleration := yawRotation(a.playerYaw).Rotate(localInput).Scale(a.config.PlayerAcceleration)
	if action.Boost {
		acceleration = acceleration.Scale(a.config.BoostMultiplier)
	}
	a.playerVel = a.playerVel.Add(acceleration.Scale(dt))
	drag := a.config.PlayerDrag
	if action.Brake {
		drag += a.config.BrakeDrag
	}
	a.playerVel = a.playerVel.Scale(math.Exp(-drag * dt))
	a.playerVel = limitVector(a.playerVel, a.config.MaxPlayerSpeed)
	a.playerPos = a.playerPos.Add(a.playerVel.Scale(dt))
	bounceBox(&a.playerPos, &a.playerVel, a.config.HalfExtents, a.config.WallRestitution)

	released := false
	if a.hasDisc && action.Release {
		a.hasDisc = false
		released = true
		forward := yawRotation(a.playerYaw).Rotate(model.Vec3{0, 0, 1})
		a.discPos = a.playerPos.Add(forward.Scale(a.config.HoldOffset))
		clampBoxPosition(&a.discPos, a.config.HalfExtents)
		a.discVel = limitVector(a.playerVel.Add(forward.Scale(a.config.ThrowSpeed)), a.config.MaxDiscSpeed)
	}
	if a.hasDisc {
		forward := yawRotation(a.playerYaw).Rotate(model.Vec3{0, 0, 1})
		a.discPos = a.playerPos.Add(forward.Scale(a.config.HoldOffset))
		clampBoxPosition(&a.discPos, a.config.HalfExtents)
		a.discVel = a.playerVel
	} else {
		previousDisc := a.discPos
		a.discVel = limitVector(a.discVel.Scale(math.Exp(-a.config.DiscDrag*dt)), a.config.MaxDiscSpeed)
		a.discPos = a.discPos.Add(a.discVel.Scale(dt))
		scoredFor, scoredAgainst := a.goalCrossing(previousDisc, a.discPos)
		if scoredFor || scoredAgainst {
			a.done = true
			if scoredFor {
				a.goalsFor++
				a.termination = "attack_goal"
				a.discPos[2] = a.config.HalfExtents[2]
			} else {
				a.goalsAgainst++
				a.termination = "defended_goal"
				a.discPos[2] = -a.config.HalfExtents[2]
			}
			clampBoxPosition(&a.discPos, a.config.HalfExtents)
		} else {
			bounceBox(&a.discPos, &a.discVel, a.config.HalfExtents, a.config.WallRestitution)
		}
	}

	acquired := false
	if !a.done && !a.hasDisc && !released && (action.LeftGrip || action.RightGrip) && a.discPos.Sub(a.playerPos).Magnitude() <= a.config.GrabRadius {
		a.hasDisc = true
		acquired = true
		forward := yawRotation(a.playerYaw).Rotate(model.Vec3{0, 0, 1})
		a.discPos = a.playerPos.Add(forward.Scale(a.config.HoldOffset))
		clampBoxPosition(&a.discPos, a.config.HalfExtents)
		a.discVel = a.playerVel
	}

	a.step++
	if !a.done && a.step >= a.config.MaxSteps {
		a.done = true
		a.termination = "step_limit"
	}
	currentTargetDistance := a.discPos.Sub(a.playerPos).Magnitude()
	if previouslyHeld {
		currentTargetDistance = model.Vec3{0, 0, a.config.HalfExtents[2]}.Sub(a.playerPos).Magnitude()
	}
	progress := clamp(previousTargetDistance-currentTargetDistance, -2, 2)
	effort := math.Abs(action.Translation[0]) + math.Abs(action.Translation[1]) + math.Abs(action.Translation[2]) + math.Abs(action.Yaw)
	objective := progress*a.config.ProgressScale - effort*a.config.ControlCost
	if acquired {
		objective += a.config.PickupBonus
	}
	if a.termination == "attack_goal" {
		objective += a.config.GoalBonus
	}
	if a.termination == "defended_goal" {
		objective -= a.config.ConcedePenalty
	}
	if !a.finiteState() || !finite(objective) {
		return nil, ArenaTransition{}, errors.New("arena integration produced non-finite state")
	}
	observation, err := a.Observation()
	if err != nil {
		return nil, ArenaTransition{}, err
	}
	transition := ArenaTransition{
		Step: a.step, SyntheticObjective: objective,
		DistanceToDisc:       a.discPos.Sub(a.playerPos).Magnitude(),
		DistanceToAttackGoal: model.Vec3{0, 0, a.config.HalfExtents[2]}.Sub(a.playerPos).Magnitude(),
		HasDisc:              a.hasDisc, AcquiredDisc: acquired, ReleasedDisc: released,
		ScoredFor: a.termination == "attack_goal", ScoredAgainst: a.termination == "defended_goal",
		Done: a.done, TerminationReason: a.termination,
	}
	return observation, transition, nil
}

// Checkpoint snapshots the initialized arena. An uninitialized arena produces
// a schema-tagged but unrestorable zero snapshot.
func (a *Arena) Checkpoint() ArenaCheckpoint {
	checkpoint := ArenaCheckpoint{Schema: ArenaCheckpointSchema}
	if a == nil || !a.initialized {
		return checkpoint
	}
	checkpoint.ConfigDigest = a.configDigest
	checkpoint.Seed = a.seed
	checkpoint.RNGState = a.rng.state
	checkpoint.Step = a.step
	checkpoint.PlayerPosition = a.playerPos
	checkpoint.PlayerVelocity = a.playerVel
	checkpoint.PlayerYaw = a.playerYaw
	checkpoint.DiscPosition = a.discPos
	checkpoint.DiscVelocity = a.discVel
	checkpoint.HasDisc = a.hasDisc
	checkpoint.Done = a.done
	checkpoint.TerminationReason = a.termination
	checkpoint.GoalsFor = a.goalsFor
	checkpoint.GoalsAgainst = a.goalsAgainst
	return checkpoint
}

// Restore replaces arena state only when the snapshot matches this arena's
// immutable configuration and passes physical bounds checks.
func (a *Arena) Restore(checkpoint ArenaCheckpoint) error {
	if a == nil {
		return errors.New("arena is nil")
	}
	if checkpoint.Schema != ArenaCheckpointSchema {
		return fmt.Errorf("arena checkpoint schema %q is unsupported", checkpoint.Schema)
	}
	if checkpoint.ConfigDigest != a.configDigest {
		return errors.New("arena checkpoint configuration does not match")
	}
	if checkpoint.Step < 0 || checkpoint.Step > a.config.MaxSteps || checkpoint.GoalsFor < 0 || checkpoint.GoalsAgainst < 0 {
		return errors.New("arena checkpoint counters are invalid")
	}
	if !finiteVec(checkpoint.PlayerPosition) || !finiteVec(checkpoint.PlayerVelocity) || !finite(checkpoint.PlayerYaw) ||
		!finiteVec(checkpoint.DiscPosition) || !finiteVec(checkpoint.DiscVelocity) {
		return errors.New("arena checkpoint contains non-finite state")
	}
	if checkpoint.PlayerYaw < -math.Pi || checkpoint.PlayerYaw > math.Pi {
		return errors.New("arena checkpoint yaw is outside [-pi, pi]")
	}
	if !insideBox(checkpoint.PlayerPosition, a.config.HalfExtents) || !insideBox(checkpoint.DiscPosition, a.config.HalfExtents) {
		return errors.New("arena checkpoint position is outside arena bounds")
	}
	if checkpoint.PlayerVelocity.Magnitude() > a.config.MaxPlayerSpeed+1e-9 || checkpoint.DiscVelocity.Magnitude() > a.config.MaxDiscSpeed+1e-9 {
		return errors.New("arena checkpoint velocity exceeds configured limits")
	}
	if checkpoint.Done != (checkpoint.TerminationReason != "") {
		return errors.New("arena checkpoint termination state is inconsistent")
	}
	if checkpoint.TerminationReason != "" && checkpoint.TerminationReason != "attack_goal" && checkpoint.TerminationReason != "defended_goal" && checkpoint.TerminationReason != "step_limit" {
		return errors.New("arena checkpoint termination reason is invalid")
	}
	if checkpoint.HasDisc && checkpoint.DiscPosition.Sub(checkpoint.PlayerPosition).Magnitude() > a.config.HoldOffset+1e-9 {
		return errors.New("arena checkpoint held disc is detached from player")
	}
	if checkpoint.HasDisc && checkpoint.DiscVelocity != checkpoint.PlayerVelocity {
		return errors.New("arena checkpoint held disc velocity differs from player")
	}
	if (!checkpoint.Done && checkpoint.Step >= a.config.MaxSteps) ||
		(checkpoint.TerminationReason == "attack_goal" && (checkpoint.GoalsFor != 1 || checkpoint.GoalsAgainst != 0)) ||
		(checkpoint.TerminationReason == "defended_goal" && (checkpoint.GoalsAgainst != 1 || checkpoint.GoalsFor != 0)) ||
		(checkpoint.TerminationReason == "step_limit" && (checkpoint.GoalsFor != 0 || checkpoint.GoalsAgainst != 0)) ||
		(checkpoint.TerminationReason == "" && (checkpoint.GoalsFor != 0 || checkpoint.GoalsAgainst != 0)) {
		return errors.New("arena checkpoint outcome is inconsistent")
	}
	a.initialized = true
	a.seed = checkpoint.Seed
	a.rng.state = checkpoint.RNGState
	a.step = checkpoint.Step
	a.playerPos = checkpoint.PlayerPosition
	a.playerVel = checkpoint.PlayerVelocity
	a.playerYaw = checkpoint.PlayerYaw
	a.discPos = checkpoint.DiscPosition
	a.discVel = checkpoint.DiscVelocity
	a.hasDisc = checkpoint.HasDisc
	a.done = checkpoint.Done
	a.termination = checkpoint.TerminationReason
	a.goalsFor = checkpoint.GoalsFor
	a.goalsAgainst = checkpoint.GoalsAgainst
	return nil
}

func (a *Arena) finiteState() bool {
	return finiteVec(a.playerPos) && finiteVec(a.playerVel) && finite(a.playerYaw) && finiteVec(a.discPos) && finiteVec(a.discVel)
}

func (a *Arena) goalCrossing(previous, current model.Vec3) (forGoal, againstGoal bool) {
	inOpeningAtPlane := func(plane float64) bool {
		deltaZ := current[2] - previous[2]
		if deltaZ == 0 {
			return false
		}
		fraction := (plane - previous[2]) / deltaZ
		at := previous.Add(current.Sub(previous).Scale(fraction))
		return math.Abs(at[0]) <= a.config.GoalHalfWidth && math.Abs(at[1]) <= a.config.GoalHalfHeight
	}
	forGoal = previous[2] < a.config.HalfExtents[2] && current[2] >= a.config.HalfExtents[2] && inOpeningAtPlane(a.config.HalfExtents[2])
	againstGoal = previous[2] > -a.config.HalfExtents[2] && current[2] <= -a.config.HalfExtents[2] && inOpeningAtPlane(-a.config.HalfExtents[2])
	return forGoal, againstGoal
}

func validateArenaAction(action ActionIntent) error {
	if action.Schema != ActionSchema {
		return fmt.Errorf("arena action schema %q is unsupported", action.Schema)
	}
	values := []float64{action.Translation[0], action.Translation[1], action.Translation[2], action.Yaw, action.Confidence}
	for _, value := range values {
		if !finite(value) {
			return errors.New("arena action contains a non-finite value")
		}
	}
	for _, value := range action.Translation {
		if value < -1 || value > 1 {
			return errors.New("arena translation action must be in [-1, 1]")
		}
	}
	if action.Yaw < -1 || action.Yaw > 1 || action.Confidence < 0 || action.Confidence > 1 {
		return errors.New("arena yaw and confidence must be bounded")
	}
	if action.NeutralReason != "" && (action.Translation != [3]float64{} || action.Yaw != 0 || action.LeftGrip || action.RightGrip || action.Release || action.Boost || action.Brake || action.Confidence != 0) {
		return errors.New("arena neutral action contains active controls")
	}
	return nil
}

func yawRotation(yaw float64) model.Quat {
	half := yaw / 2
	return model.Quat{0, math.Sin(half), 0, math.Cos(half)}
}

func normalizeAngle(angle float64) float64 {
	angle = math.Mod(angle+math.Pi, 2*math.Pi)
	if angle < 0 {
		angle += 2 * math.Pi
	}
	return angle - math.Pi
}

func limitVector(value model.Vec3, maximum float64) model.Vec3 {
	magnitude := value.Magnitude()
	if magnitude > maximum && magnitude > 0 {
		return value.Scale(maximum / magnitude)
	}
	return value
}

func bounceBox(position, velocity *model.Vec3, half model.Vec3, restitution float64) {
	for axis := 0; axis < 3; axis++ {
		for crossings := 0; crossings < 8 && (position[axis] < -half[axis] || position[axis] > half[axis]); crossings++ {
			if position[axis] > half[axis] {
				position[axis] = 2*half[axis] - position[axis]
				velocity[axis] = -math.Abs(velocity[axis]) * restitution
			} else {
				position[axis] = -2*half[axis] - position[axis]
				velocity[axis] = math.Abs(velocity[axis]) * restitution
			}
		}
		position[axis] = clamp(position[axis], -half[axis], half[axis])
	}
}

func insideBox(position, half model.Vec3) bool {
	for axis := 0; axis < 3; axis++ {
		if position[axis] < -half[axis]-1e-9 || position[axis] > half[axis]+1e-9 {
			return false
		}
	}
	return true
}

func clampBoxPosition(position *model.Vec3, half model.Vec3) {
	for axis := 0; axis < 3; axis++ {
		position[axis] = clamp(position[axis], -half[axis], half[axis])
	}
}

// arenaRNG is SplitMix64: small, deterministic, and completely checkpointable.
// It is not used for security or claims about real match distributions.
type arenaRNG struct{ state uint64 }

func (r *arenaRNG) next() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (r *arenaRNG) unit() float64 {
	return float64(r.next()>>11) * (1.0 / (1 << 53))
}

func (r *arenaRNG) signed(maximum float64) float64 {
	return (2*r.unit() - 1) * maximum
}
