package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"golang.org/x/net/websocket"
)

// Sender defaults. All are overridable through BridgeConfig for tests.
const (
	defaultQueueSize       = 256
	defaultDialTimeout     = 5 * time.Second
	defaultHelloTimeout    = 5 * time.Second
	defaultWriteTimeout    = 5 * time.Second
	defaultAckTimeout      = 30 * time.Second
	defaultKeepalive       = 20 * time.Second
	defaultConnectAttempts = 3
	defaultBackoffMax      = 30 * time.Second
	controlEnqueueWait     = 500 * time.Millisecond
)

// outboundMsg is one message waiting for the writer goroutine.
type outboundMsg struct {
	payload any
	frames  int
	control bool
	matchID string
}

// errAuthRejected marks a hello-phase failure that a retry cannot fix.
var errAuthRejected = errors.New("ingest server rejected the bridge credentials")

// wsSender owns the single WebSocket link to the anticheat ingest server.
//
// Design (audit F8/F9): pollers never touch the socket. They enqueue into a
// bounded queue; one writer goroutine drains it with a write deadline on every
// send, so a half-open link can only ever cost queued frames (counted in
// TotalFramesDropped), never freeze polling. A read loop consumes the server's
// control messages: the hello that proves auth, and the periodic acks that are
// the only thing allowed to move TotalFramesForwarded. Frames sent on a
// connection that dies before being acked are counted as unacked-lost.
type wsSender struct {
	cfg    *BridgeConfig
	logger *slog.Logger
	config *websocket.Config
	stats  *bridgeStats

	queue chan outboundMsg

	mu     sync.Mutex
	conn   *websocket.Conn
	gen    uint64
	closed bool

	inflightMu    sync.Mutex
	inflight      int
	inflightSince time.Time

	lastSendNanos atomic.Int64
	connected     atomic.Bool
	pending       atomic.Int64 // queued + in-delivery messages

	dialTimeout     time.Duration
	helloTimeout    time.Duration
	writeTimeout    time.Duration
	ackTimeout      time.Duration
	keepalive       time.Duration
	connectAttempts int
	backoffMax      time.Duration

	authFailLogged atomic.Bool
	dialFailures   int

	done   chan struct{}
	wg     sync.WaitGroup
	readWG sync.WaitGroup
}

func newWSSender(cfg *BridgeConfig, stats *bridgeStats, logger *slog.Logger) (*wsSender, error) {
	wsConfig, err := websocket.NewConfig(cfg.AnticheatURL, "http://localhost/")
	if err != nil {
		return nil, fmt.Errorf("invalid anticheat WebSocket URL %q: %w", cfg.AnticheatURL, err)
	}
	if cfg.AnticheatToken != "" {
		wsConfig.Header.Set("Authorization", "Bearer "+cfg.AnticheatToken)
	}
	s := &wsSender{
		cfg:             cfg,
		logger:          logger,
		config:          wsConfig,
		stats:           stats,
		dialTimeout:     orDuration(cfg.DialTimeout, defaultDialTimeout),
		helloTimeout:    orDuration(cfg.HelloTimeout, defaultHelloTimeout),
		writeTimeout:    orDuration(cfg.WriteTimeout, defaultWriteTimeout),
		ackTimeout:      orDuration(cfg.AckTimeout, defaultAckTimeout),
		keepalive:       orDuration(cfg.Keepalive, defaultKeepalive),
		connectAttempts: cfg.ConnectAttempts,
		backoffMax:      defaultBackoffMax,
		done:            make(chan struct{}),
	}
	if s.connectAttempts <= 0 {
		s.connectAttempts = defaultConnectAttempts
	}
	qs := cfg.QueueSize
	if qs <= 0 {
		qs = defaultQueueSize
	}
	s.queue = make(chan outboundMsg, qs)
	wsConfig.Dialer = &net.Dialer{Timeout: s.dialTimeout, KeepAlive: 30 * time.Second}
	if stats == nil {
		s.stats = &bridgeStats{}
	}
	return s, nil
}

func orDuration(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

// start launches the writer goroutine. The writer connects lazily (with
// backoff) when the first message arrives, so the bridge can start before
// the ingest server is up.
func (s *wsSender) start(ctx context.Context) {
	s.wg.Add(1)
	go s.writerLoop(ctx)
}

// connect performs a bounded, synchronous dial+hello (up to connectAttempts
// with exponential backoff). Used by --once and as the eager first attempt
// in continuous mode; failure there is reported but not fatal.
func (s *wsSender) connect(ctx context.Context) error {
	backoff := 500 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= s.connectAttempts; attempt++ {
		conn, err := s.dial()
		if err == nil {
			s.adopt(conn)
			return nil
		}
		lastErr = err
		if errors.Is(err, errAuthRejected) {
			return err
		}
		if attempt == s.connectAttempts {
			break
		}
		s.logger.Warn("anticheat connect failed, retrying", "attempt", attempt, "of", s.connectAttempts, "error", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return lastErr
}

// dial opens the socket and completes the hello handshake.
func (s *wsSender) dial() (*websocket.Conn, error) {
	conn, err := websocket.DialConfig(s.config)
	if err != nil {
		return nil, fmt.Errorf("anticheat WebSocket dial: %w", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(s.helloTimeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("set hello deadline: %w", err)
	}
	var raw json.RawMessage
	if err := websocket.JSON.Receive(conn, &raw); err != nil {
		conn.Close()
		if errors.Is(err, io.EOF) {
			s.stats.AuthFailures.Add(1)
			return nil, fmt.Errorf("%w: connection closed before hello (wrong --anticheat-token, or the server rejected the connection)", errAuthRejected)
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			s.stats.AuthFailures.Add(1)
			return nil, fmt.Errorf("%w: no hello within %v (wrong --anticheat-token, or the server does not implement the hello/ack protocol)", errAuthRejected, s.helloTimeout)
		}
		return nil, fmt.Errorf("reading hello: %w", err)
	}
	var msg model.ControlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("malformed hello: %w", err)
	}
	switch msg.Type {
	case model.ControlHello:
		if msg.Auth != "ok" {
			conn.Close()
			s.stats.AuthFailures.Add(1)
			return nil, fmt.Errorf("%w: hello auth=%q", errAuthRejected, msg.Auth)
		}
	case model.ControlError:
		conn.Close()
		s.stats.AuthFailures.Add(1)
		return nil, fmt.Errorf("%w: %s", errAuthRejected, msg.Reason)
	default:
		conn.Close()
		return nil, fmt.Errorf("unexpected first message from ingest server: type=%q", msg.Type)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("clear hello deadline: %w", err)
	}
	return conn, nil
}

// adopt installs a freshly dialed connection and starts its read loop. A
// connection dialed while the sender was being closed is discarded so no
// read loop can outlive close().
func (s *wsSender) adopt(conn *websocket.Conn) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		conn.Close()
		return
	}
	if s.conn != nil {
		s.conn.Close()
	}
	s.conn = conn
	s.gen++
	gen := s.gen
	s.mu.Unlock()

	s.resetInflight(0)
	s.connected.Store(true)
	s.lastSendNanos.Store(time.Now().UnixNano())
	s.authFailLogged.Store(false)
	s.dialFailures = 0
	s.stats.Reconnects.Add(1)
	s.logger.Info("connected to anticheat WebSocket (hello ok)", "url", s.cfg.AnticheatURL)

	s.readWG.Add(1)
	go s.readLoop(conn, gen)
}

// current returns the live connection and its generation, or nil.
func (s *wsSender) current() (*websocket.Conn, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn, s.gen
}

// dropConn closes the connection of generation gen if it is still current.
// Frames in flight on it are counted as lost.
func (s *wsSender) dropConn(gen uint64, why string) {
	s.mu.Lock()
	if s.conn == nil || s.gen != gen {
		s.mu.Unlock()
		return
	}
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()
	conn.Close()
	s.connected.Store(false)
	lost := s.resetInflight(0)
	if lost > 0 {
		s.stats.TotalFramesUnackedLost.Add(int64(lost))
	}
	if !s.isClosed() {
		s.logger.Warn("anticheat connection dropped", "reason", why, "frames_unacked_lost", lost)
	}
}

func (s *wsSender) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// resetInflight sets the in-flight frame count and returns the previous value.
func (s *wsSender) resetInflight(n int) int {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	prev := s.inflight
	s.inflight = n
	if n == 0 {
		s.inflightSince = time.Time{}
	}
	return prev
}

func (s *wsSender) addInflight(n int) {
	if n <= 0 {
		return
	}
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if s.inflight == 0 {
		s.inflightSince = time.Now()
	}
	s.inflight += n
}

func (s *wsSender) settleInflight(n int) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	s.inflight -= n
	if s.inflight <= 0 {
		s.inflight = 0
		s.inflightSince = time.Time{}
	} else {
		s.inflightSince = time.Now()
	}
}

// inflightAge reports how long the oldest unacked frames have been waiting.
func (s *wsSender) inflightAge(now time.Time) (int, time.Duration) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if s.inflight == 0 {
		return 0, 0
	}
	return s.inflight, now.Sub(s.inflightSince)
}

// readLoop consumes control messages from one connection until it fails.
func (s *wsSender) readLoop(conn *websocket.Conn, gen uint64) {
	defer s.readWG.Done()
	for {
		var raw json.RawMessage
		if err := websocket.JSON.Receive(conn, &raw); err != nil {
			s.dropConn(gen, "read: "+err.Error())
			return
		}
		s.handleControl(raw)
	}
}

// handleControl applies one server->producer control message.
func (s *wsSender) handleControl(raw json.RawMessage) {
	var msg model.ControlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		s.logger.Debug("ignoring malformed control message from ingest", "error", err)
		return
	}
	switch msg.Type {
	case model.ControlAck:
		s.stats.AcksReceived.Add(1)
		if msg.Accepted > 0 {
			s.stats.TotalFramesAcked.Add(int64(msg.Accepted))
			s.stats.TotalFramesForwarded.Add(int64(msg.Accepted))
		}
		if msg.Rejected > 0 {
			s.stats.TotalFramesRejected.Add(int64(msg.Rejected))
		}
		if msg.Ignored > 0 {
			s.stats.TotalFramesIgnored.Add(int64(msg.Ignored))
		}
		s.settleInflight(msg.Accepted + msg.Rejected + msg.Ignored)
		if msg.Rejected > 0 || msg.Ignored > 0 {
			s.logger.Warn("ingest ack reported non-accepted frames",
				"accepted", msg.Accepted, "rejected", msg.Rejected, "ignored_duplicates", msg.Ignored)
		}
	case model.ControlError:
		s.logger.Error("ingest server reported an error", "reason", msg.Reason, "match_id", msg.MatchID)
	case model.ControlHello:
		// Duplicate hello after handshake: harmless.
	default:
		s.logger.Debug("ignoring unknown control message from ingest", "type", msg.Type)
	}
}

// enqueueBatch queues a frame batch without blocking. Returns false (and
// counts the frames as dropped) when the queue is full.
func (s *wsSender) enqueueBatch(b *FrameBatch) bool {
	s.pending.Add(1)
	select {
	case s.queue <- outboundMsg{payload: b, frames: len(b.Frames), matchID: b.MatchID}:
		return true
	default:
		s.pending.Add(-1)
		s.stats.TotalFramesDropped.Add(int64(len(b.Frames)))
		s.stats.TotalBatchesDropped.Add(1)
		return false
	}
}

// enqueueControl queues a control message, waiting briefly for room.
func (s *wsSender) enqueueControl(m *model.ControlMessage) bool {
	s.pending.Add(1)
	select {
	case s.queue <- outboundMsg{payload: m, control: true, matchID: m.MatchID}:
		return true
	case <-time.After(controlEnqueueWait):
		s.pending.Add(-1)
		s.stats.ControlDropped.Add(1)
		s.logger.Warn("dropped control message: send queue full", "type", m.Type, "match_id", m.MatchID)
		return false
	}
}

// queued reports how many messages are waiting for the writer.
func (s *wsSender) queued() int { return len(s.queue) }

// outstanding reports queued messages plus the one the writer is delivering.
func (s *wsSender) outstanding() int { return int(s.pending.Load()) }

// writerLoop is the only goroutine that writes application frames.
func (s *wsSender) writerLoop(ctx context.Context) {
	defer s.wg.Done()
	monitor := time.NewTicker(time.Second)
	defer monitor.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-monitor.C:
			s.monitorTick(ctx)
		case msg := <-s.queue:
			s.deliver(ctx, msg)
		}
	}
}

// monitorTick enforces the ack timeout and sends keepalive pings.
func (s *wsSender) monitorTick(ctx context.Context) {
	conn, gen := s.current()
	if conn == nil {
		return
	}
	now := time.Now()
	if n, age := s.inflightAge(now); n > 0 && age > s.ackTimeout {
		s.logger.Warn("no ack from ingest server; treating link as dead",
			"frames_unacked", n, "waited", age.Round(time.Second), "ack_timeout", s.ackTimeout)
		// Drop first so the in-flight frames are counted as lost before the
		// timeout counter becomes visible to observers (stats readers, tests).
		s.dropConn(gen, "ack timeout")
		s.stats.AckTimeouts.Add(1)
		return
	}
	if now.Sub(time.Unix(0, s.lastSendNanos.Load())) >= s.keepalive {
		s.writePing(conn, gen)
	}
}

// writePing sends a WebSocket ping frame so an idle link is exercised (and a
// dead one fails fast at the write deadline).
func (s *wsSender) writePing(conn *websocket.Conn, gen uint64) {
	_ = conn.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	conn.PayloadType = websocket.PingFrame
	_, err := conn.Write([]byte("nevr-bridge"))
	conn.PayloadType = websocket.TextFrame
	if err != nil {
		s.dropConn(gen, "keepalive ping: "+err.Error())
		return
	}
	s.lastSendNanos.Store(time.Now().UnixNano())
}

// deliver writes one message, connecting first if needed.
func (s *wsSender) deliver(ctx context.Context, msg outboundMsg) {
	defer s.pending.Add(-1)
	conn, gen := s.current()
	if conn == nil {
		c, err := s.dial()
		if err != nil {
			s.onDialFailure(ctx, msg, err)
			return
		}
		s.adopt(c)
		conn, gen = s.current()
		if conn == nil { // closed during the dial
			return
		}
	}
	_ = conn.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	if err := websocket.JSON.Send(conn, msg.payload); err != nil {
		s.stats.TotalSendFailures.Add(1)
		if msg.frames > 0 {
			s.stats.TotalFramesDropped.Add(int64(msg.frames))
		}
		s.dropConn(gen, "write: "+err.Error())
		return
	}
	s.lastSendNanos.Store(time.Now().UnixNano())
	if msg.control {
		s.stats.ControlSent.Add(1)
		return
	}
	s.stats.TotalBatchesSent.Add(1)
	s.stats.TotalFramesSent.Add(int64(msg.frames))
	s.addInflight(msg.frames)
}

// onDialFailure records a failed reconnect, drops the message that triggered
// it, and sleeps an exponential backoff (bounded) before the next attempt.
func (s *wsSender) onDialFailure(ctx context.Context, msg outboundMsg, err error) {
	s.stats.TotalSendFailures.Add(1)
	if msg.frames > 0 {
		s.stats.TotalFramesDropped.Add(int64(msg.frames))
	}
	if msg.control {
		s.stats.ControlDropped.Add(1)
	}
	s.dialFailures++
	if errors.Is(err, errAuthRejected) {
		if !s.authFailLogged.Swap(true) {
			s.logger.Error("ANTICHEAT AUTH FAILED — frames are NOT being ingested",
				"error", err, "action", "verify --anticheat-token matches the ingest server's NEVR_AC_AUTH_TOKEN")
		}
	} else if s.dialFailures <= 3 || s.dialFailures%20 == 0 {
		s.logger.Warn("anticheat reconnect failed", "error", err, "consecutive_failures", s.dialFailures)
	}
	backoff := time.Second << uint(min(s.dialFailures-1, 10))
	if backoff > s.backoffMax {
		backoff = s.backoffMax
	}
	select {
	case <-ctx.Done():
	case <-s.done:
	case <-time.After(backoff):
	}
}

// waitForAcks blocks until at least want frames have been acked (accepted or
// rejected) since the counters were read, or the timeout elapses. It returns
// the accepted and rejected counts observed.
func (s *wsSender) waitForAcks(ctx context.Context, baseAccepted, baseRejected int64, want int, timeout time.Duration) (accepted, rejected int, err error) {
	deadline := time.Now().Add(timeout)
	for {
		accepted = int(s.stats.TotalFramesAcked.Load() - baseAccepted)
		rejected = int(s.stats.TotalFramesRejected.Load() - baseRejected)
		if accepted+rejected >= want {
			return accepted, rejected, nil
		}
		if !s.connected.Load() && s.outstanding() == 0 {
			return accepted, rejected, errors.New("connection lost before the ingest server acknowledged the frames")
		}
		if time.Now().After(deadline) {
			return accepted, rejected, fmt.Errorf("no ack from ingest server within %v (acked %d of %d)", timeout, accepted+rejected, want)
		}
		select {
		case <-ctx.Done():
			return accepted, rejected, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// drain waits (bounded) for every queued message to be written so shutdown
// control messages reach the server. It gives up early when the link is down.
func (s *wsSender) drain(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for s.outstanding() > 0 && time.Now().Before(deadline) && s.connected.Load() {
		time.Sleep(10 * time.Millisecond)
	}
}

// close stops the writer, then closes whatever connection is current (the
// writer may adopt one last connection before it notices done), and finally
// waits for the read loop so no goroutine outlives the sender.
func (s *wsSender) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	s.mu.Unlock()
	s.wg.Wait()

	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()
	s.connected.Store(false)
	if conn != nil {
		conn.Close()
	}
	s.readWG.Wait()
}
