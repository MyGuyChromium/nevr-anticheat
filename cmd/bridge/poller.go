package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Poller defaults (overridable through BridgeConfig).
const (
	// failureIdleThreshold is the number of consecutive failed polls
	// (transport, non-200, unreadable/invalid body) after which the poller
	// backs off to idle polling instead of hammering the broadcaster.
	failureIdleThreshold = 30
	defaultIdleInterval  = 2 * time.Second
	defaultIdleGiveUp    = 10 * time.Minute
	maxSessionBody       = 256 * 1024
)

// pollerState is the poller's lifecycle phase.
type pollerState int32

const (
	stateActive pollerState = iota
	stateIdlePostMatch
	stateIdleUnreachable
	stateIdleError
)

func (st pollerState) String() string {
	switch st {
	case stateActive:
		return "active"
	case stateIdlePostMatch:
		return "idle_post_match"
	case stateIdleUnreachable:
		return "idle_unreachable"
	case stateIdleError:
		return "idle_error"
	}
	return "unknown"
}

// pollOutcome classifies one /session poll.
type pollOutcome int

const (
	pollOK pollOutcome = iota
	pollTransportErr
	pollNon200
	pollBadBody
	pollInvalidSession
)

// matchPoller polls one broadcaster's /session API for one Nakama match and
// forwards frames. It is the single owner of that match's lifecycle inside
// the bridge: it announces match_start once metadata is known, match_end when
// the match ends or the session behind the match_id changes, and it idles
// (rather than exiting and being recreated every discovery cycle) while the
// lobby sits in post_match or the broadcaster is unreachable.
type matchPoller struct {
	match  DiscoveredMatch
	cfg    *BridgeConfig
	logger *slog.Logger
	sender *wsSender // nil = dry-run mode
	cancel context.CancelFunc
	stats  *bridgeStats
	epoch  *frameEpoch
	client *http.Client

	framesSent        atomic.Int64
	framesDropped     atomic.Int64
	consecutiveErrors atomic.Int32
	stateValue        atomic.Int32
	stoppedWith       atomic.Value // stopReason

	// per-run state
	state          pollerState
	idleSince      time.Time
	terminalSess   string // sessionid seen in post_match
	matchStarted   bool
	sessionID      string
	lastBodyHash   uint64
	lastSampleAt   time.Time
	sampleDtSum    time.Duration
	sampleDtMax    time.Duration
	sampleCount    int
	zeroFrameRuns  int
	mismatchLogged bool
	mapper         *adapter.Mapper
	sendErrors     int

	// once-per-match mapping facts (see logMappingOnce)
	spectatorsLogged bool
	basisLogged      bool
}

func newMatchPoller(m DiscoveredMatch, cfg *BridgeConfig, sender *wsSender, stats *bridgeStats, epoch *frameEpoch, cancel context.CancelFunc, logger *slog.Logger) *matchPoller {
	// Live path: skip snapshots whose game state did not change since the
	// previous poll (the broadcaster updates slower than we sample), so no
	// zero-velocity frame followed by a double-distance frame reaches ingest.
	mapper := adapter.NewMapper()
	mapper.SetObservationSource("echovr_http", "http_response_body_received", fmt.Sprintf("%s:%d", m.BroadcasterIP, cfg.APIPort))
	mapper.SetDedupeIdentical(true)
	return &matchPoller{
		match:  m,
		cfg:    cfg,
		logger: logger.With("match_id", m.MatchID, "broadcaster", m.BroadcasterIP),
		sender: sender,
		cancel: cancel,
		stats:  stats,
		epoch:  epoch,
		client: scopedHTTPClient(2 * time.Second),
		mapper: mapper,
	}
}

// logMappingOnce reports, once per match, the mapping facts an operator
// needs from the first real session: how many spectator/moderator entries
// the mapper excluded, and whether Echo VR's direction vectors form a
// reflected basis (the adapter handles both conventions; the log line
// settles which one the game uses).
func (p *matchPoller) logMappingOnce(result *adapter.MappingResult) {
	if result.SpectatorsDropped > 0 && !p.spectatorsLogged {
		p.spectatorsLogged = true
		p.logger.Info("spectator entries excluded from telemetry by the mapper",
			"spectators", result.SpectatorsDropped)
	}
	if !p.basisLogged {
		for _, w := range result.Warnings {
			if w.Field == "basis_reflected" {
				p.basisLogged = true
				p.logger.Info("mapping: direction vectors form a reflected basis", "message", w.Message)
				break
			}
		}
	}
}

// serverID is the provenance stamp for every message about this match: the
// broadcaster host:port the telemetry was pulled from.
func (p *matchPoller) serverID() string {
	return fmt.Sprintf("%s:%d", p.match.BroadcasterIP, p.cfg.APIPort)
}

func (p *matchPoller) sessionURL() string {
	return fmt.Sprintf("http://%s:%d/session", p.match.BroadcasterIP, p.cfg.APIPort)
}

func (p *matchPoller) idleInterval() time.Duration {
	iv := orDuration(p.cfg.IdleInterval, defaultIdleInterval)
	if iv < p.cfg.PollInterval {
		iv = p.cfg.PollInterval
	}
	return iv
}

func (p *matchPoller) idleGiveUp() time.Duration {
	return orDuration(p.cfg.IdleGiveUp, defaultIdleGiveUp)
}

func (p *matchPoller) currentState() pollerState {
	return pollerState(p.stateValue.Load())
}

func (p *matchPoller) setState(st pollerState) {
	p.state = st
	p.stateValue.Store(int32(st))
}

func (p *matchPoller) stopReason() stopReason {
	v, _ := p.stoppedWith.Load().(stopReason)
	return v
}

// run is the poll loop. It returns when the context is cancelled or the
// poller gives up on the match.
func (p *matchPoller) run(ctx context.Context) {
	startTime := time.Now()
	statusTicker := time.NewTicker(orDuration(p.cfg.StatusInterval, 30*time.Second))
	defer statusTicker.Stop()

	timer := time.NewTimer(0)
	defer timer.Stop()

	stopWith := func(reason stopReason) {
		p.stoppedWith.Store(reason)
		p.endMatch(string(reason))
		p.logger.Info("poller stopped",
			"reason", string(reason),
			"state", p.state.String(),
			"frames_sent", p.framesSent.Load(),
			"frames_dropped", p.framesDropped.Load(),
			"duration", time.Since(startTime).Round(time.Second),
		)
	}

	for {
		select {
		case <-ctx.Done():
			stopWith(stopCancelled)
			return

		case <-statusTicker.C:
			p.logStatus(startTime)
			continue // a status tick must never trigger an extra sample (audit F178)

		case <-timer.C:
		}

		keepGoing, reason := p.poll(ctx)
		if !keepGoing {
			stopWith(reason)
			return
		}
		if p.state == stateActive {
			timer.Reset(p.cfg.PollInterval)
		} else {
			timer.Reset(p.idleInterval())
		}
	}
}

func (p *matchPoller) logStatus(startTime time.Time) {
	avgDt := time.Duration(0)
	if p.sampleCount > 0 {
		avgDt = p.sampleDtSum / time.Duration(p.sampleCount)
	}
	p.logger.Info("poller status",
		"state", p.state.String(),
		"frames_sent", p.framesSent.Load(),
		"frames_dropped", p.framesDropped.Load(),
		"consecutive_errors", p.consecutiveErrors.Load(),
		"send_errors", p.sendErrors,
		"sample_dt_avg", avgDt.Round(time.Millisecond),
		"sample_dt_max", p.sampleDtMax.Round(time.Millisecond),
		"uptime", time.Since(startTime).Round(time.Second),
	)
}

// fetch performs one GET and classifies the result.
func (p *matchPoller) fetch(ctx context.Context) (pollOutcome, []byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.sessionURL(), nil)
	if err != nil {
		return pollTransportErr, nil, 0, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return pollTransportErr, nil, 0, err
	}
	defer resp.Body.Close()
	body, readErr := readSessionBody(resp.Body)
	if readErr != nil {
		return pollBadBody, nil, resp.StatusCode, readErr
	}
	if resp.StatusCode != http.StatusOK {
		return pollNon200, body, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return pollOK, body, resp.StatusCode, nil
}

// readSessionBody enforces the limit without silently treating a truncated
// prefix as the exact broadcaster response. The extra byte distinguishes an
// exactly-at-limit document from an oversized one.
func readSessionBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxSessionBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxSessionBody {
		return nil, fmt.Errorf("/session response exceeds %d-byte limit", maxSessionBody)
	}
	return body, nil
}

// shouldLogFailure throttles repeated failure warnings: 1, 5, 15, 30, then
// every 300th poll while idle (about every 10 minutes at the idle rate).
func shouldLogFailure(n int) bool {
	switch n {
	case 1, 5, 15, failureIdleThreshold:
		return true
	}
	return n > failureIdleThreshold && n%300 == 0
}

// recordFailure counts a failed poll and moves to idle after the threshold.
// It returns false when the poller should give up entirely.
func (p *matchPoller) recordFailure(outcome pollOutcome, status int, err error, now time.Time) (bool, stopReason) {
	n := int(p.consecutiveErrors.Add(1))
	p.stats.TotalPollFailures.Add(1)
	if shouldLogFailure(n) {
		attrs := []any{"consecutive_errors", n, "state", p.state.String()}
		switch outcome {
		case pollTransportErr:
			p.logger.Warn("session poll failed", append(attrs, "error", err)...)
		case pollNon200:
			p.logger.Warn("session API non-200", append(attrs, "status", status)...)
		case pollBadBody:
			p.logger.Warn("session body unreadable or not JSON", append(attrs, "error", err)...)
		case pollInvalidSession:
			p.logger.Warn("session response structurally invalid", append(attrs, "error", err)...)
		}
	}
	if n < failureIdleThreshold {
		return true, ""
	}
	target := stateIdleError
	giveUpReason := stopBroadcasterError
	if outcome == pollTransportErr {
		target = stateIdleUnreachable
		giveUpReason = stopBroadcasterUnreachable
	}
	if p.state == stateActive {
		// Back off, but keep the announced match open: 30 failed polls is
		// ~2s at the active rate, and a broadcaster that is restarting or
		// briefly unreachable comes back with the same session. Announcing
		// match_end here would make the ingest finalize the live match
		// (scores persisted, review cases created, player state discarded)
		// and the recovery would then be scored as a second fragment.
		// match_end is announced only when the poller gives up
		// (--idle-give-up elapsed) or is cancelled (match left Nakama).
		p.logger.Error("broadcaster failing for consecutive polls; backing off to idle polling (match stays open)",
			"consecutive_errors", n, "idle_interval", p.idleInterval(), "give_up_after", p.idleGiveUp())
		p.enterIdle(target, now)
		return true, ""
	}
	if p.state != target {
		p.setState(target)
	}
	if now.Sub(p.idleSince) > p.idleGiveUp() {
		return false, giveUpReason
	}
	return true, ""
}

func (p *matchPoller) enterIdle(st pollerState, now time.Time) {
	p.setState(st)
	p.idleSince = now
	p.stats.PollerIdleTransitions.Add(1)
}

// poll performs one sample. It returns false with a stop reason when the
// poller must exit.
func (p *matchPoller) poll(ctx context.Context) (bool, stopReason) {
	p.stats.TotalPolls.Add(1)
	outcome, body, status, err := p.fetch(ctx)
	sampleAt := time.Now()
	if ctx.Err() != nil {
		return false, stopCancelled
	}
	if outcome != pollOK {
		return p.recordFailure(outcome, status, err, sampleAt)
	}

	var session adapter.EchoVRSessionResponse
	if err := json.Unmarshal(body, &session); err != nil {
		return p.recordFailure(pollBadBody, status, err, sampleAt)
	}
	roster := buildRoster(&session)
	if err := validateSessionRoster(&session, roster); err != nil {
		return p.recordFailure(pollInvalidSession, status, err, sampleAt)
	}

	if n := p.consecutiveErrors.Swap(0); n > 0 {
		p.logger.Info("broadcaster recovered", "after_errors", n)
	}

	// Identity cross-check: the polled instance must be the Nakama match.
	// "off" means do not compare at all: no counter, no log line.
	mode := p.cfg.SessionCheck
	if mode == "" {
		mode = sessionCheckStrict
	}
	if mode != sessionCheckOff && !sessionMatchesMatchID(session.SessionID, p.match) {
		p.stats.SessionMismatches.Add(1)
		if !p.mismatchLogged {
			p.mismatchLogged = true
			p.logger.Error("/session sessionid does not match the Nakama match id — this broadcaster endpoint may belong to another game-server instance",
				"session_id", session.SessionID, "nakama_match_id", p.match.MatchID, "label_id", p.match.LabelID,
				"session_check", mode, "action", "check --api-port for this host; use --session-check warn to forward anyway")
		}
		if mode == sessionCheckStrict {
			return false, stopSessionMismatch
		}
	}

	// post_match: announce the end once and idle until the lobby starts a
	// new game (new sessionid) or leaves Nakama.
	if session.GameStatus == "post_match" {
		switch p.state {
		case stateIdlePostMatch:
		case stateActive:
			p.logger.Info("post_match detected; idling until the lobby starts a new game", "session_id", session.SessionID)
			p.endMatch(string(stopPostMatch))
			p.enterIdle(stateIdlePostMatch, sampleAt)
		default:
			// Already idle (error/unreachable): keep the idle clock running so
			// a lobby flapping between failures and a stuck post_match still
			// hits --idle-give-up, and do not count another idle transition.
			p.logger.Info("post_match detected while idle; idle clock keeps running",
				"from_state", p.state.String(), "session_id", session.SessionID, "idle_for", sampleAt.Sub(p.idleSince).Round(time.Second))
			p.endMatch(string(stopPostMatch))
			p.setState(stateIdlePostMatch)
		}
		p.terminalSess = session.SessionID
		p.epoch.setSession(session.SessionID)
		if sampleAt.Sub(p.idleSince) > p.idleGiveUp() {
			return false, stopPostMatch
		}
		return true, ""
	}

	if p.state != stateActive {
		p.logger.Info("broadcaster active; resuming polling", "from_state", p.state.String(), "session_id", session.SessionID)
		p.setState(stateActive)
	}

	// Session continuity: a new sessionid under the same match_id is a new
	// game in the same lobby. End the old one so the server can close its
	// context; the frame epoch keeps counting so no row is ever discarded.
	if p.epoch.setSession(session.SessionID) {
		p.logger.Info("new session under the same match id (rematch); announcing match_end/match_start",
			"previous_session", p.sessionID, "session_id", session.SessionID)
		p.endMatch("session_changed")
	}
	if p.sessionID != session.SessionID {
		p.sessionID = session.SessionID
	}

	// Drop byte-identical snapshots: a poll landing inside the same game tick
	// would otherwise produce a zero-motion frame at a real dt.
	h := fnv.New64a()
	h.Write(body)
	sum := h.Sum64()
	if sum == p.lastBodyHash {
		p.stats.PollsDuplicate.Add(1)
		return true, ""
	}
	p.lastBodyHash = sum

	// Map at the real HTTP sample time (contract 1). The mapper's change
	// detection reports a snapshot whose game state equals the previous one
	// (broadcaster ticking slower than the poll) as SkippedDuplicate: that is
	// "no new state", not a zero-frame mapping failure.
	result := p.mapper.MapSessionAt(&session, sampleAt)
	if result.SkippedDuplicate {
		p.stats.PollsDuplicateState.Add(1)
		return true, ""
	}
	p.logMappingOnce(result)
	frames, droppedSpectators := filterFrames(result.Frames, roster)
	droppedSpectators += result.SpectatorsDropped
	if droppedSpectators > 0 {
		p.stats.FramesDroppedSpectator.Add(int64(droppedSpectators))
	}
	if len(frames) == 0 {
		p.zeroFrameRuns++
		if p.zeroFrameRuns == 5 || p.zeroFrameRuns == 30 || p.zeroFrameRuns%100 == 0 {
			p.logger.Warn("mapper produced zero frames from valid session — game may be in non-active phase or players may have zero/invalid positions",
				"consecutive_zero_frames", p.zeroFrameRuns,
				"game_status", session.GameStatus,
				"session_id", session.SessionID,
				"spectators", len(roster.Spectators),
			)
		}
		return true, ""
	}
	p.zeroFrameRuns = 0

	if !p.lastSampleAt.IsZero() {
		dt := sampleAt.Sub(p.lastSampleAt)
		p.sampleDtSum += dt
		p.sampleCount++
		if dt > p.sampleDtMax {
			p.sampleDtMax = dt
		}
	}
	p.lastSampleAt = sampleAt

	p.epoch.stamp(sampleAt, frames)
	p.stats.TotalFramesMapped.Add(int64(len(frames)))

	if !p.matchStarted {
		p.startMatch(&session, roster)
	}

	batch := &FrameBatch{
		MatchID:   p.match.MatchID,
		ServerID:  p.serverID(),
		Timestamp: sampleAt,
		Frames:    frames,
		RawJSON:   string(body),
	}

	if p.sender == nil {
		// Dry-run mode: count frames but don't send
		p.framesSent.Add(int64(len(frames)))
		p.stats.TotalFramesForwarded.Add(int64(len(frames)))
		return true, ""
	}

	if !p.sender.enqueueBatch(batch) {
		p.framesDropped.Add(int64(len(frames)))
		p.sendErrors++
		if p.sendErrors <= 3 || p.sendErrors%50 == 0 {
			p.logger.Warn("dropped batch: anticheat send queue full (link stalled?)",
				"frames", len(frames), "total_dropped", p.framesDropped.Load())
		}
		return true, ""
	}
	p.framesSent.Add(int64(len(frames)))
	return true, ""
}

// startMatch announces the match with the metadata Nakama and /session provide.
func (p *matchPoller) startMatch(session *adapter.EchoVRSessionResponse, roster sessionRoster) {
	p.matchStarted = true
	gameMode := session.MatchType
	if gameMode == "" {
		gameMode = p.match.Mode
	}
	mapName := session.MapName
	if mapName == "" {
		mapName = p.match.Level
	}
	p.logger.Info("match_start",
		"session_id", session.SessionID, "game_mode", gameMode, "map", mapName,
		"is_private", session.PrivateMatch, "players", roster.PlayerCount, "spectators", len(roster.Spectators))
	if p.sender == nil {
		return
	}
	p.sender.enqueueControl(&model.ControlMessage{
		Type:      model.ControlMatchStart,
		MatchID:   p.match.MatchID,
		ServerID:  p.serverID(),
		GameMode:  gameMode,
		Map:       mapName,
		IsPrivate: session.PrivateMatch,
		Teams:     roster.teamsCopy(),
	})
}

// endMatch announces match_end once per announced match.
func (p *matchPoller) endMatch(reason string) {
	if !p.matchStarted {
		return
	}
	p.matchStarted = false
	p.logger.Info("match_end", "reason", reason)
	if p.sender == nil {
		return
	}
	p.sender.enqueueControl(&model.ControlMessage{
		Type:     model.ControlMatchEnd,
		MatchID:  p.match.MatchID,
		ServerID: p.serverID(),
		Reason:   reason,
	})
}

// sessionMatchesMatchID compares Echo VR's sessionid with the Nakama match.
// EchoVRCE hands the game server the match UUID as its lobby session id, so
// the UUID part of "<uuid>.<node>" (or the label's id) must equal sessionid.
// Comparison is case-insensitive and tolerates braces.
func sessionMatchesMatchID(sessionID string, m DiscoveredMatch) bool {
	norm := func(s string) string {
		s = strings.TrimSpace(strings.ToLower(s))
		s = strings.Trim(s, "{}")
		return s
	}
	sid := norm(sessionID)
	if sid == "" {
		return false
	}
	candidates := []string{norm(m.MatchID), norm(m.LabelID)}
	if i := strings.IndexByte(m.MatchID, '.'); i > 0 {
		candidates = append(candidates, norm(m.MatchID[:i]))
	}
	for _, c := range candidates {
		if c != "" && c == sid {
			return true
		}
	}
	return false
}
