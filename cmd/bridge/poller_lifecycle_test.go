package main

import (
	"context"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// queuedControls drains the (unstarted) sender's queue and returns the
// control messages found as "type:reason", in order.
func queuedControls(t *testing.T, s *wsSender) []string {
	t.Helper()
	var out []string
	for {
		select {
		case m := <-s.queue:
			s.pending.Add(-1)
			if cm, ok := m.payload.(*model.ControlMessage); ok {
				out = append(out, cm.Type+":"+cm.Reason)
			}
		default:
			return out
		}
	}
}

// unstartedSender returns a sender whose writer never runs, so every message
// a poller enqueues stays observable in its queue.
func unstartedSender(t *testing.T, stats *bridgeStats) *wsSender {
	t.Helper()
	s, err := newWSSender(senderConfig("ws://"+refusedAddr(t)+"/telemetry"), stats, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func pollN(t *testing.T, p *matchPoller, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if ok, reason := p.poll(context.Background()); !ok {
			t.Fatalf("poll %d stopped early: %s", i+1, reason)
		}
	}
}

// C1 (fix pass 2): backing off to idle after failureIdleThreshold failed
// polls (~2s at the active rate) must NOT announce match_end: the ingest
// treats match_end as final and would score the recovery as a second match.
// match_end is announced only when the poller gives up or is cancelled.
func TestPoller_FailureBackoffKeepsMatchOpen(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess", "playing", 2), 200)
	cfg := testConfig("http://unused", fb.port(t))
	cfg.IdleGiveUp = 30 * time.Millisecond
	stats := &bridgeStats{}
	sender := unstartedSender(t, stats)
	m := DiscoveredMatch{MatchID: "m", BroadcasterIP: "127.0.0.1"}
	p := newMatchPoller(m, cfg, sender, stats, &frameEpoch{}, func() {}, testLogger())

	pollN(t, p, 2)
	if got := queuedControls(t, sender); len(got) != 1 || got[0] != "match_start:" {
		t.Fatalf("controls after first frames = %v, want [match_start:]", got)
	}

	// Broadcaster restarts: a burst of failures backs the poller off to idle.
	fb.set(`{"error":"restarting"}`, 500)
	pollN(t, p, failureIdleThreshold)
	if p.state != stateIdleError {
		t.Fatalf("state = %s, want idle_error", p.state)
	}
	if !p.matchStarted {
		t.Error("match was ended on the idle transition")
	}
	if got := queuedControls(t, sender); len(got) != 0 {
		t.Errorf("controls on idle transition = %v, want none (no match_end)", got)
	}

	// It comes back with the same session: the same match simply resumes,
	// without a second match_start (the ingest never saw an end).
	fb.set(fakeSessionJSON("sess", "playing", 2), 200)
	pollN(t, p, 2)
	if p.state != stateActive || !p.matchStarted {
		t.Fatalf("state=%s started=%v after recovery, want active/true", p.state, p.matchStarted)
	}
	if got := queuedControls(t, sender); len(got) != 0 {
		t.Errorf("controls after recovery = %v, want none (same match continues)", got)
	}
	if stats.PollerIdleTransitions.Load() != 1 {
		t.Errorf("idle transitions = %d, want 1", stats.PollerIdleTransitions.Load())
	}

	// A failure run that outlasts --idle-give-up does stop the poller; the
	// run loop then announces match_end with the give-up reason.
	fb.set(`{"error":"gone"}`, 500)
	pollN(t, p, failureIdleThreshold)
	if p.state != stateIdleError {
		t.Fatalf("state = %s, want idle_error", p.state)
	}
	time.Sleep(cfg.IdleGiveUp + 10*time.Millisecond)
	ok, reason := p.poll(context.Background())
	if ok || reason != stopBroadcasterError {
		t.Fatalf("poll after give-up window = (%v, %q), want (false, %q)", ok, reason, stopBroadcasterError)
	}
	if !p.matchStarted {
		t.Error("match ended before the run loop stop path")
	}
	p.endMatch(string(reason)) // what stopWith in run() does
	if got := queuedControls(t, sender); len(got) != 1 || got[0] != "match_end:"+string(stopBroadcasterError) {
		t.Errorf("controls at give-up = %v, want [match_end:%s]", got, stopBroadcasterError)
	}
}

// C1: the run loop announces match_end(cancelled) for a match that is idle
// on failures when discovery drops it, so the ingest still gets exactly one
// end for the match.
func TestPoller_CancelWhileIdleAnnouncesEnd(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess", "playing", 2), 200)
	cfg := testConfig("http://unused", fb.port(t))
	cfg.PollInterval = time.Millisecond
	cfg.IdleInterval = time.Millisecond
	cfg.IdleGiveUp = time.Minute
	stats := &bridgeStats{}
	sender := unstartedSender(t, stats)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := DiscoveredMatch{MatchID: "m", BroadcasterIP: "127.0.0.1"}
	p := newMatchPoller(m, cfg, sender, stats, &frameEpoch{}, cancel, testLogger())

	done := make(chan struct{})
	go func() { p.run(ctx); close(done) }()
	if !waitFor(2*time.Second, func() bool { return stats.TotalFramesMapped.Load() > 0 }) {
		t.Fatal("no frames mapped")
	}
	fb.set(`{"error":"x"}`, 500)
	if !waitFor(2*time.Second, func() bool { return p.currentState() == stateIdleError }) {
		t.Fatal("poller never went idle")
	}
	cancel()
	<-done
	got := queuedControls(t, sender)
	if len(got) != 2 || got[0] != "match_start:" || got[1] != "match_end:"+string(stopCancelled) {
		t.Errorf("controls = %v, want [match_start: match_end:%s]", got, stopCancelled)
	}
}

// L1 (fix pass 2): --session-check off means the ids are not compared at
// all: no mismatch counter, no error log. warn counts and continues; strict
// stops the poller.
func TestPoller_SessionCheckOffDoesNotCompare(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("other-session", "playing", 2), 200)
	m := DiscoveredMatch{MatchID: "nakama-match", BroadcasterIP: "127.0.0.1"}

	for _, tc := range []struct {
		mode       string
		keepGoing  bool
		mismatches int64
		logged     bool
	}{
		{sessionCheckOff, true, 0, false},
		{sessionCheckWarn, true, 1, true},
		{sessionCheckStrict, false, 1, true},
		{"", false, 1, true}, // unset = strict
	} {
		cfg := testConfig("http://unused", fb.port(t))
		cfg.SessionCheck = tc.mode
		stats := &bridgeStats{}
		p := newMatchPoller(m, cfg, nil, stats, &frameEpoch{}, func() {}, testLogger())
		ok, _ := p.poll(context.Background())
		if ok != tc.keepGoing || stats.SessionMismatches.Load() != tc.mismatches || p.mismatchLogged != tc.logged {
			t.Errorf("mode %q: keepGoing=%v mismatches=%d logged=%v, want %v/%d/%v",
				tc.mode, ok, stats.SessionMismatches.Load(), p.mismatchLogged, tc.keepGoing, tc.mismatches, tc.logged)
		}
	}
}

// L2 (fix pass 2): post_match reached from an idle failure state keeps the
// idle clock (and does not count another idle transition), so a lobby that
// flaps between errors and a stuck post_match still hits --idle-give-up.
func TestPoller_PostMatchFromIdleKeepsIdleClock(t *testing.T) {
	fb := newFakeBroadcaster(t, fakeSessionJSON("sess", "playing", 2), 200)
	cfg := testConfig("http://unused", fb.port(t))
	cfg.IdleGiveUp = 40 * time.Millisecond
	stats := &bridgeStats{}
	sender := unstartedSender(t, stats)
	m := DiscoveredMatch{MatchID: "m", BroadcasterIP: "127.0.0.1"}
	p := newMatchPoller(m, cfg, sender, stats, &frameEpoch{}, func() {}, testLogger())

	pollN(t, p, 2)
	fb.set(`{"error":"x"}`, 500)
	pollN(t, p, failureIdleThreshold)
	if p.state != stateIdleError {
		t.Fatalf("state = %s, want idle_error", p.state)
	}
	idleSince := p.idleSince
	queuedControls(t, sender) // discard match_start

	deadline := time.Now().Add(2 * time.Second)
	var gaveUp bool
	for !gaveUp && time.Now().Before(deadline) {
		// Flap: a few post_match polls, then a failure, each swing far
		// shorter than --idle-give-up.
		fb.set(fakeSessionJSON("sess", "post_match", 2), 200)
		for i := 0; i < 3 && !gaveUp; i++ {
			ok, reason := p.poll(context.Background())
			if !ok {
				if reason != stopPostMatch {
					t.Fatalf("stop reason = %q, want %q", reason, stopPostMatch)
				}
				gaveUp = true
				break
			}
			if p.state != stateIdlePostMatch {
				t.Fatalf("state = %s, want idle_post_match", p.state)
			}
			if !p.idleSince.Equal(idleSince) {
				t.Fatalf("idleSince was reset on the idle_error -> post_match swing")
			}
		}
		if gaveUp {
			break
		}
		fb.set(`{"error":"x"}`, 500)
		if ok, _ := p.poll(context.Background()); !ok {
			gaveUp = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !gaveUp {
		t.Fatal("flapping lobby never hit --idle-give-up")
	}
	if stats.PollerIdleTransitions.Load() != 1 {
		t.Errorf("idle transitions = %d, want 1 (active -> idle only)", stats.PollerIdleTransitions.Load())
	}
	if got := queuedControls(t, sender); len(got) != 1 || got[0] != "match_end:"+string(stopPostMatch) {
		t.Errorf("controls = %v, want exactly one match_end:%s", got, stopPostMatch)
	}
}
