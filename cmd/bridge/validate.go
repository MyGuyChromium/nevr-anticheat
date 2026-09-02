package main

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
)

// Session identity check modes (--session-check).
const (
	sessionCheckStrict = "strict" // stop the poller on mismatch (default)
	sessionCheckWarn   = "warn"   // log once, keep forwarding
	sessionCheckOff    = "off"    // do not compare
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
	// Normalise trailing slashes so "/v2/match" never becomes "//v2/match".
	cfg.NakamaURL = strings.TrimRight(cfg.NakamaURL, "/")

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
	if cfg.IdleInterval < 0 || cfg.IdleGiveUp < 0 || cfg.AckTimeout < 0 {
		return fmt.Errorf("--idle-interval, --idle-give-up and --ack-timeout must not be negative")
	}
	if cfg.QueueSize < 0 {
		return fmt.Errorf("--queue-size must not be negative (got %d)", cfg.QueueSize)
	}

	// Match ID filter whitespace check
	if cfg.MatchIDFilter != strings.TrimSpace(cfg.MatchIDFilter) {
		return fmt.Errorf("--match-id has leading/trailing whitespace (got %q)", cfg.MatchIDFilter)
	}

	// Auth checks
	switch cfg.NakamaAuthMode {
	case "", nakamaAuthDevice, nakamaAuthBearer, nakamaAuthBasic:
	default:
		return fmt.Errorf("--nakama-auth must be one of device, bearer, basic (got %q)", cfg.NakamaAuthMode)
	}
	if cfg.NakamaAuthMode == nakamaAuthBearer && cfg.NakamaBearerToken == "" {
		return fmt.Errorf("--nakama-auth bearer requires --nakama-bearer-token")
	}
	if cfg.NakamaBearerToken == "" && cfg.NakamaServerKey == "" {
		return fmt.Errorf("--nakama-server-key or --nakama-bearer-token is required")
	}
	if (cfg.NakamaAuthMode == nakamaAuthDevice || cfg.NakamaAuthMode == nakamaAuthBasic) && cfg.NakamaServerKey == "" {
		return fmt.Errorf("--nakama-auth %s requires --nakama-server-key", cfg.NakamaAuthMode)
	}

	switch cfg.SessionCheck {
	case "", sessionCheckStrict, sessionCheckWarn, sessionCheckOff:
	default:
		return fmt.Errorf("--session-check must be one of strict, warn, off (got %q)", cfg.SessionCheck)
	}

	if cfg.BroadcasterAllowlist != "" {
		if _, err := parseAllowlist(cfg.BroadcasterAllowlist); err != nil {
			return fmt.Errorf("--broadcaster-allowlist: %w", err)
		}
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

// allowlist is a set of IPs / CIDR prefixes broadcasters must fall into.
type allowlist []netip.Prefix

// parseAllowlist parses a comma-separated list of IPs and CIDRs.
func parseAllowlist(spec string) (allowlist, error) {
	var out allowlist
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			p, err := netip.ParsePrefix(item)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q: %w", item, err)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("invalid IP %q: %w", item, err)
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("allowlist is empty")
	}
	return out, nil
}

// allows reports whether ip is covered by the allowlist. An empty allowlist
// allows everything (no trust boundary configured).
func (al allowlist) allows(ip string) bool {
	if len(al) == 0 {
		return true
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range al {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// modeAllowed applies the --modes filter: a comma-separated list of
// case-insensitive prefixes ("echo_arena" matches echo_arena_private too).
// Empty or "*" allows every mode; an empty label mode is always allowed
// (and logged by the caller) because it cannot be classified.
func modeAllowed(modes, mode string) bool {
	modes = strings.TrimSpace(modes)
	if modes == "" || modes == "*" {
		return true
	}
	if mode == "" {
		return true
	}
	lower := strings.ToLower(mode)
	for _, m := range strings.Split(modes, ",") {
		m = strings.ToLower(strings.TrimSpace(m))
		if m == "" {
			continue
		}
		if m == "*" || strings.HasPrefix(lower, m) {
			return true
		}
	}
	return false
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
// fields needed for meaningful mapping. Spectators do not count as players.
func validateSession(s *adapter.EchoVRSessionResponse) error {
	return validateSessionRoster(s, buildRoster(s))
}

func validateSessionRoster(s *adapter.EchoVRSessionResponse, roster sessionRoster) error {
	if s.SessionID == "" {
		return fmt.Errorf("session_id is empty")
	}
	if roster.PlayerCount == 0 {
		return fmt.Errorf("no players on blue/orange teams (teams=%d, spectators=%d)", len(s.Teams), len(roster.Spectators))
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
	stopCancelled              stopReason = "cancelled"
	stopBroadcasterUnreachable stopReason = "broadcaster_unreachable"
	stopBroadcasterError       stopReason = "broadcaster_error"
	stopPostMatch              stopReason = "post_match_detected"
	stopSessionMismatch        stopReason = "session_mismatch"
)
