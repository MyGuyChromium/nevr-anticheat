package main

import (
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
)

func TestValidateConfig(t *testing.T) {
	good := &BridgeConfig{
		NakamaURL:         "http://localhost:7350",
		NakamaServerKey:   "testkey",
		AnticheatURL:      "ws://localhost:8080/telemetry",
		APIPort:           6721,
		PollInterval:      67 * time.Millisecond,
		DiscoveryInterval: 10 * time.Second,
	}

	if err := validateConfig(good, "continuous"); err != nil {
		t.Fatalf("good config should pass: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(c *BridgeConfig)
	}{
		{"empty nakama url", func(c *BridgeConfig) { c.NakamaURL = "" }},
		{"bad nakama url scheme", func(c *BridgeConfig) { c.NakamaURL = "ftp://localhost" }},
		{"bad anticheat url scheme", func(c *BridgeConfig) { c.AnticheatURL = "http://localhost" }},
		{"port zero", func(c *BridgeConfig) { c.APIPort = 0 }},
		{"port too high", func(c *BridgeConfig) { c.APIPort = 70000 }},
		{"negative poll interval", func(c *BridgeConfig) { c.PollInterval = -1 }},
		{"zero discovery interval", func(c *BridgeConfig) { c.DiscoveryInterval = 0 }},
		{"match-id whitespace", func(c *BridgeConfig) { c.MatchIDFilter = " abc " }},
		{"empty server key", func(c *BridgeConfig) { c.NakamaServerKey = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := *good // copy
			tt.mutate(&c)
			if err := validateConfig(&c, "continuous"); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}

	// Empty anticheat URL is allowed (dry-run)
	t.Run("empty anticheat url ok", func(t *testing.T) {
		c := *good
		c.AnticheatURL = ""
		if err := validateConfig(&c, "continuous"); err != nil {
			t.Errorf("empty anticheat URL should be allowed: %v", err)
		}
	})
}

func TestValidateBroadcasterIP(t *testing.T) {
	tests := []struct {
		ip      string
		wantErr bool
	}{
		{"192.168.1.1", false},
		{"10.0.0.1", false},
		{"203.0.113.50", false},
		{"", true},
		{" 1.2.3.4", true},   // leading space
		{"not-an-ip", true},
		{"0.0.0.0", true},     // unspecified
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			err := validateBroadcasterIP(tt.ip)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateBroadcasterIP(%q) err=%v, wantErr=%v", tt.ip, err, tt.wantErr)
			}
		})
	}
}

func TestValidateSession(t *testing.T) {
	t.Run("valid session", func(t *testing.T) {
		s := &adapter.EchoVRSessionResponse{
			SessionID: "test-session",
			Teams: []adapter.EchoVRTeam{
				{Players: []adapter.EchoVRPlayer{{Name: "p1"}}},
			},
		}
		if err := validateSession(s); err != nil {
			t.Errorf("expected valid, got: %v", err)
		}
	})

	t.Run("empty session_id", func(t *testing.T) {
		s := &adapter.EchoVRSessionResponse{
			Teams: []adapter.EchoVRTeam{
				{Players: []adapter.EchoVRPlayer{{Name: "p1"}}},
			},
		}
		if err := validateSession(s); err == nil {
			t.Error("expected error for empty session_id")
		}
	})

	t.Run("no players", func(t *testing.T) {
		s := &adapter.EchoVRSessionResponse{
			SessionID: "test",
			Teams:     []adapter.EchoVRTeam{{}, {}},
		}
		if err := validateSession(s); err == nil {
			t.Error("expected error for no players")
		}
	})
}

func TestSelectMatch(t *testing.T) {
	matches := []DiscoveredMatch{
		{MatchID: "charlie"},
		{MatchID: "alpha"},
		{MatchID: "bravo"},
	}

	selected := selectMatch(matches)
	if selected.MatchID != "alpha" {
		t.Errorf("selectMatch should pick 'alpha' (ascending), got %q", selected.MatchID)
	}

	// Single match
	single := []DiscoveredMatch{{MatchID: "only"}}
	if selectMatch(single).MatchID != "only" {
		t.Error("single match should return itself")
	}
}
