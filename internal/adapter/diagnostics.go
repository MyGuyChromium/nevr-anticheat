package adapter

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
)

// DiagnosticReport collects statistics about adapter mapping quality.
// Used during first live integration to validate telemetry assumptions.
type DiagnosticReport struct {
	mu sync.Mutex

	FramesMapped  int `json:"frames_mapped"`
	FramesRejected int `json:"frames_rejected"`
	PlayersSeenIDs map[string]bool `json:"-"`
	PlayerCount   int `json:"player_count"`

	// Per-field presence tracking
	FieldPresence map[string]*FieldDiagnostic `json:"field_presence"`

	// Quaternion quality
	NonUnitQuaternions int `json:"non_unit_quaternions"`
	ZeroHandRotations  int `json:"zero_hand_rotations"`

	// Possession/disc consistency
	PossessionWithNoDisc   int `json:"possession_with_no_disc"`
	PossessionMultiplePlayers int `json:"possession_multiple_players"`
	DiscHeldButHighSpeed   int `json:"disc_held_but_high_speed"`

	// Coordinate sanity
	PositionOutOfBounds    int `json:"position_out_of_bounds"`
	HandFarFromBody        int `json:"hand_far_from_body"`
	NegativeTimestamps     int `json:"negative_timestamps"`

	// Sample raw payloads for unknown structures (first 3)
	SamplePayloads []string `json:"sample_payloads,omitempty"`
	maxSamples     int
}

// FieldDiagnostic tracks per-field statistics.
type FieldDiagnostic struct {
	Present     int     `json:"present"`
	Missing     int     `json:"missing"`     // zero-value or null
	Invalid     int     `json:"invalid"`     // NaN, Inf, out of range
	MinValue    float64 `json:"min_value,omitempty"`
	MaxValue    float64 `json:"max_value,omitempty"`
	SampleValue string  `json:"sample_value,omitempty"` // first non-zero value seen
}

// NewDiagnosticReport creates a diagnostic report collector.
func NewDiagnosticReport() *DiagnosticReport {
	return &DiagnosticReport{
		PlayersSeenIDs: make(map[string]bool),
		FieldPresence:  make(map[string]*FieldDiagnostic),
		maxSamples:     3,
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

func (dr *DiagnosticReport) recordFloat(name string, value float64) {
	fd := dr.getField(name)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		fd.Invalid++
		return
	}
	if value == 0 {
		fd.Missing++
	} else {
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
}

func (dr *DiagnosticReport) recordBool(name string, value bool) {
	fd := dr.getField(name)
	if value {
		fd.Present++
	} else {
		fd.Missing++ // false counts as "not active" not truly missing
	}
}

func (dr *DiagnosticReport) recordVec3(name string, v [3]float64) {
	fd := dr.getField(name)
	if v[0] == 0 && v[1] == 0 && v[2] == 0 {
		fd.Missing++
	} else if math.IsNaN(v[0]) || math.IsNaN(v[1]) || math.IsNaN(v[2]) {
		fd.Invalid++
	} else {
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
}

// RecordSession analyzes a raw session payload for diagnostics.
func (dr *DiagnosticReport) RecordSession(raw *EchoVRSessionResponse) {
	dr.mu.Lock()
	defer dr.mu.Unlock()

	if raw == nil {
		return
	}

	// Capture sample payload
	if len(dr.SamplePayloads) < dr.maxSamples {
		data, _ := json.Marshal(raw)
		if len(data) > 2000 {
			data = data[:2000]
		}
		dr.SamplePayloads = append(dr.SamplePayloads, string(data))
	}

	// Track scores
	dr.recordFloat("blue_points", float64(raw.BluePoints))
	dr.recordFloat("orange_points", float64(raw.OrangePoints))
	dr.recordFloat("game_clock", raw.GameClock)

	// Disc
	if raw.Disc != nil {
		dr.recordVec3("disc.position", raw.Disc.Position)
		dr.recordVec3("disc.velocity", raw.Disc.Velocity)
	} else {
		dr.getField("disc").Missing++
	}

	// Count possession holders per frame
	possessionHolders := 0

	for _, team := range raw.Teams {
		for _, p := range team.Players {
			dr.FramesMapped++
			pid := fmt.Sprintf("echovr:%d", p.UserID)
			dr.PlayersSeenIDs[pid] = true

			// Position
			dr.recordVec3("position", p.Position)

			// Direction vectors
			dr.recordVec3("forward", p.Forward)
			dr.recordVec3("left", p.Left)
			dr.recordVec3("up", p.Up)

			// Hand positions
			dr.recordVec3("lhand.pos", p.LHand.Position)
			dr.recordVec3("rhand.pos", p.RHand.Position)

			// Hand rotations
			dr.recordVec3("lhand.forward", p.LHand.Forward)
			dr.recordVec3("lhand.left", p.LHand.Left)
			dr.recordVec3("lhand.up", p.LHand.Up)
			dr.recordVec3("rhand.forward", p.RHand.Forward)
			dr.recordVec3("rhand.left", p.RHand.Left)
			dr.recordVec3("rhand.up", p.RHand.Up)

			if isZeroVec(p.LHand.Forward) || isZeroVec(p.RHand.Forward) {
				dr.ZeroHandRotations++
			}

			// Check hand-to-body distance
			bodyPos := p.Position
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
			fwd := p.Forward
			fwdMag := math.Sqrt(fwd[0]*fwd[0] + fwd[1]*fwd[1] + fwd[2]*fwd[2])
			if fwdMag > 0 && (fwdMag < 0.95 || fwdMag > 1.05) {
				dr.NonUnitQuaternions++ // misnomer but tracks direction vector quality
			}

			// Booleans
			dr.recordBool("stunned", p.Stunned)
			dr.recordBool("invulnerable", p.Invulnerable)
			dr.recordBool("blocking", p.Blocking)
			dr.recordBool("possession", p.Possession)

			if p.Possession {
				possessionHolders++
			}

			// Ping
			dr.recordFloat("ping", float64(p.Ping))

			// Stats
			dr.recordFloat("stats.goals", float64(p.Stats.Goals))
			dr.recordFloat("stats.stuns", float64(p.Stats.Stuns))
			dr.recordFloat("stats.assists", float64(p.Stats.Assists))
			dr.recordFloat("stats.saves", float64(p.Stats.Saves))

			// Arena bounds check
			if math.Abs(p.Position[0]) > 45 || math.Abs(p.Position[1]) > 20 || math.Abs(p.Position[2]) > 20 {
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
	b.WriteString(fmt.Sprintf("Frames mapped:     %d\n", dr.FramesMapped))
	b.WriteString(fmt.Sprintf("Frames rejected:   %d\n", dr.FramesRejected))
	b.WriteString(fmt.Sprintf("Players seen:      %d\n", dr.PlayerCount))
	b.WriteString("\n")

	b.WriteString("--- Consistency Checks ---\n")
	b.WriteString(fmt.Sprintf("Non-unit direction vectors:  %d\n", dr.NonUnitQuaternions))
	b.WriteString(fmt.Sprintf("Zero hand rotations:         %d\n", dr.ZeroHandRotations))
	b.WriteString(fmt.Sprintf("Position out of bounds:      %d\n", dr.PositionOutOfBounds))
	b.WriteString(fmt.Sprintf("Hand far from body (>2m):    %d\n", dr.HandFarFromBody))
	b.WriteString(fmt.Sprintf("Possession with no disc:     %d\n", dr.PossessionWithNoDisc))
	b.WriteString(fmt.Sprintf("Multiple players possession: %d\n", dr.PossessionMultiplePlayers))
	b.WriteString(fmt.Sprintf("Disc held but speed > 5m/s:  %d\n", dr.DiscHeldButHighSpeed))
	b.WriteString("\n")

	b.WriteString("--- Field Presence ---\n")
	b.WriteString(fmt.Sprintf("%-25s %8s %8s %8s %s\n", "Field", "Present", "Missing", "Invalid", "Sample"))
	for name, fd := range dr.FieldPresence {
		b.WriteString(fmt.Sprintf("%-25s %8d %8d %8d %s\n",
			name, fd.Present, fd.Missing, fd.Invalid, fd.SampleValue))
	}

	return b.String()
}

// CompatibilityReport analyzes what detectors would be reliable with the observed telemetry.
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
		{"THROW_001-008", []string{"position", "lhand.pos", "rhand.pos", "disc.position", "disc.velocity", "possession"}, ""},
		{"BIO_001", []string{"lhand.forward", "rhand.forward"}, ""},
		{"BIO_002", []string{"lhand.pos", "rhand.pos"}, ""},
		{"BIO_003", []string{"lhand.pos", "rhand.pos", "position"}, ""},
		{"BIO_004", []string{"lhand.forward", "rhand.forward"}, ""},
		{"MOV_001", []string{"position"}, ""},
		{"MOV_002", []string{"position"}, ""},
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
		allPresent := true
		for _, req := range checks[i].requires {
			fd, ok := dr.FieldPresence[req]
			if !ok || fd.Present == 0 {
				allPresent = false
				if checks[i].status == "" {
					checks[i].status = fmt.Sprintf("MISSING: %s", req)
				} else {
					checks[i].status += fmt.Sprintf(", %s", req)
				}
			}
		}
		if allPresent && checks[i].status == "" {
			checks[i].status = "OK"
		}
	}

	b.WriteString(fmt.Sprintf("%-20s %s\n", "Detector", "Status"))
	b.WriteString(strings.Repeat("-", 70) + "\n")
	for _, c := range checks {
		b.WriteString(fmt.Sprintf("%-20s %s\n", c.id, c.status))
	}

	return b.String()
}
