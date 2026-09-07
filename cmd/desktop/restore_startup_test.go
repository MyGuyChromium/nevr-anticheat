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
