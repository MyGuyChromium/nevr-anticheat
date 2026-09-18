package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	return applyPendingRestoreWithRename(dbPath, os.Rename)
}

// errRestoreRollbackFailed marks a restore that moved live database files away
// and could not put every one of them back. Only then is the live database not
// what it was before the restore started.
var errRestoreRollbackFailed = errors.New("the original database files could not all be put back")

// restoreFailure is the record of a scheduled restore that could not be
// applied. It replaces the request, so the failure happens once: the app
// starts on the untouched database and shows this instead of refusing to
// start at every launch.
type restoreFailure struct {
	Source      string `json:"source"`
	RequestedAt string `json:"requested_at,omitempty"`
	FailedAt    string `json:"failed_at"`
	Error       string `json:"error"`
}

func restoreFailedPath(db string) string { return db + ".restore-request.failed.json" }

// resolvePendingRestore is what startup calls. A restore that fails while the
// live database is still exactly as it was (the backup was deleted or moved,
// the folder was renamed, the disk is full, a file is locked) is cancelled:
// the request becomes a failure record and startup continues. An error is
// returned only when the live database itself could not be put back, which is
// the one case where opening it would be wrong.
func resolvePendingRestore(dbPath string) (*restoreFailure, error) {
	return resolvePendingRestoreWithRename(dbPath, os.Rename)
}

func resolvePendingRestoreWithRename(dbPath string, rename func(string, string) error) (*restoreFailure, error) {
	requestPath := restoreRequestPath(dbPath)
	if _, err := os.Lstat(requestPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	err := applyPendingRestoreWithRename(dbPath, rename)
	if err == nil {
		// The database is another one now: what was remembered about analyzed
		// files described the replaced database.
		_ = os.Remove(filepath.Join(filepath.Dir(dbPath), sourceIndexFileName))
		_ = os.Remove(restoreFailedPath(dbPath))
		return nil, nil
	}
	if errors.Is(err, errRestoreRollbackFailed) {
		return nil, err
	}
	failure := &restoreFailure{FailedAt: fmtTime(time.Now()), Error: err.Error()}
	var request restoreRequest
	if doc, readErr := os.ReadFile(requestPath); readErr == nil && json.Unmarshal(doc, &request) == nil {
		failure.Source, failure.RequestedAt = request.Source, request.RequestedAt
	}
	doc, _ := json.MarshalIndent(failure, "", "  ")
	if writeErr := os.WriteFile(restoreFailedPath(dbPath), doc, 0o600); writeErr != nil {
		// Without a record the failure would be silent; keep the old behaviour.
		return nil, errors.Join(err, fmt.Errorf("recording the failed restore: %w", writeErr))
	}
	if removeErr := os.Remove(requestPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return nil, errors.Join(err, fmt.Errorf("cancelling the failed restore request: %w", removeErr))
	}
	return failure, nil
}

// readRestoreFailure returns the record of the last failed restore, if any.
func readRestoreFailure(dbPath string) *restoreFailure {
	doc, err := os.ReadFile(restoreFailedPath(dbPath))
	if err != nil {
		return nil
	}
	var failure restoreFailure
	if json.Unmarshal(doc, &failure) != nil || failure.Error == "" {
		return nil
	}
	return &failure
}

// readPendingRestore returns the restore scheduled for the next launch, if any.
func readPendingRestore(dbPath string) *restoreRequest {
	doc, err := os.ReadFile(restoreRequestPath(dbPath))
	if err != nil {
		return nil
	}
	var request restoreRequest
	if json.Unmarshal(doc, &request) != nil {
		return &restoreRequest{}
	}
	return &request
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
	if err := sqlite.VerifyEvidenceDatabase(context.Background(), source); err != nil {
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
	if err := sqlite.VerifyEvidenceDatabase(context.Background(), tmp); err != nil {
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
				cause = errors.Join(cause, errRestoreRollbackFailed, fmt.Errorf("restoring original database file %s (preserved at %s): %w", target+suffix, recovery+suffix, err))
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

// handleCancelRestore cancels a restore scheduled for the next launch and
// dismisses the record of a restore that failed at the last one. It never
// touches a database or a backup.
func (s *server) handleCancelRestore(w http.ResponseWriter, _ *http.Request) {
	db := s.engine.Store().Path()
	cancelled, dismissed := false, false
	for path, done := range map[string]*bool{restoreRequestPath(db): &cancelled, restoreFailedPath(db): &dismissed} {
		err := os.Remove(path)
		if err == nil {
			*done = true
		} else if !errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusInternalServerError, "cancelling restore: %v", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "cancelled_pending": cancelled, "dismissed_failure": dismissed})
}
