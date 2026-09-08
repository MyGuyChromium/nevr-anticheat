package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// discoverer queries Nakama's standard match list API for active matches
// WITH full broadcaster endpoints and keeps the auth session alive between
// calls.
//
// IMPORTANT: We use the standard Nakama API (GET /v2/match), NOT the custom
// match/public RPC. The match/public RPC calls PublicView() which deliberately
// strips broadcaster Endpoint (IP:port) from the response. The standard API,
// called with a user session token, returns the full match label including
// the endpoint.
type discoverer struct {
	cfg    *BridgeConfig
	logger *slog.Logger
	stats  *bridgeStats
	auth   *nakamaAuth
	client *http.Client

	warnedEndpoints sync.Map // match_id -> struct{} (warn once per match)
}

func newDiscoverer(cfg *BridgeConfig, stats *bridgeStats, logger *slog.Logger) *discoverer {
	return &discoverer{
		cfg:    cfg,
		logger: logger,
		stats:  stats,
		auth:   newNakamaAuth(cfg, logger),
		client: scopedHTTPClient(10 * time.Second),
	}
}

// discoverMatches is the one-shot form used by probe/once mode and tests.
func discoverMatches(ctx context.Context, cfg *BridgeConfig, logger *slog.Logger, stats ...*bridgeStats) ([]DiscoveredMatch, error) {
	var st *bridgeStats
	if len(stats) > 0 {
		st = stats[0]
	}
	return newDiscoverer(cfg, st, logger).discover(ctx)
}

func (d *discoverer) discover(ctx context.Context) ([]DiscoveredMatch, error) {
	url := fmt.Sprintf("%s/v2/match?authoritative=true&limit=100&min_size=1", nakamaBaseURL(d.cfg))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	authz, err := d.auth.authorization(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authz)

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nakama API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		d.auth.invalidate()
		return nil, &nakamaAuthError{Status: resp.StatusCode, Body: truncate(string(body), 200), Mode: d.auth.mode()}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nakama API returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var apiResp nakamaMatchListResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("parsing Nakama response: %w", err)
	}

	var matches []DiscoveredMatch
	for _, m := range apiResp.Matches {
		if m.Label == "" {
			continue
		}

		var label matchLabel
		if err := json.Unmarshal([]byte(m.Label), &label); err != nil {
			d.warnOnce(m.MatchID, "skipping match with unparseable label", "error", err.Error())
			continue
		}

		if label.Broadcaster == nil {
			d.logger.Debug("skipping match without broadcaster", "match_id", m.MatchID)
			continue
		}

		ip, port := parseEndpoint(label.Broadcaster.Endpoint)
		if err := validateBroadcasterIP(ip); err != nil {
			d.warnOnce(m.MatchID, "skipping match with invalid broadcaster endpoint",
				"endpoint_raw", string(label.Broadcaster.Endpoint), "reason", err.Error())
			if d.stats != nil {
				d.stats.MatchesSkippedNoEP.Add(1)
			}
			continue
		}

		matches = append(matches, DiscoveredMatch{
			MatchID:             m.MatchID,
			LabelID:             label.ID,
			Mode:                label.Mode,
			Level:               label.Level,
			PlayerCount:         m.Size,
			BroadcasterIP:       ip,
			BroadcasterGamePort: port,
			Region:              label.Broadcaster.Region,
			StartTime:           label.StartTime,
		})
	}

	return matches, nil
}

// warnOnce logs an endpoint problem at Warn the first time it is seen for a
// match and at Debug afterwards, so a persistent label problem is visible
// without flooding the log every discovery cycle.
func (d *discoverer) warnOnce(matchID, msg string, attrs ...any) {
	attrs = append([]any{"match_id", matchID}, attrs...)
	if _, seen := d.warnedEndpoints.LoadOrStore(matchID, struct{}{}); seen {
		d.logger.Debug(msg, attrs...)
		return
	}
	d.logger.Warn(msg, attrs...)
}

// isNakamaAuthError reports whether err is a credential rejection.
func isNakamaAuthError(err error) bool {
	var ae *nakamaAuthError
	return errors.As(err, &ae)
}

// nakamaMatchListResponse is the standard Nakama match list API response.
type nakamaMatchListResponse struct {
	Matches []nakamaMatch `json:"matches"`
}

type nakamaMatch struct {
	MatchID       string `json:"match_id"`
	Authoritative bool   `json:"authoritative"`
	Label         string `json:"label"` // JSON string of MatchLabel
	Size          int    `json:"size"`
}

// matchLabel is a minimal parse of the EchoTools MatchLabel.
// We only extract the fields we need for bridge operation.
type matchLabel struct {
	ID          string            `json:"id"`
	Mode        string            `json:"mode"`
	Level       string            `json:"level"`
	Broadcaster *matchBroadcaster `json:"broadcaster"`
	Players     []matchPlayer     `json:"players"`
	GameState   *matchGameState   `json:"game_state"`
	StartTime   time.Time         `json:"start_time"`
}

type matchBroadcaster struct {
	Endpoint json.RawMessage `json:"endpoint"`
	Region   string          `json:"region"`
}

type matchPlayer struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	TeamIndex   int    `json:"team_index"`
}

type matchGameState struct {
	BlueScore   int  `json:"blue_score"`
	OrangeScore int  `json:"orange_score"`
	MatchOver   bool `json:"match_over"`
}

// parseEndpoint extracts the external IP and game port from the label's
// broadcaster endpoint. Accepted shapes:
//
//   - "internalIP:externalIP:port" (EchoVRCE Endpoint.MarshalJSON, IPv4)
//   - "ip:port" and "[ipv6]:port"
//   - a bare IP (v4 or v6)
//   - {"external_ip": "...", "internal_ip": "...", "port": N}
//
// Anything else yields ("", 0) and the caller reports the raw value.
func parseEndpoint(raw json.RawMessage) (string, int) {
	if len(raw) == 0 {
		return "", 0
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return parseEndpointString(s)
	}

	var obj struct {
		ExternalIP string `json:"external_ip"`
		InternalIP string `json:"internal_ip"`
		Port       int    `json:"port"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		ip := obj.ExternalIP
		if ip == "" {
			ip = obj.InternalIP
		}
		return ip, obj.Port
	}
	return "", 0
}

func parseEndpointString(s string) (string, int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0
	}
	// Bare IP (v4 or v6 literal without port).
	if ip := net.ParseIP(s); ip != nil {
		return s, 0
	}
	// "[v6]:port" or "host:port" — SplitHostPort handles brackets.
	if host, portStr, err := net.SplitHostPort(s); err == nil {
		if net.ParseIP(host) != nil {
			port, _ := strconv.Atoi(portStr)
			return host, port
		}
	}
	parts := strings.Split(s, ":")
	switch {
	case len(parts) == 3:
		// internal:external:port — the external IP is the only routable one;
		// never fall back to the internal address.
		port, _ := strconv.Atoi(parts[2])
		return parts[1], port
	case len(parts) == 2:
		port, _ := strconv.Atoi(parts[1])
		return parts[0], port
	case len(parts) == 1:
		return parts[0], 0
	}
	// Possibly "v6internal:v6external:port" — take the last segment as port
	// and try to find a parsable IP in what remains.
	port, _ := strconv.Atoi(parts[len(parts)-1])
	rest := strings.Join(parts[:len(parts)-1], ":")
	if ip := net.ParseIP(rest); ip != nil {
		return rest, port
	}
	return "", 0
}

// extractIP and extractPort are kept for callers/tests that only need one half.
func extractIP(raw json.RawMessage) string {
	ip, _ := parseEndpoint(raw)
	return ip
}

func extractPort(raw json.RawMessage) int {
	_, port := parseEndpoint(raw)
	return port
}
