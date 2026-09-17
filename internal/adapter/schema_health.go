package adapter

import (
	"fmt"
	"sort"
	"strings"
)

// TelemetryWarning is an actionable schema or source-quality finding.
type TelemetryWarning struct {
	Level   string `json:"level"` // info, warning, error
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Count   int    `json:"count,omitempty"`
	Message string `json:"message"`
}

var expectedTopKeys = stringSet(
	"sessionid", "match_type", "map_name", "game_status", "game_clock", "game_clock_display",
	"private_match", "client_name", "disc", "teams", "possession", "blue_points", "orange_points",
	"last_score", "last_throw",
)
var expectedDiscKeys = stringSet("position", "velocity", "bounce_count")
var expectedLastThrowKeys = stringSet(
	"arm_speed", "total_speed", "off_axis_spin_deg", "wrist_throw_penalty", "rot_per_sec",
	"pot_speed_from_rot", "speed_from_arm", "speed_from_movement", "speed_from_wrist",
	"wrist_align_to_throw_deg", "throw_align_to_movement_deg", "off_axis_penalty", "throw_move_penalty",
)
var expectedPlayerKeys = stringSet(
	"name", "userid", "playerid", "level", "body", "head", "velocity", "lhand", "rhand",
	"stunned", "invulnerable", "possession", "blocking", "holding_left", "holding_right", "ping", "stats",
	"body.position", "body.forward", "body.left", "body.up", "head.position", "head.forward", "head.left", "head.up",
	"lhand.pos", "lhand.forward", "lhand.left", "lhand.up", "rhand.pos", "rhand.forward", "rhand.left", "rhand.up",
	"stats.points", "stats.goals", "stats.assists", "stats.saves", "stats.steals", "stats.stuns", "stats.passes",
	"stats.catches", "stats.blocks", "stats.interceptions", "stats.possession_time", "stats.shots_taken",
)

func stringSet(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func (dr *DiagnosticReport) recordUnknownFields(sp *sessionPresence) {
	if sp == nil {
		return
	}
	for key := range sp.top {
		if key == "_nevr_tape" && sp.nativeProjection {
			continue
		}
		if !expectedTopKeys[key] {
			dr.UnknownFields["top."+key]++
		}
	}
	for key := range sp.disc {
		if !expectedDiscKeys[key] {
			dr.UnknownFields["disc."+key]++
		}
	}
	for key := range sp.lastThrow {
		if !expectedLastThrowKeys[key] {
			dr.UnknownFields["last_throw."+key]++
		}
	}
	for _, team := range sp.players {
		for _, player := range team {
			for key := range player {
				if !expectedPlayerKeys[key] {
					dr.UnknownFields["player."+key]++
				}
			}
		}
	}
}

// HealthWarnings turns raw field-presence counters into a compact automatic
// compatibility verdict. A warning never changes a detection or score.
func (dr *DiagnosticReport) HealthWarnings() []TelemetryWarning {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	var out []TelemetryWarning
	if dr.Snapshots == 0 {
		return []TelemetryWarning{{Level: "error", Code: "no_raw_snapshots", Message: "No raw Echo/Spark snapshots were available for schema validation."}}
	}
	if !dr.PresenceTracked {
		out = append(out, TelemetryWarning{Level: "warning", Code: "presence_untracked", Message: "Field presence was not tracked, so absent and inactive values cannot be distinguished."})
	}
	if dr.NativeProjectionSnapshots > 0 {
		out = append(out, TelemetryWarning{Level: "info", Code: "native_projection", Count: dr.NativeProjectionSnapshots, Message: "Native tape schema counters describe a derived compatibility projection, not original HTTP field presence. Zero scalar values do not prove an observation; original native records are retained. This is not an authenticity or complete-stream validation."})
	}
	required := []string{"sessionid", "game_status", "position", "velocity", "lhand.pos", "rhand.pos", "disc.position", "disc.velocity"}
	for _, field := range required {
		fd := dr.FieldPresence[field]
		if fd == nil || fd.Seen() == 0 {
			out = append(out, TelemetryWarning{Level: "error", Code: "required_field_unavailable", Field: field, Message: fmt.Sprintf("Required telemetry field %s was never available; dependent detectors are not trustworthy for this replay.", field)})
			continue
		}
		total := fd.Seen() + fd.Missing
		if dr.PresenceTracked && total > 0 && fd.Missing*20 > total { // >5%
			out = append(out, TelemetryWarning{Level: "warning", Code: "field_intermittent", Field: field, Count: fd.Missing, Message: fmt.Sprintf("Telemetry field %s was absent in %d of %d observations.", field, fd.Missing, total)})
		}
		if fd.Invalid > 0 {
			out = append(out, TelemetryWarning{Level: "error", Code: "field_invalid", Field: field, Count: fd.Invalid, Message: fmt.Sprintf("Telemetry field %s contained %d non-finite value(s).", field, fd.Invalid)})
		}
	}
	for _, field := range []string{"position", "velocity", "lhand.pos", "rhand.pos"} {
		if fd := dr.FieldPresence[field]; fd != nil && fd.Present == 0 && fd.Inactive > 0 {
			out = append(out, TelemetryWarning{Level: "warning", Code: "field_always_inactive", Field: field, Count: fd.Inactive, Message: fmt.Sprintf("Telemetry field %s was present but never non-zero; check for a frozen or incomplete recording.", field)})
		}
	}
	if dr.FramesRejected > 0 || dr.PlayerFramesRejected > 0 {
		out = append(out, TelemetryWarning{Level: "warning", Code: "mapping_loss", Count: dr.FramesRejected + dr.PlayerFramesRejected, Message: fmt.Sprintf("Mapping rejected %d replay line(s) and %d player frame(s).", dr.FramesRejected, dr.PlayerFramesRejected)})
	}
	if dr.PossessionMultiplePlayers > 0 || dr.PossessionWithNoDisc > 0 {
		out = append(out, TelemetryWarning{Level: "warning", Code: "possession_inconsistent", Count: dr.PossessionMultiplePlayers + dr.PossessionWithNoDisc, Message: "Disc possession was internally inconsistent in one or more snapshots."})
	}
	if warning, ok := dr.constantPingWarning(); ok {
		out = append(out, warning)
	}
	if len(dr.UnknownFields) > 0 {
		keys := make([]string, 0, len(dr.UnknownFields))
		for key := range dr.UnknownFields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		const maxShown = 12
		shown := keys
		if len(shown) > maxShown {
			shown = shown[:maxShown]
		}
		message := "Unrecognized telemetry keys were preserved in raw data but ignored by the adapter: " + strings.Join(shown, ", ")
		if len(keys) > len(shown) {
			message += fmt.Sprintf(" (and %d more)", len(keys)-len(shown))
		}
		out = append(out, TelemetryWarning{Level: "info", Code: "unknown_fields", Count: len(keys), Message: message + "."})
	}
	return out
}

// constantPingMinSnapshots is the shortest recording (in snapshots) on which
// an unchanging ping is called out: 900 snapshots is 30 s at 30 Hz and 60 s
// at 15 Hz. A real round-trip time, reported in whole milliseconds for two
// or more players, does not stay one identical number that long.
const constantPingMinSnapshots = 900

// constantPingWarning reports a recording in which every ping observation of
// every player is one identical value. A game server that measures no
// round-trip time can advertise a fixed figure for every entrant; ping-based
// tolerances and high-ping guards then mean nothing for that recording.
//
// Diagnostic only: it changes no detector input, tolerance or score, and it
// is not a statement about any player. Short clips and single-player
// recordings are left alone because a constant value proves nothing there.
// The caller holds dr.mu.
func (dr *DiagnosticReport) constantPingWarning() (TelemetryWarning, bool) {
	fd := dr.FieldPresence["ping"]
	if fd == nil || fd.Invalid > 0 || dr.Snapshots < constantPingMinSnapshots || dr.PlayerCount < 2 {
		return TelemetryWarning{}, false
	}
	observations := fd.Present + fd.Inactive
	if observations < 2*constantPingMinSnapshots {
		return TelemetryWarning{}, false // fewer than two players' worth of samples
	}
	var value float64
	switch {
	case fd.Present == 0:
		value = 0 // every observation was zero
	case fd.Inactive == 0 && fd.MinValue == fd.MaxValue:
		value = fd.MinValue
	default:
		return TelemetryWarning{}, false
	}
	return TelemetryWarning{Level: "warning", Code: "ping_constant", Field: "ping", Count: observations,
		Message: fmt.Sprintf("Ping appears constant: all %d observations across %d players were %g ms. Ping-based tolerances and high-ping guards are not meaningful for this recording. This is a telemetry-quality note, not a finding about any player.", observations, dr.PlayerCount, value)}, true
}
