package evidence

import (
	"encoding/json"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// MatchServerID returns the ingest server a match's telemetry came from, or
// "" when the context does not record one. Provenance lives on the match
// context (its server_id, populated by the ingest server from the bridge's
// hello / match_start and persisted with the context), not on every event
// row, so evidence output reads it from there.
//
// The value is read through the context's JSON form so this package does not
// depend on the exact field name at compile time; it is equivalent to reading
// MatchContext.ServerID directly.
func MatchServerID(mc *model.MatchContext) string {
	if mc == nil {
		return ""
	}
	b, err := json.Marshal(mc)
	if err != nil {
		return ""
	}
	return serverIDFromJSON(b)
}

func serverIDFromJSON(b []byte) string {
	var m struct {
		ServerID string `json:"server_id"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return ""
	}
	return strings.TrimSpace(m.ServerID)
}
