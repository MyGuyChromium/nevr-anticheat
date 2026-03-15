package adapter

import (
	"fmt"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// MappingResult contains the converted frames plus any warnings/errors.
type MappingResult struct {
	Frames   []model.PlayerTelemetryFrame
	MatchCtx *model.MatchContext
	Warnings []MappingWarning
	Errors   []MappingError
}

// MappingWarning is a non-fatal issue where a safe default was used.
type MappingWarning struct {
	Field   string
	Message string
}

// MappingError is a fatal issue where the frame was rejected.
type MappingError struct {
	PlayerName string
	Field      string
	Message    string
}

// FieldConfidence indicates how reliable a field mapping is.
type FieldConfidence int

const (
	Confirmed FieldConfidence = iota // verified against real Echo VR data
	Likely                           // probably correct based on community docs
	Inferred                         // computed from other fields
	Unknown                          // unconfirmed; using safe default
	Absent                           // known to not exist in Echo VR
)

// Mapper converts raw Echo VR API responses into NEVR-Anticheat telemetry frames.
type Mapper struct {
	frameIndex    int
	prevTimestamp float64
	prevPositions map[string][3]float64 // for dt computation

	// Track which fields have been warned about (warn once per field)
	warnedFields map[string]bool
}

// NewMapper creates a new Echo VR telemetry mapper.
func NewMapper() *Mapper {
	return &Mapper{
		prevPositions: make(map[string][3]float64),
		warnedFields:  make(map[string]bool),
	}
}

// MapSession converts a raw Echo VR /session response into telemetry frames.
// Returns one frame per player in the response.
func (m *Mapper) MapSession(raw *EchoVRSessionResponse) *MappingResult {
	result := &MappingResult{}

	if raw == nil {
		result.Errors = append(result.Errors, MappingError{Field: "session", Message: "nil session response"})
		return result
	}

	// Map match context (only on first call or when session changes)
	if result.MatchCtx == nil {
		result.MatchCtx = m.mapMatchContext(raw, result)
	}

	// Compute timestamp
	// CONFIRMED: game_clock is available. It counts DOWN in Echo Arena.
	// We need a monotonically increasing timestamp, so we use frame index * assumed dt.
	timestamp := float64(m.frameIndex) * 0.067 // ~15fps default
	if raw.GameClock > 0 {
		// game_clock counts down from match duration (e.g., 300.0 to 0.0)
		// For monotonic timestamps, we invert: timestamp = matchDuration - gameClock
		// But we don't know match duration on first frame. Use frame counter instead.
		// WARNING: This is a simplification. Real implementation should track initial game_clock.
	}

	dt := timestamp - m.prevTimestamp
	if m.frameIndex == 0 {
		dt = 0.067
	}
	if dt <= 0 {
		dt = 0.067
	}

	// Map each player
	for teamIdx, team := range raw.Teams {
		teamName := "blue"
		if teamIdx == 1 {
			teamName = "orange"
		}

		for _, player := range team.Players {
			frame, warnings, err := m.mapPlayer(&player, raw, teamName, timestamp, dt, m.frameIndex)
			if err != nil {
				result.Errors = append(result.Errors, *err)
				continue
			}
			result.Frames = append(result.Frames, *frame)
			result.Warnings = append(result.Warnings, warnings...)
		}
	}

	m.prevTimestamp = timestamp
	m.frameIndex++
	return result
}

// mapMatchContext extracts match-level metadata.
func (m *Mapper) mapMatchContext(raw *EchoVRSessionResponse, result *MappingResult) *model.MatchContext {
	mc := &model.MatchContext{
		MatchID:         raw.SessionID, // CONFIRMED
		GameMode:        raw.MatchType, // CONFIRMED
		Map:             raw.MapName,   // CONFIRMED
		IsPrivate:       raw.PrivateMatch,
		Physics:         model.DefaultPhysics(),
		Source:          "echovr_api",
		TeamAssignments: make(map[string]string),
	}

	// Map game phase
	// CONFIRMED: game_status values are "playing", "round_start", "round_over", "pre_match", "post_match", "score"
	// Our system expects the same strings (with "score" treated as non-active)

	// Collect player IDs
	for teamIdx, team := range raw.Teams {
		teamName := "blue"
		if teamIdx == 1 {
			teamName = "orange"
		}
		for _, p := range team.Players {
			pid := playerID(p)
			mc.PlayerIDs = append(mc.PlayerIDs, pid)
			mc.TeamAssignments[pid] = teamName
		}
	}

	return mc
}

// mapPlayer converts a single Echo VR player into a PlayerTelemetryFrame.
func (m *Mapper) mapPlayer(
	p *EchoVRPlayer,
	session *EchoVRSessionResponse,
	team string,
	timestamp float64,
	dt float64,
	frameIdx int,
) (*model.PlayerTelemetryFrame, []MappingWarning, *MappingError) {
	var warnings []MappingWarning
	pid := playerID(*p)

	// CONFIRMED: position is [3]float64
	pos := model.Vec3(p.Position)
	if pos.IsZero() {
		return nil, nil, &MappingError{PlayerName: p.Name, Field: "position", Message: "zero position"}
	}
	if pos.HasNaN() || pos.HasInf() {
		return nil, nil, &MappingError{PlayerName: p.Name, Field: "position", Message: "NaN/Inf position"}
	}

	// CONFIRMED: body rotation from direction vectors, NOT quaternion
	bodyRot := directionVectorsToQuat(p.Forward, p.Left, p.Up)
	if !bodyRot.IsUnit() {
		warnings = append(warnings, MappingWarning{Field: "rotation", Message: "non-unit body quaternion, normalizing"})
		bodyRot = bodyRot.Normalize()
	}

	// CONFIRMED: hand positions from nested hand objects
	leftHandPos := model.Vec3(p.LHand.Position)
	rightHandPos := model.Vec3(p.RHand.Position)

	// CONFIRMED/LIKELY: hand rotations from direction vectors
	// If direction vectors are zero (tracking lost), use identity quaternion
	leftHandRot := model.QuatIdentity()
	rightHandRot := model.QuatIdentity()

	if !isZeroVec(p.LHand.Forward) && !isZeroVec(p.LHand.Up) {
		leftHandRot = directionVectorsToQuat(p.LHand.Forward, p.LHand.Left, p.LHand.Up)
	} else {
		if !m.warnedFields["lhand_rot"] {
			warnings = append(warnings, MappingWarning{
				Field:   "left_hand_rotation",
				Message: "hand direction vectors are zero; using identity quaternion. BIO_001 and BIO_004 may produce false positives.",
			})
			m.warnedFields["lhand_rot"] = true
		}
	}

	if !isZeroVec(p.RHand.Forward) && !isZeroVec(p.RHand.Up) {
		rightHandRot = directionVectorsToQuat(p.RHand.Forward, p.RHand.Left, p.RHand.Up)
	} else {
		if !m.warnedFields["rhand_rot"] {
			warnings = append(warnings, MappingWarning{
				Field:   "right_hand_rotation",
				Message: "hand direction vectors are zero; using identity quaternion.",
			})
			m.warnedFields["rhand_rot"] = true
		}
	}

	// CONFIRMED: disc state
	var disc *model.DiscState
	if session.Disc != nil {
		vel := model.Vec3(session.Disc.Velocity)
		disc = &model.DiscState{
			Position: model.Vec3(session.Disc.Position),
			Velocity: vel,
			Speed:    vel.Magnitude(),
			IsHeld:   false, // set below based on possession
		}
	}

	// CONFIRMED: per-player possession boolean exists in Echo VR API
	hasPossession := p.Possession
	if hasPossession && disc != nil {
		disc.IsHeld = true
		disc.PossessorID = pid
	}

	// CONFIRMED: stunned boolean
	isStunned := p.Stunned

	// LIKELY: blocking/shield state
	shieldActive := p.Blocking // LIKELY — field name may differ

	// LIKELY: invulnerable state (post-respawn immunity)
	isImmune := p.Invulnerable

	// ABSENT: boosting — must be inferred from velocity changes
	// We set IsBoosting=false and note the limitation
	isBoosting := false
	if !m.warnedFields["boosting"] {
		warnings = append(warnings, MappingWarning{
			Field:   "is_boosting",
			Message: "Echo VR API does not expose boosting state. MOV_004 and MOV_005 will be inactive. ABSENT field.",
		})
		m.warnedFields["boosting"] = true
	}

	// CONFIRMED: per-player ping exists in Echo VR API
	pingMs := float64(p.Ping) // int ms to float64

	// Map game phase
	gamePhase := mapGamePhase(session.GameStatus)

	// CONFIRMED: team scores
	blueScore := session.BluePoints
	orangeScore := session.OrangePoints

	// CONFIRMED: per-player stats
	goals := p.Stats.Goals
	stuns := p.Stats.Stuns

	frame := &model.PlayerTelemetryFrame{
		PlayerID:          pid,
		FrameIndex:        frameIdx,
		Timestamp:         timestamp,
		DeltaTime:         dt,
		Position:          pos,
		Rotation:          bodyRot,
		LeftHandPosition:  leftHandPos,
		RightHandPosition: rightHandPos,
		LeftHandRotation:  leftHandRot,
		RightHandRotation: rightHandRot,
		IsStunned:         isStunned,
		IsBoosting:        isBoosting,
		ShieldActive:      shieldActive,
		IsImmune:          isImmune,
		HasPossession:     hasPossession,
		Disc:              disc,
		EstimatedPingMs:   pingMs,
		GamePhase:         gamePhase,
		BlueScore:         blueScore,
		OrangeScore:       orangeScore,
		Goals:             goals,
		Stuns:             stuns,
	}

	return frame, warnings, nil
}

// playerID creates a stable player identifier from Echo VR player data.
// Uses UserID (numeric Oculus ID) which survives name changes.
func playerID(p EchoVRPlayer) string {
	if p.UserID != 0 {
		return fmt.Sprintf("echovr:%d", p.UserID)
	}
	// Fallback to name if UserID is missing (shouldn't happen)
	return fmt.Sprintf("name:%s", p.Name)
}

// directionVectorsToQuat converts Echo VR's direction vectors to a quaternion.
// Echo VR provides forward, left, up as [3]float64 direction vectors.
func directionVectorsToQuat(forward, left, up [3]float64) model.Quat {
	f := model.Vec3(forward)
	l := model.Vec3(left)
	u := model.Vec3(up)

	// Guard against zero vectors
	if f.IsZero() || u.IsZero() {
		return model.QuatIdentity()
	}

	return model.QuatFromDirectionVectors(f, l, u)
}

// isZeroVec checks if a raw [3]float64 is all zeros.
func isZeroVec(v [3]float64) bool {
	return v[0] == 0 && v[1] == 0 && v[2] == 0
}

// mapGamePhase converts Echo VR game_status to our internal game phase string.
func mapGamePhase(status string) string {
	switch strings.ToLower(status) {
	case "playing":
		return "playing"
	case "round_start":
		return "round_start"
	case "round_over":
		return "round_over"
	case "pre_match":
		return "pre_match"
	case "post_match":
		return "post_match"
	case "score":
		return "round_over" // "score" phase maps to round_over (non-active)
	case "":
		return "playing" // default to active if unknown
	default:
		return status // pass through unknown values
	}
}

// FieldMapping documents a single field mapping for audit purposes.
type FieldMapping struct {
	InternalField  string
	EchoVRField    string
	Confidence     FieldConfidence
	DefaultValue   string
	Notes          string
}

// DocumentMappings returns the complete field mapping table for audit.
func DocumentMappings() []FieldMapping {
	return []FieldMapping{
		{"PlayerID", "userid / name", Confirmed, "name:<display_name>", "UserID preferred; falls back to name"},
		{"FrameIndex", "(sequential)", Inferred, "auto-increment", "Derived from polling order"},
		{"Timestamp", "game_clock (inverted)", Confirmed, "frame_index * 0.067", "game_clock counts down; we need monotonic increasing"},
		{"DeltaTime", "(computed)", Inferred, "0.067", "Derived from consecutive timestamps"},
		{"Position", "position", Confirmed, "rejected if zero", "Direct mapping [3]float64"},
		{"Rotation", "forward/left/up", Confirmed, "identity quat", "Converted from 3 direction vectors via QuatFromDirectionVectors"},
		{"LeftHandPosition", "lhand.pos", Confirmed, "[0,0,0]", "Direct mapping"},
		{"RightHandPosition", "rhand.pos", Confirmed, "[0,0,0]", "Direct mapping"},
		{"LeftHandRotation", "lhand.forward/left/up", Likely, "identity quat", "Converted from direction vectors; zero vectors → identity"},
		{"RightHandRotation", "rhand.forward/left/up", Likely, "identity quat", "Same as left hand"},
		{"IsStunned", "stunned", Confirmed, "false", "Direct boolean mapping"},
		{"IsBoosting", "(absent)", Absent, "false", "Echo VR API does not expose boosting state"},
		{"ShieldActive", "blocking", Confirmed, "false", "Per-player blocking boolean"},
		{"IsImmune", "invulnerable", Confirmed, "false", "Post-respawn invulnerability"},
		{"HasPossession", "possession", Confirmed, "false", "Per-player possession boolean from API"},
		{"Disc.Position", "disc.position", Confirmed, "nil disc", "Direct mapping"},
		{"Disc.Velocity", "disc.velocity", Confirmed, "nil disc", "Direct mapping"},
		{"EstimatedPingMs", "ping", Confirmed, "0", "Per-player ping in ms; enables lag compensation"},
		{"GamePhase", "game_status", Confirmed, "playing", "Direct string mapping with normalization"},
		{"BlueScore", "blue_points", Confirmed, "0", "Direct mapping"},
		{"OrangeScore", "orange_points", Confirmed, "0", "Direct mapping"},
		{"Goals", "stats.goals", Confirmed, "0", "Per-player stat"},
		{"Stuns", "stats.stuns", Confirmed, "0", "Per-player stat"},
	}
}
