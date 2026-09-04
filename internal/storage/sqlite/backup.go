package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaintenanceResult describes a read-only integrity check followed by a WAL
// checkpoint. The checkpoint copies committed WAL pages into the main database
// and truncates the sidecar; it does not delete replay evidence.
type MaintenanceResult struct {
	Integrity       string `json:"integrity"`
	BusyConnections int    `json:"busy_connections"`
	WALFrames       int    `json:"wal_frames"`
	Checkpointed    int    `json:"checkpointed_frames"`
}

// ErrBackupExists is returned instead of overwriting an existing backup.
// Backups are evidence-preservation artifacts; silent replacement would make
// it impossible to prove which database snapshot was reviewed.
var ErrBackupExists = errors.New("sqlite: backup destination already exists")

// Backup writes a transactionally consistent, compact copy of the live
// database using SQLite's VACUUM INTO operation. It includes committed WAL
// content and does not require stopping ingestion, although SQLite serializes
// the operation on this store's single connection. The destination must not
// already exist.
func (s *Store) Backup(ctx context.Context, destination string) (err error) {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return fmt.Errorf("sqlite: backup destination is required")
	}
	abs, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("sqlite: resolving backup destination: %w", err)
	}
	if info, statErr := os.Stat(abs); statErr == nil {
		return fmt.Errorf("%w: %s (%d bytes)", ErrBackupExists, abs, info.Size())
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("sqlite: checking backup destination: %w", statErr)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return fmt.Errorf("sqlite: creating backup directory: %w", err)
	}

	// VACUUM INTO accepts a bound filename expression. Binding instead of
	// interpolating keeps paths containing quotes from becoming SQL.
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, abs); err != nil {
		_ = os.Remove(abs) // only a partial file can exist: pre-existence was refused
		return fmt.Errorf("sqlite: creating backup: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(abs)
		}
	}()

	// Verify the copy independently before reporting success. This is a read-
	// only connection and does not run migrations or mutate the snapshot.
	return VerifyDatabase(ctx, abs)
}

// VerifyDatabase opens path read-only and runs SQLite's quick integrity
// check. It is shared by backup creation and the desktop restore wizard.
func VerifyDatabase(ctx context.Context, path string) error {
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_busy_timeout=5000"
	copyDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return fmt.Errorf("sqlite: opening backup for verification: %w", err)
	}
	defer copyDB.Close()
	var result string
	if err := copyDB.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		return fmt.Errorf("sqlite: verifying backup: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("sqlite: database integrity check returned %q", result)
	}
	return nil
}

// CheckAndCheckpoint verifies the live database and then requests a TRUNCATE
// checkpoint. It is intentionally separate from VACUUM: checkpointing is a
// quick, space-safe maintenance operation and never needs a second database-
// sized temporary file.
func (s *Store) CheckAndCheckpoint(ctx context.Context) (MaintenanceResult, error) {
	var out MaintenanceResult
	if err := s.db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&out.Integrity); err != nil {
		return out, fmt.Errorf("sqlite: checking live database: %w", err)
	}
	if out.Integrity != "ok" {
		return out, fmt.Errorf("sqlite: live database integrity check returned %q", out.Integrity)
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(
		&out.BusyConnections, &out.WALFrames, &out.Checkpointed,
	); err != nil {
		return out, fmt.Errorf("sqlite: checkpointing live database: %w", err)
	}
	if out.BusyConnections != 0 {
		return out, fmt.Errorf("sqlite: checkpoint could not finish because the database is busy")
	}
	return out, nil
}
