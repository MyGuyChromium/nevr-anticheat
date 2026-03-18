package main

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
)

// validateConfig checks all bridge configuration for correctness before any work starts.
// Returns a non-nil error describing the first problem found.
func validateConfig(cfg *BridgeConfig, mode string) error {
	// URL format checks
	if cfg.NakamaURL == "" {
		return fmt.Errorf("--nakama-url is required")
	}
	if _, err := url.Parse(cfg.NakamaURL); err != nil {
		return fmt.Errorf("--nakama-url is not a valid URL: %w", err)
	}
	if !strings.HasPrefix(cfg.NakamaURL, "http://") && !strings.HasPrefix(cfg.NakamaURL, "https://") {
		return fmt.Errorf("--nakama-url must start with http:// or https://")
	}

	if cfg.AnticheatURL != "" {
		if !strings.HasPrefix(cfg.AnticheatURL, "ws://") && !strings.HasPrefix(cfg.AnticheatURL, "wss://") {
			return fmt.Errorf("--anticheat-url must start with ws:// or wss:// (got %q)", cfg.AnticheatURL)
		}
	}

	// Numeric range checks
	if cfg.APIPort < 1 || cfg.APIPort > 65535 {
		return fmt.Errorf("--api-port must be 1-65535 (got %d)", cfg.APIPort)
	}
	if cfg.PollInterval <= 0 {
		return fmt.Errorf("--poll-interval must be positive (got %v)", cfg.PollInterval)
	}
	if cfg.DiscoveryInterval <= 0 {
		return fmt.Errorf("--discovery-interval must be positive (got %v)", cfg.DiscoveryInterval)
	}

	// Match ID filter whitespace check
	if cfg.MatchIDFilter != strings.TrimSpace(cfg.MatchIDFilter) {
		return fmt.Errorf("--match-id has leading/trailing whitespace (got %q)", cfg.MatchIDFilter)
	}

	// Server key check
	if cfg.NakamaServerKey == "" {
		return fmt.Errorf("--nakama-server-key is required")
	}

	// Dump dir validation
	if cfg.DumpDir != "" {
		if mode == "continuous" {
			return fmt.Errorf("--dump-dir is only supported in probe/once mode, not continuous")
		}
		if strings.TrimSpace(cfg.DumpDir) != cfg.DumpDir {
			return fmt.Errorf("--dump-dir has leading/trailing whitespace (got %q)", cfg.DumpDir)
		}
	}

	return nil
}

// validateBroadcasterIP checks whether an IP string is usable for HTTP polling.
func validateBroadcasterIP(ip string) error {
	if ip == "" {
		return fmt.Errorf("empty IP")
	}
	if strings.TrimSpace(ip) != ip {
		return fmt.Errorf("IP has whitespace: %q", ip)
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return fmt.Errorf("invalid IP address: %q", ip)
	}
	if parsed.IsUnspecified() {
		return fmt.Errorf("unspecified IP (0.0.0.0): %q", ip)
	}
	return nil
}

// validateSession checks whether a broadcaster /session response has the minimum
// fields needed for meaningful mapping. This catches structurally empty responses
// before they reach the mapper.
func validateSession(s *adapter.EchoVRSessionResponse) error {
	if s.SessionID == "" {
		return fmt.Errorf("session_id is empty")
	}
	playerCount := 0
	for _, t := range s.Teams {
		playerCount += len(t.Players)
	}
	if playerCount == 0 {
		return fmt.Errorf("no players in any team (teams=%d)", len(s.Teams))
	}
	return nil
}

// selectMatch picks one match deterministically from a list.
// Uses match_id ascending for stable ordering regardless of Nakama return order.
func selectMatch(matches []DiscoveredMatch) DiscoveredMatch {
	if len(matches) == 1 {
		return matches[0]
	}
	sorted := make([]DiscoveredMatch, len(matches))
	copy(sorted, matches)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].MatchID < sorted[j].MatchID
	})
	return sorted[0]
}

// stopReason classifies why a poller stopped.
type stopReason string

const (
	stopCancelled             stopReason = "cancelled"
	stopBroadcasterUnreachable stopReason = "broadcaster_unreachable"
	stopPostMatch             stopReason = "post_match_detected"
	stopInvalidSession        stopReason = "invalid_session_response"
)
