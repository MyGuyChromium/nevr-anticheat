package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func TestApplyPendingRestorePreservesAndReplacesDatabase(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "desktop.db")
	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TABLE restore_marker (value TEXT); INSERT INTO restore_marker VALUES ('backup')`); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(backupDir, "known-good.db")
	if err := store.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE restore_marker SET value = 'current'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(restoreRequest{Source: backup, Target: target})
	if err := os.WriteFile(restoreRequestPath(target), request, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyPendingRestore(target); err != nil {
		t.Fatal(err)
	}
	restored, err := sqlite.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var value string
	if err := restored.DB().QueryRow(`SELECT value FROM restore_marker`).Scan(&value); err != nil || value != "backup" {
		t.Fatalf("restored marker=%q err=%v", value, err)
	}
	if files, _ := filepath.Glob(target + ".pre-restore-*"); len(files) == 0 {
		t.Fatal("the replaced database was not preserved")
	}
	if _, err := os.Stat(restoreRequestPath(target)); !os.IsNotExist(err) {
		t.Fatalf("restore request still exists: %v", err)
	}
}

func preparePendingRestore(t *testing.T) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "desktop.db")
	backup := filepath.Join(dir, "backups", "known-good.db")
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Backup(context.Background(), backup); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// These stand in for existing sidecars; no SQLite connection opens them.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(target+suffix, []byte("original"+suffix), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	original := make(map[string]string)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(target + suffix)
		if err != nil {
			t.Fatal(err)
		}
		original[suffix] = string(data)
	}
	request, err := json.Marshal(restoreRequest{Source: backup, Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restoreRequestPath(target), request, 0o600); err != nil {
		t.Fatal(err)
	}
	return target, original
}

func TestApplyPendingRestoreRollsBackEveryMovedFile(t *testing.T) {
	for _, failAt := range []string{"database", "wal", "shm", "install"} {
		t.Run(failAt, func(t *testing.T) {
			target, original := preparePendingRestore(t)
			injected := errors.New("injected locked file")
			err := applyPendingRestoreWithRename(target, func(from, to string) error {
				if (failAt == "database" && from == target) || (failAt == "wal" && from == target+"-wal") ||
					(failAt == "shm" && from == target+"-shm") || (failAt == "install" && strings.Contains(from, ".nevr-restore-stage-")) {
					return injected
				}
				return os.Rename(from, to)
			})
			if !errors.Is(err, injected) {
				t.Fatalf("restore error = %v, want injected failure", err)
			}
			for suffix, want := range original {
				data, err := os.ReadFile(target + suffix)
				if err != nil || string(data) != want {
					t.Fatalf("original %q changed after failed restore: %v", suffix, err)
				}
			}
			if _, err := os.Stat(restoreRequestPath(target)); err != nil {
				t.Fatalf("failed restore discarded its request: %v", err)
			}
			if stages, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".nevr-restore-stage-*")); len(stages) != 0 {
				t.Fatalf("staging files leaked: %v", stages)
			}
		})
	}
}

func TestApplyPendingRestoreReportsRollbackFailureAndRetainsRecovery(t *testing.T) {
	target, original := preparePendingRestore(t)
	injectedInstall, injectedRollback := errors.New("install failed"), errors.New("rollback locked")
	err := applyPendingRestoreWithRename(target, func(from, to string) error {
		if strings.Contains(from, ".nevr-restore-stage-") {
			return injectedInstall
		}
		if to == target+"-wal" {
			return injectedRollback
		}
		return os.Rename(from, to)
	})
	if !errors.Is(err, injectedInstall) || !errors.Is(err, injectedRollback) {
		t.Fatalf("restore must report installation and rollback failures: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(target+".pre-restore-*", filepath.Base(target)+"-wal"))
	if len(files) != 1 {
		t.Fatalf("original WAL recovery file missing: %v", files)
	}
	data, readErr := os.ReadFile(files[0])
	if readErr != nil || string(data) != original["-wal"] || !strings.Contains(err.Error(), files[0]) {
		t.Fatalf("recovery was not preserved and identified: %v; %v", readErr, err)
	}
}

func TestApplyPendingRestoreKeepsDistinctRecoverySets(t *testing.T) {
	target, original := preparePendingRestore(t)
	request, err := os.ReadFile(restoreRequestPath(target))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(restoreRequestPath(target), request, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := applyPendingRestore(target); err != nil {
			t.Fatal(err)
		}
	}
	dirs, _ := filepath.Glob(target + ".pre-restore-*")
	if len(dirs) != 2 {
		t.Fatalf("successive restores must preserve distinct originals: %v", dirs)
	}
	walFiles, _ := filepath.Glob(filepath.Join(target+".pre-restore-*", filepath.Base(target)+"-wal"))
	if len(walFiles) != 1 {
		t.Fatalf("original WAL not preserved: %v", walFiles)
	}
	data, err := os.ReadFile(walFiles[0])
	if err != nil || string(data) != original["-wal"] {
		t.Fatalf("original recovery set changed: %v", err)
	}
}

// A scheduled restore whose backup was deleted used to fail every later
// launch: the request stayed and main returned the error before the store
// opened. Mutation: make resolvePendingRestore return the error instead of
// recording it and the first assertion fails.
func TestFailedScheduledRestoreIsCancelledAndStartupContinues(t *testing.T) {
	target, original := preparePendingRestore(t)
	var request restoreRequest
	doc, _ := os.ReadFile(restoreRequestPath(target))
	if err := json.Unmarshal(doc, &request); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(request.Source); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(filepath.Dir(target), sourceIndexFileName)
	if err := os.WriteFile(index, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	failure, err := resolvePendingRestore(target)
	if err != nil || failure == nil || !strings.Contains(failure.Error, "verifying restore source") || failure.Source != request.Source {
		t.Fatalf("launch 1: failure=%+v err=%v, want a recorded failure and no startup error", failure, err)
	}
	if _, err := os.Stat(restoreRequestPath(target)); !os.IsNotExist(err) {
		t.Fatalf("the failed request is still pending and would fail the next launch too: %v", err)
	}
	for suffix, want := range original {
		if data, err := os.ReadFile(target + suffix); err != nil || string(data) != want {
			t.Fatalf("live database file %q changed: %v", suffix, err)
		}
	}
	if _, err := os.Stat(index); err != nil {
		t.Fatalf("a restore that did not happen dropped the analyzed-files index: %v", err)
	}
	if recorded := readRestoreFailure(target); recorded == nil || recorded.Error != failure.Error {
		t.Fatalf("failure record: %+v", recorded)
	}
	if failure, err := resolvePendingRestore(target); err != nil || failure != nil {
		t.Fatalf("launch 2: failure=%+v err=%v, want a normal start", failure, err)
	}
}

// Startup is refused only when the live database could not be put back.
func TestRestoreRollbackFailureStillStopsStartup(t *testing.T) {
	target, _ := preparePendingRestore(t)
	failure, err := resolvePendingRestoreWithRename(target, func(from, to string) error {
		if strings.Contains(from, ".nevr-restore-stage-") || to == target+"-wal" {
			return errors.New("locked")
		}
		return os.Rename(from, to)
	})
	if failure != nil || !errors.Is(err, errRestoreRollbackFailed) {
		t.Fatalf("failure=%+v err=%v, want a startup error", failure, err)
	}
	if _, statErr := os.Stat(restoreRequestPath(target)); statErr != nil {
		t.Fatalf("request dropped although the database needs attention: %v", statErr)
	}
}

func TestSetupReportsAndCancelsRestores(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	db := s.engine.Store().Path()
	failed, _ := json.Marshal(restoreFailure{Source: "gone.db", FailedAt: "now", Error: "verifying restore source: missing"})
	pending, _ := json.Marshal(restoreRequest{Source: "next.db", Target: db})
	if err := os.WriteFile(restoreFailedPath(db), failed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restoreRequestPath(db), pending, 0o600); err != nil {
		t.Fatal(err)
	}
	var setup struct {
		Pending *restoreRequest `json:"restore_pending"`
		Failed  *restoreFailure `json:"restore_failed"`
	}
	if resp := getJSON(t, base+"/api/setup", &setup); resp.StatusCode != 200 || setup.Pending == nil || setup.Pending.Source != "next.db" ||
		setup.Failed == nil || !strings.Contains(setup.Failed.Error, "missing") {
		t.Fatalf("setup: %+v", setup)
	}
	var out map[string]bool
	if resp := postAPI(t, base+"/api/maintenance/restore/cancel", nil, &out); resp.StatusCode != 200 || !out["cancelled_pending"] || !out["dismissed_failure"] {
		t.Fatalf("cancel: %d %+v", resp.StatusCode, out)
	}
	setup.Pending, setup.Failed = nil, nil
	if getJSON(t, base+"/api/setup", &setup); setup.Pending != nil || setup.Failed != nil {
		t.Fatalf("after cancel: %+v", setup)
	}
}
