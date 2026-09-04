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
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// applyPendingRestore performs a user-scheduled restore before SQLite opens.
// The replaced database and any sidecars are renamed, not deleted, so even a
// power loss during restore leaves a recoverable copy.
func applyPendingRestore(dbPath string) error {
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
	tmp := target + ".restore-tmp"
	if err := copyRestoreFile(source, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := sqlite.VerifyDatabase(context.Background(), tmp); err != nil {
		return fmt.Errorf("verifying staged restore: %w", err)
	}
	suffix := ".pre-restore-" + time.Now().UTC().Format("20060102-150405")
	recovery := target + suffix
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, recovery); err != nil {
			return fmt.Errorf("preserving current database: %w", err)
		}
	}
	for _, sidecar := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(target + sidecar); err == nil {
			_ = os.Rename(target+sidecar, recovery+sidecar)
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		if _, recoveryErr := os.Stat(recovery); recoveryErr == nil {
			_ = os.Rename(recovery, target)
		}
		return fmt.Errorf("installing restored database: %w", err)
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
	if errors.Is(err, os.ErrExist) {
		if removeErr := os.Remove(destination); removeErr != nil {
			return fmt.Errorf("removing stale restore staging file: %w", removeErr)
		}
		out, err = os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
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
