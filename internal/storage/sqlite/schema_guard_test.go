package sqlite

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// An older binary (after an updater rollback, or a stale portable copy) must
// refuse a database a newer build migrated, and must not modify it.
func TestNewStoreRefusesNewerSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "newer.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	future := SchemaVersion() + 1
	if _, err := s.DB().Exec(`INSERT INTO schema_migrations(version, description, applied_at) VALUES(?, 'from a newer build', ?)`, future, fmtDBTime(nowUTC())); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`PRAGMA user_version = ` + strings.Repeat("9", 3)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewStore(dbPath)
	if err == nil {
		reopened.Close()
		t.Fatal("a database with a newer schema was opened")
	}
	var newer *NewerSchemaError
	if !errors.Is(err, ErrNewerSchema) || !errors.As(err, &newer) || newer.Applied != future || newer.Supported != SchemaVersion() {
		t.Fatalf("err = %v (%+v)", err, newer)
	}
	if msg := err.Error(); !strings.Contains(msg, "newer NEVR-Anticheat") || !strings.Contains(msg, "Update the app") {
		t.Fatalf("message does not tell the user what to do: %q", msg)
	}

	// Nothing was re-stamped or migrated underneath the newer build.
	check, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var userVersion, maxVersion int
	if err := check.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&maxVersion); err != nil {
		t.Fatal(err)
	}
	if userVersion != 999 || maxVersion != future {
		t.Fatalf("refused database was modified: user_version=%d max=%d", userVersion, maxVersion)
	}
}
