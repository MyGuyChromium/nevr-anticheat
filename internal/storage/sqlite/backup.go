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
	dsn := sqliteFileURI(path) + "?mode=ro&_busy_timeout=5000"
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

// ErrNotEvidenceDatabase is matched (errors.Is) by the error
// VerifyEvidenceDatabase returns for a healthy SQLite file that is not a
// NEVR-Anticheat evidence database.
var ErrNotEvidenceDatabase = errors.New("sqlite: not a NEVR-Anticheat evidence database")

// evidenceCoreTables exist in every NEVR-Anticheat database since schema
// version 1. requiredTables cannot be used here: it is the CURRENT schema
// surface, and a backup written by an older build legitimately lacks the tables
// later migrations add (NewStore migrates it after the restore).
var evidenceCoreTables = []string{"detection_events", "match_summaries", "review_cases"}

// VerifyEvidenceDatabase is VerifyDatabase plus proof that the file is a NEVR
// evidence database this build can open. It is the check for anything that is
// about to REPLACE the live database.
//
// An integrity check alone is not enough for that. A zero-byte file is a valid,
// empty SQLite database, and so is any other application's database; both pass
// PRAGMA quick_check, and restoring either would leave the moderator with an
// empty library. The file must therefore record at least one applied migration
// and hold the version-1 core tables. A database written by a NEWER build is
// refused too: NewStore would refuse to open it after the swap, which is worse
// than refusing the restore while the current database is still in place.
//
// The file is opened read-only; nothing is migrated or written.
func VerifyEvidenceDatabase(ctx context.Context, path string) error {
	if err := VerifyDatabase(ctx, path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite3", sqliteFileURI(path)+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return fmt.Errorf("sqlite: opening database for schema verification: %w", err)
	}
	defer db.Close()
	hasTable := func(name string) (bool, error) {
		var n int
		err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
		return n > 0, err
	}
	ok, err := hasTable("schema_migrations")
	if err != nil {
		return fmt.Errorf("sqlite: reading database schema: %w", err)
	}
	if !ok {
		return fmt.Errorf("%w: it records no NEVR schema version (an empty file or another program's database)", ErrNotEvidenceDatabase)
	}
	var applied sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&applied); err != nil {
		return fmt.Errorf("%w: its schema version cannot be read: %v", ErrNotEvidenceDatabase, err)
	}
	if applied.Int64 < 1 {
		return fmt.Errorf("%w: its schema version is %d", ErrNotEvidenceDatabase, applied.Int64)
	}
	if int(applied.Int64) > SchemaVersion() {
		return &NewerSchemaError{Applied: int(applied.Int64), Supported: SchemaVersion()}
	}
	for _, table := range evidenceCoreTables {
		ok, err := hasTable(table)
		if err != nil {
			return fmt.Errorf("sqlite: reading database schema: %w", err)
		}
		if !ok {
			return fmt.Errorf("%w: table %q is missing", ErrNotEvidenceDatabase, table)
		}
	}
	return nil
}

// sqliteFileURI renders a filesystem path as the path part of a SQLite file:
// URI. SQLite decodes %HH escapes and ends the path at '?' (query) or '#'
// (fragment), so a literal path containing them would open a different file:
// a data folder named "100%25 data" verified ".../100% data", which does not
// exist, and Backup then removed a snapshot VACUUM INTO had written correctly.
// Only those three bytes are significant inside the path; everything else,
// including spaces and non-ASCII names, is taken literally.
func sqliteFileURI(path string) string {
	return "file:" + sqliteURIPathEscaper.Replace(filepath.ToSlash(path))
}

var sqliteURIPathEscaper = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

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
