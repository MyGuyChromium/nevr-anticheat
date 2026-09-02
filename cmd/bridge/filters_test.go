package main

import (
	"encoding/json"
	"testing"
	"time"
)

// F176: endpoint shapes beyond "internal:external:port".
func TestParseEndpoint_Shapes(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantIP   string
		wantPort int
	}{
		{"internal:external:port", `"192.168.1.100:203.0.113.50:6792"`, "203.0.113.50", 6792},
		{"ip:port", `"203.0.113.50:6792"`, "203.0.113.50", 6792},
		{"bare ipv4", `"203.0.113.50"`, "203.0.113.50", 0},
		{"bare ipv6", `"2001:db8::1"`, "2001:db8::1", 0},
		{"bracketed ipv6 with port", `"[2001:db8::1]:6792"`, "2001:db8::1", 6792},
		{"object external", `{"external_ip":"203.0.113.50","internal_ip":"10.0.0.1","port":6792}`, "203.0.113.50", 6792},
		{"object internal only", `{"internal_ip":"10.0.0.1","port":6792}`, "10.0.0.1", 6792},
		{"garbage", `"not:an:ip:at:all"`, "", 0},
		{"empty", `""`, "", 0},
		{"null", `null`, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip, port := parseEndpoint(json.RawMessage(tt.raw))
			if ip != tt.wantIP || port != tt.wantPort {
				t.Errorf("parseEndpoint(%s) = (%q, %d), want (%q, %d)", tt.raw, ip, port, tt.wantIP, tt.wantPort)
			}
		})
	}
}

func TestParseAllowlist(t *testing.T) {
	al, err := parseAllowlist("203.0.113.5, 10.0.0.0/8,2001:db8::/32")
	if err != nil {
		t.Fatal(err)
	}
	for ip, want := range map[string]bool{
		"203.0.113.5": true, "203.0.113.6": false, "10.20.30.40": true, "11.0.0.1": false,
		"2001:db8::1": true, "2001:db9::1": false, "not-an-ip": false,
	} {
		if got := al.allows(ip); got != want {
			t.Errorf("allows(%q) = %v, want %v", ip, got, want)
		}
	}
	if !(allowlist(nil)).allows("203.0.113.6") {
		t.Error("empty allowlist must allow everything")
	}
	if _, err := parseAllowlist("300.1.1.1"); err == nil {
		t.Error("expected error for invalid IP")
	}
	if _, err := parseAllowlist(" , "); err == nil {
		t.Error("expected error for empty allowlist")
	}
}

func TestModeAllowed(t *testing.T) {
	tests := []struct {
		modes, mode string
		want        bool
	}{
		{"", "echo_combat", true},
		{"*", "echo_combat", true},
		{"echo_arena", "echo_arena", true},
		{"echo_arena", "Echo_Arena_Private", true},
		{"echo_arena", "echo_combat", false},
		{"echo_arena,echo_combat", "echo_combat_private", true},
		{"echo_arena", "social_2.0", false},
		{"echo_arena", "", true}, // unclassifiable labels are not dropped
	}
	for _, tt := range tests {
		if got := modeAllowed(tt.modes, tt.mode); got != tt.want {
			t.Errorf("modeAllowed(%q, %q) = %v, want %v", tt.modes, tt.mode, got, tt.want)
		}
	}
}

func TestValidateConfig_NewFlags(t *testing.T) {
	good := &BridgeConfig{
		NakamaURL: "http://localhost:7350", NakamaServerKey: "testkey", APIPort: 6721,
		PollInterval: 67 * time.Millisecond, DiscoveryInterval: 10 * time.Second,
	}
	bad := map[string]func(c *BridgeConfig){
		"unknown auth mode":    func(c *BridgeConfig) { c.NakamaAuthMode = "magic" },
		"bearer without token": func(c *BridgeConfig) { c.NakamaAuthMode = nakamaAuthBearer },
		"device without server key": func(c *BridgeConfig) {
			c.NakamaAuthMode = nakamaAuthDevice
			c.NakamaServerKey = ""
			c.NakamaBearerToken = "x"
		},
		"bad session check": func(c *BridgeConfig) { c.SessionCheck = "maybe" },
		"bad allowlist":     func(c *BridgeConfig) { c.BroadcasterAllowlist = "nope" },
		"negative queue":    func(c *BridgeConfig) { c.QueueSize = -1 },
	}
	for name, mutate := range bad {
		c := *good
		mutate(&c)
		if err := validateConfig(&c, "continuous"); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	c := *good
	c.NakamaAuthMode = nakamaAuthDevice
	c.SessionCheck = sessionCheckWarn
	c.BroadcasterAllowlist = "127.0.0.1,10.0.0.0/8"
	c.Modes = "echo_arena"
	if err := validateConfig(&c, "continuous"); err != nil {
		t.Errorf("good config rejected: %v", err)
	}
}
