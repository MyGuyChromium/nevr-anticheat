package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// discoverMatches queries Nakama's standard match list API with server-key auth
// to get active matches WITH full broadcaster endpoints.
//
// IMPORTANT: We use the standard Nakama API (GET /v2/match), NOT the custom
// match/public RPC. The match/public RPC calls PublicView() which deliberately
// strips broadcaster Endpoint (IP:port) from the response. The standard API
// with server-key auth returns the full match label including the endpoint.
func discoverMatches(ctx context.Context, cfg *BridgeConfig, logger *slog.Logger, stats ...*bridgeStats) ([]DiscoveredMatch, error) {
	// Build request: GET /v2/match?authoritative=true&limit=100&min_size=1
	url := fmt.Sprintf("%s/v2/match?authoritative=true&limit=100&min_size=1", cfg.NakamaURL)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	// Auth: bearer token takes precedence over basic server-key auth.
	if cfg.NakamaBearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.NakamaBearerToken)
	} else {
		// Nakama server-key auth: Basic base64(serverkey:)
		// The colon with empty password is required by Nakama's auth scheme.
		auth := base64.StdEncoding.EncodeToString([]byte(cfg.NakamaServerKey + ":"))
		req.Header.Set("Authorization", "Basic "+auth)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Nakama API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Nakama API returned %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	// Parse standard Nakama match list response.
	// The response is: { "matches": [ { "match_id": "...", "label": "{json}", ... } ] }
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
			logger.Debug("skipping match with unparseable label", "match_id", m.MatchID, "error", err)
			continue
		}

		// Skip matches without a broadcaster
		if label.Broadcaster == nil {
			logger.Debug("skipping match without broadcaster", "match_id", m.MatchID)
			continue
		}

		// Extract and validate broadcaster IP
		ip := extractIP(label.Broadcaster.Endpoint)
		if err := validateBroadcasterIP(ip); err != nil {
			logger.Debug("skipping match with invalid broadcaster endpoint",
				"match_id", m.MatchID,
				"endpoint_raw", string(label.Broadcaster.Endpoint),
				"reason", err.Error(),
			)
			if len(stats) > 0 && stats[0] != nil {
				stats[0].MatchesSkippedNoEP.Add(1)
			}
			continue
		}

		port := extractPort(label.Broadcaster.Endpoint)

		matches = append(matches, DiscoveredMatch{
			MatchID:             m.MatchID,
			Mode:                label.Mode,
			Level:               label.Level,
			PlayerCount:         m.Size,
			BroadcasterIP:       ip,
			BroadcasterGamePort: port,
		})
	}

	return matches, nil
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
	ID          string               `json:"id"`
	Mode        string               `json:"mode"`
	Level       string               `json:"level"`
	Broadcaster *matchBroadcaster    `json:"broadcaster"`
	Players     []matchPlayer        `json:"players"`
	GameState   *matchGameState      `json:"game_state"`
	StartTime   time.Time            `json:"start_time"`
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

// extractIP pulls the ExternalIP from the Endpoint field.
// Nakama's Endpoint serializes as "internalIP:externalIP:port" via custom MarshalJSON.
// It may also serialize as a JSON object with fields.
func extractIP(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try string format first: "internalIP:externalIP:port"
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		parts := strings.Split(s, ":")
		if len(parts) >= 2 {
			return parts[1] // ExternalIP is the second part
		}
		if len(parts) == 1 {
			return parts[0]
		}
		return ""
	}

	// Try object format: {"external_ip": "...", "port": ...}
	var obj struct {
		ExternalIP string `json:"external_ip"`
		InternalIP string `json:"internal_ip"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.ExternalIP != "" {
		return obj.ExternalIP
	}

	return ""
}

// extractPort pulls the Port from the Endpoint field.
func extractPort(raw json.RawMessage) int {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		parts := strings.Split(s, ":")
		if len(parts) >= 3 {
			var port int
			fmt.Sscanf(parts[2], "%d", &port)
			return port
		}
	}

	var obj struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Port
	}

	return 0
}
