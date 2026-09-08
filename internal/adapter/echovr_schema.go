// Package adapter maps raw Echo VR telemetry into NEVR-Anticheat's internal format.
//
// This package handles two data sources:
//  1. The Echo VR HTTP API (/session endpoint at :6721)
//  2. The .echoreplay protobuf format (via nevr-common library)
//
// FIELD CONFIDENCE LEGEND:
//
//	CONFIRMED  = Field exists in documented Echo VR API, mapping is verified
//	LIKELY     = Field probably exists based on community tool source code, not verified against live data
//	INFERRED   = Field must be derived/computed from other confirmed fields
//	UNKNOWN    = Field existence or format is unconfirmed; safe default is used
//	ABSENT     = Field does not exist in Echo VR telemetry; always uses safe default
package adapter

import "encoding/json"

// EchoVRSessionResponse represents the raw JSON from Echo VR's /session API endpoint.
// Based on community documentation (VRML, Spark, IgniteBot).
//
// Reference: The Echo VR API returns this at GET http://127.0.0.1:6721/session
//
// Field confidence annotations are in comments.
type EchoVRSessionResponse struct {
	// Match context
	SessionID        string  `json:"sessionid"`          // CONFIRMED: unique match/session ID
	MatchType        string  `json:"match_type"`         // CONFIRMED: "Echo_Arena", "Echo_Combat", etc.
	MapName          string  `json:"map_name"`           // CONFIRMED: arena map name
	GameStatus       string  `json:"game_status"`        // CONFIRMED: "playing", "round_start", "round_over", "pre_match", "post_match", "score"
	GameClock        float64 `json:"game_clock"`         // CONFIRMED: seconds remaining or elapsed
	GameClockDisplay string  `json:"game_clock_display"` // CONFIRMED: "MM:SS" formatted
	PrivateMatch     bool    `json:"private_match"`      // LIKELY: whether this is a private lobby
	ClientName       string  `json:"client_name"`        // CONFIRMED: local player name

	// Disc state
	Disc *EchoVRDisc `json:"disc"` // CONFIRMED: disc object

	// Teams
	// LIKELY: community documentation (Spark, IgniteBot) describes three
	// entries — "BLUE TEAM", "ORANGE TEAM" and "SPECTATORS" — and replays are
	// typically recorded by a client that appears in the third entry. The
	// mapper selects teams by TeamName and never by index alone; spectators
	// and any other team are dropped from telemetry (see mappedTeamName).
	Teams []EchoVRTeam `json:"teams"`

	// CONFIRMED from real replay: top-level "possession" is [team_idx, player_idx] array.
	// Per-player "possession" boolean is the reliable field for disc ownership.
	// We ignore this top-level array and use the per-player boolean instead.
	Possession json.RawMessage `json:"possession"` // CONFIRMED: [int, int] array, ignored

	// Blue/Orange points
	BluePoints   int `json:"blue_points"`   // CONFIRMED
	OrangePoints int `json:"orange_points"` // CONFIRMED

	// Last score info
	LastScore *EchoVRLastScore `json:"last_score"` // LIKELY

	// CONFIRMED: Echo VR's engine-authored breakdown of the local client's
	// latest throw. It changes on that player's release and is not populated
	// for remote players.
	LastThrow *EchoVRLastThrow `json:"last_throw"`
}

// EchoVRLastThrow mirrors the top-level last_throw object in /session and
// .echoreplay snapshots. Field names match EchoTools/nevr-proto ThrowDetails.
type EchoVRLastThrow struct {
	ArmSpeed                float64 `json:"arm_speed"`
	TotalSpeed              float64 `json:"total_speed"`
	OffAxisSpinDeg          float64 `json:"off_axis_spin_deg"`
	WristThrowPenalty       float64 `json:"wrist_throw_penalty"`
	RotPerSec               float64 `json:"rot_per_sec"`
	PotentialSpeedFromRot   float64 `json:"pot_speed_from_rot"`
	SpeedFromArm            float64 `json:"speed_from_arm"`
	SpeedFromMovement       float64 `json:"speed_from_movement"`
	SpeedFromWrist          float64 `json:"speed_from_wrist"`
	WristAlignToThrowDeg    float64 `json:"wrist_align_to_throw_deg"`
	ThrowAlignToMovementDeg float64 `json:"throw_align_to_movement_deg"`
	OffAxisPenalty          float64 `json:"off_axis_penalty"`
	ThrowMovePenalty        float64 `json:"throw_move_penalty"`
}

// EchoVRDisc represents the disc state in the Echo VR API.
type EchoVRDisc struct {
	Position         [3]float64 `json:"position"` // CONFIRMED: [x, y, z] in meters
	Velocity         [3]float64 `json:"velocity"` // CONFIRMED: [vx, vy, vz] in m/s
	positionObserved *bool
	velocityObserved *bool

	// Some sources omit the counter. A pointer retains that absence rather
	// than inventing a zero-bounce observation when decoding or reserializing.
	BounceCount *int `json:"bounce_count,omitempty"`
}

// EchoVRTeam represents a team in the Echo VR API.
type EchoVRTeam struct {
	TeamName string           `json:"team"`    // LIKELY: "BLUE TEAM" or "ORANGE TEAM"
	Players  []EchoVRPlayer   `json:"players"` // CONFIRMED: array of player objects
	Stats    *EchoVRTeamStats `json:"stats"`   // LIKELY
}

// EchoVRPlayer represents a player in the Echo VR API.
type EchoVRPlayer struct {
	// Identity
	Name     string `json:"name"`     // CONFIRMED: display name
	UserID   int64  `json:"userid"`   // CONFIRMED: unique numeric Oculus/Meta user ID
	PlayerID int    `json:"playerid"` // CONFIRMED: in-match player slot index (0-based)
	Level    int    `json:"level"`    // LIKELY: player level

	// CONFIRMED from real replay data: players do NOT have a top-level "position" field.
	// Position is under "body.position" and "head.position" (identical in all observed frames).
	// Body and Head are nested objects with position + direction vectors.
	Body EchoVRBodyHead `json:"body"` // CONFIRMED: body position + orientation
	Head EchoVRBodyHead `json:"head"` // CONFIRMED: head position + orientation (== body.position in practice)

	// CONFIRMED: Player velocity as top-level [3]float64.
	Velocity         [3]float64 `json:"velocity"` // CONFIRMED
	velocityObserved *bool

	// CONFIRMED: Hand data is nested objects with pos + direction vectors.
	LHand EchoVRHand `json:"lhand"` // CONFIRMED: left hand/controller
	RHand EchoVRHand `json:"rhand"` // CONFIRMED: right hand/controller

	// Game state booleans
	Stunned      bool `json:"stunned"`      // CONFIRMED: player is stunned
	Invulnerable bool `json:"invulnerable"` // CONFIRMED: post-respawn invulnerability
	Possession   bool `json:"possession"`   // CONFIRMED: current/last carrier; may remain true after release
	Blocking     bool `json:"blocking"`     // CONFIRMED: shield/block active

	// CONFIRMED from a real Spark recording: what each hand holds, "none",
	// "disc" (both hands report "disc" while the player carries it), "geo"
	// (arena geometry) or another player's number. Unlike Possession, which
	// stays true for the last carrier until someone else grabs the disc,
	// these drop to "none" at the release, so they mark the throw.
	HoldingLeft  string `json:"holding_left"`  // CONFIRMED
	HoldingRight string `json:"holding_right"` // CONFIRMED

	// CONFIRMED: ping field exists in Echo VR API
	Ping int `json:"ping"` // CONFIRMED: player ping in ms

	// ABSENT: The following fields do NOT exist in the known Echo VR API:
	// - "boosting" (must be inferred from velocity changes)
	// - "immune" (use "invulnerable" instead)

	// Per-player stats
	Stats EchoVRPlayerStats `json:"stats"` // CONFIRMED: nested stats object
}

// EchoVRBodyHead represents body or head tracking data.
// CONFIRMED from real replay: uses "position" (not "pos") as key.
type EchoVRBodyHead struct {
	Position [3]float64 `json:"position"` // CONFIRMED
	Forward  [3]float64 `json:"forward"`  // CONFIRMED
	Left     [3]float64 `json:"left"`     // CONFIRMED
	Up       [3]float64 `json:"up"`       // CONFIRMED
}

// EchoVRHand represents hand/controller tracking data.
// CONFIRMED from real replay: uses "pos" (not "position") as key.
type EchoVRHand struct {
	Position [3]float64 `json:"pos"`     // CONFIRMED: [x, y, z] position
	Forward  [3]float64 `json:"forward"` // CONFIRMED: forward direction vector
	Left     [3]float64 `json:"left"`    // CONFIRMED: left direction vector
	Up       [3]float64 `json:"up"`      // CONFIRMED: up direction vector
}

// EchoVRPlayerStats contains per-player match statistics.
type EchoVRPlayerStats struct {
	Points        int     `json:"points"`          // CONFIRMED: total points scored
	Goals         int     `json:"goals"`           // CONFIRMED
	Assists       int     `json:"assists"`         // CONFIRMED
	Saves         int     `json:"saves"`           // CONFIRMED
	Steals        int     `json:"steals"`          // CONFIRMED
	Stuns         int     `json:"stuns"`           // CONFIRMED: stuns dealt
	Passes        int     `json:"passes"`          // LIKELY
	Catches       int     `json:"catches"`         // LIKELY
	Blocks        int     `json:"blocks"`          // LIKELY
	Interceptions int     `json:"interceptions"`   // LIKELY
	Possession    float64 `json:"possession_time"` // LIKELY: seconds of disc possession
	ShotsOnGoal   int     `json:"shots_taken"`     // LIKELY
}

// EchoVRTeamStats contains per-team statistics.
type EchoVRTeamStats struct {
	Points int `json:"points"` // LIKELY
	// Additional team-level stats may exist but are unconfirmed
}

// EchoVRLastScore contains info about the last goal scored.
type EchoVRLastScore struct {
	DiscSpeed      float64 `json:"disc_speed"`      // CONFIRMED from a real replay: disc speed at goal (m/s)
	Team           string  `json:"team"`            // CONFIRMED: "blue" / "orange"
	GoalType       string  `json:"goal_type"`       // CONFIRMED: "INSIDE SHOT", "LONG SHOT", ...
	PointAmount    int     `json:"point_amount"`    // CONFIRMED: 2 or 3
	DistanceThrown float64 `json:"distance_thrown"` // CONFIRMED: metres
	PersonScored   string  `json:"person_scored"`   // CONFIRMED: player name, "[INVALID]" once the player left
	AssistedBy     string  `json:"assisted_by"`     // LIKELY: community docs spell the assist this way
	AssistScored   string  `json:"assist_scored"`   // CONFIRMED: the real API's spelling ("[INVALID]" when none)
}

// invalidName is what the API reports for a player it can no longer name.
const invalidName = "[INVALID]"

// cleanName returns name, or "" when it is empty or the API's "[INVALID]".
func cleanName(name string) string {
	if name == invalidName {
		return ""
	}
	return name
}

// Scorer returns the scorer's name, "" when unknown.
func (ls *EchoVRLastScore) Scorer() string { return cleanName(ls.PersonScored) }

// Assist returns the assisting player's name under either spelling, "" when
// the goal was unassisted or the API reported "[INVALID]".
func (ls *EchoVRLastScore) Assist() string {
	if n := cleanName(ls.AssistScored); n != "" {
		return n
	}
	return cleanName(ls.AssistedBy)
}

// HoldsDisc reports whether either hand carries the disc (see HoldingLeft).
func (p *EchoVRPlayer) HoldsDisc() bool {
	return p.HoldingLeft == "disc" || p.HoldingRight == "disc"
}

// HasHoldingFields reports whether the snapshot carries the holding_left /
// holding_right fields at all (they are absent from some synthetic or older
// sources, whose only release signal is the Possession boolean).
func (p *EchoVRPlayer) HasHoldingFields() bool {
	return p.HoldingLeft != "" || p.HoldingRight != ""
}

// HasDisc uses the hand-held item fields when the source supplies them. In
// real Spark replays the older Possession flag stays true for the last carrier
// after release, while holding_left / holding_right change from "disc" to
// "none" on the release tick. Older sources without holding fields fall back
// to Possession.
func (p *EchoVRPlayer) HasDisc() bool {
	if p.HasHoldingFields() {
		return p.HoldsDisc()
	}
	return p.Possession
}

// Note: Top-level "possession" is a [2]int array (team_idx, player_idx).
// CONFIRMED from real replay data. Parsed as json.RawMessage and ignored
// in favor of per-player "possession" booleans.
