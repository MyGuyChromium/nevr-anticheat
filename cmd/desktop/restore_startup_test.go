package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
