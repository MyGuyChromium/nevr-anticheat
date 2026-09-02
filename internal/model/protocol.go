package model

// ControlMessage is the JSON envelope for every non-telemetry message
// exchanged between telemetry producers (cmd/bridge) and the ingest server
// (internal/ingest). Telemetry FrameBatch messages carry no "type" field;
// any message whose "type" is non-empty is a ControlMessage.
//
// Direction:
//   server -> producer: hello (after successful auth), ack, error
//   producer -> server: match_start, match_end
type ControlMessage struct {
	Type     string `json:"type"`
	MatchID  string `json:"match_id,omitempty"`
	ServerID string `json:"server_id,omitempty"`
	// Reason carries the match_end reason or the error detail.
	Reason string `json:"reason,omitempty"`
	// Auth is "ok" on a hello message.
	Auth string `json:"auth,omitempty"`
	// Ack counters cover frames since the previous ack.
	Accepted int `json:"accepted,omitempty"`
	Rejected int `json:"rejected,omitempty"`
	// Ignored counts frames the store discarded as duplicates.
	Ignored int `json:"ignored,omitempty"`
	// Optional match metadata on match_start / match_end.
	GameMode  string            `json:"game_mode,omitempty"`
	Map       string            `json:"map,omitempty"`
	IsPrivate bool              `json:"is_private,omitempty"`
	Teams     map[string]string `json:"teams,omitempty"` // player_id -> "blue" | "orange"
}

const (
	ControlHello      = "hello"
	ControlAck        = "ack"
	ControlMatchStart = "match_start"
	ControlMatchEnd   = "match_end"
	ControlError      = "error"
)
