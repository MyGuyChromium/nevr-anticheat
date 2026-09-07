package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// prepareDesktopDatabaseDirectory creates the parent of a normal database
// filename before restores and SQLite migrations run on a fresh installation.
// SQLite file: URIs may describe memory databases, read-only files or custom
// VFS paths; leave their interpretation and validation to SQLite.
func prepareDesktopDatabaseDirectory(dbPath string) error {
	if strings.HasPrefix(dbPath, "file:") {
		return nil
	}
	// The SQLite driver removes query parameters from non-URI filenames.
	if query := strings.IndexByte(dbPath, '?'); query >= 1 {
		dbPath = dbPath[:query]
	}
	if dbPath == "" || dbPath == ":memory:" {
		return nil
	}
	parent := filepath.Dir(dbPath)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("creating database directory %q: %w", parent, err)
	}
	return nil
}
