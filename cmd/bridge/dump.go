package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
)

// dumpJSON writes a JSON-serializable value to a file inside the dump directory.
// If dumpDir is empty, does nothing. Safe to call unconditionally.
func dumpJSON(dumpDir, filename string, v any, logger *slog.Logger) {
	if dumpDir == "" {
		return
	}
	if err := os.MkdirAll(dumpDir, 0755); err != nil {
		logger.Warn("dump: failed to create directory", "dir", dumpDir, "error", err)
		return
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		logger.Warn("dump: failed to marshal JSON", "file", filename, "error", err)
		return
	}
	path := filepath.Join(dumpDir, filename)
	if err := os.WriteFile(path, data, 0644); err != nil {
		logger.Warn("dump: failed to write file", "path", path, "error", err)
		return
	}
	logger.Info("dump: wrote debug artifact", "path", path, "bytes", len(data))
}

// dumpRaw writes raw bytes to a file inside the dump directory.
func dumpRaw(dumpDir, filename string, data []byte, logger *slog.Logger) {
	if dumpDir == "" {
		return
	}
	if err := os.MkdirAll(dumpDir, 0755); err != nil {
		logger.Warn("dump: failed to create directory", "dir", dumpDir, "error", err)
		return
	}
	path := filepath.Join(dumpDir, filename)
	if err := os.WriteFile(path, data, 0644); err != nil {
		logger.Warn("dump: failed to write file", "path", path, "error", err)
		return
	}
	logger.Info("dump: wrote debug artifact", "path", path, "bytes", len(data))
}

// sanitizeForFilename replaces characters unsafe for filenames.
func sanitizeForFilename(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_", ".", "_")
	return r.Replace(s)
}

// dumpFilename produces a deterministic dump filename including the match ID.
func dumpFilename(matchID, suffix string) string {
	if matchID == "" {
		return suffix
	}
	return sanitizeForFilename(matchID) + "_" + suffix
}

// resultManifest is a compact JSON summary of a probe/once run, written to dump dir.
type resultManifest struct {
	Mode                   string `json:"mode"`
	Timestamp              string `json:"timestamp"`
	SelectedMatchID        string `json:"selected_match_id"`
	BroadcasterIP          string `json:"broadcaster_ip"`
	APIPort                int    `json:"api_port"`
	ServerID               string `json:"server_id"`
	SessionID              string `json:"session_id"`
	SessionMatchesMatchID  bool   `json:"session_matches_match_id"`
	GameStatus             string `json:"game_status"`
	GameMode               string `json:"game_mode,omitempty"`
	PlayersSeen            int    `json:"players_seen"`
	SpectatorsSeen         int    `json:"spectators_seen"`
	MappedFrames           int    `json:"mapped_frames"`
	WarningsCount          int    `json:"warnings_count"`
	ErrorsCount            int    `json:"errors_count"`
	SamplePlayerID         string `json:"sample_player_id,omitempty"`
	AnticheatSendAttempted bool   `json:"anticheat_send_attempted"`
	AnticheatSendSucceeded bool   `json:"anticheat_send_succeeded"`
	FramesAcked            int    `json:"frames_acked"`
	FramesRejected         int    `json:"frames_rejected"`
	SendSkipReason         string `json:"send_skip_reason,omitempty"`
	DryRun                 bool   `json:"dry_run"`
}

// cycleResult carries what a probe/once cycle learned, for logging and the manifest.
type cycleResult struct {
	match          DiscoveredMatch
	session        *adapter.EchoVRSessionResponse
	roster         sessionRoster
	frames         int
	warnings       int
	errors         int
	samplePlayerID string
	sendAttempted  bool
	sendSucceeded  bool
	framesAcked    int
	framesRejected int
	reason         string
}

// dumpManifest writes the result manifest to the dump directory.
func dumpManifest(dumpDir string, r cycleResult, cfg *BridgeConfig, mode string, logger *slog.Logger) {
	if dumpDir == "" {
		return
	}
	manifest := resultManifest{
		Mode:                   mode,
		Timestamp:              time.Now().UTC().Format(time.RFC3339),
		SelectedMatchID:        r.match.MatchID,
		BroadcasterIP:          r.match.BroadcasterIP,
		APIPort:                cfg.APIPort,
		ServerID:               r.match.serverID(cfg),
		SessionID:              r.session.SessionID,
		SessionMatchesMatchID:  sessionMatchesMatchID(r.session.SessionID, r.match),
		GameStatus:             r.session.GameStatus,
		GameMode:               r.session.MatchType,
		PlayersSeen:            r.roster.PlayerCount,
		SpectatorsSeen:         len(r.roster.Spectators),
		MappedFrames:           r.frames,
		WarningsCount:          r.warnings,
		ErrorsCount:            r.errors,
		SamplePlayerID:         r.samplePlayerID,
		AnticheatSendAttempted: r.sendAttempted,
		AnticheatSendSucceeded: r.sendSucceeded,
		FramesAcked:            r.framesAcked,
		FramesRejected:         r.framesRejected,
		SendSkipReason:         r.reason,
		DryRun:                 cfg.AnticheatURL == "",
	}
	dumpJSON(dumpDir, dumpFilename(r.match.MatchID, "manifest.json"), manifest, logger)
}

// logFirstRunChecklist prints a short guidance block for first-time operators.
func logFirstRunChecklist(logger *slog.Logger) {
	logger.Info("first-run guidance: recommended validation sequence")
	logger.Info("  1. run with --probe to verify Nakama discovery + broadcaster /session reachability")
	logger.Info("  2. run with --once to verify full cycle including anticheat WebSocket delivery (hello + ack)")
	logger.Info("  3. after --once, check: sqlite3 <db> \"SELECT COUNT(*) FROM telemetry_frames;\"")
	logger.Info("  4. run continuous mode with --match-id to isolate a single match")
	logger.Info("  5. use --dump-dir <path> with probe/once to save raw responses for debugging")
	logger.Info("  identity format in use: echovr:<userid>")
}
