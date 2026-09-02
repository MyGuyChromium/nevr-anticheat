package model

import "time"

// MatchContext holds match-level metadata passed to all detectors.
type MatchContext struct {
	MatchID         string            `json:"match_id"`
	Map             string            `json:"map"`
	GameMode        string            `json:"game_mode"`
	IsRanked        bool              `json:"is_ranked"`
	IsPrivate       bool              `json:"is_private"`
	StartTime       time.Time         `json:"start_time"`
	Duration        time.Duration     `json:"duration"`
	PlayerIDs       []string          `json:"player_ids"`
	TeamAssignments map[string]string `json:"team_assignments"`
	TickRate        float64           `json:"tick_rate"`
	Source          string            `json:"source"`
	ReplayFile      string            `json:"replay_file,omitempty"`
	ServerRegion    string            `json:"server_region,omitempty"`
	Physics         PhysicsConstants  `json:"physics"`
}

// IsActivePhase returns true if the game phase is active gameplay.
//
// Echo VR reports overtime as "sudden_death" (with "pre_sudden_death" and
// "post_sudden_death" transitions around it). Sudden death is live play and
// must be covered by detectors; the pre/post transitions teleport players to
// spawn exactly like round_start/round_over and stay inactive.
func (mc *MatchContext) IsActivePhase(phase string) bool {
	switch phase {
	case "playing", "round", "overtime", "sudden_death", "":
		return true
	default:
		return false
	}
}

// PhysicsConstants holds the game physics parameters for a match.
type PhysicsConstants struct {
	DiscSpeedCap   float64 `json:"disc_speed_cap" toml:"disc_speed_cap"`
	BoostSpeedCap  float64 `json:"boost_speed_cap" toml:"boost_speed_cap"`
	MaxPlayerSpeed float64 `json:"max_player_speed" toml:"max_player_speed"`
	MaxThrowSpeed  float64 `json:"max_throw_speed" toml:"max_throw_speed"`
	StunDuration   float64 `json:"stun_duration" toml:"stun_duration"`
	ShieldCooldown float64 `json:"shield_cooldown" toml:"shield_cooldown"`
	ImmunityWindow float64 `json:"immunity_window" toml:"immunity_window"`
	GrabRange      float64 `json:"grab_range" toml:"grab_range"`
	ArenaLength    float64 `json:"arena_length" toml:"arena_length"`
	ArenaWidth     float64 `json:"arena_width" toml:"arena_width"`
	ArenaHeight    float64 `json:"arena_height" toml:"arena_height"`
	GoalRadius     float64 `json:"goal_radius" toml:"goal_radius"`
}

// DefaultPhysics returns the default physics constants for Echo VR.
func DefaultPhysics() PhysicsConstants {
	return PhysicsConstants{
		DiscSpeedCap:   18.7,
		BoostSpeedCap:  5.0,
		MaxPlayerSpeed: 55.0,
		MaxThrowSpeed:  20.0,
		StunDuration:   3.0,
		ShieldCooldown: 5.0,
		ImmunityWindow: 1.5,
		GrabRange:      0.8,
		// CONFIRMED from real replay: Z range is [-77, +77], X range is [-5, +5], Y range is [-4, +7].
		// Arena is elongated on Z axis. Goals at Z ≈ ±36.
		ArenaLength: 154.0, // Z axis (was 80 — confirmed wrong from real data)
		ArenaWidth:  15.0,  // X axis
		ArenaHeight: 15.0,  // Y axis
		GoalRadius:  1.0,
	}
}

// MatchSummary holds the end-of-match summary.
type MatchSummary struct {
	MatchID              string                        `json:"match_id"`
	Map                  string                        `json:"map"`
	GameMode             string                        `json:"game_mode"`
	IsRanked             bool                          `json:"is_ranked"`
	StartTime            time.Time                     `json:"start_time"`
	Duration             time.Duration                 `json:"duration"`
	FrameCount           int                           `json:"frame_count"`
	InvalidFrameCount    int                           `json:"invalid_frame_count"`
	AverageTickRate      float64                       `json:"average_tick_rate"`
	PlayerSummaries      map[string]PlayerMatchSummary `json:"player_summaries"`
	TotalDetectionEvents int                           `json:"total_detection_events"`
	DetectionsByDetector map[string]int                `json:"detections_by_detector"`
	FlaggedPlayers       []string                      `json:"flagged_players"`
	AnalysisDuration     time.Duration                 `json:"analysis_duration"`
	Source               string                        `json:"source"`
	ReplayFile           string                        `json:"replay_file,omitempty"`
}

// PlayerMatchSummary holds per-player statistics from a single match.
type PlayerMatchSummary struct {
	PlayerID            string  `json:"player_id"`
	Team                string  `json:"team"`
	DetectionEventCount int     `json:"detection_event_count"`
	SuspicionScoreDelta float64 `json:"suspicion_score_delta"`
	ThrowCount          int     `json:"throw_count"`
	MaxThrowSpeed       float64 `json:"max_throw_speed"`
	AverageThrowSpeed   float64 `json:"average_throw_speed"`
	MaxPlayerSpeed      float64 `json:"max_player_speed"`
	AveragePlayerSpeed  float64 `json:"average_player_speed"`
	PossessionTime      float64 `json:"possession_time"`
	StunCount           int     `json:"stun_count"`
	Goals               int     `json:"goals"`
	Assists             int     `json:"assists"`
	Saves               int     `json:"saves"`
	Steals              int     `json:"steals"`
	AveragePingMs       float64 `json:"average_ping_ms"`
}
