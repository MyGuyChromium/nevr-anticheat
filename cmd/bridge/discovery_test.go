package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
)

func TestExtractIP_StringFormat(t *testing.T) {
	tests := []struct {
		name     string
		input    string // raw JSON value
		wantIP   string
		wantPort int
	}{
		{
			name:     "standard ipv4 format",
			input:    `"192.168.1.100:203.0.113.50:6792"`,
			wantIP:   "203.0.113.50",
			wantPort: 6792,
		},
		{
			name:     "same internal and external",
			input:    `"10.0.0.5:10.0.0.5:6792"`,
			wantIP:   "10.0.0.5",
			wantPort: 6792,
		},
		{
			name:     "single ip (no delimiter)",
			input:    `"192.168.1.1"`,
			wantIP:   "192.168.1.1",
			wantPort: 0,
		},
		{
			name:     "empty string",
			input:    `""`,
			wantIP:   "",
			wantPort: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := json.RawMessage(tt.input)
			gotIP := extractIP(raw)
			if gotIP != tt.wantIP {
				t.Errorf("extractIP(%s) = %q, want %q", tt.input, gotIP, tt.wantIP)
			}
			gotPort := extractPort(raw)
			if gotPort != tt.wantPort {
				t.Errorf("extractPort(%s) = %d, want %d", tt.input, gotPort, tt.wantPort)
			}
		})
	}
}

func TestExtractIP_ObjectFormat(t *testing.T) {
	input := `{"external_ip": "203.0.113.50", "internal_ip": "192.168.1.100", "port": 6792}`
	raw := json.RawMessage(input)

	gotIP := extractIP(raw)
	if gotIP != "203.0.113.50" {
		t.Errorf("extractIP(object) = %q, want %q", gotIP, "203.0.113.50")
	}

	gotPort := extractPort(raw)
	if gotPort != 6792 {
		t.Errorf("extractPort(object) = %d, want %d", gotPort, 6792)
	}
}

func TestExtractIP_NilInput(t *testing.T) {
	gotIP := extractIP(nil)
	if gotIP != "" {
		t.Errorf("extractIP(nil) = %q, want empty", gotIP)
	}
	gotPort := extractPort(nil)
	if gotPort != 0 {
		t.Errorf("extractPort(nil) = %d, want 0", gotPort)
	}
}

func TestParseMatchLabel(t *testing.T) {
	labelJSON := `{
		"id": "test-match-123",
		"mode": "echo_arena",
		"level": "mpl_arena_a",
		"broadcaster": {
			"endpoint": "10.0.0.1:203.0.113.50:6792",
			"region": "us-east"
		},
		"players": [
			{"user_id": "uuid-1", "display_name": "Player1", "team_index": 1},
			{"user_id": "uuid-2", "display_name": "Player2", "team_index": 2}
		],
		"game_state": {
			"blue_score": 3,
			"orange_score": 1,
			"match_over": false
		},
		"start_time": "2026-03-17T20:00:00Z"
	}`

	var label matchLabel
	if err := json.Unmarshal([]byte(labelJSON), &label); err != nil {
		t.Fatalf("failed to parse label: %v", err)
	}

	if label.ID != "test-match-123" {
		t.Errorf("ID = %q, want %q", label.ID, "test-match-123")
	}
	if label.Mode != "echo_arena" {
		t.Errorf("Mode = %q, want %q", label.Mode, "echo_arena")
	}
	if label.Level != "mpl_arena_a" {
		t.Errorf("Level = %q, want %q", label.Level, "mpl_arena_a")
	}
	if label.Broadcaster == nil {
		t.Fatal("Broadcaster is nil")
	}
	if label.GameState == nil {
		t.Fatal("GameState is nil")
	}
	if label.GameState.BlueScore != 3 {
		t.Errorf("BlueScore = %d, want 3", label.GameState.BlueScore)
	}
	if len(label.Players) != 2 {
		t.Errorf("Players count = %d, want 2", len(label.Players))
	}

	ip := extractIP(label.Broadcaster.Endpoint)
	if ip != "203.0.113.50" {
		t.Errorf("broadcaster IP = %q, want %q", ip, "203.0.113.50")
	}
}

func TestParseMatchLabel_NoBroadcaster(t *testing.T) {
	labelJSON := `{"id": "test", "mode": "echo_arena"}`
	var label matchLabel
	if err := json.Unmarshal([]byte(labelJSON), &label); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if label.Broadcaster != nil {
		t.Error("expected nil broadcaster")
	}
}

func TestFilterMatches(t *testing.T) {
	matches := []DiscoveredMatch{
		{MatchID: "match-1", BroadcasterIP: "1.1.1.1"},
		{MatchID: "match-2", BroadcasterIP: "2.2.2.2"},
		{MatchID: "match-3", BroadcasterIP: "3.3.3.3"},
	}

	t.Run("no filter returns all", func(t *testing.T) {
		cfg := &BridgeConfig{}
		stats := &bridgeStats{}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		result := filterMatches(matches, cfg, stats, logger)
		if len(result) != 3 {
			t.Errorf("got %d matches, want 3", len(result))
		}
	})

	t.Run("filter by match-id", func(t *testing.T) {
		cfg := &BridgeConfig{MatchIDFilter: "match-2"}
		stats := &bridgeStats{}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		result := filterMatches(matches, cfg, stats, logger)
		if len(result) != 1 {
			t.Fatalf("got %d matches, want 1", len(result))
		}
		if result[0].MatchID != "match-2" {
			t.Errorf("got match %q, want match-2", result[0].MatchID)
		}
		if stats.MatchesSkippedFilter.Load() != 2 {
			t.Errorf("skipped = %d, want 2", stats.MatchesSkippedFilter.Load())
		}
	})

	t.Run("filter no match", func(t *testing.T) {
		cfg := &BridgeConfig{MatchIDFilter: "nonexistent"}
		stats := &bridgeStats{}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		result := filterMatches(matches, cfg, stats, logger)
		if len(result) != 0 {
			t.Errorf("got %d matches, want 0", len(result))
		}
		if stats.MatchesSkippedFilter.Load() != 3 {
			t.Errorf("skipped = %d, want 3", stats.MatchesSkippedFilter.Load())
		}
	})
}

func TestParseNakamaResponse(t *testing.T) {
	responseJSON := `{
		"matches": [
			{
				"match_id": "match-1.server-node",
				"authoritative": true,
				"label": "{\"id\":\"m1\",\"mode\":\"echo_arena\",\"level\":\"arena\",\"broadcaster\":{\"endpoint\":\"10.0.0.1:1.2.3.4:6792\"}}",
				"size": 6
			},
			{
				"match_id": "match-2.server-node",
				"authoritative": true,
				"label": "{\"id\":\"m2\",\"mode\":\"echo_combat\"}",
				"size": 4
			}
		]
	}`

	var resp nakamaMatchListResponse
	if err := json.Unmarshal([]byte(responseJSON), &resp); err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(resp.Matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(resp.Matches))
	}

	// First match has a broadcaster
	var label1 matchLabel
	if err := json.Unmarshal([]byte(resp.Matches[0].Label), &label1); err != nil {
		t.Fatalf("label parse: %v", err)
	}
	if label1.Broadcaster == nil {
		t.Error("match 1 should have broadcaster")
	}
	if ip := extractIP(label1.Broadcaster.Endpoint); ip != "1.2.3.4" {
		t.Errorf("match 1 IP = %q, want %q", ip, "1.2.3.4")
	}

	// Second match has no broadcaster — should be skipped by discovery
	var label2 matchLabel
	if err := json.Unmarshal([]byte(resp.Matches[1].Label), &label2); err != nil {
		t.Fatalf("label parse: %v", err)
	}
	if label2.Broadcaster != nil {
		t.Error("match 2 should NOT have broadcaster")
	}
}
