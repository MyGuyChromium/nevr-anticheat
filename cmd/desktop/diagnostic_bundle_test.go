package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The "redacted" bundle replaced match_id and sessionid but not the app's own
// observation.session_id, so every frame carried the real session id. It also
// gave "echovr:1001" and userid 1001 different placeholders. Mutation: compare
// keys without normalising them and the session_id assertion fails.
func TestDiagnosticRedactionCoversTheAppsOwnIdentityFields(t *testing.T) {
	const matchID = "8A0C0B6E-1111-4222-8333-444455556666"
	doc := map[string]any{
		"match_id":  matchID,
		"player_id": "echovr:3964000000001234",
		"frames": []any{map[string]any{
			"observation": map[string]any{"session_id": matchID, "source_player_id": "echovr:3964000000001234", "authority": "client_reported"},
			"player_id":   "echovr:3964000000001234",
		}},
		"raw_focus_tick": map[string]any{
			"sessionid": matchID, "sessionip": "203.0.113.7", "client_name": "SomeRecorder",
			"teams": []any{map[string]any{"players": []any{
				map[string]any{"name": "SomePlayer", "userid": json.Number("3964000000001234"), "playerid": json.Number("3")},
			}}},
		},
		"context": map[string]any{"replay_file": `C:\Users\someone\rec_SomePlayer.echoreplay`, "server_id": "203.0.113.7:6721",
			"player_names": map[string]any{"echovr:3964000000001234": "SomePlayer"}},
		"limitations": []any{"Session " + matchID + " was recorded by SomeRecorder; SomePlayer is the focus player."},
	}
	out, err := redactedJSON(doc, map[string]string{matchID: "MATCH"})
	if err != nil || !json.Valid(out) {
		t.Fatalf("redaction produced invalid JSON: %v\n%s", err, out)
	}
	text := string(out)
	for _, leak := range []string{matchID, "3964000000001234", "SomePlayer", "SomeRecorder", "203.0.113.7", "someone"} {
		if strings.Contains(text, leak) {
			t.Errorf("redacted bundle still contains %q:\n%s", leak, text)
		}
	}
	if strings.Count(text, "PLAYER-001") < 4 || strings.Contains(text, "PLAYER-002") {
		t.Errorf("one player must keep one placeholder across id forms:\n%s", text)
	}
	if !strings.Contains(text, `"playerid": 3`) || !strings.Contains(text, `"authority": "client_reported"`) {
		t.Errorf("non-identifying values were lost:\n%s", text)
	}
}
