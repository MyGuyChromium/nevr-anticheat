package flyagent

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const ObservationSchema = "nevr.fly.observation/v1"

var ErrPlayerAbsent = errors.New("selected player is absent from replay tick")

// AttackGoal is explicit because Echo telemetry does not authoritatively say
// which end a team is attacking. Guessing would silently swap the policy.
type AttackGoal string

const (
	GoalNegativeZ AttackGoal = "negative-z"
	GoalPositiveZ AttackGoal = "positive-z"
)

// RelativeEntity is an ego-frame observation. Echo's pose basis uses +X left,
// +Y up and +Z forward.
type RelativeEntity struct {
	Available     bool       `json:"available"`
	VelocityKnown bool       `json:"velocity_known"`
	Relative      model.Vec3 `json:"relative"`
	Velocity      model.Vec3 `json:"velocity"`
	Distance      float64    `json:"distance"`
	PlayerID      string     `json:"player_id,omitempty"`
}

// Observation is the stable boundary between Echo telemetry and any frozen
// connectome implementation. Currents are bounded 0..1 semantic stimulation;
// they are engineered inputs, not claims about biological sensory firing.
type Observation struct {
	Schema             string             `json:"schema"`
	Sequence           uint64             `json:"sequence"`
	MatchID            string             `json:"match_id"`
	FrameIndex         int                `json:"frame_index"`
	Timestamp          float64            `json:"timestamp"`
	DeltaTime          float64            `json:"delta_time"`
	SourceKind         string             `json:"source_kind,omitempty"`
	SourceID           string             `json:"source_id,omitempty"`
	SourceAuthority    string             `json:"source_authority,omitempty"`
	SourceTimeBasis    string             `json:"source_time_basis,omitempty"`
	SourceEpoch        uint64             `json:"source_epoch,omitempty"`
	SourceEpochKnown   bool               `json:"source_epoch_known"`
	PlayerID           string             `json:"player_id"`
	Team               string             `json:"team,omitempty"`
	TeamKnown          bool               `json:"team_known"`
	IsPrivate          bool               `json:"is_private"`
	GamePhase          string             `json:"game_phase"`
	PhaseKnown         bool               `json:"phase_known"`
	GameActive         bool               `json:"game_active"`
	OrientationKnown   bool               `json:"orientation_known"`
	SelfVelocityKnown  bool               `json:"self_velocity_known"`
	PossessionKnown    bool               `json:"possession_known"`
	PossessionConflict bool               `json:"possession_conflict"`
	AttackGoal         AttackGoal         `json:"attack_goal"`
	SelfPosition       model.Vec3         `json:"self_position"`
	SelfVelocity       model.Vec3         `json:"self_velocity"`
	HasDisc            bool               `json:"has_disc"`
	Stunned            bool               `json:"stunned"`
	Shield             bool               `json:"shield"`
	TargetKind         string             `json:"target_kind"`
	Target             RelativeEntity     `json:"target"`
	Disc               RelativeEntity     `json:"disc"`
	Opponent           RelativeEntity     `json:"opponent"`
	Teammate           RelativeEntity     `json:"teammate"`
	Currents           map[string]float64 `json:"currents"`
}

// Encoder chooses one player and converts each grouped replay tick into an
// egocentric, fixed-channel observation.
type Encoder struct {
	selector      string
	goal          AttackGoal
	sequence      uint64
	boundPlayerID string
	boundTeam     string
}

func NewEncoder(playerSelector string, goal AttackGoal) (*Encoder, error) {
	if strings.TrimSpace(playerSelector) == "" {
		return nil, errors.New("player selector is required")
	}
	if goal != GoalNegativeZ && goal != GoalPositiveZ {
		return nil, fmt.Errorf("attack goal %q is invalid (use %q or %q)", goal, GoalNegativeZ, GoalPositiveZ)
	}
	return &Encoder{selector: playerSelector, goal: goal}, nil
}

// Reset starts a fresh episode sequence while retaining the resolved player
// identity and team. A display-name selector cannot silently switch players or
// attack direction between matches in one replay file.
func (e *Encoder) Reset() { e.sequence = 0 }

// Encode converts one tick. The selector may be a stable player ID or an exact
// display name; duplicate display names fail closed.
func (e *Encoder) Encode(tick *adapter.ParsedTick) (*Observation, error) {
	if tick == nil || tick.MatchCtx == nil {
		return nil, errors.New("replay tick and match context are required")
	}
	selector := e.selector
	if e.boundPlayerID != "" {
		selector = e.boundPlayerID
	}
	self, err := selectFrame(tick, selector)
	if err != nil {
		return nil, err
	}
	if self.FrameIndex != tick.FrameIndex {
		return nil, fmt.Errorf("selected frame index %d differs from tick %d", self.FrameIndex, tick.FrameIndex)
	}
	if !finiteVec(self.Position) {
		return nil, errors.New("selected player position is non-finite")
	}
	if !finite(self.Timestamp) || !finite(self.DeltaTime) || self.DeltaTime < 0 {
		return nil, errors.New("selected player time is non-finite or negative")
	}

	selfVelocity, selfVelocityKnown := observedVelocity(self)
	rotation, orientationKnown := self.Rotation.NormalizedRotation()
	toLocal := func(world model.Vec3) model.Vec3 {
		if orientationKnown {
			return rotation.Conjugate().Rotate(world)
		}
		return model.Vec3{}
	}
	entity := func(position, velocity model.Vec3, velocityKnown bool, playerID string) RelativeEntity {
		worldRelative := position.Sub(self.Position)
		worldVelocity := model.Vec3{}
		if velocityKnown && selfVelocityKnown {
			worldVelocity = velocity.Sub(selfVelocity)
		}
		distance := worldRelative.Magnitude()
		if !finiteVec(worldRelative) || !finite(distance) ||
			(velocityKnown && selfVelocityKnown && !finiteVec(worldVelocity)) {
			return RelativeEntity{}
		}
		relative := toLocal(worldRelative)
		relativeVelocity := model.Vec3{}
		if velocityKnown && selfVelocityKnown {
			relativeVelocity = toLocal(worldVelocity)
		}
		if !finiteVec(relative) || !finiteVec(relativeVelocity) {
			return RelativeEntity{}
		}
		return RelativeEntity{
			Available: true, VelocityKnown: velocityKnown && selfVelocityKnown,
			Relative: relative, Velocity: relativeVelocity,
			Distance: distance, PlayerID: playerID,
		}
	}

	disc := RelativeEntity{}
	if self.Disc != nil && finiteVec(self.Disc.Position) && finiteVec(self.Disc.Velocity) {
		disc = entity(self.Disc.Position, self.Disc.Velocity, true, "")
	}
	team, teamKnown := playingTeam(self.Team)
	if e.boundTeam != "" && teamKnown && team != e.boundTeam {
		return nil, fmt.Errorf("selected player changed team from %q to %q; attack goal must be supplied for a new run", e.boundTeam, team)
	}
	opponent, teammate := nearestPlayers(tick.Frames, self, team, teamKnown, toLocal, selfVelocity, selfVelocityKnown)

	goalZ := tick.MatchCtx.Physics.GoalZ
	if !finite(goalZ) || goalZ <= 0 {
		goalZ = model.DefaultPhysics().GoalZ
	}
	if e.goal == GoalNegativeZ {
		goalZ = -goalZ
	}
	goal := entity(model.Vec3{0, 0, goalZ}, model.Vec3{}, true, "")
	possessionKnown, hasDisc, discFree, possessionConflict := confirmedPossession(self)
	target, targetKind := RelativeEntity{}, "none"
	if possessionKnown && !possessionConflict {
		if hasDisc {
			target, targetKind = goal, "goal"
		} else if disc.Available {
			target, targetKind = disc, "disc"
		}
	}

	phaseKnown, active := agentPhase(tick, self.GamePhase)
	currents := make(map[string]float64, 48)
	encodeDirections(currents, "target", target, orientationKnown)
	encodeDirections(currents, "opponent", opponent, orientationKnown)
	encodeDirections(currents, "teammate", teammate, orientationKnown)
	currents["game_active"] = boolCurrent(active)
	currents["phase_known"] = boolCurrent(phaseKnown)
	currents["orientation_known"] = boolCurrent(orientationKnown)
	currents["orientation_missing"] = boolCurrent(!orientationKnown)
	currents["self_velocity_known"] = boolCurrent(selfVelocityKnown)
	currents["self_velocity_missing"] = boolCurrent(!selfVelocityKnown)
	currents["team_known"] = boolCurrent(teamKnown)
	currents["possession_known"] = boolCurrent(possessionKnown)
	currents["possession_conflict"] = boolCurrent(possessionConflict)
	currents["has_disc"] = boolCurrent(hasDisc)
	currents["disc_free"] = boolCurrent(disc.Available && discFree && !possessionConflict)
	currents["stunned"] = boolCurrent(self.IsStunned)
	currents["shield"] = boolCurrent(self.ShieldActive)
	currents["target_is_disc"] = boolCurrent(targetKind == "disc")
	currents["target_is_goal"] = boolCurrent(targetKind == "goal")
	currents["disc_missing"] = boolCurrent(!disc.Available)
	currents["opponent_missing"] = boolCurrent(!opponent.Available)
	currents["teammate_missing"] = boolCurrent(!teammate.Available)
	currents["disc_approaching"] = approachingCurrent(disc)
	currents["opponent_closing"] = approachingCurrent(opponent)
	currents["grab_opportunity"] = grabOpportunity(disc, discFree, possessionConflict)
	currents["throw_opportunity"] = throwOpportunity(hasDisc, possessionConflict, goal)
	currents["boost_opportunity"] = currents["target_far"] * currents["target_ahead"]
	currents["brake_opportunity"] = currents["opponent_closing"] * currents["opponent_near"]
	if opponent.Available && opponent.Relative[0] < 0 {
		currents["evade_left"] = currents["brake_opportunity"]
	} else if opponent.Available {
		currents["evade_right"] = currents["brake_opportunity"]
	}

	observation := &Observation{
		Schema: ObservationSchema, Sequence: e.sequence, MatchID: tick.MatchID,
		FrameIndex: tick.FrameIndex, Timestamp: self.Timestamp, DeltaTime: self.DeltaTime,
		PlayerID: self.PlayerID, Team: team, TeamKnown: teamKnown, IsPrivate: tick.MatchCtx.IsPrivate,
		GamePhase: self.GamePhase, PhaseKnown: phaseKnown, GameActive: active,
		OrientationKnown: orientationKnown, SelfVelocityKnown: selfVelocityKnown,
		PossessionKnown:    possessionKnown,
		PossessionConflict: possessionConflict, AttackGoal: e.goal,
		SelfPosition: self.Position, SelfVelocity: selfVelocity, HasDisc: hasDisc,
		Stunned: self.IsStunned, Shield: self.ShieldActive, TargetKind: targetKind,
		Target: target, Disc: disc, Opponent: opponent, Teammate: teammate, Currents: currents,
	}
	if sourceObservationBound(self, tick) {
		observation.SourceKind = self.Observation.Source
		observation.SourceID = self.Observation.SourceID
		observation.SourceAuthority = self.Observation.Authority
		observation.SourceTimeBasis = self.Observation.TimeBasis
		observation.SourceEpoch = self.Observation.SourceEpoch
		observation.SourceEpochKnown = true
	}
	if e.boundPlayerID == "" {
		e.boundPlayerID = self.PlayerID
	}
	if e.boundTeam == "" && teamKnown {
		e.boundTeam = team
	}
	e.sequence++
	return observation, nil
}

func selectFrame(tick *adapter.ParsedTick, selector string) (model.PlayerTelemetryFrame, error) {
	var idMatches []model.PlayerTelemetryFrame
	for _, frame := range tick.Frames {
		if frame.PlayerID == selector {
			idMatches = append(idMatches, frame)
		}
	}
	if len(idMatches) == 1 {
		return idMatches[0], nil
	}
	if len(idMatches) > 1 {
		return model.PlayerTelemetryFrame{}, fmt.Errorf("player id %q is duplicated in frame %d", selector, tick.FrameIndex)
	}
	var matches []model.PlayerTelemetryFrame
	for _, frame := range tick.Frames {
		name := tick.MatchCtx.PlayerNames[frame.PlayerID]
		if strings.EqualFold(name, selector) {
			matches = append(matches, frame)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return model.PlayerTelemetryFrame{}, fmt.Errorf("player selector %q is ambiguous in frame %d", selector, tick.FrameIndex)
	}
	available := make([]string, 0, len(tick.Frames))
	for _, frame := range tick.Frames {
		name := tick.MatchCtx.PlayerNames[frame.PlayerID]
		if name == "" {
			available = append(available, frame.PlayerID)
		} else {
			available = append(available, fmt.Sprintf("%s (%s)", name, frame.PlayerID))
		}
	}
	sort.Strings(available)
	return model.PlayerTelemetryFrame{}, fmt.Errorf("%w: player %q in frame %d; available: %s", ErrPlayerAbsent, selector, tick.FrameIndex, strings.Join(available, ", "))
}

func nearestPlayers(frames []model.PlayerTelemetryFrame, self model.PlayerTelemetryFrame, selfTeam string, selfTeamKnown bool, toLocal func(model.Vec3) model.Vec3, selfVelocity model.Vec3, selfVelocityKnown bool) (opponent, teammate RelativeEntity) {
	if !selfTeamKnown {
		return RelativeEntity{}, RelativeEntity{}
	}
	for _, other := range frames {
		if other.PlayerID == self.PlayerID || !finiteVec(other.Position) {
			continue
		}
		otherTeam, otherTeamKnown := playingTeam(other.Team)
		if !otherTeamKnown {
			continue
		}
		worldRelative := other.Position.Sub(self.Position)
		otherVelocity, otherVelocityKnown := observedVelocity(other)
		worldVelocity := model.Vec3{}
		if selfVelocityKnown && otherVelocityKnown {
			worldVelocity = otherVelocity.Sub(selfVelocity)
		}
		distance := worldRelative.Magnitude()
		if !finiteVec(worldRelative) || !finite(distance) ||
			(selfVelocityKnown && otherVelocityKnown && !finiteVec(worldVelocity)) {
			continue
		}
		relative := toLocal(worldRelative)
		relativeVelocity := model.Vec3{}
		if selfVelocityKnown && otherVelocityKnown {
			relativeVelocity = toLocal(worldVelocity)
		}
		if !finiteVec(relative) || !finiteVec(relativeVelocity) {
			continue
		}
		candidate := RelativeEntity{
			Available: true, VelocityKnown: selfVelocityKnown && otherVelocityKnown,
			Relative: relative, Velocity: relativeVelocity,
			Distance: distance, PlayerID: other.PlayerID,
		}
		if otherTeam == selfTeam {
			if !teammate.Available || candidate.Distance < teammate.Distance || (candidate.Distance == teammate.Distance && candidate.PlayerID < teammate.PlayerID) {
				teammate = candidate
			}
		} else if !opponent.Available || candidate.Distance < opponent.Distance || (candidate.Distance == opponent.Distance && candidate.PlayerID < opponent.PlayerID) {
			opponent = candidate
		}
	}
	return opponent, teammate
}

func encodeDirections(currents map[string]float64, prefix string, entity RelativeEntity, orientationKnown bool) {
	if !entity.Available || !finite(entity.Distance) {
		currents[prefix+"_missing"] = 1
		return
	}
	if !orientationKnown {
		return
	}
	distance := math.Max(entity.Distance, 1e-9)
	x, y, z := entity.Relative[0]/distance, entity.Relative[1]/distance, entity.Relative[2]/distance
	currents[prefix+"_left"] = clamp(x, 0, 1)
	currents[prefix+"_right"] = clamp(-x, 0, 1)
	currents[prefix+"_up"] = clamp(y, 0, 1)
	currents[prefix+"_down"] = clamp(-y, 0, 1)
	currents[prefix+"_ahead"] = clamp(z, 0, 1)
	currents[prefix+"_behind"] = clamp(-z, 0, 1)
	currents[prefix+"_near"] = 1 - clamp(entity.Distance/8, 0, 1)
	currents[prefix+"_far"] = clamp(entity.Distance/24, 0, 1)
}

func approachingCurrent(entity RelativeEntity) float64 {
	if !entity.Available || !entity.VelocityKnown || entity.Distance < 1e-9 {
		return 0
	}
	distanceRate := entity.Relative.Dot(entity.Velocity) / entity.Distance
	return clamp(-distanceRate/10, 0, 1)
}

func grabOpportunity(disc RelativeEntity, discFree, possessionConflict bool) float64 {
	if !discFree || possessionConflict || !disc.Available {
		return 0
	}
	return 1 - clamp(disc.Distance/1.2, 0, 1)
}

func throwOpportunity(hasDisc, possessionConflict bool, goal RelativeEntity) float64 {
	if !hasDisc || possessionConflict || !goal.Available || goal.Distance < 1e-9 {
		return 0
	}
	alignment := clamp(goal.Relative[2]/goal.Distance, 0, 1)
	rangeFactor := 1 - clamp((goal.Distance-8)/32, 0, 1)
	return alignment * alignment * rangeFactor
}

func playingTeam(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "blue":
		return "blue", true
	case "orange":
		return "orange", true
	default:
		return "", false
	}
}

func agentPhase(tick *adapter.ParsedTick, mapped string) (known, active bool) {
	if tick == nil || tick.Session == nil {
		return false, false
	}
	raw := strings.ToLower(strings.TrimSpace(tick.Session.GameStatus))
	switch raw {
	case "playing", "round", "overtime", "sudden_death":
		return true, isAgentActivePhase(mapped)
	case "round_start", "round_over", "pre_match", "post_match", "score", "pre_sudden_death", "post_sudden_death":
		return true, false
	default:
		return false, false
	}
}

func isAgentActivePhase(phase string) bool {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "playing", "round", "overtime", "sudden_death":
		return true
	default:
		return false
	}
}

func confirmedPossession(self model.PlayerTelemetryFrame) (known, hasDisc, discFree, conflict bool) {
	disc := self.Disc
	if disc == nil {
		return false, false, false, false
	}
	conflict = disc.PossessionConflict
	if disc.Attachment != nil {
		if !disc.Attachment.Known() {
			return false, false, false, conflict
		}
		hasDisc = disc.Attachment.HeldBy(self.PlayerID)
		discFree = disc.Attachment.Free()
		if self.HasPossession != hasDisc {
			conflict = true
		}
		if disc.PossessionKnown && (disc.IsHeld == discFree ||
			(disc.IsHeld && disc.PossessorID != "" && disc.PossessorID != disc.Attachment.HolderID) ||
			(!disc.IsHeld && disc.PossessorID != "")) {
			conflict = true
		}
		return true, hasDisc, discFree, conflict
	}
	if !disc.PossessionKnown {
		return false, false, false, conflict
	}
	hasDisc = self.HasPossession
	discFree = !disc.IsHeld
	if disc.IsHeld != (disc.PossessorID != "") {
		conflict = true
	}
	if hasDisc && (!disc.IsHeld || (disc.PossessorID != "" && disc.PossessorID != self.PlayerID)) {
		conflict = true
	}
	if !hasDisc && disc.PossessorID == self.PlayerID {
		conflict = true
	}
	return true, hasDisc, discFree, conflict
}

func sourceObservationBound(self model.PlayerTelemetryFrame, tick *adapter.ParsedTick) bool {
	if self.Observation == nil || !self.Observation.Valid() || tick == nil || tick.MatchCtx == nil {
		return false
	}
	observation := self.Observation
	if observation.SessionID != tick.MatchID || tick.MatchCtx.MatchID != tick.MatchID ||
		observation.FrameIndex != self.FrameIndex || self.FrameIndex != tick.FrameIndex ||
		observation.Timestamp != self.Timestamp || observation.Authority != "client_reported" {
		return false
	}
	switch observation.Source {
	case "echoreplay":
		return strings.HasPrefix(observation.TimeBasis, "recorder_prefix")
	case "tape":
		return strings.HasPrefix(observation.TimeBasis, "capture_offset_ms")
	default:
		return false
	}
}

func observedVelocity(frame model.PlayerTelemetryFrame) (model.Vec3, bool) {
	if frame.ReportedVelocity != nil && finiteVec(*frame.ReportedVelocity) {
		return *frame.ReportedVelocity, true
	}
	return model.Vec3{}, false
}

func finiteVec(v model.Vec3) bool { return !v.HasNaN() && !v.HasInf() }

func boolCurrent(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
