package adapter

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

// DiagnosticReport collects statistics about adapter mapping quality.
// Used during first live integration to validate telemetry assumptions.
//
// Counter units are explicit in the field names:
//   - Snapshots*: one /session payload (one tick, all players)
//   - Lines*: one NDJSON replay line (may or may not become a snapshot)
//   - PlayerEntries*: one player object inside a snapshot, before mapping
//   - Frames*: one PlayerTelemetryFrame produced (or rejected) by the Mapper
type DiagnosticReport struct {
	mu sync.Mutex

	// Snapshots is the number of session payloads recorded via RecordSession*.
	Snapshots int `json:"snapshots"`
	// SnapshotsNoTeams counts payloads skipped because they carried no teams
	// (lobby/error states).
	SnapshotsNoTeams int `json:"snapshots_no_teams"`
	// LinesWithBones counts lines that carried Spark's trailing bones JSON
	// document (ignored by the parser).
	LinesWithBones int `json:"lines_with_bones"`
	// SnapshotsDuplicate counts payloads skipped by the mapper's change detection.
	SnapshotsDuplicate int `json:"snapshots_duplicate"`

	// FramesRejected counts NDJSON lines that could not be parsed (no tab
	// separator, bad timestamp prefix, empty or invalid JSON). Kept under its
	// historical name; it is a LINE count, not a player-frame count.
	FramesRejected int `json:"lines_rejected"`
	// LinesBadTimestamp counts rejected lines whose timestamp prefix did not parse.
	LinesBadTimestamp int `json:"lines_bad_timestamp"`
	// SessionChanges counts session-id changes mid-file; the file holds
	// SessionChanges+1 matches (EchoReplayParser.Matches).
	SessionChanges int `json:"session_changes"`

	// PlayerEntriesSeen counts player objects on blue/orange teams in recorded snapshots.
	PlayerEntriesSeen int `json:"player_entries_seen"`
	// SpectatorEntriesDropped counts player objects on SPECTATORS/unknown teams.
	SpectatorEntriesDropped int `json:"spectator_entries_dropped"`

	// FramesMapped counts PlayerTelemetryFrames the mapper produced
	// (populated via RecordMappingResult).
	FramesMapped int `json:"frames_mapped"`
	// PlayerFramesRejected counts player entries the mapper rejected, with a
	// per-field breakdown in RejectionsByField.
	PlayerFramesRejected int            `json:"player_frames_rejected"`
	RejectionsByField    map[string]int `json:"rejections_by_field"`

	// MapperStats mirrors the mapper's own counters when RecordMapperStats was called.
	MapperStats *MapperStats `json:"mapper_stats,omitempty"`

	PlayersSeenIDs map[string]bool `json:"-"`
	PlayerCount    int             `json:"player_count"`

	// Per-field presence tracking
	FieldPresence map[string]*FieldDiagnostic `json:"field_presence"`
	// PresenceTracked is true when at least one snapshot was recorded from raw
	// JSON (RecordSessionJSON), so FieldDiagnostic.Missing reflects absent keys.
	PresenceTracked bool `json:"presence_tracked"`

	// Quaternion quality
	NonUnitQuaternions int `json:"non_unit_quaternions"`
	ZeroHandRotations  int `json:"zero_hand_rotations"`

	// Possession/disc consistency
	PossessionWithNoDisc      int `json:"possession_with_no_disc"`
	PossessionMultiplePlayers int `json:"possession_multiple_players"`
	DiscHeldButHighSpeed      int `json:"disc_held_but_high_speed"`

	// Coordinate sanity
	PositionOutOfBounds int `json:"position_out_of_bounds"`
	HandFarFromBody     int `json:"hand_far_from_body"`
	NegativeTimestamps  int `json:"negative_timestamps"`

	// Sample raw payloads for unknown structures (first 3)
	SamplePayloads []string `json:"sample_payloads,omitempty"`
	maxSamples     int
}

// FieldDiagnostic tracks per-field statistics.
//
// Present  = key present with a non-zero / true value
// Inactive = key present (or presence unknown) with a zero / false value
// Missing  = JSON key absent (only counted when the snapshot came from raw JSON)
// Invalid  = NaN / Inf
type FieldDiagnostic struct {
	Present     int     `json:"present"`
	Inactive    int     `json:"inactive"`
	Missing     int     `json:"missing"`
	Invalid     int     `json:"invalid"`
	MinValue    float64 `json:"min_value,omitempty"`
	MaxValue    float64 `json:"max_value,omitempty"`
	SampleValue string  `json:"sample_value,omitempty"` // first non-zero value seen
}

// Seen returns how many times the key was observed in a payload (any value).
func (fd *FieldDiagnostic) Seen() int { return fd.Present + fd.Inactive + fd.Invalid }

// NewDiagnosticReport creates a diagnostic report collector.
func NewDiagnosticReport() *DiagnosticReport {
	return &DiagnosticReport{
		PlayersSeenIDs:    make(map[string]bool),
		FieldPresence:     make(map[string]*FieldDiagnostic),
		RejectionsByField: make(map[string]int),
		maxSamples:        3,
	}
}

func (dr *DiagnosticReport) getField(name string) *FieldDiagnostic {
	fd, ok := dr.FieldPresence[name]
	if !ok {
		fd = &FieldDiagnostic{MinValue: math.MaxFloat64, MaxValue: -math.MaxFloat64}
		dr.FieldPresence[name] = fd
	}
	return fd
}

func (dr *DiagnosticReport) recordFloat(name string, value float64, present bool) {
	fd := dr.getField(name)
	if !present {
		fd.Missing++
		return
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		fd.Invalid++
		return
	}
	if value == 0 {
		fd.Inactive++
		return
	}
	fd.Present++
	if value < fd.MinValue {
		fd.MinValue = value
	}
	if value > fd.MaxValue {
		fd.MaxValue = value
	}
	if fd.SampleValue == "" {
		fd.SampleValue = fmt.Sprintf("%.4f", value)
	}
}

func (dr *DiagnosticReport) recordBool(name string, value bool, present bool) {
	fd := dr.getField(name)
	switch {
	case !present:
		fd.Missing++
	case value:
		fd.Present++
	default:
		fd.Inactive++
	}
}

func (dr *DiagnosticReport) recordVec3(name string, v [3]float64, present bool) {
	fd := dr.getField(name)
	if !present {
		fd.Missing++
		return
	}
	if math.IsNaN(v[0]) || math.IsNaN(v[1]) || math.IsNaN(v[2]) ||
		math.IsInf(v[0], 0) || math.IsInf(v[1], 0) || math.IsInf(v[2], 0) {
		fd.Invalid++
		return
	}
	if v[0] == 0 && v[1] == 0 && v[2] == 0 {
		fd.Inactive++
		return
	}
	fd.Present++
	mag := math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])
	if mag < fd.MinValue {
		fd.MinValue = mag
	}
	if mag > fd.MaxValue {
		fd.MaxValue = mag
	}
	if fd.SampleValue == "" {
		fd.SampleValue = fmt.Sprintf("[%.2f, %.2f, %.2f]", v[0], v[1], v[2])
	}
}

// sessionPresence records which JSON keys existed in a raw payload.
type sessionPresence struct {
	top       map[string]bool
	disc      map[string]bool
	lastThrow map[string]bool
	players   [][]map[string]bool // [team][player] -> dotted key set
}

// probeSessionPresence decodes the raw payload into key sets. It is a second
// decode of the line; the cost is accepted because it is the only way to tell
// "field absent" (schema drift) from "field false/zero" (nothing happening).
func probeSessionPresence(data []byte) (*sessionPresence, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	sp := &sessionPresence{
		top: make(map[string]bool, len(top)), disc: make(map[string]bool),
		lastThrow: make(map[string]bool),
	}
	for k := range top {
		sp.top[k] = true
	}
	if raw, ok := top["disc"]; ok && !isJSONNull(raw) {
		var disc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &disc); err == nil {
			for k := range disc {
				sp.disc[k] = true
			}
		}
	}
	if raw, ok := top["last_throw"]; ok && !isJSONNull(raw) {
		var lastThrow map[string]json.RawMessage
		if err := json.Unmarshal(raw, &lastThrow); err == nil {
			for k := range lastThrow {
				sp.lastThrow[k] = true
			}
		}
	}
	if raw, ok := top["teams"]; ok {
		var teams []struct {
			Players []map[string]json.RawMessage `json:"players"`
		}
		if err := json.Unmarshal(raw, &teams); err == nil {
			for _, team := range teams {
				var players []map[string]bool
				for _, p := range team.Players {
					keys := make(map[string]bool, len(p)*2)
					for k, v := range p {
						keys[k] = true
						switch k {
						case "body", "head", "lhand", "rhand", "stats":
							var nested map[string]json.RawMessage
							if err := json.Unmarshal(v, &nested); err == nil {
								for nk := range nested {
									keys[k+"."+nk] = true
								}
							}
						}
					}
					players = append(players, keys)
				}
				sp.players = append(sp.players, players)
			}
		}
	}
	return sp, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func (sp *sessionPresence) hasTop(key string) bool {
	return sp == nil || sp.top[key]
}

func (sp *sessionPresence) hasDisc(key string) bool {
	return sp == nil || sp.disc[key]
}

func (sp *sessionPresence) hasLastThrow(key string) bool {
	return sp == nil || sp.lastThrow[key]
}

func (sp *sessionPresence) hasPlayer(teamIdx, playerIdx int, key string) bool {
	if sp == nil {
		return true
	}
	if teamIdx >= len(sp.players) || playerIdx >= len(sp.players[teamIdx]) {
		return false
	}
	return sp.players[teamIdx][playerIdx][key]
}

// RecordSessionJSON decodes a raw /session payload, records diagnostics with
// key-presence tracking and returns the decoded session for mapping.
func (dr *DiagnosticReport) RecordSessionJSON(data []byte) (*EchoVRSessionResponse, error) {
	var session EchoVRSessionResponse
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	presence, err := probeSessionPresence(data)
	if err != nil {
		return nil, err
	}
	dr.mu.Lock()
	dr.PresenceTracked = true
	dr.recordSession(&session, presence)
	dr.mu.Unlock()
	return &session, nil
}

// RecordSessionWithJSON records diagnostics for an already-decoded session
// while probing the raw payload for key presence.
func (dr *DiagnosticReport) RecordSessionWithJSON(session *EchoVRSessionResponse, data []byte) {
	presence, err := probeSessionPresence(data)
	dr.mu.Lock()
	defer dr.mu.Unlock()
	if err == nil {
		dr.PresenceTracked = true
	} else {
		presence = nil
	}
	dr.recordSession(session, presence)
}

func (dr *DiagnosticReport) recordBadTimestamp() {
	dr.mu.Lock()
	dr.LinesBadTimestamp++
	dr.mu.Unlock()
}

// RecordSession analyzes an already-decoded session payload. Key presence is
// unknown on this path: zero/false values are counted as Inactive, never Missing.
func (dr *DiagnosticReport) RecordSession(raw *EchoVRSessionResponse) {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	dr.recordSession(raw, nil)
}

// RecordMappingResult folds the mapper's per-snapshot output into the report so
// operators can see mapping loss (frames rejected, spectators dropped, duplicates).
func (dr *DiagnosticReport) RecordMappingResult(result *MappingResult) {
	if result == nil {
		return
	}
	dr.mu.Lock()
	defer dr.mu.Unlock()
	if result.SkippedDuplicate {
		dr.SnapshotsDuplicate++
		return
	}
	dr.FramesMapped += len(result.Frames)
	dr.PlayerFramesRejected += len(result.Errors)
	for _, e := range result.Errors {
		dr.RejectionsByField[e.Field]++
	}
}

// RecordMapperStats stores the mapper's counters for the report.
func (dr *DiagnosticReport) RecordMapperStats(stats MapperStats) {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	s := stats
	dr.MapperStats = &s
}

// RecordSessionChange counts a session-id change mid-file (a new match).
func (dr *DiagnosticReport) RecordSessionChange() {
	dr.mu.Lock()
	dr.SessionChanges++
	dr.mu.Unlock()
}

// RecordSnapshotNoTeams counts a payload that carried no teams and was skipped.
func (dr *DiagnosticReport) RecordSnapshotNoTeams() {
	dr.mu.Lock()
	dr.SnapshotsNoTeams++
	dr.mu.Unlock()
}

func (dr *DiagnosticReport) recordSession(raw *EchoVRSessionResponse, presence *sessionPresence) {
	if raw == nil {
		return
	}
	dr.Snapshots++

	// Capture sample payload
	if len(dr.SamplePayloads) < dr.maxSamples {
		data, _ := json.Marshal(raw)
		if len(data) > 2000 {
			data = data[:2000]
		}
		dr.SamplePayloads = append(dr.SamplePayloads, string(data))
	}

	// Track scores
	dr.recordFloat("blue_points", float64(raw.BluePoints), presence.hasTop("blue_points"))
	dr.recordFloat("orange_points", float64(raw.OrangePoints), presence.hasTop("orange_points"))
	dr.recordFloat("game_clock", raw.GameClock, presence.hasTop("game_clock"))

	// Disc
	if raw.Disc != nil {
		dr.recordVec3("disc.position", raw.Disc.Position, presence.hasDisc("position"))
		dr.recordVec3("disc.velocity", raw.Disc.Velocity, presence.hasDisc("velocity"))
	} else {
		dr.getField("disc").Missing++
	}

	// Engine-authored local-player throw breakdown. Record every component so
	// operators can distinguish a source that lacks last_throw from one where
	// a component simply remained zero in the sampled matches.
	lt := EchoVRLastThrow{}
	if raw.LastThrow != nil {
		lt = *raw.LastThrow
	}
	dr.recordFloat("last_throw.arm_speed", lt.ArmSpeed, presence.hasLastThrow("arm_speed"))
	dr.recordFloat("last_throw.total_speed", lt.TotalSpeed, presence.hasLastThrow("total_speed"))
	dr.recordFloat("last_throw.off_axis_spin_deg", lt.OffAxisSpinDeg, presence.hasLastThrow("off_axis_spin_deg"))
	dr.recordFloat("last_throw.wrist_throw_penalty", lt.WristThrowPenalty, presence.hasLastThrow("wrist_throw_penalty"))
	dr.recordFloat("last_throw.rot_per_sec", lt.RotPerSec, presence.hasLastThrow("rot_per_sec"))
	dr.recordFloat("last_throw.pot_speed_from_rot", lt.PotentialSpeedFromRot, presence.hasLastThrow("pot_speed_from_rot"))
	dr.recordFloat("last_throw.speed_from_arm", lt.SpeedFromArm, presence.hasLastThrow("speed_from_arm"))
	dr.recordFloat("last_throw.speed_from_movement", lt.SpeedFromMovement, presence.hasLastThrow("speed_from_movement"))
	dr.recordFloat("last_throw.speed_from_wrist", lt.SpeedFromWrist, presence.hasLastThrow("speed_from_wrist"))
	dr.recordFloat("last_throw.wrist_align_to_throw_deg", lt.WristAlignToThrowDeg, presence.hasLastThrow("wrist_align_to_throw_deg"))
	dr.recordFloat("last_throw.throw_align_to_movement_deg", lt.ThrowAlignToMovementDeg, presence.hasLastThrow("throw_align_to_movement_deg"))
	dr.recordFloat("last_throw.off_axis_penalty", lt.OffAxisPenalty, presence.hasLastThrow("off_axis_penalty"))
	dr.recordFloat("last_throw.throw_move_penalty", lt.ThrowMovePenalty, presence.hasLastThrow("throw_move_penalty"))

	// Count possession holders per frame
	possessionHolders := 0

	for teamIdx, team := range raw.Teams {
		if _, ok := mappedTeamName(team.TeamName, teamIdx); !ok {
			dr.SpectatorEntriesDropped += len(team.Players)
			continue
		}
		for playerIdx := range team.Players {
			p := &team.Players[playerIdx]
			has := func(key string) bool { return presence.hasPlayer(teamIdx, playerIdx, key) }

			dr.PlayerEntriesSeen++
			dr.PlayersSeenIDs[playerID(*p)] = true

			// Position
			dr.recordVec3("position", p.Body.Position, has("body.position"))

			// Direction vectors
			dr.recordVec3("forward", p.Body.Forward, has("body.forward"))
			dr.recordVec3("left", p.Body.Left, has("body.left"))
			dr.recordVec3("up", p.Body.Up, has("body.up"))

			// Hand positions
			dr.recordVec3("lhand.pos", p.LHand.Position, has("lhand.pos"))
			dr.recordVec3("rhand.pos", p.RHand.Position, has("rhand.pos"))

			// Hand rotations
			dr.recordVec3("lhand.forward", p.LHand.Forward, has("lhand.forward"))
			dr.recordVec3("lhand.left", p.LHand.Left, has("lhand.left"))
			dr.recordVec3("lhand.up", p.LHand.Up, has("lhand.up"))
			dr.recordVec3("rhand.forward", p.RHand.Forward, has("rhand.forward"))
			dr.recordVec3("rhand.left", p.RHand.Left, has("rhand.left"))
			dr.recordVec3("rhand.up", p.RHand.Up, has("rhand.up"))

			if isZeroVec(p.LHand.Forward) || isZeroVec(p.RHand.Forward) {
				dr.ZeroHandRotations++
			}

			// Check hand-to-body distance
			bodyPos := p.Body.Position
			for _, hand := range [][3]float64{p.LHand.Position, p.RHand.Position} {
				dx := hand[0] - bodyPos[0]
				dy := hand[1] - bodyPos[1]
				dz := hand[2] - bodyPos[2]
				dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
				if dist > 2.0 {
					dr.HandFarFromBody++
				}
			}

			// Direction vector unit-ness check
			fwd := p.Body.Forward
			fwdMag := math.Sqrt(fwd[0]*fwd[0] + fwd[1]*fwd[1] + fwd[2]*fwd[2])
			if fwdMag > 0 && (fwdMag < 0.95 || fwdMag > 1.05) {
				dr.NonUnitQuaternions++ // misnomer but tracks direction vector quality
			}

			// Booleans
			dr.recordBool("stunned", p.Stunned, has("stunned"))
			dr.recordBool("invulnerable", p.Invulnerable, has("invulnerable"))
			dr.recordBool("blocking", p.Blocking, has("blocking"))
			dr.recordBool("possession", p.Possession, has("possession"))

			if p.HasDisc() {
				possessionHolders++
			}

			// Ping
			dr.recordFloat("ping", float64(p.Ping), has("ping"))

			// Stats
			dr.recordFloat("stats.goals", float64(p.Stats.Goals), has("stats.goals"))
			dr.recordFloat("stats.stuns", float64(p.Stats.Stuns), has("stats.stuns"))
			dr.recordFloat("stats.assists", float64(p.Stats.Assists), has("stats.assists"))
			dr.recordFloat("stats.saves", float64(p.Stats.Saves), has("stats.saves"))

			// Arena bounds check
			// CONFIRMED from real replay: X range [-5,+5], Y range [-4,+7], Z range [-77,+77]
			if math.Abs(p.Body.Position[0]) > 15 || math.Abs(p.Body.Position[1]) > 15 || math.Abs(p.Body.Position[2]) > 82 {
				dr.PositionOutOfBounds++
			}
		}
	}

	// Possession consistency
	if possessionHolders > 1 {
		dr.PossessionMultiplePlayers++
	}
	if possessionHolders > 0 && raw.Disc == nil {
		dr.PossessionWithNoDisc++
	}
	if raw.Disc != nil && possessionHolders > 0 {
		vel := raw.Disc.Velocity
		speed := math.Sqrt(vel[0]*vel[0] + vel[1]*vel[1] + vel[2]*vel[2])
		if speed > 5.0 {
			dr.DiscHeldButHighSpeed++
		}
	}

	dr.PlayerCount = len(dr.PlayersSeenIDs)
}

// FormatReport generates a human-readable diagnostic summary.
func (dr *DiagnosticReport) FormatReport() string {
	dr.mu.Lock()
	defer dr.mu.Unlock()

	var b strings.Builder
	b.WriteString("=== NEVR-Anticheat Adapter Diagnostic Report ===\n\n")
	b.WriteString(fmt.Sprintf("Snapshots recorded:            %d\n", dr.Snapshots))
	b.WriteString(fmt.Sprintf("Snapshots skipped (no teams):  %d\n", dr.SnapshotsNoTeams))
	b.WriteString(fmt.Sprintf("Snapshots skipped (duplicate): %d\n", dr.SnapshotsDuplicate))
	b.WriteString(fmt.Sprintf("Lines rejected (unparseable):  %d (bad timestamp prefix: %d)\n", dr.FramesRejected, dr.LinesBadTimestamp))
	if dr.SessionChanges > 0 {
		b.WriteString(fmt.Sprintf("Session id changes (matches):  %d (%d matches in file)\n", dr.SessionChanges, dr.SessionChanges+1))
	}
	b.WriteString(fmt.Sprintf("Player entries seen:           %d\n", dr.PlayerEntriesSeen))
	b.WriteString(fmt.Sprintf("Spectator entries dropped:     %d\n", dr.SpectatorEntriesDropped))
	b.WriteString(fmt.Sprintf("Player frames mapped:          %d\n", dr.FramesMapped))
	b.WriteString(fmt.Sprintf("Player frames rejected:        %d", dr.PlayerFramesRejected))
	if len(dr.RejectionsByField) > 0 {
		fields := make([]string, 0, len(dr.RejectionsByField))
		for f := range dr.RejectionsByField {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		parts := make([]string, 0, len(fields))
		for _, f := range fields {
			parts = append(parts, fmt.Sprintf("%s=%d", f, dr.RejectionsByField[f]))
		}
		b.WriteString(" (" + strings.Join(parts, ", ") + ")")
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("Players seen:                  %d\n", dr.PlayerCount))
	b.WriteString("\n")

	b.WriteString("--- Consistency Checks ---\n")
	b.WriteString(fmt.Sprintf("Non-unit direction vectors:  %d\n", dr.NonUnitQuaternions))
	b.WriteString(fmt.Sprintf("Zero hand rotations:         %d\n", dr.ZeroHandRotations))
	b.WriteString(fmt.Sprintf("Position out of bounds:      %d\n", dr.PositionOutOfBounds))
	b.WriteString(fmt.Sprintf("Hand far from body (>2m):    %d\n", dr.HandFarFromBody))
	b.WriteString(fmt.Sprintf("Possession with no disc:     %d\n", dr.PossessionWithNoDisc))
	b.WriteString(fmt.Sprintf("Multiple players possession: %d\n", dr.PossessionMultiplePlayers))
	b.WriteString(fmt.Sprintf("Disc held but speed > 5m/s:  %d\n", dr.DiscHeldButHighSpeed))
	if dr.MapperStats != nil {
		ms := dr.MapperStats
		b.WriteString(fmt.Sprintf("Basis quality (poses):       proper=%d reflected=%d non_orthonormal=%d degenerate=%d\n",
			ms.BasesProper, ms.BasesReflected, ms.BasesNonOrthonormal, ms.BasesDegenerate))
		b.WriteString(fmt.Sprintf("Hand tracking lost (poses):  %d\n", ms.HandTrackingLost))
		b.WriteString(fmt.Sprintf("Non-monotonic sample times:  %d\n", ms.NonMonotonicSamples))
		b.WriteString(fmt.Sprintf("Clock steps re-based:        %d\n", ms.ClockSteps))
	}
	b.WriteString("\n")

	b.WriteString("--- Field Presence ---\n")
	if dr.PresenceTracked {
		b.WriteString("(Missing = JSON key absent; Inactive = key present but zero/false)\n")
	} else {
		b.WriteString("(key presence not tracked on this path: Inactive = zero/false OR absent)\n")
	}
	b.WriteString(fmt.Sprintf("%-25s %8s %8s %8s %8s %s\n", "Field", "Present", "Inactive", "Missing", "Invalid", "Sample"))
	names := make([]string, 0, len(dr.FieldPresence))
	for name := range dr.FieldPresence {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fd := dr.FieldPresence[name]
		b.WriteString(fmt.Sprintf("%-25s %8d %8d %8d %8d %s\n",
			name, fd.Present, fd.Inactive, fd.Missing, fd.Invalid, fd.SampleValue))
	}

	return b.String()
}

// CompatibilityReport analyzes what detectors would be reliable with the observed telemetry.
//
// A required field counts as available when its JSON key was seen, even if the
// value was never true/non-zero in the sample (nobody stunned, no possession);
// such fields are listed as "inactive" rather than MISSING. When key presence
// was not tracked (RecordSession on a decoded struct) an inactive-only field is
// reported as UNVERIFIED because absence and inactivity are indistinguishable.
func (dr *DiagnosticReport) CompatibilityReport() string {
	dr.mu.Lock()
	defer dr.mu.Unlock()

	var b strings.Builder
	b.WriteString("=== Detector Compatibility Report ===\n\n")

	type detCheck struct {
		id       string
		requires []string
		status   string
	}

	checks := []detCheck{
		{"THROW_001-008", []string{"position", "lhand.pos", "rhand.pos", "disc.position", "disc.velocity", "possession"}, "holding_left/right are preferred for release timing when present"},
		{"BIO_001", []string{"lhand.forward", "rhand.forward"}, ""},
		{"BIO_002", []string{"lhand.pos", "rhand.pos"}, ""},
		{"BIO_003", []string{"lhand.pos", "rhand.pos", "position"}, ""},
		{"BIO_004", []string{"lhand.forward", "rhand.forward"}, ""},
		{"MOV_001", []string{"position"}, ""},
		{"MOV_002", []string{"position"}, ""},
		{"MOV_006", []string{"position", "velocity", "lhand.pos", "rhand.pos"}, "game velocity is subtracted from tracked-rig motion"},
		{"MOV_004", []string{"blocking"}, "Needs IsBoosting (ABSENT)"},
		{"MOV_005", []string{"blocking"}, "Needs IsBoosting (ABSENT)"},
		{"STATE_001", []string{"disc.position", "lhand.pos", "rhand.pos", "possession"}, ""},
		{"STATE_002", []string{"stunned"}, ""},
		{"STATE_003", []string{"blocking"}, ""},
		{"STATE_004", []string{"invulnerable"}, ""},
		{"STATE_005", []string{"blocking"}, ""},
		{"STATE_006", []string{"blue_points", "orange_points"}, ""},
		{"STATE_007", []string{"stats.stuns", "position"}, ""},
		{"PAT_005", []string{"lhand.pos", "rhand.pos", "position"}, ""},
	}

	for i := range checks {
		var missing, inactive, unverified []string
		for _, req := range checks[i].requires {
			fd, ok := dr.FieldPresence[req]
			switch {
			case !ok || fd.Seen() == 0:
				missing = append(missing, req)
			case fd.Present > 0:
				// available
			case dr.PresenceTracked && fd.Missing == 0:
				inactive = append(inactive, req)
			case dr.PresenceTracked:
				// key present in some snapshots, absent in others, never active
				inactive = append(inactive, fmt.Sprintf("%s(absent in %d/%d)", req, fd.Missing, fd.Seen()+fd.Missing))
			default:
				unverified = append(unverified, req)
			}
		}
		var status string
		switch {
		case len(missing) > 0:
			status = "MISSING: " + strings.Join(missing, ", ")
		case len(unverified) > 0:
			status = "UNVERIFIED (never non-zero; key presence not tracked): " + strings.Join(unverified, ", ")
		case len(inactive) > 0:
			status = "OK (inactive in sample: " + strings.Join(inactive, ", ") + ")"
		default:
			status = "OK"
		}
		if checks[i].status != "" {
			status += " — " + checks[i].status
		}
		checks[i].status = status
	}

	b.WriteString(fmt.Sprintf("%-20s %s\n", "Detector", "Status"))
	b.WriteString(strings.Repeat("-", 70) + "\n")
	for _, c := range checks {
		b.WriteString(fmt.Sprintf("%-20s %s\n", c.id, c.status))
	}

	return b.String()
}
