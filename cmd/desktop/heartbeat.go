package main

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// The app window is an ordinary Edge/Chrome --app window: the server cannot
// see it close. The page therefore sends POST api/heartbeat every few seconds
// and a final navigator.sendBeacon('api/heartbeat?leaving=1') on pagehide, and
// the server exits on its own once the page has gone quiet. Without this a
// GUI-subsystem build (no console to close) would leave an invisible process
// behind every time the window is closed.
//
// Guard rails, all unit-tested in heartbeat_test.go:
//   - nothing happens until a first heartbeat was seen, so --no-browser runs,
//     scripts and an older page that never beats keep today's behaviour;
//   - the server never exits while work is running (an analysis, recovery or
//     watch-folder import, an update install, or any other API request such as
//     a backup, restore or support bundle), and the silence clock restarts when
//     that work ends;
//   - any request from the page counts as a sign of life and cancels a
//     "leaving" notice, so a reload (pagehide, then the new page) never exits;
//   - --no-auto-exit disables the watchdog entirely.
type heartbeatTimings struct {
	// Silence is how long the page may stay quiet before the server exits.
	Silence time.Duration
	// LeavingSilence applies instead when the last signal was leaving=1.
	LeavingSilence time.Duration
	// Poll is how often the watchdog re-evaluates.
	Poll time.Duration
}

// defaultHeartbeatTimings: the page beats every 5 s. Browsers throttle the
// timers of a minimized or fully covered window to at most one wake-up per
// minute after it has been hidden for five minutes, so the ordinary silence
// limit must comfortably exceed 60 s or minimizing the app would quit it. A
// closed window announces itself (leaving=1) and is honoured after 8 s.
var defaultHeartbeatTimings = heartbeatTimings{Silence: 90 * time.Second, LeavingSilence: 8 * time.Second, Poll: time.Second}

type heartbeatWatchdog struct {
	mu       sync.Mutex
	timings  heartbeatTimings
	now      func() time.Time
	disabled bool
	// busy reports background work that has no request in flight.
	busy func() bool

	seen     bool      // a real heartbeat arrived at least once
	last     time.Time // last sign of life (heartbeat, request, or end of work)
	leaving  bool      // the last heartbeat said the page is going away
	inflight int       // API requests currently being served (heartbeats excluded)
}

func newHeartbeatWatchdog(timings heartbeatTimings, now func() time.Time, busy func() bool) *heartbeatWatchdog {
	if now == nil {
		now = time.Now
	}
	if busy == nil {
		busy = func() bool { return false }
	}
	return &heartbeatWatchdog{timings: timings, now: now, busy: busy}
}

// beat records a heartbeat from the page.
func (w *heartbeatWatchdog) beat(leaving bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen, w.last, w.leaving = true, w.now(), leaving
}

// requestStarted and requestFinished bracket every other API request.
func (w *heartbeatWatchdog) requestStarted() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inflight++
	w.last, w.leaving = w.now(), false
}

func (w *heartbeatWatchdog) requestFinished() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inflight > 0 {
		w.inflight--
	}
	w.last = w.now()
}

// shouldExit is the whole policy; run only polls it.
func (w *heartbeatWatchdog) shouldExit() bool {
	busy := w.busy() // outside the lock: it takes the server's own locks
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.disabled || !w.seen {
		return false
	}
	now := w.now()
	if busy || w.inflight > 0 {
		// Work counts as life: the silence clock starts over when it ends, so a
		// long analysis that outlives the window still finishes and is stored.
		w.last = now
		return false
	}
	limit := w.timings.Silence
	if w.leaving {
		limit = w.timings.LeavingSilence
	}
	return now.Sub(w.last) >= limit
}

// run polls until done is closed or the policy says exit, then calls exit once.
func (w *heartbeatWatchdog) run(done <-chan struct{}, exit func()) {
	ticker := time.NewTicker(w.timings.Poll)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if w.shouldExit() {
				exit()
				return
			}
		}
	}
}

// track wraps the routed handler so every request other than the heartbeat
// itself counts as activity and as work in flight.
func (w *heartbeatWatchdog) track(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/heartbeat") {
			next.ServeHTTP(rw, r)
			return
		}
		w.requestStarted()
		defer w.requestFinished()
		next.ServeHTTP(rw, r)
	})
}

// handleHeartbeat is POST api/heartbeat[?leaving=1] -> 204 No Content.
func (s *server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	s.heartbeat.beat(r.URL.Query().Get("leaving") == "1")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// backgroundWorkActive reports work that must never be interrupted by the
// auto-exit and that is not tied to a request in flight: an upload analysis,
// crash recovery, a watch-folder import (all hold analyzeMu), or an update
// install that has been handed to the helper.
func (s *server) backgroundWorkActive() bool {
	if s.analysisActive() || s.runtime.updateInProgress() {
		return true
	}
	s.runtime.mu.Lock()
	recovering, scanning := s.runtime.recovering, s.runtime.watchStatus == "scanning"
	s.runtime.mu.Unlock()
	if recovering || scanning {
		return true
	}
	if !s.analyzeMu.TryLock() {
		return true
	}
	s.analyzeMu.Unlock()
	return false
}

// startAutoExit starts the watchdog; when it fires the app quits exactly as
// the in-app Quit button does.
func (s *server) startAutoExit() {
	go s.heartbeat.run(s.quit, func() {
		s.engine.Logger().Info("the app window has gone quiet; shutting down")
		s.quitOnce.Do(func() { close(s.quit) })
	})
}
