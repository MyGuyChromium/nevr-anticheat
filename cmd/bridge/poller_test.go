package main

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

// F178: a status tick logs and goes back to waiting; it never triggers an
// extra /session sample.
func TestPoller_StatusTickDoesNotPoll(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess", "playing", 2), 200)
	cfg := testConfig("http://unused", fb.port(t))
	cfg.PollInterval = time.Hour
	cfg.StatusInterval = 5 * time.Millisecond
	stats := &bridgeStats{}
	ctx, cancel := context.WithCancel(context.Background())
	m := DiscoveredMatch{MatchID: "m", BroadcasterIP: "127.0.0.1"}
	p := newMatchPoller(m, cfg, nil, stats, &frameEpoch{}, cancel, testLogger())

	done := make(chan struct{})
	go func() { p.run(ctx); close(done) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	if got := stats.TotalPolls.Load(); got != 1 {
		t.Errorf("polls = %d, want exactly 1 (status ticks must not sample)", got)
	}
	if fb.requests.Load() != 1 {
		t.Errorf("broadcaster requests = %d, want 1", fb.requests.Load())
	}
	if p.stopReason() != stopCancelled {
		t.Errorf("stop reason = %q", p.stopReason())
	}
}

// F84: every failure class counts toward the same idle threshold and stops
// with a distinct reason once the idle budget is exhausted.
func TestPoller_TransportFailuresIdleThenGiveUp(t *testing.T) {
	_, portStr, _ := net.SplitHostPort(refusedAddr(t))
	port, _ := strconv.Atoi(portStr)
	cfg := testConfig("http://unused", port) // released port: connection refused
	cfg.PollInterval = time.Millisecond
	cfg.IdleInterval = time.Millisecond
	cfg.IdleGiveUp = 20 * time.Millisecond
	stats := &bridgeStats{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := DiscoveredMatch{MatchID: "m", BroadcasterIP: "127.0.0.1"}
	p := newMatchPoller(m, cfg, nil, stats, &frameEpoch{}, cancel, testLogger())

	done := make(chan struct{})
	go func() { p.run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("poller never gave up")
	}
	if p.stopReason() != stopBroadcasterUnreachable {
		t.Errorf("stop reason = %q, want %q", p.stopReason(), stopBroadcasterUnreachable)
	}
	if stats.TotalPollFailures.Load() < failureIdleThreshold {
		t.Errorf("failures = %d, want >= %d", stats.TotalPollFailures.Load(), failureIdleThreshold)
	}
	if stats.PollerIdleTransitions.Load() != 1 {
		t.Errorf("idle transitions = %d, want 1", stats.PollerIdleTransitions.Load())
	}
}

func TestShouldLogFailure(t *testing.T) {
	want := map[int]bool{1: true, 2: false, 5: true, 15: true, 30: true, 31: false, 300: true, 600: true, 301: false}
	for n, w := range want {
		if got := shouldLogFailure(n); got != w {
			t.Errorf("shouldLogFailure(%d) = %v, want %v", n, got, w)
		}
	}
}
