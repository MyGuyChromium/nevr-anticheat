package main

import (
	"context"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func senderConfig(url string) *BridgeConfig {
	return &BridgeConfig{
		AnticheatURL:    url,
		HelloTimeout:    time.Second,
		DialTimeout:     time.Second,
		WriteTimeout:    time.Second,
		AckTimeout:      3 * time.Second,
		Keepalive:       time.Hour,
		ConnectAttempts: 1,
	}
}

func TestNewWSSender_InvalidURLReturnsError(t *testing.T) {
	_, err := newWSSender(&BridgeConfig{AnticheatURL: "://bad"}, &bridgeStats{}, testLogger())
	if err == nil {
		t.Fatal("expected error for invalid URL (must not os.Exit)")
	}
}

// F9: a full queue drops frames with a counter instead of blocking callers.
func TestWSSender_QueueOverflowDropsWithoutBlocking(t *testing.T) {
	cfg := senderConfig("ws://" + refusedAddr(t) + "/telemetry")
	cfg.QueueSize = 2
	stats := &bridgeStats{}
	s, err := newWSSender(cfg, stats, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	// Writer not started: nothing drains the queue.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5; i++ {
			s.enqueueBatch(&FrameBatch{MatchID: "m", Frames: make([]model.PlayerTelemetryFrame, 3)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("enqueueBatch blocked on a full queue")
	}
	if stats.TotalFramesDropped.Load() != 9 || stats.TotalBatchesDropped.Load() != 3 {
		t.Errorf("dropped frames=%d batches=%d, want 9/3", stats.TotalFramesDropped.Load(), stats.TotalBatchesDropped.Load())
	}
	if s.queued() != 2 {
		t.Errorf("queued = %d, want 2", s.queued())
	}
}

// Frames only count as forwarded once the server acks them.
func TestWSSender_OnlyAckedFramesCountAsForwarded(t *testing.T) {
	fa := startFakeAnticheat(t)
	fa.ackDelay = 150 * time.Millisecond
	stats := &bridgeStats{}
	s, err := newWSSender(senderConfig(fa.wsURL()), stats, testLogger())
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

	s.enqueueBatch(&FrameBatch{MatchID: "m", Frames: make([]model.PlayerTelemetryFrame, 4)})
	if !waitFor(time.Second, func() bool { return stats.TotalFramesSent.Load() == 4 }) {
		t.Fatal("batch not written")
	}
	if stats.TotalFramesForwarded.Load() != 0 {
		t.Errorf("forwarded counted before ack: %d", stats.TotalFramesForwarded.Load())
	}
	if !waitFor(time.Second, func() bool { return stats.TotalFramesForwarded.Load() == 4 }) {
		t.Errorf("forwarded after ack = %d, want 4", stats.TotalFramesForwarded.Load())
	}
	if n, _ := s.inflightAge(time.Now()); n != 0 {
		t.Errorf("inflight after ack = %d, want 0", n)
	}
}

// A link that stops acking is declared dead, the frames on it counted lost,
// and the next message reconnects.
func TestWSSender_AckTimeoutReconnects(t *testing.T) {
	fa := startFakeAnticheat(t)
	fa.noAck = true
	cfg := senderConfig(fa.wsURL())
	cfg.AckTimeout = 300 * time.Millisecond
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

	s.enqueueBatch(&FrameBatch{MatchID: "m", Frames: make([]model.PlayerTelemetryFrame, 2)})
	if !waitFor(3*time.Second, func() bool { return stats.AckTimeouts.Load() >= 1 }) {
		t.Fatal("ack timeout never fired")
	}
	if stats.TotalFramesUnackedLost.Load() != 2 {
		t.Errorf("unacked lost = %d, want 2", stats.TotalFramesUnackedLost.Load())
	}
	if stats.TotalFramesForwarded.Load() != 0 {
		t.Errorf("forwarded = %d, want 0", stats.TotalFramesForwarded.Load())
	}
	s.enqueueBatch(&FrameBatch{MatchID: "m", Frames: make([]model.PlayerTelemetryFrame, 1)})
	if !waitFor(2*time.Second, func() bool { return fa.connCount.Load() >= 2 && stats.TotalFramesSent.Load() == 3 }) {
		t.Errorf("did not reconnect and resend: conns=%d sent=%d", fa.connCount.Load(), stats.TotalFramesSent.Load())
	}
}

func TestWSSender_ReconnectAfterServerDrop(t *testing.T) {
	fa := startFakeAnticheat(t)
	stats := &bridgeStats{}
	s, err := newWSSender(senderConfig(fa.wsURL()), stats, testLogger())
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

	s.enqueueBatch(&FrameBatch{MatchID: "send-1", Frames: make([]model.PlayerTelemetryFrame, 1)})
	if !waitForBatches(fa, 1, time.Second) {
		t.Fatal("fake did not receive batch 1")
	}
	fa.closeConnections()
	if !waitFor(time.Second, func() bool { return !s.connected.Load() }) {
		t.Fatal("sender did not notice the server closing the socket")
	}
	s.enqueueBatch(&FrameBatch{MatchID: "send-2", Frames: make([]model.PlayerTelemetryFrame, 1)})
	if !waitFor(3*time.Second, func() bool { b := fa.lastBatch(); return b != nil && b.MatchID == "send-2" }) {
		t.Errorf("fake did not receive send-2 after reconnect; batches=%d", fa.batchCount())
	}
	if fa.connCount.Load() < 2 {
		t.Errorf("connections = %d, want >= 2", fa.connCount.Load())
	}
}

func TestWSSender_ControlMessagesAreDelivered(t *testing.T) {
	fa := startFakeAnticheat(t)
	stats := &bridgeStats{}
	s, err := newWSSender(senderConfig(fa.wsURL()), stats, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.start(ctx) // lazy connect on first message
	defer s.close()
	s.enqueueControl(&model.ControlMessage{Type: model.ControlMatchStart, MatchID: "m", ServerID: "1.2.3.4:6721", GameMode: "echo_arena"})
	if !waitFor(2*time.Second, func() bool { return len(fa.controlsOf(model.ControlMatchStart)) == 1 }) {
		t.Fatal("match_start not delivered")
	}
	if stats.ControlSent.Load() != 1 || stats.TotalBatchesSent.Load() != 0 {
		t.Errorf("control_sent=%d batches=%d", stats.ControlSent.Load(), stats.TotalBatchesSent.Load())
	}
}

func TestWSSender_ConnectRetriesWithBackoff(t *testing.T) {
	cfg := senderConfig("ws://" + refusedAddr(t) + "/telemetry")
	cfg.ConnectAttempts = 3
	stats := &bridgeStats{}
	s, err := newWSSender(cfg, stats, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := s.connect(context.Background()); err == nil {
		t.Fatal("expected connect to fail")
	}
	// backoff 500ms + 1s between three attempts
	if elapsed := time.Since(start); elapsed < 1400*time.Millisecond {
		t.Errorf("connect returned after %v; expected bounded retries with backoff", elapsed)
	}
	if stats.AuthFailures.Load() != 0 {
		t.Errorf("a refused connection must not count as an auth failure")
	}
}
