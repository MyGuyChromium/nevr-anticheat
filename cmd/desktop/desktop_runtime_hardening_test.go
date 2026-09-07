package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stopRuntimeForTest(t *testing.T, s *server) {
	t.Helper()
	s.quitOnce.Do(func() { close(s.quit) })
	select {
	case <-s.runtime.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not stop")
	}
}

func TestDesktopSettingsFailureKeepsPreviousSettings(t *testing.T) {
	s, _ := newTestServer(t)
	stopRuntimeForTest(t, s)
	s.runtime.settings.WatchFolder = "previous-folder"
	s.runtime.settings.AutomaticUpdates = true
	s.runtime.settingsPath = t.TempDir() // Renaming a settings file onto a directory must fail.
	r := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"watch_folder":"changed","automatic_update_checks":false}`))
	w := httptest.NewRecorder()
	s.handleSaveSettings(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("save status = %d: %s", w.Code, w.Body.String())
	}
	if s.runtime.settings.WatchFolder != "previous-folder" || !s.runtime.settings.AutomaticUpdates {
		t.Fatalf("failed save changed active settings: %+v", s.runtime.settings)
	}
}

func TestDesktopSettingsRejectEmptyEnabledWatchFolder(t *testing.T) {
	s, _ := newTestServer(t)
	stopRuntimeForTest(t, s)
	r := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"watch_folder":"  ","watch_enabled":true}`))
	w := httptest.NewRecorder()
	s.handleSaveSettings(w, r)
	if w.Code != http.StatusBadRequest || s.runtime.settings.WatchEnabled {
		t.Fatalf("empty watch folder silently selected working directory: %d %+v", w.Code, s.runtime.settings)
	}
}

func TestWatchScanRetriesPersistenceFailure(t *testing.T) {
	s, _ := newTestServer(t)
	stopRuntimeForTest(t, s)
	watch := t.TempDir()
	path := filepath.Join(watch, "retry.echoreplay")
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	s.runtime.settings.WatchFolder = watch
	if _, err := s.engine.Store().DB().Exec(`CREATE TRIGGER reject_summary BEFORE INSERT ON match_summaries
		BEGIN SELECT RAISE(ABORT, 'summary storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if count, err := s.runtime.scanWatchFolder(context.Background()); err == nil || count != 0 {
		t.Fatalf("injected failed scan = %d, %v", count, err)
	}
	if s.runtime.settings.SeenFiles[path] != "" {
		t.Fatal("database failure permanently suppressed the unchanged replay")
	}
	if _, err := s.engine.Store().DB().Exec(`DROP TRIGGER reject_summary`); err != nil {
		t.Fatal(err)
	}
	if count, err := s.runtime.scanWatchFolder(context.Background()); err != nil || count != 1 {
		t.Fatalf("retry of unchanged replay = %d, %v", count, err)
	}
}

func TestRecoveryReportsPersistenceFailureAndRetainsReplay(t *testing.T) {
	s, _ := newTestServer(t)
	stopRuntimeForTest(t, s)
	path := filepath.Join(s.runtime.pendingDir, "retry.echoreplay")
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.engine.Store().DB().Exec(`CREATE TRIGGER reject_summary BEFORE INSERT ON match_summaries
		BEGIN SELECT RAISE(ABORT, 'summary storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	s.runtime.resumePending(context.Background())
	if !strings.Contains(s.runtime.recoveryErr, "summary storage unavailable") || s.runtime.recovered != 0 {
		t.Fatalf("failure hidden from recovery status: %q, recovered=%d", s.runtime.recoveryErr, s.runtime.recovered)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("incomplete recovery discarded replay: %v", err)
	}
	if _, err := s.engine.Store().DB().Exec(`DROP TRIGGER reject_summary`); err != nil {
		t.Fatal(err)
	}
	s.runtime.resumePending(context.Background())
	if s.runtime.recoveryErr != "" || s.runtime.recovered != 1 {
		t.Fatalf("recovery retry failed: %q, recovered=%d", s.runtime.recoveryErr, s.runtime.recovered)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful recovery retained consumed input: %v", err)
	}
	if info, err := os.Stat(s.runtime.pendingDir); err != nil || !info.IsDir() {
		t.Fatalf("recovery removed the upload queue root: %v", err)
	}
}

func TestWatchScanCancelsEmptyFolder(t *testing.T) {
	s, _ := newTestServer(t)
	stopRuntimeForTest(t, s)
	s.runtime.settings.WatchFolder = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if count, err := s.runtime.scanWatchFolder(ctx); !errors.Is(err, context.Canceled) || count != 0 {
		t.Fatalf("canceled walk reported success: count=%d err=%v", count, err)
	}
}

func TestSupportBundleRepeatedCreationUsesUniqueFiles(t *testing.T) {
	s, _ := newTestServer(t)
	stopRuntimeForTest(t, s)
	paths := make(map[string]bool)
	for i := 0; i < 3; i++ {
		path, err := s.createSupportBundle(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if paths[path] {
			t.Fatalf("support bundle was overwritten: %s", path)
		}
		paths[path] = true
	}
}
