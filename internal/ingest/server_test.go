package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/nevr-anticheat/nevr-anticheat/internal/metrics"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeHandler records batches and control messages.
type fakeHandler struct {
	mu       sync.Mutex
	batches  [][]model.PlayerTelemetryFrame
	matchIDs []string
	controls []model.ControlMessage
	ignored  int // reported as Ignored on every batch
}

func (h *fakeHandler) HandleFrames(matchID string, frames []model.PlayerTelemetryFrame) FrameResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.batches = append(h.batches, frames)
	h.matchIDs = append(h.matchIDs, matchID)
	return FrameResult{Accepted: len(frames), Ignored: h.ignored}
}

func (h *fakeHandler) HandleControl(msg model.ControlMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.controls = append(h.controls, msg)
}

func (h *fakeHandler) frameCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, b := range h.batches {
		n += len(b)
	}
	return n
}

func (h *fakeHandler) controlTypes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, c := range h.controls {
		out = append(out, c.Type)
	}
	return out
}

func testServerConfig() ServerConfig {
	cfg := DefaultServerConfig()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.AuthToken = "secret-token"
	cfg.AckInterval = 30 * time.Millisecond
	cfg.WriteTimeout = 2 * time.Second
	return cfg
}

func startServer(t *testing.T, cfg ServerConfig, h Handler) (*Server, string, context.CancelFunc) {
	t.Helper()
	s := NewServer(cfg, h, quietLogger())
	s.SetMetrics(metrics.NewMetrics())
	if err := s.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	return s, "ws://" + s.Addr().String() + "/telemetry", cancel
}

func dial(t *testing.T, url, token string) *websocket.Conn {
	t.Helper()
	cfg, err := websocket.NewConfig(url, "http://localhost/")
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		cfg.Header.Set("Authorization", "Bearer "+token)
	}
	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func recvControl(t *testing.T, conn *websocket.Conn, timeout time.Duration) (model.ControlMessage, error) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	var msg model.ControlMessage
	err := websocket.JSON.Receive(conn, &msg)
	return msg, err
}

// waitAck reads control messages until an ack arrives (or timeout).
func waitAck(t *testing.T, conn *websocket.Conn) model.ControlMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := recvControl(t, conn, time.Until(deadline))
		if err != nil {
			t.Fatalf("waiting for ack: %v", err)
		}
		if msg.Type == model.ControlAck {
			return msg
		}
	}
	t.Fatal("no ack received")
	return model.ControlMessage{}
}

func goodFrame(pid string, i int) model.PlayerTelemetryFrame {
	return model.PlayerTelemetryFrame{
		PlayerID: pid, FrameIndex: i, Timestamp: float64(i) * 0.067, DeltaTime: 0.067,
		Position: model.Vec3{1, 1, 5 + 0.1*float64(i)}, Rotation: model.QuatIdentity(),
		LeftHandPosition: model.Vec3{0.7, 1.3, 5}, RightHandPosition: model.Vec3{1.3, 1.3, 5},
		LeftHandRotation: model.QuatIdentity(), RightHandRotation: model.QuatIdentity(),
		GamePhase: "playing",
	}
}

func sendJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	if err := websocket.JSON.Send(conn, v); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func TestServer_RefusesToStartWithoutToken(t *testing.T) {
	cfg := testServerConfig()
	cfg.AuthToken = ""
	s := NewServer(cfg, &fakeHandler{}, quietLogger())
	if err := s.Listen(); !errors.Is(err, ErrAuthTokenRequired) {
		t.Fatalf("expected ErrAuthTokenRequired, got %v", err)
	}
	cfg.AllowUnauthenticated = true
	s = NewServer(cfg, &fakeHandler{}, quietLogger())
	if err := s.Listen(); err != nil {
		t.Fatalf("explicit opt-in should bind: %v", err)
	}
	_ = s.Shutdown(context.Background())
}

func TestServer_HelloAckAndControlDispatch(t *testing.T) {
	h := &fakeHandler{ignored: 1}
	_, url, _ := startServer(t, testServerConfig(), h)
	conn := dial(t, url, "secret-token")

	hello, err := recvControl(t, conn, 2*time.Second)
	if err != nil || hello.Type != model.ControlHello || hello.Auth != "ok" {
		t.Fatalf("expected hello auth=ok, got %+v err=%v", hello, err)
	}

	sendJSON(t, conn, model.ControlMessage{Type: model.ControlMatchStart, MatchID: "M1", ServerID: "srv",
		GameMode: "Echo_Arena", Teams: map[string]string{"P1": "blue"}})
	sendJSON(t, conn, FrameBatch{MatchID: "M1", Frames: []model.PlayerTelemetryFrame{goodFrame("P1", 0), goodFrame("P1", 1), goodFrame("P1", 2)}})
	sendJSON(t, conn, map[string]any{"type": "what_is_this", "match_id": "M1"})
	sendJSON(t, conn, model.ControlMessage{Type: model.ControlMatchEnd, MatchID: "M1", Reason: "poller_stopped"})

	ack := waitAck(t, conn)
	if ack.Accepted != 3 || ack.Ignored != 1 || ack.Rejected != 0 {
		t.Errorf("ack=%+v want accepted=3 ignored=1", ack)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(h.controlTypes()) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := h.controlTypes(); fmt.Sprint(got) != "[match_start match_end]" {
		t.Errorf("controls=%v", got)
	}
	if h.frameCount() != 3 {
		t.Errorf("frames forwarded=%d", h.frameCount())
	}
}

func TestServer_RejectsBadToken(t *testing.T) {
	h := &fakeHandler{}
	_, url, _ := startServer(t, testServerConfig(), h)
	conn := dial(t, url, "wrong")
	msg, err := recvControl(t, conn, 2*time.Second)
	if err != nil || msg.Type != model.ControlError || msg.Reason != "unauthorized" {
		t.Fatalf("expected error/unauthorized, got %+v err=%v", msg, err)
	}
	if _, err := recvControl(t, conn, 2*time.Second); err == nil {
		t.Error("connection should be closed after auth failure")
	}
	if h.frameCount() != 0 {
		t.Error("no frames should reach the handler")
	}
}

func TestServer_BoundsIdentifiers(t *testing.T) {
	h := &fakeHandler{}
	s, url, _ := startServer(t, testServerConfig(), h)
	conn := dial(t, url, "secret-token")
	if _, err := recvControl(t, conn, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 5000)
	sendJSON(t, conn, FrameBatch{MatchID: long, Frames: []model.PlayerTelemetryFrame{goodFrame("P1", 0)}})
	bad := goodFrame(strings.Repeat("p", 200), 1)
	ctl := goodFrame("ok\x00id", 2)
	sendJSON(t, conn, FrameBatch{MatchID: "M1", Frames: []model.PlayerTelemetryFrame{bad, ctl, goodFrame("P1", 3)}})
	ack := waitAck(t, conn)
	if ack.Accepted != 1 || ack.Rejected != 3 {
		t.Errorf("ack=%+v want accepted=1 rejected=3", ack)
	}
	if h.frameCount() != 1 || h.matchIDs[0] != "M1" {
		t.Errorf("handler got %d frames for %v", h.frameCount(), h.matchIDs)
	}
	if s.FramesRejected.Load() != 3 {
		t.Errorf("FramesRejected=%d", s.FramesRejected.Load())
	}
}

func TestServer_OversizeMessageKeepsConnection(t *testing.T) {
	h := &fakeHandler{}
	cfg := testServerConfig()
	cfg.MaxFrameSize = 2048
	_, url, _ := startServer(t, cfg, h)
	conn := dial(t, url, "secret-token")
	if _, err := recvControl(t, conn, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	var big []model.PlayerTelemetryFrame
	for i := 0; i < 40; i++ {
		big = append(big, goodFrame("P1", i))
	}
	raw, _ := json.Marshal(FrameBatch{MatchID: "M1", Frames: big})
	if len(raw) <= cfg.MaxFrameSize {
		t.Fatalf("test payload too small: %d", len(raw))
	}
	if err := websocket.Message.Send(conn, string(raw)); err != nil {
		t.Fatal(err)
	}
	sendJSON(t, conn, FrameBatch{MatchID: "M1", Frames: []model.PlayerTelemetryFrame{goodFrame("P1", 100)}})
	ack := waitAck(t, conn)
	if ack.Accepted != 1 {
		t.Errorf("connection unusable after oversize message: ack=%+v", ack)
	}
}

func TestServer_ValidatesBeforeRateLimiting(t *testing.T) {
	h := &fakeHandler{}
	cfg := testServerConfig()
	cfg.MaxFrameRatePerPlayer = 5
	_, url, _ := startServer(t, cfg, h)
	conn := dial(t, url, "secret-token")
	if _, err := recvControl(t, conn, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	var frames []model.PlayerTelemetryFrame
	for i := 0; i < 5; i++ {
		f := goodFrame("P1", i)
		f.Position = model.Vec3{} // invalid: must not consume rate budget
		frames = append(frames, f)
	}
	for i := 5; i < 12; i++ {
		frames = append(frames, goodFrame("P1", i))
	}
	sendJSON(t, conn, FrameBatch{MatchID: "M1", Frames: frames})
	ack := waitAck(t, conn)
	if ack.Accepted != 5 || ack.Rejected != 7 {
		t.Errorf("ack=%+v want accepted=5 (rate cap) rejected=7 (5 invalid + 2 throttled)", ack)
	}
}

func TestServer_ShutdownClosesConnections(t *testing.T) {
	h := &fakeHandler{}
	s, url, cancel := startServer(t, testServerConfig(), h)
	conn := dial(t, url, "secret-token")
	if _, err := recvControl(t, conn, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	cancel()
	shutdownCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
	defer c()
	_ = s.Shutdown(shutdownCtx)
	if _, err := recvControl(t, conn, 2*time.Second); err == nil {
		t.Error("client should observe the close")
	}
	if s.activeConns.Load() != 0 {
		t.Errorf("active connections after shutdown: %d", s.activeConns.Load())
	}
}

func TestServer_IdleDeadline(t *testing.T) {
	h := &fakeHandler{}
	cfg := testServerConfig()
	cfg.ReadTimeout = 200 * time.Millisecond
	s, url, _ := startServer(t, cfg, h)
	conn := dial(t, url, "secret-token")
	if _, err := recvControl(t, conn, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := recvControl(t, conn, 2*time.Second); err == nil {
		t.Error("idle connection should be closed by the server")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s.activeConns.Load() != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if s.activeConns.Load() != 0 {
		t.Error("idle connection slot not released")
	}
}

func TestValidateIngestFrame_DerivesDiscSpeed(t *testing.T) {
	f := goodFrame("P1", 0)
	f.Disc = &model.DiscState{Velocity: model.Vec3{3, 4, 0}}
	if err := validateIngestFrame(&f, 128); err != nil {
		t.Fatal(err)
	}
	if f.Disc.Speed != 5 {
		t.Errorf("speed=%v want 5", f.Disc.Speed)
	}
	f.Disc = &model.DiscState{Velocity: model.Vec3{3, 4, 0}, Speed: 2}
	_ = validateIngestFrame(&f, 128)
	if f.Disc.Speed != 2 {
		t.Error("explicit speed must be preserved")
	}
}
