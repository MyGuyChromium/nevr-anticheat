package main

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var testHeartbeatTimings = heartbeatTimings{Silence: 25 * time.Second, LeavingSilence: 8 * time.Second, Poll: time.Millisecond}

func newTestWatchdog(busy *atomic.Bool) (*heartbeatWatchdog, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return newHeartbeatWatchdog(testHeartbeatTimings, clock.now, func() bool { return busy != nil && busy.Load() }), clock
}

func TestHeartbeatNeverExitsBeforeTheFirstBeat(t *testing.T) {
	w, clock := newTestWatchdog(nil)
	clock.advance(24 * time.Hour)
	if w.shouldExit() {
		t.Fatal("exited although no page ever sent a heartbeat (--no-browser, scripts, an older page)")
	}
	// Ordinary API traffic is not a heartbeat either: a script driving the API
	// must not arm the watchdog.
	w.requestStarted()
	w.requestFinished()
	clock.advance(24 * time.Hour)
	if w.shouldExit() {
		t.Fatal("API requests alone armed the auto-exit")
	}
}

func TestHeartbeatExitsAfterSilence(t *testing.T) {
	w, clock := newTestWatchdog(nil)
	w.beat(false)
	clock.advance(24 * time.Second)
	if w.shouldExit() {
		t.Fatal("exited before the silence limit")
	}
	w.beat(false)
	clock.advance(24 * time.Second)
	if w.shouldExit() {
		t.Fatal("a fresh heartbeat did not restart the silence clock")
	}
	clock.advance(time.Second)
	if !w.shouldExit() {
		t.Fatal("did not exit after 25 s of silence")
	}
}

func TestHeartbeatLeavingShortensTheWait(t *testing.T) {
	w, clock := newTestWatchdog(nil)
	w.beat(false)
	w.beat(true)
	clock.advance(7 * time.Second)
	if w.shouldExit() {
		t.Fatal("exited before the leaving limit")
	}
	clock.advance(time.Second)
	if !w.shouldExit() {
		t.Fatal("did not exit 8 s after leaving=1")
	}
}

// A reload is pagehide (leaving=1) followed by the new page: the new page's
// first request or heartbeat must cancel the short wait.
func TestHeartbeatReloadCancelsLeaving(t *testing.T) {
	for name, comeBack := range map[string]func(*heartbeatWatchdog){
		"heartbeat": func(w *heartbeatWatchdog) { w.beat(false) },
		"request":   func(w *heartbeatWatchdog) { w.requestStarted(); w.requestFinished() },
	} {
		w, clock := newTestWatchdog(nil)
		w.beat(true)
		clock.advance(3 * time.Second)
		comeBack(w)
		clock.advance(20 * time.Second)
		if w.shouldExit() {
			t.Fatalf("%s: a reloaded page was treated as closed", name)
		}
		clock.advance(5 * time.Second)
		if !w.shouldExit() {
			t.Fatalf("%s: ordinary silence limit no longer applies", name)
		}
	}
}

func TestHeartbeatNeverExitsDuringWork(t *testing.T) {
	var busy atomic.Bool
	w, clock := newTestWatchdog(&busy)
	w.beat(true) // the window was closed...
	busy.Store(true)
	clock.advance(3 * time.Hour) // ...while a long analysis kept running
	if w.shouldExit() {
		t.Fatal("exited during an analysis")
	}
	busy.Store(false)
	// The end of the work restarts the clock; it does not exit on the spot.
	clock.advance(7 * time.Second)
	if w.shouldExit() {
		t.Fatal("exited immediately after the work ended")
	}
	clock.advance(time.Second)
	if !w.shouldExit() {
		t.Fatal("did not exit once the work was done and the page stayed gone")
	}

	// A request in flight (backup, restore, support bundle, upload) is work too.
	w, clock = newTestWatchdog(nil)
	w.beat(false)
	w.requestStarted()
	clock.advance(time.Hour)
	if w.shouldExit() {
		t.Fatal("exited while a request was in flight")
	}
	w.requestFinished()
	clock.advance(25 * time.Second)
	if !w.shouldExit() {
		t.Fatal("did not exit after the request finished and the page stayed silent")
	}
}

func TestHeartbeatDisabled(t *testing.T) {
	w, clock := newTestWatchdog(nil)
	w.disabled = true
	w.beat(true)
	clock.advance(24 * time.Hour)
	if w.shouldExit() {
		t.Fatal("a disabled watchdog exited")
	}
}

func TestHeartbeatRunCallsExitOnce(t *testing.T) {
	w, clock := newTestWatchdog(nil)
	w.beat(true)
	clock.advance(time.Minute)
	exited := make(chan struct{}, 2)
	finished := make(chan struct{})
	go func() { w.run(make(chan struct{}), func() { exited <- struct{}{} }); close(finished) }()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("run never called exit")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after exit")
	}
	if len(exited) != 0 {
		t.Fatal("exit was called more than once")
	}

	// Closing done stops a watchdog that has nothing to do.
	w, _ = newTestWatchdog(nil)
	done := make(chan struct{})
	finished = make(chan struct{})
	go func() { w.run(done, func() { t.Error("exit called without a heartbeat") }); close(finished) }()
	close(done)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("run ignored done")
	}
}

func TestHeartbeatEndpointAndAutoExit(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s.heartbeat = newHeartbeatWatchdog(testHeartbeatTimings, clock.now, s.backgroundWorkActive)

	post := func(path string) *http.Response {
		t.Helper()
		resp, err := http.Post(base+path, "text/plain", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	if resp, err := http.Get(base + "/api/heartbeat"); err != nil || resp.StatusCode == http.StatusNoContent {
		t.Fatalf("GET heartbeat must not count as a beat: %v %v", resp, err)
	}
	if s.heartbeat.shouldExit() {
		t.Fatal("armed before a heartbeat")
	}
	if resp := post("/api/heartbeat"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat status=%d, want 204", resp.StatusCode)
	}
	if resp := post("/api/heartbeat?leaving=1"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("leaving heartbeat status=%d, want 204", resp.StatusCode)
	}
	// A cross-site page must not be able to keep the app alive or end it.
	req, _ := http.NewRequest(http.MethodPost, base+"/api/heartbeat?leaving=1", nil)
	req.Header.Set("Origin", "https://example.invalid")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin heartbeat: %v %v", resp, err)
	}

	// The window is gone, but an analysis is running: no exit.
	_, id := s.beginAnalysis()
	clock.advance(time.Hour)
	if s.heartbeat.shouldExit() {
		t.Fatal("exited during an analysis")
	}
	s.finishAnalysis(id)
	s.startAutoExit()
	clock.advance(9 * time.Second)
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not quit after the window left and the work ended")
	}
}
