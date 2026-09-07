package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// L3 (fix pass 2): a pre-hello error that is not a credential problem
// (too_many_connections) is retried and is not counted as an auth failure.
func TestWSSender_NonAuthRefusalIsRetried(t *testing.T) {
	fa := startFakeAnticheat(t)
	fa.refuseWith = "too_many_connections"
	cfg := senderConfig(fa.wsURL())
	cfg.ConnectAttempts = 3
	stats := &bridgeStats{}
	s, err := newWSSender(cfg, stats, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	err = s.connect(context.Background())
	if err == nil {
		t.Fatal("expected connect to fail")
	}
	if errors.Is(err, errAuthRejected) {
		t.Errorf("too_many_connections classified as an auth rejection: %v", err)
	}
	if !strings.Contains(err.Error(), "too_many_connections") {
		t.Errorf("error should name the real cause: %v", err)
	}
	if stats.AuthFailures.Load() != 0 {
		t.Errorf("auth failures = %d, want 0", stats.AuthFailures.Load())
	}
	if fa.connCount.Load() != 3 {
		t.Errorf("connection attempts = %d, want 3 (retried)", fa.connCount.Load())
	}
}

// A credential rejection still stops retrying immediately.
func TestWSSender_UnauthorizedIsNotRetried(t *testing.T) {
	fa := startFakeAnticheat(t)
	fa.expectToken = "secret"
	cfg := senderConfig(fa.wsURL())
	cfg.AnticheatToken = "wrong"
	cfg.ConnectAttempts = 3
	stats := &bridgeStats{}
	s, err := newWSSender(cfg, stats, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.connect(context.Background()); !errors.Is(err, errAuthRejected) {
		t.Fatalf("expected errAuthRejected, got %v", err)
	}
	if stats.AuthFailures.Load() != 1 || fa.connCount.Load() != 1 {
		t.Errorf("auth failures=%d attempts=%d, want 1/1", stats.AuthFailures.Load(), fa.connCount.Load())
	}
}

func TestIsAuthRejection(t *testing.T) {
	for reason, want := range map[string]bool{"unauthorized": true, "auth_failed": true, "too_many_connections": false, "": false} {
		if got := isAuthRejection(reason); got != want {
			t.Errorf("isAuthRejection(%q) = %v, want %v", reason, got, want)
		}
	}
}

// L4/L5 (fix pass 2): a message that fails at write time is accounted for:
// a control message counts as control_dropped, a batch as dropped (not as
// unacked-lost), and nothing stays registered in flight.
func TestWSSender_WriteFailureAccounting(t *testing.T) {
	fa := startFakeAnticheat(t)
	stats := &bridgeStats{}
	s, err := newWSSender(senderConfig(fa.wsURL()), stats, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.close()
	s.writeTimeout = -time.Second // every write deadline is already past

	s.pending.Add(1)
	s.deliver(ctx, outboundMsg{payload: &model.ControlMessage{Type: model.ControlMatchEnd, MatchID: "m"}, control: true, matchID: "m"})
	if stats.ControlDropped.Load() != 1 || stats.ControlSent.Load() != 0 || stats.TotalSendFailures.Load() != 1 {
		t.Errorf("control_dropped=%d control_sent=%d send_failures=%d, want 1/0/1",
			stats.ControlDropped.Load(), stats.ControlSent.Load(), stats.TotalSendFailures.Load())
	}
	if s.connected.Load() {
		t.Error("connection should have been dropped after the write failure")
	}

	// Reconnect (deliver dials lazily) and fail a batch the same way.
	s.writeTimeout = time.Second
	if err := s.connect(ctx); err != nil {
		t.Fatal(err)
	}
	s.writeTimeout = -time.Second
	s.pending.Add(1)
	s.deliver(ctx, outboundMsg{payload: &FrameBatch{MatchID: "m", Frames: make([]model.PlayerTelemetryFrame, 3)}, frames: 3, matchID: "m"})
	if stats.TotalFramesDropped.Load() != 3 || stats.TotalFramesUnackedLost.Load() != 0 || stats.TotalFramesSent.Load() != 0 {
		t.Errorf("dropped=%d unacked_lost=%d sent=%d, want 3/0/0",
			stats.TotalFramesDropped.Load(), stats.TotalFramesUnackedLost.Load(), stats.TotalFramesSent.Load())
	}
	if n, _ := s.inflightAge(time.Now()); n != 0 {
		t.Errorf("inflight after failed write = %d, want 0", n)
	}
}

// L5 (fix pass 2): frames are registered in flight before the write, so an
// ack that arrives immediately can never be settled before the registration
// and leave a phantom in-flight count (which would later trip the ack
// timeout on an idle link). Each batch is acked synchronously by the fake;
// after the ack, in-flight must be exactly zero every time.
func TestWSSender_ImmediateAckLeavesNoPhantomInflight(t *testing.T) {
	fa := startFakeAnticheat(t)
	stats := &bridgeStats{}
	s, err := newWSSender(senderConfig(fa.wsURL()), stats, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.close()
	for i := 1; i <= 300; i++ {
		s.pending.Add(1)
		s.deliver(ctx, outboundMsg{payload: &FrameBatch{MatchID: "m", Frames: make([]model.PlayerTelemetryFrame, 2)}, frames: 2, matchID: "m"})
		want := int64(i)
		// AcksReceived records receipt before handleControl settles the batch.
		// Wait for both effects: observing the receipt counter alone can race
		// with correct settlement, especially under the race detector. A real
		// phantom count still fails within the same bounded deadline.
		if !waitFor(2*time.Second, func() bool {
			n, _ := s.inflightAge(time.Now())
			return stats.AcksReceived.Load() == want && n == 0
		}) {
			n, _ := s.inflightAge(time.Now())
			t.Fatalf("batch %d: ack processing did not settle: received=%d, in-flight=%d", i, stats.AcksReceived.Load(), n)
		}
	}
	if stats.TotalFramesForwarded.Load() != 600 {
		t.Errorf("forwarded = %d, want 600", stats.TotalFramesForwarded.Load())
	}
}

func TestWSSender_RemoveInflight(t *testing.T) {
	s := &wsSender{}
	s.addInflight(5)
	s.addInflight(3)
	since := s.inflightSince
	s.removeInflight(3)
	if n, _ := s.inflightAge(time.Now()); n != 5 || !s.inflightSince.Equal(since) {
		t.Errorf("after remove: n=%d since=%v, want 5 with the original age (%v)", n, s.inflightSince, since)
	}
	s.removeInflight(10)
	if n, _ := s.inflightAge(time.Now()); n != 0 || !s.inflightSince.IsZero() {
		t.Errorf("after removing everything: n=%d since=%v, want 0/zero", n, s.inflightSince)
	}
}

// L0 (fix pass 2): the keepalive is an application-level control message
// (type "ping") that reaches the server's read loop, not a WebSocket ping
// frame that x/net consumes inside Receive without refreshing the idle
// deadline.
func TestWSSender_KeepaliveIsAControlMessage(t *testing.T) {
	fa := startFakeAnticheat(t)
	cfg := senderConfig(fa.wsURL())
	cfg.Keepalive = 50 * time.Millisecond
	stats := &bridgeStats{}
	s, err := newWSSender(cfg, stats, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.close()
	s.start(ctx)
	if !waitFor(3*time.Second, func() bool { return len(fa.controlsOf(controlKeepalive)) >= 1 }) {
		t.Fatalf("no keepalive control received by the server; controls=%+v", fa.controlsOf(controlKeepalive))
	}
	if !s.connected.Load() || stats.TotalSendFailures.Load() != 0 {
		t.Errorf("keepalive disturbed the link: connected=%v send_failures=%d", s.connected.Load(), stats.TotalSendFailures.Load())
	}
}
