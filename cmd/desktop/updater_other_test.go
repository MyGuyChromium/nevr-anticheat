//go:build !windows

package main

import (
	"path/filepath"
	"testing"
)

// testUpdateDir returns a private staging directory for one test. Only Windows
// pins the staging directory to the user profile; see updater_windows_test.go.
func testUpdateDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "updates")
}
