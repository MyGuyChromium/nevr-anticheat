package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func rawSQLiteFile(t *testing.T, name string, statements ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

const migrationTableSQL = `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, description TEXT, applied_at TEXT NOT NULL DEFAULT '')`

var coreTableSQL = []string{
	`CREATE TABLE detection_events (event_id TEXT)`, `CREATE TABLE match_summaries (match_id TEXT)`, `CREATE TABLE review_cases (case_id TEXT)`,
}

// Every file below is a healthy SQLite database: PRAGMA quick_check says "ok"
// for each, which is why the integrity check alone let them replace the
// evidence library. Each case must be refused by the schema requirement.
func TestVerifyEvidenceDatabaseRefusesHealthyFilesThatAreNotNEVRDatabases(t *testing.T) {
	ctx := context.Background()
	empty := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path string }{
		{"zero-byte file", empty},
		{"database of another program", rawSQLiteFile(t, "foreign.db", `CREATE TABLE bookmarks (url TEXT)`, `INSERT INTO bookmarks VALUES ('x')`)},
		{"migration table with no applied version", rawSQLiteFile(t, "unversioned.db", append([]string{migrationTableSQL}, coreTableSQL...)...)},
		{"schema version without the core tables", rawSQLiteFile(t, "hollow.db", migrationTableSQL, `INSERT INTO schema_migrations (version) VALUES (1)`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := VerifyDatabase(ctx, tc.path); err != nil {
				t.Fatalf("test setup: the file must pass the plain integrity check, got %v", err)
			}
			err := VerifyEvidenceDatabase(ctx, tc.path)
			if !errors.Is(err, ErrNotEvidenceDatabase) {
				t.Fatalf("VerifyEvidenceDatabase = %v, want ErrNotEvidenceDatabase", err)
			}
		})
	}
}

func TestVerifyEvidenceDatabaseRefusesANewerSchema(t *testing.T) {
	statements := append([]string{migrationTableSQL,
		fmt.Sprintf(`INSERT INTO schema_migrations (version) VALUES (%d)`, SchemaVersion()+1)}, coreTableSQL...)
	newer := rawSQLiteFile(t, "newer.db", statements...)
	if err := VerifyEvidenceDatabase(context.Background(), newer); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("VerifyEvidenceDatabase = %v, want ErrNewerSchema", err)
	}
}

func TestVerifyEvidenceDatabaseAcceptsCurrentAndOlderNEVRDatabases(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	current := filepath.Join(t.TempDir(), "current.db")
	if err := s.Backup(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvidenceDatabase(ctx, current); err != nil {
		t.Fatalf("a backup of the current store was refused: %v", err)
	}
	// A backup taken by an older build holds only the tables its migrations had
	// created. It must stay restorable: NewStore migrates it afterwards.
	statements := append([]string{migrationTableSQL, `INSERT INTO schema_migrations (version) VALUES (1)`}, coreTableSQL...)
	older := rawSQLiteFile(t, "older.db", statements...)
	if err := VerifyEvidenceDatabase(ctx, older); err != nil {
		t.Fatalf("a version-1 database was refused: %v", err)
	}
	// Corruption is still reported by the integrity check it builds on.
	garbage := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(garbage, []byte("this is not a SQLite database at all, not even close"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvidenceDatabase(ctx, garbage); err == nil || errors.Is(err, ErrNotEvidenceDatabase) {
		t.Fatalf("garbage file: %v, want the integrity failure", err)
	}
}
