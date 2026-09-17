package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// resumeAndWait runs crash recovery and returns only once no recovery pass is
// in flight, so callers can assert on the pending queue deterministically.
//
// The runtime's background loop calls resumePending by itself 750 ms after
// start, and resumePending returns immediately when another pass is already
// running. A test that calls it directly can therefore get an instant return
// while the background pass is still analysing the very file the test is about
// to stat. Recovery is correct whichever goroutine performs it; only the
// test's timing assumption was wrong.
//
// Order matters: wait for idle first (a pass that started before the caller
// finished writing its pending file may have listed the queue too early, or
// seen a partial file, which recovery retains for retry), then run a pass, then
// wait again (if the background loop won the race for that pass, it started
// after the file was complete, so it covers it). When the runtime is stopped
// this is exactly one direct resumePending call.
func resumeAndWait(t *testing.T, s *server) {
	t.Helper()
	waitRecoveryIdle(t, s)
	s.runtime.resumePending(context.Background())
	waitRecoveryIdle(t, s)
}

func waitRecoveryIdle(t *testing.T, s *server) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		s.runtime.mu.Lock()
		busy := s.runtime.recovering
		s.runtime.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("crash recovery did not finish within 30s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestResumeAndWaitSurvivesBackgroundRecoveryRace pins the collision that made
// the recovery assertions flaky: the pending file is in place before the
// background loop's start-up pass, the test waits until that pass is actually
// running, and only then asks for recovery. A bare resumePending call returns
// immediately at that point and the file is still on disk; resumeAndWait must
// not return until it has been consumed exactly once.
func TestResumeAndWaitSurvivesBackgroundRecoveryRace(t *testing.T) {
	s, _ := newTestServer(t)
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(s.runtime.pendingDir, "collision.echoreplay")
	if err := os.WriteFile(pending, data, 0o600); err != nil {
		t.Fatal(err)
	}
	collided := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		s.runtime.mu.Lock()
		collided = s.runtime.recovering
		s.runtime.mu.Unlock()
		if collided {
			break
		}
		if _, err := os.Stat(pending); os.IsNotExist(err) {
			break // the background pass already finished between two polls
		}
	}
	t.Logf("background recovery pass observed in flight: %v", collided)
	resumeAndWait(t, s)
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("recovered upload still exists: %v", err)
	}
	s.runtime.mu.Lock()
	recovered, recoveryErr := s.runtime.recovered, s.runtime.recoveryErr
	s.runtime.mu.Unlock()
	if recovered != 1 || recoveryErr != "" {
		t.Fatalf("recovery ran %d times, error %q; want exactly one clean recovery", recovered, recoveryErr)
	}
}
