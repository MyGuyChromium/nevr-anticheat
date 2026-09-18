package regression

import (
	"context"
	"fmt"
	"math"
	"strings"
)

const CaptureProtocolVersion = "nevr-controlled-comparison/v1"

const (
	maxCaptureCases   = 4096
	maxCaptureAnchors = 32
	maxCaptureSeconds = 7 * 24 * 60 * 60
)

// CaptureIdentity is operator-recorded provenance, never authenticated runtime
// configuration. Unknown identities must be explicit rather than empty values
// that can be mistaken for a matched build or default settings.
type CaptureIdentity struct {
	Status string `json:"status"` // unknown or recorded
	Value  string `json:"value,omitempty"`
}

// CaptureProvenance groups correlated views before evaluation. The split guard
// applies to this manifest, not to other manifests, renamed sessions, previously
// viewed recordings, or fabricated metadata. It is not a sealed holdout service.
type CaptureProvenance struct {
	Protocol         string            `json:"protocol"`
	SessionGroup     string            `json:"session_group"`
	Split            string            `json:"split"` // development or holdout
	View             string            `json:"view"`  // local, remote, or server_relay
	SubjectPlayerID  string            `json:"subject_player_id"`
	RecorderPlayerID string            `json:"recorder_player_id,omitempty"`
	GameBuild        CaptureIdentity   `json:"game_build"`
	GameConfig       CaptureIdentity   `json:"game_config"` // recorded value is a SHA-256
	Anchors          []AlignmentAnchor `json:"alignment_anchors,omitempty"`
}

// AlignmentAnchor retains a measured event on one recording's elapsed clock.
// A shared ID links measurements of the same marker in a session group; it does
// not shift frames, infer latency from ping, or grant server-relay authority.
type AlignmentAnchor struct {
	ID                   string   `json:"id"`
	MatchID              string   `json:"match_id"`
	Method               string   `json:"method"` // visual_event or instrumented_marker
	RecordingTimeSeconds float64  `json:"recording_time_seconds"`
	UncertaintySeconds   float64  `json:"uncertainty_seconds"`
	Evidence             Artifact `json:"evidence"`
}

func (p *CaptureProvenance) clone() *CaptureProvenance {
	if p == nil {
		return nil
	}
	out := *p
	out.Anchors = append([]AlignmentAnchor(nil), p.Anchors...)
	return &out
}

func captureID(s string, build bool) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i, c := range s {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || (c != '-' && c != '_' && c != '.' && !(build && (c == '+' || c == ':' || c == '/')))) {
			return false
		}
	}
	return true
}

func (id CaptureIdentity) valid(config bool) bool {
	if id.Status == "unknown" {
		return id.Value == ""
	}
	if id.Status != "recorded" || strings.EqualFold(id.Value, "unknown") {
		return false
	}
	if config {
		return validHash(id.Value)
	}
	return captureID(id.Value, true)
}

func captureSeconds(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= maxCaptureSeconds
}

func (manifest Manifest) validateCaptureProtocol() error {
	enabled := false
	for _, item := range manifest.Cases {
		enabled = enabled || item.Capture != nil
	}
	if !enabled {
		return nil // Legacy behavior-only manifests retain their existing contract.
	}
	if len(manifest.Cases) > maxCaptureCases {
		return fmt.Errorf("controlled comparison exceeds %d cases", maxCaptureCases)
	}
	splits := map[string]string{}
	for _, item := range manifest.Cases {
		p := item.Capture
		if p == nil {
			return fmt.Errorf("controlled comparison requires capture provenance on every case; case %s is unassigned", item.ID)
		}
		if !captureID(item.ID, false) || p.Protocol != CaptureProtocolVersion || !captureID(p.SessionGroup, false) ||
			(p.Split != "development" && p.Split != "holdout") ||
			(p.View != "local" && p.View != "remote" && p.View != "server_relay") ||
			!p.GameBuild.valid(false) || !p.GameConfig.valid(true) || len(p.Anchors) > maxCaptureAnchors {
			return fmt.Errorf("case %s has invalid controlled comparison provenance", item.ID)
		}
		players := map[string]bool{}
		for _, match := range item.Expected {
			for player := range match.PlayerFrames {
				players[player] = true
			}
		}
		if !captureID(p.SubjectPlayerID, true) || !players[p.SubjectPlayerID] ||
			(p.View == "local" && p.RecorderPlayerID != p.SubjectPlayerID) ||
			(p.View == "remote" && (!captureID(p.RecorderPlayerID, true) || !players[p.RecorderPlayerID] || p.RecorderPlayerID == p.SubjectPlayerID)) ||
			(p.View == "server_relay" && p.RecorderPlayerID != "") {
			return fmt.Errorf("case %s has an unbound or contradictory recording viewpoint", item.ID)
		}
		// A different viewpoint or derived clip must not launder the same session
		// into the other split. Match IDs supplement the declared group, while the
		// digest catches relabeling of identical bytes. Missing identities are not
		// invented or grouped together as an empty session.
		keys := []string{"digest:" + strings.ToLower(item.SHA256), "group:" + strings.ToLower(p.SessionGroup)}
		matches := map[string]bool{}
		for _, match := range item.Expected {
			if id := strings.TrimSpace(match.MatchID); id != "" {
				keys = append(keys, "match:"+strings.ToLower(id))
				matches[match.MatchID] = true
			}
		}
		for _, key := range keys {
			if prior, exists := splits[key]; exists && prior != p.Split {
				return fmt.Errorf("case %s crosses development/holdout with a shared recording digest or session", item.ID)
			}
			splits[key] = p.Split
		}
		anchorIDs := map[string]bool{}
		for _, anchor := range p.Anchors {
			if !captureID(anchor.ID, false) || anchorIDs[strings.ToLower(anchor.ID)] || !captureID(anchor.MatchID, true) || !matches[anchor.MatchID] ||
				(anchor.Method != "visual_event" && anchor.Method != "instrumented_marker") ||
				!captureSeconds(anchor.RecordingTimeSeconds) || !captureSeconds(anchor.UncertaintySeconds) ||
				anchor.UncertaintySeconds <= 0 || anchor.UncertaintySeconds > 3600 ||
				strings.TrimSpace(anchor.Evidence.Path) == "" || len(anchor.Evidence.Path) > 4096 ||
				strings.ContainsAny(anchor.Evidence.Path, "\x00\r\n") || !validHash(anchor.Evidence.SHA256) {
				return fmt.Errorf("case %s has an invalid measured alignment anchor", item.ID)
			}
			anchorIDs[strings.ToLower(anchor.ID)] = true
		}
	}
	return nil
}

func verifyCaptureEvidence(ctx context.Context, item ReplayCase, baseDir string) error {
	if item.Capture == nil {
		return nil
	}
	for _, anchor := range item.Capture.Anchors {
		if err := verifyFile(ctx, ResolvePath(baseDir, anchor.Evidence.Path), anchor.Evidence.SHA256); err != nil {
			return fmt.Errorf("case %s alignment anchor %s: %w", item.ID, anchor.ID, err)
		}
	}
	return nil
}
