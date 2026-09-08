package main

import (
	"encoding/json"
	"testing"
)

// The bridge is an ingestion producer, not an administration channel. Even an
// authenticated ingest peer cannot turn returned control messages into player
// actions or queued outbound commands. This test opens no network connection.
func TestReviewOnlyBridgeIgnoresPunitiveControlMessages(t *testing.T) {
	stats := &bridgeStats{}
	s, err := newWSSender(senderConfig("ws://127.0.0.1:8080/telemetry"), stats, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"kick", "ban", "temp_ban", "perm_ban", "suspend", "restrict", "enforce", "execute", "moderator_decision"} {
		for attempt := 0; attempt < 3; attempt++ {
			body, err := json.Marshal(map[string]any{"type": action, "match_id": "test-match", "player_id": "test-player", "accepted": 99, "command": action})
			if err != nil {
				t.Fatal(err)
			}
			s.handleControl(body)
		}
	}
	if s.queued() != 0 || s.outstanding() != 0 || stats.AcksReceived.Load() != 0 || stats.TotalFramesForwarded.Load() != 0 {
		t.Fatal("punitive control message changed the telemetry sender or queued work")
	}
}
