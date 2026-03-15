// Package adapter maps raw Echo VR telemetry into NEVR-Anticheat's internal format.
//
// This package handles two data sources:
//   1. The Echo VR HTTP API (/session endpoint at :6721)
//   2. The .echoreplay protobuf format (via nevr-common library)
//
// FIELD CONFIDENCE LEGEND:
//   CONFIRMED  = Field exists in documented Echo VR API, mapping is verified
//   LIKELY     = Field probably exists based on community tool source code, not verified against live data
//   INFERRED   = Field must be derived/computed from other confirmed fields
//   UNKNOWN    = Field existence or format is unconfirmed; safe default is used
//   ABSENT     = Field does not exist in Echo VR telemetry; always uses safe default
package adapter

// EchoVRSessionResponse represents the raw JSON from Echo VR's /session API endpoint.
// Based on community documentation (VRML, Spark, IgniteBot).
//
// Reference: The Echo VR API returns this at GET http://127.0.0.1:6721/session
//
// Field confidence annotations are in comments.
type EchoVRSessionResponse struct {
	// Match context
	SessionID    string `json:"sessionid"`          // CONFIRMED: unique match/session ID
	MatchType    string `json:"match_type"`         // CONFIRMED: "Echo_Arena", "Echo_Combat", etc.
	MapName      string `json:"map_name"`           // CONFIRMED: arena map name
	GameStatus   string `json:"game_status"`        // CONFIRMED: "playing", "round_start", "round_over", "pre_match", "post_match", "score"
	GameClock    float64 `json:"game_clock"`        // CONFIRMED: seconds remaining or elapsed
	GameClockDisplay string `json:"game_clock_display"` // CONFIRMED: "MM:SS" formatted
	PrivateMatch bool   `json:"private_match"`      // LIKELY: whether this is a private lobby
	ClientName   string `json:"client_name"`        // CONFIRMED: local player name

	// Disc state
	Disc *EchoVRDisc `json:"disc"` // CONFIRMED: disc object

	// Teams
	Teams []EchoVRTeam `json:"teams"` // CONFIRMED: array of 2 teams (blue=0, orange=1)
		// Note: some API versions use [2]EchoVRTeam, others []EchoVRTeam

	// Possession
	Possession *EchoVRPossession `json:"possession"` // LIKELY: array with [2] entries (team possession)
		// Format uncertain: may be [2]int, [2]bool, or nested object

	// Blue/Orange points
	BluePoints   int `json:"blue_points"`    // CONFIRMED
	OrangePoints int `json:"orange_points"`  // CONFIRMED

	// Last score info
	LastScore *EchoVRLastScore `json:"last_score"` // LIKELY
}

// EchoVRDisc represents the disc state in the Echo VR API.
type EchoVRDisc struct {
	Position [3]float64 `json:"position"` // CONFIRMED: [x, y, z] in meters
	Velocity [3]float64 `json:"velocity"` // CONFIRMED: [vx, vy, vz] in m/s

	// UNKNOWN: The API may include additional fields like "bounce_count".
	// These are not confirmed and are ignored during mapping.
	BounceCount int `json:"bounce_count"` // UNKNOWN
}

// EchoVRTeam represents a team in the Echo VR API.
type EchoVRTeam struct {
	TeamName string          `json:"team"`    // LIKELY: "BLUE TEAM" or "ORANGE TEAM"
	Players  []EchoVRPlayer  `json:"players"` // CONFIRMED: array of player objects
	Stats    *EchoVRTeamStats `json:"stats"`  // LIKELY
}

// EchoVRPlayer represents a player in the Echo VR API.
type EchoVRPlayer struct {
	// Identity
	Name     string `json:"name"`      // CONFIRMED: display name
	UserID   int64  `json:"userid"`    // CONFIRMED: unique numeric Oculus/Meta user ID
	PlayerID int    `json:"playerid"`  // CONFIRMED: in-match player slot index (0-based)
	Level    int    `json:"level"`     // LIKELY: player level

	// Spatial data
	// CONFIRMED: Position as [3]float64 [x, y, z] in meters, Y-up coordinate system.
	Position [3]float64 `json:"position"`

	// CONFIRMED: The API provides direction vectors, NOT a quaternion.
	// Format: [fx, fy, fz] for each direction.
	Forward [3]float64 `json:"forward"` // CONFIRMED: forward direction vector
	Left    [3]float64 `json:"left"`    // CONFIRMED: left direction vector
	Up      [3]float64 `json:"up"`      // CONFIRMED: up direction vector

	// Velocity
	// LIKELY: Player velocity. May not be present in all API versions.
	Velocity [3]float64 `json:"velocity"` // LIKELY

	// Hand/Controller data
	// CONFIRMED: Hand data is nested objects with position and direction vectors.
	// Format: {"pos": [x,y,z], "forward": [fx,fy,fz], "left": [lx,ly,lz], "up": [ux,uy,uz]}
	LHand EchoVRHand `json:"lhand"` // CONFIRMED: left hand/controller
	RHand EchoVRHand `json:"rhand"` // CONFIRMED: right hand/controller

	// Head data
	// LIKELY: Head tracking data, same format as hands.
	Head EchoVRHand `json:"head"` // LIKELY

	// Game state booleans
	Stunned      bool `json:"stunned"`      // CONFIRMED: player is stunned
	Invulnerable bool `json:"invulnerable"` // CONFIRMED: post-respawn invulnerability
	Possession   bool `json:"possession"`   // CONFIRMED: player holds the disc
	Blocking     bool `json:"blocking"`     // CONFIRMED: shield/block active

	// CONFIRMED: ping field exists in Echo VR API
	Ping int `json:"ping"` // CONFIRMED: player ping in ms

	// ABSENT: The following fields do NOT exist in the known Echo VR API:
	// - "boosting" (must be inferred from velocity changes)
	// - "immune" (use "invulnerable" instead)

	// Per-player stats
	Stats EchoVRPlayerStats `json:"stats"` // CONFIRMED: nested stats object
}

// EchoVRHand represents hand/head tracking data.
type EchoVRHand struct {
	Position [3]float64 `json:"pos"`     // CONFIRMED: [x, y, z] position
	Forward  [3]float64 `json:"forward"` // CONFIRMED: forward direction vector
	Left     [3]float64 `json:"left"`    // LIKELY: left direction vector
	Up       [3]float64 `json:"up"`      // LIKELY: up direction vector
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
	DiscSpeed     float64 `json:"disc_speed"`      // LIKELY: disc speed at goal
	Team          string  `json:"team"`             // LIKELY: scoring team
	GoalType      string  `json:"goal_type"`        // LIKELY: "INSIDE SHOT", "OUTSIDE SHOT", etc.
	PointAmount   int     `json:"point_amount"`     // LIKELY: 2 or 3
	DistanceThrown float64 `json:"distance_thrown"` // LIKELY
	PersonScored  string  `json:"person_scored"`    // LIKELY: player name
	AssistedBy    string  `json:"assisted_by"`      // LIKELY
}

// EchoVRPossession represents disc possession state.
// UNKNOWN: The exact format varies in community documentation.
// Some sources show [2]int (team possession), others show a nested object.
type EchoVRPossession struct {
	TeamIdx    int    `json:"-"` // which team has possession (0=blue, 1=orange, -1=none)
	PlayerName string `json:"-"` // which player holds the disc
}
