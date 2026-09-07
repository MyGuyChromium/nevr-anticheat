package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// applyPendingRestore performs a user-scheduled restore before SQLite opens.
// The replaced database and any sidecars are renamed, not deleted, so even a
// power loss during restore leaves a recoverable copy.
func applyPendingRestore(dbPath string) error {
	return applyPendingRestoreWithRename(dbPath, os.Rename)
}

func applyPendingRestoreWithRename(dbPath string, rename func(string, string) error) error {
	requestPath := restoreRequestPath(dbPath)
	doc, err := os.ReadFile(requestPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var request restoreRequest
	if err := json.Unmarshal(doc, &request); err != nil {
		return fmt.Errorf("reading restore request: %w", err)
	}
	target, err := filepath.Abs(dbPath)
	if err != nil {
		return err
	}
	requestedTarget, err := filepath.Abs(request.Target)
	if err != nil || !strings.EqualFold(filepath.Clean(requestedTarget), filepath.Clean(target)) {
		return fmt.Errorf("restore target does not match the configured database")
	}
	backupDir, _ := filepath.Abs(filepath.Join(filepath.Dir(target), "backups"))
	source, err := filepath.Abs(request.Source)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(backupDir, source)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("restore source is outside the backup directory")
	}
	if err := sqlite.VerifyDatabase(context.Background(), source); err != nil {
		return fmt.Errorf("verifying restore source: %w", err)
	}
	stageDir, err := os.MkdirTemp(filepath.Dir(target), ".nevr-restore-stage-*")
	if err != nil {
		return fmt.Errorf("creating restore staging directory: %w", err)
	}
	defer os.Remove(stageDir)
	tmp := filepath.Join(stageDir, filepath.Base(target))
	defer os.Remove(tmp)
	if err := copyRestoreFile(source, tmp); err != nil {
		return err
	}
	if err := sqlite.VerifyDatabase(context.Background(), tmp); err != nil {
		return fmt.Errorf("verifying staged restore: %w", err)
	}
	// Keep each recovery set together in an exclusively-created directory.
	// Seconds-resolution names could overwrite an earlier recovery copy.
	recoveryDir, err := os.MkdirTemp(filepath.Dir(target), filepath.Base(target)+".pre-restore-*")
	if err != nil {
		return fmt.Errorf("creating restore recovery directory: %w", err)
	}
	defer os.Remove(recoveryDir) // Removes only an empty directory, never recovery data.
	recovery := filepath.Join(recoveryDir, filepath.Base(target))
	var moved []string
	rollback := func(cause error) error {
		for i := len(moved) - 1; i >= 0; i-- {
			suffix := moved[i]
			if err := rename(recovery+suffix, target+suffix); err != nil {
				cause = errors.Join(cause, fmt.Errorf("restoring original database file %s (preserved at %s): %w", target+suffix, recovery+suffix, err))
			}
		}
		return cause
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Lstat(target + suffix); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return rollback(fmt.Errorf("inspecting current database file %s: %w", target+suffix, err))
		}
		if err := rename(target+suffix, recovery+suffix); err != nil {
			return rollback(fmt.Errorf("preserving current database file %s: %w", target+suffix, err))
		}
		moved = append(moved, suffix)
	}
	if err := rename(tmp, target); err != nil {
		return rollback(fmt.Errorf("installing restored database: %w", err))
	}
	if err := os.Remove(requestPath); err != nil {
		return fmt.Errorf("restore completed but request cleanup failed: %w", err)
	}
	return nil
}

func copyRestoreFile(source, destination string) (err error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
	}()
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	return err
}
