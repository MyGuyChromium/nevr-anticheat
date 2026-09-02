// Command bridge is a standalone profiler bridge between EchoTools/Nakama
// broadcasters and the NEVR-Anticheat telemetry ingestion server.
//
// Architecture:
//
//	Broadcaster (/session API)  →  bridge (this binary)  →  NEVR-Anticheat (/telemetry WS)
//
// The bridge discovers active matches from Nakama's match API, resolves
// broadcaster endpoints, polls each broadcaster's Echo VR /session HTTP API,
// converts the response to NEVR telemetry frames using the existing adapter,
// and forwards FrameBatch payloads to the anticheat server's WebSocket endpoint.
//
// This process runs outside both Nakama's match loop and the broadcaster's
// game loop. It cannot block or slow live gameplay.
//
// Identity: PlayerID is set to "echovr:<userid>" by the existing adapter.Mapper,
// which is the same format used by all existing anticheat telemetry. This ensures
// cross-match tracking consistency. Nakama's UUID-based user_id can be derived
// deterministically from this via EvrId.UUID() when cross-system queries are needed.
//
// Trust boundary: everything the bridge forwards was pulled over plaintext HTTP
// from whatever host Nakama's match label advertises. Every batch and control
// message therefore carries ServerID = "<broadcaster_ip>:<api_port>" so stored
// evidence can be traced to its source, and --broadcaster-allowlist restricts
// polling to known hosts.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func main() {
	nakamaURL := flag.String("nakama-url", "http://127.0.0.1:7350", "Nakama HTTP API base URL")
	nakamaServerKey := flag.String("nakama-server-key", "defaultkey", "Nakama server key (used to authenticate the bridge account; cannot list matches on its own)")
	nakamaAuthMode := flag.String("nakama-auth", nakamaAuthDevice, "Nakama auth mode: device (authenticate a bridge account with the server key and refresh the session), bearer (use --nakama-bearer-token), basic (raw server-key Basic auth; only works behind a proxy)")
	nakamaDeviceID := flag.String("nakama-device-id", "nevr-anticheat-bridge", "Device id for the bridge account (--nakama-auth device)")
	nakamaUsername := flag.String("nakama-username", "nevr-anticheat-bridge", "Username for the bridge account (--nakama-auth device)")
	nakamaBearerToken := flag.String("nakama-bearer-token", "", "Nakama session token for discovery auth (--nakama-auth bearer)")
	nakamaRefreshToken := flag.String("nakama-refresh-token", "", "Nakama refresh token paired with --nakama-bearer-token (enables refresh before expiry)")
	anticheatURL := flag.String("anticheat-url", "", "NEVR-Anticheat WebSocket ingestion URL (e.g. ws://127.0.0.1:8080/telemetry)")
	anticheatToken := flag.String("anticheat-token", "", "Bearer token for anticheat auth (empty = no auth)")
	apiPort := flag.Int("api-port", 6721, "Echo VR session API port on broadcasters")
	pollInterval := flag.Duration("poll-interval", 67*time.Millisecond, "Polling interval for broadcaster /session API (~15fps); frames carry the real sample time regardless")
	discoveryInterval := flag.Duration("discovery-interval", 10*time.Second, "Interval between match discovery scans")
	idleInterval := flag.Duration("idle-interval", defaultIdleInterval, "Polling interval while a match is in post_match or its broadcaster is failing")
	idleGiveUp := flag.Duration("idle-give-up", defaultIdleGiveUp, "Stop polling a match after it has been idle (post_match / failing) this long")
	ackTimeout := flag.Duration("ack-timeout", defaultAckTimeout, "Treat the anticheat link as dead when sent frames are not acked within this time")
	queueSize := flag.Int("queue-size", defaultQueueSize, "Bounded anticheat send queue (batches); a stalled link drops frames instead of blocking pollers")
	modes := flag.String("modes", "echo_arena", "Comma-separated match mode prefixes to poll (case-insensitive); empty or * = all modes")
	allowlist := flag.String("broadcaster-allowlist", "", "Comma-separated IPs/CIDRs; when set, only broadcasters inside the list are polled")
	sessionCheck := flag.String("session-check", sessionCheckStrict, "Cross-check /session sessionid against the Nakama match id: strict (stop poller on mismatch), warn, off")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error")
	probeOnly := flag.Bool("probe", false, "Probe mode: discover + fetch + map one match, then exit")
	once := flag.Bool("once", false, "Once mode: full cycle (discover + fetch + map + send + wait for ack) for one match, then exit")
	matchID := flag.String("match-id", "", "Only poll this specific match ID (for validation; empty = all matches)")
	dumpDir := flag.String("dump-dir", "", "Write debug artifacts (raw responses, mapped batches) to this directory (probe/once only)")
	flag.Parse()

	var level slog.Level
	switch strings.ToLower(*logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg := &BridgeConfig{
		NakamaURL:            *nakamaURL,
		NakamaServerKey:      *nakamaServerKey,
		NakamaAuthMode:       *nakamaAuthMode,
		NakamaDeviceID:       *nakamaDeviceID,
		NakamaUsername:       *nakamaUsername,
		NakamaBearerToken:    *nakamaBearerToken,
		NakamaRefreshToken:   *nakamaRefreshToken,
		AnticheatURL:         *anticheatURL,
		AnticheatToken:       *anticheatToken,
		APIPort:              *apiPort,
		PollInterval:         *pollInterval,
		DiscoveryInterval:    *discoveryInterval,
		IdleInterval:         *idleInterval,
		IdleGiveUp:           *idleGiveUp,
		AckTimeout:           *ackTimeout,
		QueueSize:            *queueSize,
		Modes:                *modes,
		BroadcasterAllowlist: *allowlist,
		SessionCheck:         *sessionCheck,
		MatchIDFilter:        *matchID,
		DumpDir:              *dumpDir,
	}

	// Determine run mode
	mode := "continuous"
	if *probeOnly {
		mode = "probe"
	} else if *once {
		mode = "once"
	}

	// Config validation
	if *probeOnly && *once {
		logger.Error("cannot use --probe and --once together")
		os.Exit(1)
	}
	if err := validateConfig(cfg, mode); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	// Startup banner
	logger.Info("nevr-bridge starting",
		"mode", mode,
		"nakama_url", cfg.NakamaURL,
		"nakama_auth_mode", cfg.NakamaAuthMode,
		"anticheat_url", anticheatURLDisplay(cfg.AnticheatURL),
		"api_port", cfg.APIPort,
		"poll_interval", cfg.PollInterval,
		"discovery_interval", cfg.DiscoveryInterval,
		"modes", cfg.Modes,
		"broadcaster_allowlist", allowlistDisplay(cfg.BroadcasterAllowlist),
		"session_check", cfg.SessionCheck,
		"match_filter", matchFilterDisplay(cfg.MatchIDFilter),
		"identity_format", "echovr:<userid>",
		"discovery_method", "Nakama standard API (GET /v2/match)",
	)

	if cfg.APIPort == 6721 {
		logger.Info("using default api-port 6721 — change with --api-port if broadcaster uses a different port")
	}
	if cfg.AnticheatURL == "" && mode != "probe" {
		logger.Warn("no --anticheat-url provided — telemetry will NOT be forwarded (dry-run behavior)")
	}
	if cfg.AnticheatURL != "" && cfg.AnticheatToken == "" {
		logger.Warn("no --anticheat-token provided — the ingest server will reject the connection unless it runs with --allow-unauthenticated")
	}
	if cfg.BroadcasterAllowlist == "" {
		logger.Warn("no --broadcaster-allowlist: telemetry will be pulled from ANY host a Nakama match label advertises (plaintext HTTP, unauthenticated)")
	}
	if cfg.NakamaAuthMode == nakamaAuthBasic {
		logger.Warn("--nakama-auth basic: Nakama itself rejects server-key Basic auth on /v2/match; expect 401 unless a proxy maps it")
	}
	logger.Info("external dependency: broadcaster must expose Echo VR /session HTTP API at <broadcaster_ip>:<api-port>/session")
	if cfg.DumpDir != "" {
		logger.Info("dump mode enabled — debug artifacts will be written", "dump_dir", cfg.DumpDir)
	}
	logFirstRunChecklist(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	stats := &bridgeStats{startTime: time.Now(), mode: mode}

	var runErr error
	switch mode {
	case "probe":
		runErr = doProbe(ctx, cfg, stats, logger)
		if runErr != nil {
			logger.Error("probe failed", "error", runErr)
		}
	case "once":
		runErr = doOnce(ctx, cfg, stats, logger)
		if runErr != nil {
			logger.Error("once mode failed", "error", runErr)
		}
	default:
		runBridge(ctx, cfg, stats, logger)
	}

	stats.logSummary(logger)
	if runErr != nil {
		os.Exit(1)
	}
}

func anticheatURLDisplay(u string) string {
	if u == "" {
		return "(not set — no forwarding)"
	}
	return u
}

func matchFilterDisplay(id string) string {
	if id == "" {
		return "(all matches)"
	}
	return id
}

func allowlistDisplay(al string) string {
	if al == "" {
		return "(none — any advertised host)"
	}
	return al
}

// BridgeConfig holds all bridge configuration.
type BridgeConfig struct {
	NakamaURL          string
	NakamaServerKey    string
	NakamaAuthMode     string // device | bearer | basic; "" = bearer if a token is set, else basic (legacy)
	NakamaDeviceID     string
	NakamaUsername     string
	NakamaBearerToken  string
	NakamaRefreshToken string
	AnticheatURL       string
	AnticheatToken     string
	APIPort            int
	PollInterval       time.Duration
	DiscoveryInterval  time.Duration
	MatchIDFilter      string // empty = all matches
	DumpDir            string // empty = no dumping

	// Lifecycle / link tuning (zero = default).
	IdleInterval    time.Duration
	IdleGiveUp      time.Duration
	AckTimeout      time.Duration
	HelloTimeout    time.Duration
	DialTimeout     time.Duration
	WriteTimeout    time.Duration
	Keepalive       time.Duration
	StatusInterval  time.Duration
	QueueSize       int
	ConnectAttempts int

	// Trust boundary.
	Modes                string        // comma-separated mode prefixes; "" = all
	BroadcasterAllowlist string        // comma-separated IPs/CIDRs; "" = any
	SessionCheck         string        // strict | warn | off; "" = strict
	MismatchCooldown     time.Duration // skip a match this long after a session_mismatch stop (zero = 5m)
}

// bridgeStats tracks process-level counters.
type bridgeStats struct {
	startTime time.Time
	mode      string

	MatchesDiscovered      atomic.Int64
	MatchesSkippedFilter   atomic.Int64
	MatchesSkippedMode     atomic.Int64
	MatchesSkippedAllow    atomic.Int64
	MatchesSkippedNoEP     atomic.Int64
	PollersStarted         atomic.Int64
	PollersStopped         atomic.Int64
	PollerIdleTransitions  atomic.Int64
	SessionMismatches      atomic.Int64
	MatchesSkippedMismatch atomic.Int64
	TotalPolls             atomic.Int64
	TotalPollFailures      atomic.Int64
	PollsDuplicate         atomic.Int64
	TotalFramesMapped      atomic.Int64
	FramesDroppedSpectator atomic.Int64

	// Link counters. TotalFramesForwarded counts frames the ingest server
	// ACKED as accepted (or, in dry-run, frames that would have been sent).
	TotalBatchesSent       atomic.Int64
	TotalBatchesDropped    atomic.Int64
	TotalFramesSent        atomic.Int64
	TotalFramesForwarded   atomic.Int64
	TotalFramesAcked       atomic.Int64
	TotalFramesRejected    atomic.Int64
	TotalFramesIgnored     atomic.Int64
	TotalFramesDropped     atomic.Int64
	TotalFramesUnackedLost atomic.Int64
	TotalSendFailures      atomic.Int64
	AcksReceived           atomic.Int64
	AckTimeouts            atomic.Int64
	Reconnects             atomic.Int64
	AuthFailures           atomic.Int64
	ControlSent            atomic.Int64
	ControlDropped         atomic.Int64
}

func (s *bridgeStats) logSummary(logger *slog.Logger) {
	logger.Info("nevr-bridge shutdown summary",
		"mode", s.mode,
		"uptime", time.Since(s.startTime).Round(time.Second),
		"matches_discovered", s.MatchesDiscovered.Load(),
		"matches_skipped_filter", s.MatchesSkippedFilter.Load(),
		"matches_skipped_mode", s.MatchesSkippedMode.Load(),
		"matches_skipped_allowlist", s.MatchesSkippedAllow.Load(),
		"matches_skipped_no_endpoint", s.MatchesSkippedNoEP.Load(),
		"pollers_started", s.PollersStarted.Load(),
		"pollers_stopped", s.PollersStopped.Load(),
		"poller_idle_transitions", s.PollerIdleTransitions.Load(),
		"session_mismatches", s.SessionMismatches.Load(),
		"matches_skipped_mismatch_cooldown", s.MatchesSkippedMismatch.Load(),
		"total_polls", s.TotalPolls.Load(),
		"total_poll_failures", s.TotalPollFailures.Load(),
		"polls_duplicate_snapshot", s.PollsDuplicate.Load(),
		"frames_mapped", s.TotalFramesMapped.Load(),
		"frames_dropped_spectator", s.FramesDroppedSpectator.Load(),
		"total_batches_sent", s.TotalBatchesSent.Load(),
		"total_frames_sent", s.TotalFramesSent.Load(),
		"total_frames_forwarded_acked", s.TotalFramesForwarded.Load(),
		"frames_rejected_by_ingest", s.TotalFramesRejected.Load(),
		"frames_ignored_duplicates", s.TotalFramesIgnored.Load(),
		"frames_dropped_queue", s.TotalFramesDropped.Load(),
		"frames_unacked_lost", s.TotalFramesUnackedLost.Load(),
		"total_send_failures", s.TotalSendFailures.Load(),
		"ack_timeouts", s.AckTimeouts.Load(),
		"connections", s.Reconnects.Load(),
		"auth_failures", s.AuthFailures.Load(),
		"control_sent", s.ControlSent.Load(),
		"control_dropped", s.ControlDropped.Load(),
	)
}

// filterMatches applies --match-id, --modes and --broadcaster-allowlist and
// tracks skip counts.
func filterMatches(matches []DiscoveredMatch, cfg *BridgeConfig, stats *bridgeStats, logger *slog.Logger) []DiscoveredMatch {
	al, _ := parseAllowlist(cfg.BroadcasterAllowlist)
	var filtered []DiscoveredMatch
	for _, m := range matches {
		if cfg.MatchIDFilter != "" && m.MatchID != cfg.MatchIDFilter {
			stats.MatchesSkippedFilter.Add(1)
			logger.Debug("skipping match (--match-id filter)", "match_id", m.MatchID)
			continue
		}
		if !modeAllowed(cfg.Modes, m.Mode) {
			stats.MatchesSkippedMode.Add(1)
			logger.Debug("skipping match (--modes filter)", "match_id", m.MatchID, "mode", m.Mode)
			continue
		}
		if !al.allows(m.BroadcasterIP) {
			stats.MatchesSkippedAllow.Add(1)
			logger.Warn("skipping match: broadcaster not in --broadcaster-allowlist", "match_id", m.MatchID, "broadcaster_ip", m.BroadcasterIP)
			continue
		}
		filtered = append(filtered, m)
	}
	return filtered
}

// fetchAndMap does a single /session fetch + mapping for one match. Returns the
// parsed session, roster, mapping result (frames already spectator-filtered and
// stamped with the real sample time) and any error. Used by probe and once modes.
// If cfg.DumpDir is set, saves raw response and mapped batch to disk.
func fetchAndMap(m DiscoveredMatch, cfg *BridgeConfig, logger *slog.Logger) (*adapter.EchoVRSessionResponse, sessionRoster, *adapter.MappingResult, error) {
	sessionURL := fmt.Sprintf("http://%s:%d/session", m.BroadcasterIP, cfg.APIPort)
	logger.Info("fetching broadcaster session", "url", sessionURL)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(sessionURL)
	if err != nil {
		return nil, sessionRoster{}, nil, fmt.Errorf("/session not reachable at %s: %w", sessionURL, err)
	}
	defer resp.Body.Close()
	sampleAt := time.Now()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxSessionBody))
	if resp.StatusCode != 200 {
		return nil, sessionRoster{}, nil, fmt.Errorf("/session returned HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	dumpRaw(cfg.DumpDir, dumpFilename(m.MatchID, "session_raw.json"), body, logger)

	var session adapter.EchoVRSessionResponse
	if err := json.Unmarshal(body, &session); err != nil {
		return nil, sessionRoster{}, nil, fmt.Errorf("invalid JSON from /session: %w (preview: %s)", err, truncate(string(body), 200))
	}

	roster := buildRoster(&session)
	mapper := adapter.NewMapper()
	result := mapper.MapSession(&session)
	var dropped int
	result.Frames, dropped = filterFrames(result.Frames, roster)
	if dropped > 0 {
		logger.Info("dropped spectator/moderator entries from frames", "dropped", dropped, "spectators", roster.Spectators)
	}
	epoch := &frameEpoch{}
	epoch.stamp(sampleAt, result.Frames)

	dumpJSON(cfg.DumpDir, dumpFilename(m.MatchID, "mapped_frames.json"), result.Frames, logger)

	return &session, roster, result, nil
}

// logResultBlock logs a structured result summary for probe/once modes.
func logResultBlock(r cycleResult, cfg *BridgeConfig, result *adapter.MappingResult, logger *slog.Logger) {
	dryRun := cfg.AnticheatURL == ""

	logger.Info("--- RESULT ---",
		"selected_match_id", r.match.MatchID,
		"broadcaster_ip", r.match.BroadcasterIP,
		"api_port", cfg.APIPort,
		"server_id", r.match.serverID(cfg),
		"session_id", r.session.SessionID,
		"session_matches_match_id", sessionMatchesMatchID(r.session.SessionID, r.match),
		"game_status", r.session.GameStatus,
		"game_mode", r.session.MatchType,
		"players_seen", r.roster.PlayerCount,
		"spectators_seen", len(r.roster.Spectators),
		"mapped_frames", r.frames,
		"warnings_count", r.warnings,
		"errors_count", r.errors,
		"sample_player_id", r.samplePlayerID,
		"anticheat_send_attempted", r.sendAttempted,
		"anticheat_send_succeeded", r.sendSucceeded,
		"frames_acked", r.framesAcked,
		"frames_rejected", r.framesRejected,
		"dry_run", dryRun,
	)

	if !sessionMatchesMatchID(r.session.SessionID, r.match) {
		logger.Warn("/session sessionid does not match the Nakama match id — verify --api-port points at the instance hosting this match",
			"session_id", r.session.SessionID, "nakama_match_id", r.match.MatchID, "label_id", r.match.LabelID)
	}
	if !r.sendAttempted && r.reason != "" {
		logger.Info("send skipped", "reason", r.reason)
	}
	if r.sendAttempted && !r.sendSucceeded && r.reason != "" {
		logger.Error("send failed", "reason", r.reason)
	}

	if result != nil {
		for _, w := range result.Warnings {
			logger.Info("mapping warning", "field", w.Field, "message", w.Message)
		}
		for _, e := range result.Errors {
			logger.Warn("mapping error", "player", e.PlayerName, "field", e.Field, "message", e.Message)
		}
	}
}

func newCycleResult(m DiscoveredMatch, session *adapter.EchoVRSessionResponse, roster sessionRoster, result *adapter.MappingResult) cycleResult {
	r := cycleResult{match: m, session: session, roster: roster}
	if result != nil {
		r.frames = len(result.Frames)
		r.warnings = len(result.Warnings)
		r.errors = len(result.Errors)
		if len(result.Frames) > 0 {
			r.samplePlayerID = result.Frames[0].PlayerID
		}
	}
	return r
}

// discoveryError wraps a discovery failure; a nakamaAuthError stays
// recognisable through errors.As so callers can report it as fatal.
func discoveryError(err error) error {
	return fmt.Errorf("discovery failed: %w", err)
}

// doProbe is the testable core of probe mode: discover + fetch + map, no send.
func doProbe(ctx context.Context, cfg *BridgeConfig, stats *bridgeStats, logger *slog.Logger) error {
	logger.Info("--- PROBE MODE ---")

	matches, err := discoverMatches(ctx, cfg, logger, stats)
	if err != nil {
		return discoveryError(err)
	}
	stats.MatchesDiscovered.Add(int64(len(matches)))
	dumpJSON(cfg.DumpDir, "discovery_matches.json", matches, logger) // not match-specific

	matches = filterMatches(matches, cfg, stats, logger)
	logger.Info("discovered matches", "total", stats.MatchesDiscovered.Load(), "after_filter", len(matches))

	if len(matches) == 0 {
		logger.Warn("no active matches found — nothing to probe")
		return nil
	}

	m := selectMatch(matches)
	logger.Info("selected match", "match_id", m.MatchID, "reason", "deterministic (match_id ascending)")

	session, roster, result, err := fetchAndMap(m, cfg, logger)
	if err != nil {
		return fmt.Errorf("broadcaster fetch/map failed: %w", err)
	}

	r := newCycleResult(m, session, roster, result)
	r.reason = "probe does not send; use --once"
	logResultBlock(r, cfg, result, logger)
	dumpManifest(cfg.DumpDir, r, cfg, "probe", logger)
	return nil
}

// doOnce is the testable core of once mode: discover → fetch → map → send →
// wait for the ingest ack → exit. A send only counts as succeeded when the
// server acknowledged at least one accepted frame.
func doOnce(ctx context.Context, cfg *BridgeConfig, stats *bridgeStats, logger *slog.Logger) error {
	logger.Info("--- ONCE MODE: single full cycle ---")

	matches, err := discoverMatches(ctx, cfg, logger, stats)
	if err != nil {
		return discoveryError(err)
	}
	stats.MatchesDiscovered.Add(int64(len(matches)))

	matches = filterMatches(matches, cfg, stats, logger)
	if len(matches) == 0 {
		logger.Warn("no active matches — nothing to process")
		return nil
	}

	m := selectMatch(matches)
	logger.Info("selected match", "match_id", m.MatchID, "reason", "deterministic (match_id ascending)")

	session, roster, result, err := fetchAndMap(m, cfg, logger)
	if err != nil {
		return fmt.Errorf("fetch/map failed: %w", err)
	}
	stats.TotalPolls.Add(1)
	r := newCycleResult(m, session, roster, result)

	if len(result.Frames) == 0 {
		logger.Warn("broadcaster returned valid JSON but mapper produced zero frames — check game_status, player count and spectators")
		r.reason = "zero frames mapped"
		logResultBlock(r, cfg, result, logger)
		dumpManifest(cfg.DumpDir, r, cfg, "once", logger)
		return nil
	}

	if cfg.AnticheatURL == "" {
		r.reason = "dry-run: no anticheat URL"
		logResultBlock(r, cfg, result, logger)
		dumpManifest(cfg.DumpDir, r, cfg, "once", logger)
		return nil
	}

	sessionCheck := cfg.SessionCheck
	if sessionCheck == "" {
		sessionCheck = sessionCheckStrict
	}
	if sessionCheck == sessionCheckStrict && !sessionMatchesMatchID(session.SessionID, m) {
		stats.SessionMismatches.Add(1)
		r.reason = fmt.Sprintf("session_mismatch: /session sessionid %q is not the Nakama match %q (use --session-check warn to send anyway)", session.SessionID, m.MatchID)
		logResultBlock(r, cfg, result, logger)
		dumpManifest(cfg.DumpDir, r, cfg, "once", logger)
		return fmt.Errorf("%s", r.reason)
	}

	serverID := m.serverID(cfg)
	batch := &FrameBatch{
		MatchID:   m.MatchID,
		ServerID:  serverID,
		Timestamp: time.Now(),
		Frames:    result.Frames,
	}

	sender, err := newWSSender(cfg, stats, logger)
	if err != nil {
		r.reason = fmt.Sprintf("WebSocket connect failed: %v", err)
		logResultBlock(r, cfg, result, logger)
		dumpManifest(cfg.DumpDir, r, cfg, "once", logger)
		return err
	}
	if err := sender.connect(ctx); err != nil {
		r.reason = fmt.Sprintf("WebSocket connect failed: %v", err)
		logResultBlock(r, cfg, result, logger)
		dumpManifest(cfg.DumpDir, r, cfg, "once", logger)
		return fmt.Errorf("anticheat WebSocket connect failed: %w", err)
	}
	defer sender.close()
	sender.start(ctx)

	baseAccepted := stats.TotalFramesAcked.Load()
	baseRejected := stats.TotalFramesRejected.Load()

	gameMode := session.MatchType
	if gameMode == "" {
		gameMode = m.Mode
	}
	sender.enqueueControl(&model.ControlMessage{
		Type: model.ControlMatchStart, MatchID: m.MatchID, ServerID: serverID,
		GameMode: gameMode, Map: session.MapName, IsPrivate: session.PrivateMatch, Teams: roster.teamsCopy(),
	})
	r.sendAttempted = true
	if !sender.enqueueBatch(batch) {
		r.reason = "send queue full"
		logResultBlock(r, cfg, result, logger)
		dumpManifest(cfg.DumpDir, r, cfg, "once", logger)
		return fmt.Errorf("anticheat send failed: %s", r.reason)
	}
	sender.enqueueControl(&model.ControlMessage{Type: model.ControlMatchEnd, MatchID: m.MatchID, ServerID: serverID, Reason: "once"})

	ackTimeout := orDuration(cfg.AckTimeout, 10*time.Second)
	accepted, rejected, err := sender.waitForAcks(ctx, baseAccepted, baseRejected, len(result.Frames), ackTimeout)
	r.framesAcked = accepted
	r.framesRejected = rejected
	if err != nil {
		r.reason = fmt.Sprintf("WebSocket send not acknowledged: %v", err)
		logger.Error("anticheat did not acknowledge the batch — verify the ingest server is running, the token matches, and the batch is within the server's limits",
			"anticheat_url", cfg.AnticheatURL, "error", err, "send_failures", stats.TotalSendFailures.Load())
		logResultBlock(r, cfg, result, logger)
		dumpManifest(cfg.DumpDir, r, cfg, "once", logger)
		return fmt.Errorf("anticheat send failed: %w", err)
	}
	if accepted == 0 {
		r.reason = fmt.Sprintf("ingest server rejected all %d frames", rejected)
		logResultBlock(r, cfg, result, logger)
		dumpManifest(cfg.DumpDir, r, cfg, "once", logger)
		return fmt.Errorf("anticheat send failed: %s", r.reason)
	}

	r.sendSucceeded = true
	logResultBlock(r, cfg, result, logger)
	dumpManifest(cfg.DumpDir, r, cfg, "once", logger)
	return nil
}

// runBridge runs the continuous bridge loop.
func runBridge(ctx context.Context, cfg *BridgeConfig, stats *bridgeStats, logger *slog.Logger) {
	if cfg.AnticheatURL == "" {
		logger.Warn("running in dry-run mode: no --anticheat-url provided, frames will be polled and mapped but NOT sent")
	}

	var activePollers sync.Map
	var mismatched sync.Map // match_id -> time.Time until which the match is skipped
	var wg sync.WaitGroup
	epochs := newEpochRegistry()
	disc := newDiscoverer(cfg, stats, logger)
	mismatchCooldown := orDuration(cfg.MismatchCooldown, 5*time.Minute)

	// WebSocket sender (nil if no anticheat URL). The writer goroutine keeps
	// its own context so match_end messages queued during shutdown still go
	// out after the pollers have stopped.
	var sender *wsSender
	if cfg.AnticheatURL != "" {
		var err error
		sender, err = newWSSender(cfg, stats, logger)
		if err != nil {
			logger.Error("invalid anticheat configuration; running without forwarding", "error", err)
		} else {
			if err := sender.connect(ctx); err != nil {
				logger.Error("initial anticheat connection failed — will keep retrying in the background; frames are NOT forwarded until it succeeds", "error", err)
			}
			senderCtx, senderCancel := context.WithCancel(context.Background())
			sender.start(senderCtx)
			defer func() {
				sender.drain(2 * time.Second)
				senderCancel()
				sender.close()
			}()
		}
	}

	discoveryTicker := time.NewTicker(cfg.DiscoveryInterval)
	defer discoveryTicker.Stop()
	var consecutiveDiscoveryFailures int
	var consecutiveEmptyDiscoveries int
	var dryRunReminder int
	var modeSkipLogged bool

	discover := func() {
		matches, err := disc.discover(ctx)
		if err != nil {
			consecutiveDiscoveryFailures++
			consecutiveEmptyDiscoveries = 0
			if consecutiveDiscoveryFailures <= 3 || consecutiveDiscoveryFailures%30 == 0 {
				if isNakamaAuthError(err) {
					logger.Error("NAKAMA AUTH FAILED — discovery cannot list matches until the credentials are fixed",
						"error", err, "consecutive_failures", consecutiveDiscoveryFailures)
				} else {
					logger.Warn("match discovery failed — Nakama may be unreachable",
						"error", err, "consecutive_failures", consecutiveDiscoveryFailures)
				}
			}
			return
		}
		if consecutiveDiscoveryFailures > 0 {
			logger.Info("match discovery recovered", "after_failures", consecutiveDiscoveryFailures)
		}
		consecutiveDiscoveryFailures = 0

		stats.MatchesDiscovered.Add(int64(len(matches)))
		before := stats.MatchesSkippedMode.Load()
		matches = filterMatches(matches, cfg, stats, logger)
		if skipped := stats.MatchesSkippedMode.Load() - before; skipped > 0 && !modeSkipLogged {
			modeSkipLogged = true
			logger.Info("matches skipped by --modes filter", "skipped", skipped, "modes", cfg.Modes)
		}

		if len(matches) == 0 {
			consecutiveEmptyDiscoveries++
			if consecutiveEmptyDiscoveries == 1 || consecutiveEmptyDiscoveries == 6 || consecutiveEmptyDiscoveries%30 == 0 {
				logger.Info("Nakama reachable but no active matches found — waiting for matches to start",
					"consecutive_empty", consecutiveEmptyDiscoveries,
				)
			}
		} else {
			consecutiveEmptyDiscoveries = 0
		}

		// Periodic dry-run reminder
		if cfg.AnticheatURL == "" {
			dryRunReminder++
			if dryRunReminder%6 == 0 { // ~every 60s at 10s interval
				logger.Warn("DRY-RUN active: frames are polled and mapped but NOT forwarded to anticheat (add --anticheat-url to enable forwarding)")
			}
		}

		now := time.Now()
		activeMatchIDs := make(map[string]bool)
		var newPollersThisCycle int
		var skippedEndpointsThisCycle int
		for _, m := range matches {
			activeMatchIDs[m.MatchID] = true
			epochs.touch(m.MatchID, now)

			if _, exists := activePollers.Load(m.MatchID); exists {
				continue
			}
			if until, ok := mismatched.Load(m.MatchID); ok {
				if now.Before(until.(time.Time)) {
					stats.MatchesSkippedMismatch.Add(1)
					continue
				}
				mismatched.Delete(m.MatchID)
			}

			if err := validateBroadcasterIP(m.BroadcasterIP); err != nil {
				logger.Warn("skipping match: invalid broadcaster endpoint",
					"match_id", m.MatchID,
					"broadcaster_ip", m.BroadcasterIP,
					"reason", err.Error(),
					"action", "check Nakama match label for this match — broadcaster endpoint may not be exposed in this API response",
				)
				stats.MatchesSkippedNoEP.Add(1)
				skippedEndpointsThisCycle++
				continue
			}

			pollerCtx, pollerCancel := context.WithCancel(ctx)
			epoch := epochs.get(m.MatchID)
			poller := newMatchPoller(m, cfg, sender, stats, epoch, pollerCancel, logger)
			activePollers.Store(m.MatchID, poller)
			stats.PollersStarted.Add(1)
			newPollersThisCycle++

			wg.Add(1)
			go func(p *matchPoller, pCtx context.Context) {
				defer wg.Done()
				// Only remove our own entry: a replacement poller stored under
				// the same key must never be deleted by a stale goroutine.
				defer activePollers.CompareAndDelete(p.match.MatchID, p)
				defer stats.PollersStopped.Add(1)
				p.run(pCtx)
				if p.stopReason() == stopSessionMismatch {
					// Do not recreate a poller for this match every cycle; the
					// endpoint is serving another instance's session.
					mismatched.Store(p.match.MatchID, time.Now().Add(mismatchCooldown))
					logger.Warn("match parked after session mismatch", "match_id", p.match.MatchID, "cooldown", mismatchCooldown)
				}
			}(poller, pollerCtx)

			nextIdx, _ := epoch.snapshot()
			logger.Info("started polling match",
				"match_id", m.MatchID,
				"broadcaster", fmt.Sprintf("%s:%d", m.BroadcasterIP, cfg.APIPort),
				"mode", m.Mode,
				"level", m.Level,
				"players", m.PlayerCount,
				"region", m.Region,
				"resume_frame_index", nextIdx,
			)
		}

		activePollers.Range(func(key, value any) bool {
			matchID := key.(string)
			if !activeMatchIDs[matchID] {
				value.(*matchPoller).cancel()
				logger.Info("match no longer in Nakama, stopping poller", "match_id", matchID)
			}
			return true
		})

		// Forget frame epochs of matches Nakama has not listed for an hour.
		if removed := epochs.expire(now, time.Hour); removed > 0 {
			logger.Debug("expired frame epochs", "removed", removed, "remaining", epochs.size())
		}

		// Operator diagnostic: all matches discovered but all had bad endpoints
		if len(matches) > 0 && skippedEndpointsThisCycle > 0 && newPollersThisCycle == 0 {
			var existingPollerCount int
			activePollers.Range(func(_, _ any) bool { existingPollerCount++; return true })
			if existingPollerCount == 0 {
				logger.Warn("all discovered matches were skipped due to invalid broadcaster endpoints — no active pollers",
					"matches_discovered", len(matches),
					"skipped_bad_endpoint", skippedEndpointsThisCycle,
					"action", "Nakama may be stripping endpoint data. Try --probe --dump-dir to inspect raw discovery responses. Alternatively, broadcaster endpoints may require a different API (e.g. server-key auth or admin API).",
				)
			}
		}

		var pollerCount, idleCount int
		activePollers.Range(func(_, v any) bool {
			pollerCount++
			if v.(*matchPoller).currentState() != stateActive {
				idleCount++
			}
			return true
		})
		attrs := []any{
			"active_pollers", pollerCount,
			"idle_pollers", idleCount,
			"matches_in_nakama", len(matches),
			"frames_sent", stats.TotalFramesSent.Load(),
			"frames_acked", stats.TotalFramesForwarded.Load(),
			"frames_rejected", stats.TotalFramesRejected.Load(),
			"frames_dropped", stats.TotalFramesDropped.Load(),
			"total_batches_sent", stats.TotalBatchesSent.Load(),
			"uptime", time.Since(stats.startTime).Round(time.Second),
		}
		if sender != nil {
			attrs = append(attrs, "anticheat_connected", sender.connected.Load(), "send_queue", sender.queued())
		}
		logger.Info("bridge status", attrs...)
	}

	discover()

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down, cancelling all pollers")
			activePollers.Range(func(_, value any) bool {
				value.(*matchPoller).cancel()
				return true
			})
			wg.Wait()
			return
		case <-discoveryTicker.C:
			discover()
		}
	}
}

// FrameBatch mirrors ingest.FrameBatch for serialization to the anticheat server.
type FrameBatch struct {
	MatchID   string                       `json:"match_id"`
	ServerID  string                       `json:"server_id"`
	Timestamp time.Time                    `json:"timestamp"`
	Frames    []model.PlayerTelemetryFrame `json:"frames"`
}

// DiscoveredMatch holds info about an active match from Nakama.
type DiscoveredMatch struct {
	MatchID             string
	LabelID             string // label "id" (match UUID without the node suffix)
	Mode                string
	Level               string
	PlayerCount         int
	BroadcasterIP       string
	BroadcasterGamePort int // UDP game port from Nakama (NOT the /session API port)
	Region              string
	StartTime           time.Time
}

// serverID is the provenance stamp for telemetry from this match's broadcaster.
func (m DiscoveredMatch) serverID(cfg *BridgeConfig) string {
	return fmt.Sprintf("%s:%d", m.BroadcasterIP, cfg.APIPort)
}
