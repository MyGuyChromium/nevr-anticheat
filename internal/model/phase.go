package model

import "strings"

// Game phases produced by PhaseNormalizer in addition to the statuses the
// game names itself.
const (
	// PhasePostScoreGap is the unnamed state the client reports (an absent or
	// empty game_status) between "score" and "round_start" after every goal.
	// The game clock is frozen, the disc is being respawned and players are
	// returned to their launch tubes: it is not live play.
	PhasePostScoreGap = "post_score_gap"
	// PhasePreRoundGap is the same unnamed state between another named
	// non-play status (in practice "pre_match") and "round_start".
	PhasePreRoundGap = "pre_round_gap"
)

// MaxUnnamedGapSeconds bounds how long an unnamed status may inherit the
// non-play reading of the named status before it. The observed gaps last
// about 4.4-4.9 s; the bound is deliberately generous. Past it the recording
// is treated as a source that stopped filling game_status, which keeps the
// long-standing active fallback instead of silently blinding every detector
// for the rest of the match.
const MaxUnnamedGapSeconds = 15.0

// IsActiveGamePhase reports whether a normalised game phase is live play.
//
// Echo VR reports overtime as "sudden_death" (with "pre_sudden_death" and
// "post_sudden_death" transitions around it). Sudden death is live play and
// must be covered by detectors; the pre/post transitions teleport players to
// spawn exactly like round_start/round_over and stay inactive.
//
// The empty phase is the active fallback for a source that carries no game
// status at all. An unnamed status inside a recording that does name its
// statuses is resolved by PhaseNormalizer before it reaches this function.
func IsActiveGamePhase(phase string) bool {
	switch phase {
	case "playing", "round", "overtime", "sudden_death", "":
		return true
	default:
		return false
	}
}

// IsUnnamedGameStatus reports whether a source status carries no name: the
// /session API's absent or empty game_status, or the native capture enum's
// UNSPECIFIED value (which the tape adapter projects as "unknown").
func IsUnnamedGameStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "unknown", "unspecified", "game_status_unspecified":
		return true
	}
	return false
}

// PhaseNormalizer turns the per-tick source game status of ONE recording
// into the internal game phase. It is the single rule shared by the replay
// and native tape ingest paths and by the match summary, so the same game
// state is active or inactive no matter how it was recorded.
//
// Named statuses map directly ("score" becomes "round_over", as before).
// An unnamed status is resolved from what the recording has already shown:
//
//   - no named status seen yet: "playing", the long-standing active fallback
//     for sources that never fill game_status;
//   - the last named status was live play: "playing" (unchanged behaviour);
//   - the last named status was "score": PhasePostScoreGap, inactive;
//   - the last named status was any other non-play status: PhasePreRoundGap,
//     inactive (the client shows the same unnamed state between "pre_match"
//     and "round_start").
//
// The two gap readings hold for at most MaxUnnamedGapSeconds of sample time.
//
// The normaliser is streaming and never looks ahead, so live ingest and file
// ingest agree. Calling Normalize again with the same arguments (the mapper
// does, once per player of a tick) returns the same phase. State restarts
// when a different non-empty session id appears or sample time runs
// backwards.
type PhaseNormalizer struct {
	sessionID string
	lastNamed string // normalised phase of the last named status
	lastScore bool   // the last named status was "score"
	haveTime  bool
	lastTime  float64
	inGap     bool
	gapStart  float64
}

// Reset forgets everything learned about the current recording.
func (n *PhaseNormalizer) Reset() { *n = PhaseNormalizer{} }

// Normalize returns the internal game phase for one source sample.
// sessionID may be empty (the sample then belongs to the current recording);
// seconds is the sample's match-relative time.
func (n *PhaseNormalizer) Normalize(sessionID, status string, seconds float64) string {
	if sessionID != "" && n.sessionID != "" && sessionID != n.sessionID {
		n.Reset()
	}
	if n.haveTime && seconds < n.lastTime {
		n.Reset()
	}
	if sessionID != "" {
		n.sessionID = sessionID
	}
	n.haveTime, n.lastTime = true, seconds

	if !IsUnnamedGameStatus(status) {
		named := strings.ToLower(strings.TrimSpace(status))
		n.inGap = false
		n.lastScore = named == "score"
		n.lastNamed = mapNamedGameStatus(named)
		return n.lastNamed
	}
	if n.lastNamed == "" || IsActiveGamePhase(n.lastNamed) {
		return "playing"
	}
	if !n.inGap {
		n.inGap, n.gapStart = true, seconds
	}
	if seconds-n.gapStart > MaxUnnamedGapSeconds {
		return "playing"
	}
	if n.lastScore {
		return PhasePostScoreGap
	}
	return PhasePreRoundGap
}

// mapNamedGameStatus maps a named (lower-cased, trimmed) source status to the
// internal phase. "score" is the goal celebration and is reported as
// "round_over", as it always has been; every other name passes through, so
// "sudden_death" stays live play and names this build does not know are
// inactive.
func mapNamedGameStatus(lower string) string {
	if lower == "score" {
		return "round_over"
	}
	return lower
}
