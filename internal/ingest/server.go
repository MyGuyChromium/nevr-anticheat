// Package ingest handles telemetry ingestion from game server profilers.
//
// The ingestion server accepts telemetry via WebSocket and performs two tasks:
//
//  1. STORE telemetry frames to the profiler database (primary purpose).
//     This makes the database the canonical long-term telemetry source.
//
//  2. RUN inline detection for immediate feedback (secondary/convenience).
//     Inline detection results are tagged with analysis_source="initial".
//     These results can be replaced at any time by async reprocessing
//     (reprocess-match, reprocess-player, etc.) which is the canonical
//     analysis path.
//
// Game servers only need to emit telemetry to this endpoint. They do not
// run detection logic, make enforcement decisions, or interact with the
// anticheat system beyond sending frames.
//
// # Wire protocol
//
// Every message is a JSON object. A message with a non-empty "type" field is
// a model.ControlMessage; anything else is a FrameBatch.
//
//	server -> producer: {"type":"hello","auth":"ok"} right after a successful
//	                    connection, {"type":"ack","accepted":N,"rejected":M,
//	                    "ignored":K} periodically (counts since the previous
//	                    ack), {"type":"error","reason":"unauthorized"} followed
//	                    by close when authentication fails.
//	producer -> server: FrameBatch, {"type":"match_start",...} when a poller
//	                    starts, {"type":"match_end",...} when it stops.
//
// Unknown control types are logged and ignored.
package ingest

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"golang.org/x/net/websocket"

	"github.com/nevr-anticheat/nevr-anticheat/internal/metrics"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// ErrAuthTokenRequired is returned by Start when no auth token is configured
// and unauthenticated ingestion has not been explicitly allowed.
var ErrAuthTokenRequired = errors.New("ingest: refusing to start without an auth token (set NEVR_AC_AUTH_TOKEN or allow unauthenticated ingestion explicitly)")

// ServerConfig configures the ingestion server.
type ServerConfig struct {
	ListenAddr              string        // e.g., ":8080"
	MaxConnectionsPerServer int           // max concurrent game server connections
	MaxFrameSize            int           // max bytes per WebSocket message (default 64KB)
	MaxFrameRatePerPlayer   int           // max frames/sec per player (default 30)
	MaxFramesPerBatch       int           // max frames in one FrameBatch (default 100)
	MaxPlayersPerMatch      int           // max players per match (default 16), enforced by MatchManager
	MaxIDLength             int           // max bytes for match_id / player_id (default 128)
	ReadTimeout             time.Duration // idle deadline: a peer silent for longer is disconnected
	WriteTimeout            time.Duration // per-message write deadline for hello/ack/error
	AckInterval             time.Duration // how often pending ack counters are flushed to the peer
	AuthToken               string        // shared secret for server auth
	AllowUnauthenticated    bool          // explicit opt-in to run with an empty AuthToken
}

// DefaultServerConfig returns the default ingest limits.
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		ListenAddr:              ":8080",
		MaxConnectionsPerServer: 100,
		MaxFrameSize:            65536,
		MaxFrameRatePerPlayer:   30,
		MaxFramesPerBatch:       100,
		MaxPlayersPerMatch:      16,
		MaxIDLength:             128,
		ReadTimeout:             5 * time.Minute,
		WriteTimeout:            10 * time.Second,
		AckInterval:             time.Second,
	}
}

// FrameResult reports what happened to the frames of one batch.
type FrameResult struct {
	Accepted int // frames processed by the pipeline
	Rejected int // frames refused by the handler (limits)
	Ignored  int // frames the store discarded as duplicates
}

// Handler receives validated telemetry batches and control messages.
type Handler interface {
	HandleFrames(matchID string, frames []model.PlayerTelemetryFrame) FrameResult
	HandleControl(msg model.ControlMessage)
}

// FrameHandler adapts a plain function to Handler (control messages are ignored).
type FrameHandler func(matchID string, frames []model.PlayerTelemetryFrame)

func (f FrameHandler) HandleFrames(matchID string, frames []model.PlayerTelemetryFrame) FrameResult {
	f(matchID, frames)
	return FrameResult{Accepted: len(frames)}
}

func (FrameHandler) HandleControl(model.ControlMessage) {}

// Server accepts telemetry streams from game servers.
type Server struct {
	cfg        ServerConfig
	handler    Handler
	logger     *slog.Logger
	httpServer *http.Server
	listener   net.Listener
	metrics    *metrics.Metrics

	// Rate limiting per player
	playerRates   map[string]*rateLimiter // key: matchID:playerID
	playerRatesMu sync.RWMutex

	// Connection tracking
	activeConns atomic.Int64
	conns       map[*websocket.Conn]struct{}
	connsMu     sync.Mutex
	connWG      sync.WaitGroup
	closing     atomic.Bool

	// Throttled warnings
	warnMu     sync.Mutex
	warnCounts map[string]int

	// Counters (also exported on /health)
	FramesReceived    atomic.Int64 // frames accepted by ingest validation and rate limiting
	FramesRejected    atomic.Int64 // frames rejected by validation or handler limits
	FramesRateLimited atomic.Int64
	FramesProcessed   atomic.Int64 // frames the handler accepted
	FramesIgnored     atomic.Int64 // duplicate rows the store discarded
	BytesReceived     atomic.Int64
	ActiveMatchCount  atomic.Int64
}

type rateLimiter struct {
	lastFrame time.Time
	count     int
	window    time.Time // start of current 1-second window (aligned to the second)
}

// NewServer creates a telemetry ingestion server.
func NewServer(cfg ServerConfig, handler Handler, logger *slog.Logger) *Server {
	if cfg.MaxFramesPerBatch <= 0 {
		cfg.MaxFramesPerBatch = 100
	}
	if cfg.MaxIDLength <= 0 {
		cfg.MaxIDLength = 128
	}
	if cfg.AckInterval <= 0 {
		cfg.AckInterval = time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		cfg:         cfg,
		handler:     handler,
		logger:      logger,
		playerRates: make(map[string]*rateLimiter),
		conns:       make(map[*websocket.Conn]struct{}),
		warnCounts:  make(map[string]int),
	}
}

// SetMetrics attaches a metrics registry (optional).
func (s *Server) SetMetrics(m *metrics.Metrics) { s.metrics = m }

// Addr returns the bound listen address (valid after Listen).
func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Listen binds the listen address without serving. Start calls it when the
// server has not been bound yet; tests use it to learn an ephemeral port.
func (s *Server) Listen() error {
	if s.cfg.AuthToken == "" {
		if !s.cfg.AllowUnauthenticated {
			return ErrAuthTokenRequired
		}
		s.logger.Warn("SECURITY: telemetry ingestion is running WITHOUT authentication; any host that can reach the port can inject telemetry and detection events",
			"addr", s.cfg.ListenAddr)
	}
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.ListenAddr, err)
	}
	s.listener = ln
	return nil
}

// Start begins accepting connections. Blocks until the context is cancelled
// (then returns nil after a graceful shutdown) or the listener fails.
func (s *Server) Start(ctx context.Context) error {
	if s.listener == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/telemetry", websocket.Handler(s.handleConnection))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","connections":%d,"frames_received":%d,"frames_rejected":%d,"frames_rate_limited":%d,"frames_ignored":%d,"active_matches":%d}`,
			s.activeConns.Load(), s.FramesReceived.Load(), s.FramesRejected.Load(),
			s.FramesRateLimited.Load(), s.FramesIgnored.Load(), s.ActiveMatchCount.Load())
	})

	// Note: http.Server deadlines do not apply to hijacked WebSocket
	// connections; the read loop sets its own idle deadline.
	s.httpServer = &http.Server{Handler: mux}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.Shutdown(shutdownCtx)
		case <-done:
		}
	}()

	s.logger.Info("telemetry server starting", "addr", s.listener.Addr().String())
	err := s.httpServer.Serve(s.listener)
	close(done)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops accepting connections, closes every open WebSocket (which
// http.Server.Shutdown does not do for hijacked connections) and waits for
// in-flight batches to finish or ctx to expire.
func (s *Server) Shutdown(ctx context.Context) error {
	s.closing.Store(true)
	var err error
	if s.httpServer != nil {
		err = s.httpServer.Shutdown(ctx)
	} else if s.listener != nil {
		err = s.listener.Close()
	}
	s.connsMu.Lock()
	for c := range s.conns {
		c.Close()
	}
	s.connsMu.Unlock()

	waited := make(chan struct{})
	go func() {
		s.connWG.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-ctx.Done():
		s.logger.Warn("shutdown timed out waiting for in-flight telemetry batches")
		if err == nil {
			err = ctx.Err()
		}
	}
	return err
}

// handleConnection manages a single game server WebSocket connection.
func (s *Server) handleConnection(ws *websocket.Conn) {
	if s.closing.Load() {
		ws.Close()
		return
	}
	if int(s.activeConns.Load()) >= s.cfg.MaxConnectionsPerServer {
		s.logger.Warn("connection rejected: max connections reached")
		_ = s.writeControl(ws, model.ControlMessage{Type: model.ControlError, Reason: "too_many_connections"})
		ws.Close()
		return
	}
	s.connWG.Add(1)
	defer s.connWG.Done()
	s.trackConn(ws, true)
	defer s.trackConn(ws, false)
	s.activeConns.Add(1)
	defer s.activeConns.Add(-1)
	if s.metrics != nil {
		s.metrics.ActiveConnections.Inc()
		defer s.metrics.ActiveConnections.Dec()
	}
	defer ws.Close()

	remoteAddr := ws.Request().RemoteAddr

	// Auth check (constant time). An empty token is only reachable when
	// AllowUnauthenticated was set at Listen time.
	if s.cfg.AuthToken != "" {
		header := ws.Request().Header.Get("Authorization")
		expected := "Bearer " + s.cfg.AuthToken
		if subtle.ConstantTimeCompare([]byte(header), []byte(expected)) != 1 {
			s.logger.Warn("connection rejected: invalid auth", "remote", remoteAddr)
			if s.metrics != nil {
				s.metrics.AuthFailures.Inc()
			}
			_ = s.writeControl(ws, model.ControlMessage{Type: model.ControlError, Reason: "unauthorized"})
			return
		}
	}

	if err := s.writeControl(ws, model.ControlMessage{Type: model.ControlHello, Auth: "ok"}); err != nil {
		s.logger.Warn("failed to send hello", "remote", remoteAddr, "error", err)
		return
	}

	s.logger.Info("game server connected", "remote", remoteAddr)
	defer s.logger.Info("game server disconnected", "remote", remoteAddr)

	// Set max frame size to prevent memory abuse
	ws.MaxPayloadBytes = s.cfg.MaxFrameSize

	// Periodic acks: counters since the previous ack.
	var pendAccepted, pendRejected, pendIgnored atomic.Int64
	ackDone := make(chan struct{})
	var ackWG sync.WaitGroup
	ackWG.Add(1)
	go func() {
		defer ackWG.Done()
		ticker := time.NewTicker(s.cfg.AckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ackDone:
				return
			case <-ticker.C:
				a, r, i := pendAccepted.Swap(0), pendRejected.Swap(0), pendIgnored.Swap(0)
				if a == 0 && r == 0 && i == 0 {
					continue
				}
				msg := model.ControlMessage{Type: model.ControlAck, Accepted: int(a), Rejected: int(r), Ignored: int(i)}
				if err := s.writeControl(ws, msg); err != nil {
					s.logger.Debug("ack write failed", "remote", remoteAddr, "error", err)
					ws.Close() // the read loop observes the close and exits
					return
				}
			}
		}
	}()
	defer func() {
		close(ackDone)
		ackWG.Wait()
	}()

	for {
		if s.cfg.ReadTimeout > 0 {
			_ = ws.SetReadDeadline(time.Now().Add(s.cfg.ReadTimeout))
		}
		var raw json.RawMessage
		if err := websocket.JSON.Receive(ws, &raw); err != nil {
			if errors.Is(err, websocket.ErrFrameTooLarge) {
				// The frame was discarded; the connection stays usable.
				s.FramesRejected.Add(1)
				if s.metrics != nil {
					s.metrics.BatchesRejected.Inc("oversized_message")
				}
				s.warnThrottled("oversize:"+remoteAddr, "oversized message discarded",
					"remote", remoteAddr, "max_bytes", s.cfg.MaxFrameSize)
				continue
			}
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				s.logger.Warn("closing idle telemetry connection", "remote", remoteAddr, "idle", s.cfg.ReadTimeout)
			}
			return // connection closed or error
		}
		s.BytesReceived.Add(int64(len(raw)))

		// Dispatch: a non-empty "type" makes this a control message.
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			s.FramesRejected.Add(1)
			if s.metrics != nil {
				s.metrics.BatchesMalformed.Inc()
			}
			s.logger.Debug("malformed message", "remote", remoteAddr, "error", err)
			continue
		}
		if probe.Type != "" {
			s.handleControlMessage(raw, probe.Type, remoteAddr)
			continue
		}

		res := s.handleBatch(raw, remoteAddr)
		pendAccepted.Add(int64(res.Accepted))
		pendRejected.Add(int64(res.Rejected))
		pendIgnored.Add(int64(res.Ignored))
	}
}

// handleControlMessage decodes and dispatches a producer control message.
func (s *Server) handleControlMessage(raw json.RawMessage, typ, remoteAddr string) {
	if s.metrics != nil {
		s.metrics.ControlMessages.Inc(typ)
	}
	var msg model.ControlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		s.logger.Debug("malformed control message", "remote", remoteAddr, "type", typ, "error", err)
		return
	}
	switch msg.Type {
	case model.ControlMatchStart, model.ControlMatchEnd:
		if !validID(msg.MatchID, s.cfg.MaxIDLength) {
			s.warnThrottled("badctl:"+remoteAddr, "control message with invalid match_id ignored",
				"remote", remoteAddr, "type", msg.Type, "len", len(msg.MatchID))
			return
		}
		for pid := range msg.Teams {
			if !validID(pid, s.cfg.MaxIDLength) {
				delete(msg.Teams, pid)
			}
		}
		s.handler.HandleControl(msg)
	default:
		s.logger.Debug("unknown control message type ignored", "remote", remoteAddr, "type", msg.Type)
	}
}

// handleBatch validates a FrameBatch and forwards the accepted frames.
func (s *Server) handleBatch(raw json.RawMessage, remoteAddr string) FrameResult {
	if s.metrics != nil {
		s.metrics.BatchesReceived.Inc()
	}
	var batch FrameBatch
	if err := json.Unmarshal(raw, &batch); err != nil {
		s.FramesRejected.Add(1)
		if s.metrics != nil {
			s.metrics.BatchesMalformed.Inc()
		}
		s.logger.Debug("malformed frame batch", "remote", remoteAddr, "error", err)
		return FrameResult{Rejected: 1}
	}
	if s.metrics != nil {
		s.metrics.FramesReceived.Add(int64(len(batch.Frames)))
	}

	// Validate batch
	if !validID(batch.MatchID, s.cfg.MaxIDLength) {
		s.FramesRejected.Add(int64(len(batch.Frames)))
		if s.metrics != nil {
			s.metrics.BatchesRejected.Inc("invalid_match_id")
			s.metrics.FramesInvalid.Add(int64(len(batch.Frames)))
		}
		s.warnThrottled("badmatch:"+remoteAddr, "batch with invalid match_id rejected",
			"remote", remoteAddr, "len", len(batch.MatchID), "max", s.cfg.MaxIDLength)
		return FrameResult{Rejected: len(batch.Frames)}
	}
	if len(batch.Frames) > s.cfg.MaxFramesPerBatch {
		s.FramesRejected.Add(int64(len(batch.Frames)))
		if s.metrics != nil {
			s.metrics.BatchesRejected.Inc("oversized_batch")
			s.metrics.FramesInvalid.Add(int64(len(batch.Frames)))
		}
		s.logger.Warn("oversized batch rejected", "match", batch.MatchID, "frames", len(batch.Frames))
		return FrameResult{Rejected: len(batch.Frames)}
	}

	// Validate, then rate limit (invalid frames must not consume budget).
	var accepted []model.PlayerTelemetryFrame
	res := FrameResult{}
	for i := range batch.Frames {
		frame := batch.Frames[i]
		if err := validateIngestFrame(&frame, s.cfg.MaxIDLength); err != nil {
			s.FramesRejected.Add(1)
			res.Rejected++
			if s.metrics != nil {
				s.metrics.FramesInvalid.Inc()
				s.metrics.FramesInvalidReasons.Inc(err.Reason)
			}
			s.warnThrottled("invalid:"+batch.MatchID+":"+frame.PlayerID+":"+err.Reason,
				"telemetry frame rejected at ingest",
				"match", batch.MatchID, "player", frame.PlayerID, "reason", err.Reason)
			continue
		}
		if !s.checkRateLimit(batch.MatchID, frame.PlayerID) {
			s.FramesRejected.Add(1)
			s.FramesRateLimited.Add(1)
			res.Rejected++
			if s.metrics != nil {
				s.metrics.FramesRateLimited.Inc()
			}
			s.warnThrottled("ratelimit:"+batch.MatchID+":"+frame.PlayerID,
				"telemetry frames dropped by per-player rate limit",
				"match", batch.MatchID, "player", frame.PlayerID, "limit_per_sec", s.cfg.MaxFrameRatePerPlayer)
			continue
		}
		accepted = append(accepted, frame)
		s.FramesReceived.Add(1)
	}

	if len(accepted) == 0 {
		return res
	}
	hr := s.handler.HandleFrames(batch.MatchID, accepted)
	res.Accepted += hr.Accepted
	res.Rejected += hr.Rejected
	res.Ignored += hr.Ignored
	s.FramesProcessed.Add(int64(hr.Accepted))
	s.FramesRejected.Add(int64(hr.Rejected))
	s.FramesIgnored.Add(int64(hr.Ignored))
	return res
}

// writeControl sends a control message with a write deadline so a peer that
// never reads cannot stall the connection goroutine.
func (s *Server) writeControl(ws *websocket.Conn, msg model.ControlMessage) error {
	if s.cfg.WriteTimeout > 0 {
		_ = ws.SetWriteDeadline(time.Now().Add(s.cfg.WriteTimeout))
	}
	return websocket.JSON.Send(ws, msg)
}

func (s *Server) trackConn(ws *websocket.Conn, add bool) {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	if add {
		s.conns[ws] = struct{}{}
	} else {
		delete(s.conns, ws)
	}
}

// warnThrottled logs the first occurrence of key at Warn, then every 1000th.
func (s *Server) warnThrottled(key, msg string, args ...any) {
	s.warnMu.Lock()
	n := s.warnCounts[key] + 1
	s.warnCounts[key] = n
	s.warnMu.Unlock()
	if n == 1 || n%1000 == 0 {
		s.logger.Warn(msg, append(args, "count", n)...)
	}
}

// checkRateLimit enforces per-player frame rate limits on a window aligned
// to whole seconds.
func (s *Server) checkRateLimit(matchID, playerID string) bool {
	if s.cfg.MaxFrameRatePerPlayer <= 0 {
		return true
	}
	key := matchID + ":" + playerID
	now := time.Now()
	window := now.Truncate(time.Second)

	s.playerRatesMu.Lock()
	defer s.playerRatesMu.Unlock()

	rl, ok := s.playerRates[key]
	if !ok {
		rl = &rateLimiter{window: window}
		s.playerRates[key] = rl
	}

	if !window.Equal(rl.window) {
		rl.count = 0
		rl.window = window
	}

	if rl.count >= s.cfg.MaxFrameRatePerPlayer {
		return false
	}
	rl.count++
	rl.lastFrame = now
	return true
}

// CleanupStaleRateLimiters removes entries older than 5 minutes.
func (s *Server) CleanupStaleRateLimiters() {
	s.playerRatesMu.Lock()
	defer s.playerRatesMu.Unlock()
	cutoff := time.Now().Add(-5 * time.Minute)
	for key, rl := range s.playerRates {
		if rl.lastFrame.Before(cutoff) {
			delete(s.playerRates, key)
		}
	}
	s.warnMu.Lock()
	if len(s.warnCounts) > 10000 {
		s.warnCounts = make(map[string]int)
	}
	s.warnMu.Unlock()
}

// FrameBatch is a batch of frames from a game server for a single match.
type FrameBatch struct {
	MatchID   string                       `json:"match_id"`
	ServerID  string                       `json:"server_id"`
	Timestamp time.Time                    `json:"timestamp"`
	Frames    []model.PlayerTelemetryFrame `json:"frames"`
}

// IngestError is a fast-validation failure with a stable reason code.
type IngestError struct {
	Reason string
}

func (e *IngestError) Error() string { return e.Reason }

// validateIngestFrame performs fast validation on an incoming frame.
func validateIngestFrame(f *model.PlayerTelemetryFrame, maxIDLen int) *IngestError {
	if f.PlayerID == "" {
		return &IngestError{Reason: "missing_player_id"}
	}
	if !validID(f.PlayerID, maxIDLen) {
		return &IngestError{Reason: "invalid_player_id"}
	}
	if f.Position.IsZero() {
		return &IngestError{Reason: "zero_position"}
	}
	if f.Position.HasNaN() || f.Position.HasInf() {
		return &IngestError{Reason: "invalid_position"}
	}
	if f.Timestamp < 0 || f.Timestamp != f.Timestamp { // negative or NaN
		return &IngestError{Reason: "invalid_timestamp"}
	}
	if f.FrameIndex < 0 {
		return &IngestError{Reason: "negative_frame_index"}
	}
	return nil
}

// validID bounds identifiers: non-empty, at most maxLen bytes, no control
// characters.
func validID(id string, maxLen int) bool {
	if id == "" || len(id) > maxLen {
		return false
	}
	if strings.ContainsFunc(id, func(r rune) bool { return unicode.IsControl(r) || r == unicode.ReplacementChar }) {
		return false
	}
	return true
}
