package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A data folder whose name contains a valid percent escape ("100%25 data",
// "Replays%20Archive") or a URI delimiter must not change which file the
// read-only verifier opens: SQLite decodes %HH in file: URIs, so an unescaped
// path verified a different (missing) file and Backup deleted a good snapshot.
func TestBackupAndVerifySurviveURIMetacharactersInPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.StoreTelemetryFrames(ctx, "uri-match", mkFrames("p1", 0, 4)); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"100%25 data", "Replays%20Archive", "odd#frag&amp", "plain % sign"} {
		t.Run(dir, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), dir, "snapshot.db")
			if err := s.Backup(ctx, destination); err != nil {
				t.Fatalf("backup into %q: %v", dir, err)
			}
			if info, err := os.Stat(destination); err != nil || info.Size() == 0 {
				t.Fatalf("verified backup is missing at the literal path: %v %v", info, err)
			}
			if err := VerifyDatabase(ctx, destination); err != nil {
				t.Fatalf("restore-side verification of %q: %v", dir, err)
			}
		})
	}
}

// The verifier must still be a real check: a missing or corrupt file fails,
// and a failed verification removes the unusable copy.
func TestVerifyDatabaseStillRejectsMissingAndCorruptFiles(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "100%25 data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.db")
	if err := VerifyDatabase(ctx, missing); err == nil {
		t.Fatal("missing database verified")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only verification created a file: %v", err)
	}
	corrupt := filepath.Join(dir, "corrupt.db")
	if err := os.WriteFile(corrupt, []byte("this is not a sqlite database, only text padding................................................................"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDatabase(ctx, corrupt); err == nil {
		t.Fatal("corrupt database verified")
	}
}

func TestSQLiteFileURIEscapesOnlyURISignificantBytes(t *testing.T) {
	got := sqliteFileURI(filepath.FromSlash("C:/Data/100%25 data/what?/odd#frag/ünï code.db"))
	want := "file:C:/Data/100%2525 data/what%3f/odd%23frag/ünï code.db"
	if got != want {
		t.Fatalf("uri = %q, want %q", got, want)
	}
}
