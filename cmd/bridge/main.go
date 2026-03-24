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
	"golang.org/x/net/websocket"
)

func main() {
	nakamaURL := flag.String("nakama-url", "http://127.0.0.1:7350", "Nakama HTTP API base URL")
	nakamaServerKey := flag.String("nakama-server-key", "defaultkey", "Nakama server key for authenticated API access")
	nakamaBearerToken := flag.String("nakama-bearer-token", "", "Nakama bearer/session token for discovery auth (overrides server-key auth when set)")
	anticheatURL := flag.String("anticheat-url", "", "NEVR-Anticheat WebSocket ingestion URL (e.g. ws://127.0.0.1:8080/telemetry)")
	anticheatToken := flag.String("anticheat-token", "", "Bearer token for anticheat auth (empty = no auth)")
	apiPort := flag.Int("api-port", 6721, "Echo VR session API port on broadcasters")
	pollInterval := flag.Duration("poll-interval", 67*time.Millisecond, "Polling interval for broadcaster /session API (~15fps)")
	discoveryInterval := flag.Duration("discovery-interval", 10*time.Second, "Interval between match discovery scans")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error")
	probeOnly := flag.Bool("probe", false, "Probe mode: discover + fetch + map one match, then exit")
	once := flag.Bool("once", false, "Once mode: full cycle (discover + fetch + map + send) for one match, then exit")
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
		NakamaURL:         *nakamaURL,
		NakamaServerKey:   *nakamaServerKey,
		NakamaBearerToken: *nakamaBearerToken,
		AnticheatURL:      *anticheatURL,
		AnticheatToken:    *anticheatToken,
		APIPort:           *apiPort,
		PollInterval:      *pollInterval,
		DiscoveryInterval: *discoveryInterval,
		MatchIDFilter:     *matchID,
		DumpDir:           *dumpDir,
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

	// Determine and log Nakama auth mode
	nakamaAuthMode := "basic_server_key"
	if cfg.NakamaBearerToken != "" {
		nakamaAuthMode = "bearer"
	}

	// Startup banner
	logger.Info("nevr-bridge starting",
		"mode", mode,
		"nakama_url", cfg.NakamaURL,
		"nakama_auth_mode", nakamaAuthMode,
		"anticheat_url", anticheatURLDisplay(cfg.AnticheatURL),
		"api_port", cfg.APIPort,
		"poll_interval", cfg.PollInterval,
		"discovery_interval", cfg.DiscoveryInterval,
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

	switch mode {
	case "probe":
		runProbe(ctx, cfg, stats, logger)
	case "once":
		runOnce(ctx, cfg, stats, logger)
	default:
		runBridge(ctx, cfg, stats, logger)
	}

	stats.logSummary(logger)
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

// BridgeConfig holds all bridge configuration.
type BridgeConfig struct {
	NakamaURL         string
	NakamaServerKey   string
	NakamaBearerToken string // if non-empty, use Bearer auth instead of Basic server-key
	AnticheatURL      string
	AnticheatToken    string
	APIPort           int
	PollInterval      time.Duration
	DiscoveryInterval time.Duration
	MatchIDFilter     string // empty = all matches
	DumpDir           string // empty = no dumping
}

// bridgeStats tracks process-level counters.
type bridgeStats struct {
	startTime time.Time
	mode      string

	MatchesDiscovered     atomic.Int64
	MatchesSkippedFilter  atomic.Int64
	MatchesSkippedNoEP    atomic.Int64
	PollersStarted        atomic.Int64
	PollersStopped        atomic.Int64
	TotalPolls            atomic.Int64
	TotalPollFailures     atomic.Int64
	TotalBatchesSent      atomic.Int64
	TotalSendFailures     atomic.Int64
	TotalFramesForwarded  atomic.Int64
}

func (s *bridgeStats) logSummary(logger *slog.Logger) {
	logger.Info("nevr-bridge shutdown summary",
		"mode", s.mode,
		"uptime", time.Since(s.startTime).Round(time.Second),
		"matches_discovered", s.MatchesDiscovered.Load(),
		"matches_skipped_filter", s.MatchesSkippedFilter.Load(),
		"matches_skipped_no_endpoint", s.MatchesSkippedNoEP.Load(),
		"pollers_started", s.PollersStarted.Load(),
		"pollers_stopped", s.PollersStopped.Load(),
		"total_polls", s.TotalPolls.Load(),
		"total_poll_failures", s.TotalPollFailures.Load(),
		"total_batches_sent", s.TotalBatchesSent.Load(),
		"total_send_failures", s.TotalSendFailures.Load(),
		"total_frames_forwarded", s.TotalFramesForwarded.Load(),
	)
}

// filterMatches applies the --match-id filter and tracks skip counts.
func filterMatches(matches []DiscoveredMatch, cfg *BridgeConfig, stats *bridgeStats, logger *slog.Logger) []DiscoveredMatch {
	if cfg.MatchIDFilter == "" {
		return matches
	}
	var filtered []DiscoveredMatch
	for _, m := range matches {
		if m.MatchID == cfg.MatchIDFilter {
			filtered = append(filtered, m)
		} else {
			stats.MatchesSkippedFilter.Add(1)
			logger.Debug("skipping match (--match-id filter)", "match_id", m.MatchID)
		}
	}
	return filtered
}

// fetchAndMap does a single /session fetch + mapping for one match. Returns the
// parsed session, mapping result, and any error. Used by probe and once modes.
// If cfg.DumpDir is set, saves raw response and mapped batch to disk.
func fetchAndMap(m DiscoveredMatch, cfg *BridgeConfig, logger *slog.Logger) (*adapter.EchoVRSessionResponse, *adapter.MappingResult, error) {
	sessionURL := fmt.Sprintf("http://%s:%d/session", m.BroadcasterIP, cfg.APIPort)
	logger.Info("fetching broadcaster session", "url", sessionURL)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(sessionURL)
	if err != nil {
		return nil, nil, fmt.Errorf("/session not reachable at %s: %w", sessionURL, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode != 200 {
		return nil, nil, fmt.Errorf("/session returned HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	dumpRaw(cfg.DumpDir, dumpFilename(m.MatchID, "session_raw.json"), body, logger)

	var session adapter.EchoVRSessionResponse
	if err := json.Unmarshal(body, &session); err != nil {
		return nil, nil, fmt.Errorf("invalid JSON from /session: %w (preview: %s)", err, string(body[:min(len(body), 200)]))
	}

	mapper := adapter.NewMapper()
	result := mapper.MapSession(&session)

	dumpJSON(cfg.DumpDir, dumpFilename(m.MatchID, "mapped_frames.json"), result.Frames, logger)

	return &session, result, nil
}

// logResultBlock logs a structured result summary for probe/once modes.
func logResultBlock(m DiscoveredMatch, session *adapter.EchoVRSessionResponse, result *adapter.MappingResult, cfg *BridgeConfig, sendAttempted, sendSucceeded bool, sendSkipReason string, logger *slog.Logger) {
	playerCount := 0
	for _, t := range session.Teams {
		playerCount += len(t.Players)
	}

	samplePlayerID := ""
	if len(result.Frames) > 0 {
		samplePlayerID = result.Frames[0].PlayerID
	}

	dryRun := cfg.AnticheatURL == ""

	logger.Info("--- RESULT ---",
		"selected_match_id", m.MatchID,
		"broadcaster_ip", m.BroadcasterIP,
		"api_port", cfg.APIPort,
		"session_id", session.SessionID,
		"game_status", session.GameStatus,
		"players_seen", playerCount,
		"mapped_frames", len(result.Frames),
		"warnings_count", len(result.Warnings),
		"errors_count", len(result.Errors),
		"sample_player_id", samplePlayerID,
		"anticheat_send_attempted", sendAttempted,
		"anticheat_send_succeeded", sendSucceeded,
		"dry_run", dryRun,
	)

	if !sendAttempted && sendSkipReason != "" {
		logger.Info("send skipped", "reason", sendSkipReason)
	}
	if sendAttempted && !sendSucceeded && sendSkipReason != "" {
		logger.Error("send failed", "reason", sendSkipReason)
	}

	for _, w := range result.Warnings {
		logger.Info("mapping warning", "field", w.Field, "message", w.Message)
	}
	for _, e := range result.Errors {
		logger.Warn("mapping error", "player", e.PlayerName, "field", e.Field, "message", e.Message)
	}
}

// runProbe discovers matches and tests one broadcaster (fetch + map only, no send).
func runProbe(ctx context.Context, cfg *BridgeConfig, stats *bridgeStats, logger *slog.Logger) {
	if err := doProbe(ctx, cfg, stats, logger); err != nil {
		logger.Error("probe failed", "error", err)
		os.Exit(1)
	}
}

// doProbe is the testable core of probe mode. Returns error instead of os.Exit.
func doProbe(ctx context.Context, cfg *BridgeConfig, stats *bridgeStats, logger *slog.Logger) error {
	logger.Info("--- PROBE MODE ---")

	matches, err := discoverMatches(ctx, cfg, logger, stats)
	if err != nil {
		return fmt.Errorf("discovery failed: %w", err)
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

	session, result, err := fetchAndMap(m, cfg, logger)
	if err != nil {
		return fmt.Errorf("broadcaster fetch/map failed: %w", err)
	}

	logResultBlock(m, session, result, cfg, false, false, "probe does not send; use --once", logger)
	dumpManifest(cfg.DumpDir, m, session, result, cfg, "probe", false, false, logger)
	return nil
}

// runOnce does one full cycle: discover → fetch → map → send → exit.
func runOnce(ctx context.Context, cfg *BridgeConfig, stats *bridgeStats, logger *slog.Logger) {
	if err := doOnce(ctx, cfg, stats, logger); err != nil {
		logger.Error("once mode failed", "error", err)
		os.Exit(1)
	}
}

// doOnce is the testable core of once mode. Returns error instead of os.Exit.
func doOnce(ctx context.Context, cfg *BridgeConfig, stats *bridgeStats, logger *slog.Logger) error {
	logger.Info("--- ONCE MODE: single full cycle ---")

	matches, err := discoverMatches(ctx, cfg, logger, stats)
	if err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}
	stats.MatchesDiscovered.Add(int64(len(matches)))

	matches = filterMatches(matches, cfg, stats, logger)
	if len(matches) == 0 {
		logger.Warn("no active matches — nothing to process")
		return nil
	}

	m := selectMatch(matches)
	logger.Info("selected match", "match_id", m.MatchID, "reason", "deterministic (match_id ascending)")

	session, result, err := fetchAndMap(m, cfg, logger)
	if err != nil {
		return fmt.Errorf("fetch/map failed: %w", err)
	}
	stats.TotalPolls.Add(1)

	if len(result.Frames) == 0 {
		logger.Warn("broadcaster returned valid JSON but mapper produced zero frames — check game_status and player count")
		logResultBlock(m, session, result, cfg, false, false, "no frames mapped", logger)
		dumpManifest(cfg.DumpDir, m, session, result, cfg, "once", false, false, logger, "zero frames mapped")
		return nil
	}

	if cfg.AnticheatURL == "" {
		logResultBlock(m, session, result, cfg, false, false, "no --anticheat-url provided", logger)
		dumpManifest(cfg.DumpDir, m, session, result, cfg, "once", false, false, logger, "dry-run: no anticheat URL")
		return nil
	}

	batch := &FrameBatch{
		MatchID:   m.MatchID,
		ServerID:  fmt.Sprintf("bridge:%s:%d", m.BroadcasterIP, cfg.APIPort),
		Timestamp: time.Now(),
		Frames:    result.Frames,
	}

	sender := newWSSender(cfg, logger)
	if err := sender.connect(); err != nil {
		reason := fmt.Sprintf("WebSocket connect failed: %v", err)
		logResultBlock(m, session, result, cfg, false, false, reason, logger)
		dumpManifest(cfg.DumpDir, m, session, result, cfg, "once", false, false, logger, reason)
		return fmt.Errorf("anticheat WebSocket connect failed: %w", err)
	}
	defer sender.close()

	if err := sender.send(batch); err != nil {
		stats.TotalSendFailures.Add(1)
		reason := fmt.Sprintf("WebSocket send failed: %v", err)
		logger.Error("anticheat send failed — verify anticheat server is running and accepting WebSocket connections", "anticheat_url", cfg.AnticheatURL, "error", err)
		logResultBlock(m, session, result, cfg, true, false, reason, logger)
		dumpManifest(cfg.DumpDir, m, session, result, cfg, "once", true, false, logger, reason)
		return fmt.Errorf("anticheat send failed: %w", err)
	}

	stats.TotalBatchesSent.Add(1)
	stats.TotalFramesForwarded.Add(int64(len(result.Frames)))
	logResultBlock(m, session, result, cfg, true, true, "", logger)
	dumpManifest(cfg.DumpDir, m, session, result, cfg, "once", true, true, logger)
	return nil
}

// runBridge runs the continuous bridge loop.
func runBridge(ctx context.Context, cfg *BridgeConfig, stats *bridgeStats, logger *slog.Logger) {
	if cfg.AnticheatURL == "" {
		logger.Warn("running in dry-run mode: no --anticheat-url provided, frames will be polled and mapped but NOT sent")
	}

	var activePollers sync.Map
	var wg sync.WaitGroup

	// WebSocket sender (nil if no anticheat URL)
	var sender *wsSender
	if cfg.AnticheatURL != "" {
		sender = newWSSender(cfg, logger)
		if err := sender.connect(); err != nil {
			logger.Error("initial anticheat connection failed", "error", err)
			os.Exit(1)
		}
		defer sender.close()
	}

	discoveryTicker := time.NewTicker(cfg.DiscoveryInterval)
	defer discoveryTicker.Stop()
	var consecutiveDiscoveryFailures int
	var consecutiveEmptyDiscoveries int
	var dryRunReminder int

	discover := func() {
		matches, err := discoverMatches(ctx, cfg, logger, stats)
		if err != nil {
			consecutiveDiscoveryFailures++
			consecutiveEmptyDiscoveries = 0
			if consecutiveDiscoveryFailures <= 3 || consecutiveDiscoveryFailures%30 == 0 {
				logger.Warn("match discovery failed — Nakama may be unreachable",
					"error", err,
					"consecutive_failures", consecutiveDiscoveryFailures,
				)
			}
			return
		}
		if consecutiveDiscoveryFailures > 0 {
			logger.Info("match discovery recovered", "after_failures", consecutiveDiscoveryFailures)
		}
		consecutiveDiscoveryFailures = 0

		stats.MatchesDiscovered.Add(int64(len(matches)))
		matches = filterMatches(matches, cfg, stats, logger)

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

		activeMatchIDs := make(map[string]bool)
		var newPollersThisCycle int
		var skippedEndpointsThisCycle int
		for _, m := range matches {
			activeMatchIDs[m.MatchID] = true

			if _, exists := activePollers.Load(m.MatchID); exists {
				continue
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
			poller := &matchPoller{
				match:  m,
				cfg:    cfg,
				logger: logger.With("match_id", m.MatchID, "broadcaster", m.BroadcasterIP),
				sender: sender,
				cancel: pollerCancel,
				stats:  stats,
			}
			activePollers.Store(m.MatchID, poller)
			stats.PollersStarted.Add(1)
			newPollersThisCycle++

			wg.Add(1)
			go func(p *matchPoller, pCtx context.Context) {
				defer wg.Done()
				defer activePollers.Delete(p.match.MatchID)
				defer stats.PollersStopped.Add(1)
				p.run(pCtx)
			}(poller, pollerCtx)

			logger.Info("started polling match",
				"match_id", m.MatchID,
				"broadcaster", fmt.Sprintf("%s:%d", m.BroadcasterIP, cfg.APIPort),
				"mode", m.Mode,
				"level", m.Level,
				"players", m.PlayerCount,
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

		var pollerCount int
		activePollers.Range(func(_, _ any) bool {
			pollerCount++
			return true
		})
		logger.Info("bridge status",
			"active_pollers", pollerCount,
			"matches_in_nakama", len(matches),
			"total_frames_forwarded", stats.TotalFramesForwarded.Load(),
			"total_batches_sent", stats.TotalBatchesSent.Load(),
			"uptime", time.Since(stats.startTime).Round(time.Second),
		)
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

// wsSender manages the WebSocket connection to the anticheat server with reconnection.
type wsSender struct {
	cfg    *BridgeConfig
	logger *slog.Logger
	mu     sync.Mutex
	conn   *websocket.Conn
	config *websocket.Config
}

func newWSSender(cfg *BridgeConfig, logger *slog.Logger) *wsSender {
	wsConfig, err := websocket.NewConfig(cfg.AnticheatURL, "http://localhost/")
	if err != nil {
		logger.Error("invalid anticheat WebSocket URL", "url", cfg.AnticheatURL, "error", err)
		os.Exit(1)
	}
	if cfg.AnticheatToken != "" {
		wsConfig.Header.Set("Authorization", "Bearer "+cfg.AnticheatToken)
	}
	return &wsSender{cfg: cfg, logger: logger, config: wsConfig}
}

func (s *wsSender) connect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectLocked()
}

func (s *wsSender) connectLocked() error {
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
	conn, err := websocket.DialConfig(s.config)
	if err != nil {
		return fmt.Errorf("anticheat WebSocket dial: %w", err)
	}
	s.conn = conn
	s.logger.Info("connected to anticheat WebSocket", "url", s.cfg.AnticheatURL)
	return nil
}

func (s *wsSender) send(batch *FrameBatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		if err := s.connectLocked(); err != nil {
			return err
		}
	}
	err := websocket.JSON.Send(s.conn, batch)
	if err != nil {
		s.conn.Close()
		s.conn = nil
		return fmt.Errorf("WebSocket send failed (will reconnect on next send): %w", err)
	}
	return nil
}

func (s *wsSender) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
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
	Mode                string
	Level               string
	PlayerCount         int
	BroadcasterIP       string
	BroadcasterGamePort int // UDP game port from Nakama (NOT the /session API port)
}

// matchPoller polls a single broadcaster's /session API and forwards frames.
type matchPoller struct {
	match  DiscoveredMatch
	cfg    *BridgeConfig
	logger *slog.Logger
	sender *wsSender // nil = dry-run mode
	cancel context.CancelFunc
	stats  *bridgeStats

	framesSent        atomic.Int64
	consecutiveErrors atomic.Int32
}

func (p *matchPoller) run(ctx context.Context) {
	sessionURL := fmt.Sprintf("http://%s:%d/session", p.match.BroadcasterIP, p.cfg.APIPort)
	client := &http.Client{Timeout: 2 * time.Second}
	mapper := adapter.NewMapper()
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	statusTicker := time.NewTicker(30 * time.Second)
	defer statusTicker.Stop()

	var sendErrors int
	var consecutiveZeroFrames int
	startTime := time.Now()

	stopWith := func(reason stopReason) {
		p.logger.Info("poller stopped",
			"reason", string(reason),
			"frames_sent", p.framesSent.Load(),
			"duration", time.Since(startTime).Round(time.Second),
		)
	}

	for {
		select {
		case <-ctx.Done():
			stopWith(stopCancelled)
			return

		case <-statusTicker.C:
			p.logger.Info("poller status",
				"frames_sent", p.framesSent.Load(),
				"consecutive_errors", p.consecutiveErrors.Load(),
				"send_errors", sendErrors,
				"uptime", time.Since(startTime).Round(time.Second),
			)

		case <-ticker.C:
			// Poll below
		}

		consErr := int(p.consecutiveErrors.Load())
		p.stats.TotalPolls.Add(1)

		resp, err := client.Get(sessionURL)
		if err != nil {
			p.consecutiveErrors.Add(1)
			p.stats.TotalPollFailures.Add(1)
			newConsErr := int(p.consecutiveErrors.Load())
			if newConsErr == 1 || newConsErr == 5 || newConsErr == 15 || newConsErr == 30 || newConsErr%60 == 0 {
				p.logger.Warn("session poll failed", "error", err, "consecutive_errors", newConsErr)
			}
			if newConsErr >= 30 && consErr < 30 {
				p.logger.Error("broadcaster unreachable for 30 consecutive polls")
				stopWith(stopBroadcasterUnreachable)
				return
			}
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		resp.Body.Close()
		if readErr != nil {
			p.consecutiveErrors.Add(1)
			p.stats.TotalPollFailures.Add(1)
			continue
		}

		if resp.StatusCode != 200 {
			p.consecutiveErrors.Add(1)
			p.stats.TotalPollFailures.Add(1)
			newConsErr := int(p.consecutiveErrors.Load())
			if newConsErr == 1 || newConsErr%10 == 0 {
				p.logger.Warn("session API non-200", "status", resp.StatusCode, "consecutive_errors", newConsErr)
			}
			continue
		}

		var session adapter.EchoVRSessionResponse
		if err := json.Unmarshal(body, &session); err != nil {
			p.consecutiveErrors.Add(1)
			p.stats.TotalPollFailures.Add(1)
			p.logger.Warn("session JSON parse error", "error", err)
			continue
		}

		if p.consecutiveErrors.Load() > 0 {
			p.logger.Info("broadcaster recovered", "after_errors", p.consecutiveErrors.Load())
		}
		p.consecutiveErrors.Store(0)

		// Validate session has minimum viable data
		if err := validateSession(&session); err != nil {
			p.stats.TotalPollFailures.Add(1)
			p.logger.Warn("session response structurally invalid", "error", err)
			// Empty session on repeated polls may mean match is over
			if session.GameStatus == "" && session.SessionID == "" {
				stopWith(stopInvalidSession)
				return
			}
			continue
		}

		if session.GameStatus == "post_match" {
			stopWith(stopPostMatch)
			return
		}

		result := mapper.MapSession(&session)
		if len(result.Frames) == 0 {
			consecutiveZeroFrames++
			if consecutiveZeroFrames == 5 || consecutiveZeroFrames == 30 || consecutiveZeroFrames%100 == 0 {
				p.logger.Warn("mapper produced zero frames from valid session — game may be in non-active phase or players may have zero/invalid positions",
					"consecutive_zero_frames", consecutiveZeroFrames,
					"game_status", session.GameStatus,
					"session_id", session.SessionID,
				)
			}
			continue
		}
		consecutiveZeroFrames = 0

		batch := &FrameBatch{
			MatchID:   p.match.MatchID,
			ServerID:  fmt.Sprintf("bridge:%s:%d", p.match.BroadcasterIP, p.cfg.APIPort),
			Timestamp: time.Now(),
			Frames:    result.Frames,
		}

		if p.sender == nil {
			// Dry-run mode: count frames but don't send
			p.framesSent.Add(int64(len(result.Frames)))
			p.stats.TotalFramesForwarded.Add(int64(len(result.Frames)))
			continue
		}

		if err := p.sender.send(batch); err != nil {
			sendErrors++
			p.stats.TotalSendFailures.Add(1)
			if sendErrors <= 3 || sendErrors%10 == 0 {
				p.logger.Warn("failed to send batch to anticheat", "error", err, "total_send_errors", sendErrors)
			}
			continue
		}

		p.framesSent.Add(int64(len(result.Frames)))
		p.stats.TotalBatchesSent.Add(1)
		p.stats.TotalFramesForwarded.Add(int64(len(result.Frames)))
	}
}
