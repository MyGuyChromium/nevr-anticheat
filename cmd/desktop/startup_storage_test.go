package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func TestPrepareDesktopDatabaseDirectoryCreatesNestedParents(t *testing.T) {
	for _, relative := range []bool{false, true} {
		t.Run(fmt.Sprintf("relative=%t", relative), func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join("Player's data", "NEVR Anticheat", "evidence.db")
			if relative {
				t.Chdir(root)
			} else {
				dbPath = filepath.Join(root, dbPath)
			}
			if _, err := os.Stat(filepath.Dir(dbPath)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("test needs an absent database directory: %v", err)
			}
			if err := prepareDesktopDatabaseDirectory(dbPath); err != nil {
				t.Fatal(err)
			}
			store, err := sqlite.NewStore(dbPath)
			if err != nil {
				t.Fatalf("opening a first-launch database: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(dbPath); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				t.Fatalf("missing migrated database: info=%v, error=%v", info, err)
			}
		})
	}
}

func TestPrepareDesktopDatabaseDirectoryPreservesExistingData(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "evidence.db")
	sibling := filepath.Join(root, "existing-settings.json")
	if err := os.WriteFile(sibling, []byte("keep this data"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec("CREATE TABLE first_launch_marker (value TEXT); INSERT INTO first_launch_marker VALUES ('retained')"); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := prepareDesktopDatabaseDirectory(dbPath); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.ReadFile(dbPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("directory preparation changed the database: %v", err)
	}
	reopened, err := sqlite.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var marker string
	if err := reopened.DB().QueryRow("SELECT value FROM first_launch_marker").Scan(&marker); err != nil || marker != "retained" {
		t.Fatalf("existing data lost: marker=%q, error=%v", marker, err)
	}
	if data, err := os.ReadFile(sibling); err != nil || string(data) != "keep this data" {
		t.Fatalf("sibling data changed: %q, %v", data, err)
	}
}

func TestPrepareDesktopDatabaseDirectoryReportsBlockedParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "NEVR-Anticheat")
	if err := os.WriteFile(parent, []byte("existing file"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := prepareDesktopDatabaseDirectory(filepath.Join(parent, "evidence.db"))
	var pathErr *os.PathError
	if err == nil || !strings.Contains(err.Error(), "creating database directory") || !strings.Contains(err.Error(), "NEVR-Anticheat") || !errors.As(err, &pathErr) {
		t.Fatalf("expected actionable directory error with underlying cause, got %v", err)
	}
	if data, err := os.ReadFile(parent); err != nil || string(data) != "existing file" {
		t.Fatalf("blocking file changed: %q, %v", data, err)
	}
}

func TestPrepareDesktopDatabaseDirectorySpecialPaths(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, dbPath := range []string{
		"", ":memory:", ":memory:?cache=shared&",
		"file::memory:", "file:URI folder/memory?mode=memory&",
		"file:URI folder/existing.db?mode=ro&",
	} {
		if err := prepareDesktopDatabaseDirectory(dbPath); err != nil {
			t.Fatalf("special filename %q: %v", dbPath, err)
		}
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("special filenames created directories: %v, %v", entries, err)
	}
	for _, dbPath := range []string{":memory:", ":memory:?cache=shared&", "file::memory:", "file:URI folder/memory?mode=memory&"} {
		store, err := sqlite.NewStore(dbPath)
		if err != nil {
			t.Fatalf("memory database %q: %v", dbPath, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("memory stores created files or directories: %v, %v", entries, err)
	}
	plainPath := filepath.Join(root, "ordinary folder", "evidence.db")
	dsn := plainPath + "?cache=private&"
	if err := prepareDesktopDatabaseDirectory(dsn); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.NewStore(dsn)
	if err != nil {
		t.Fatalf("ordinary filename with SQLite options: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plainPath); err != nil {
		t.Fatalf("DSN did not create its database at the plain filename: %v", err)
	}
}

// Exercise the actual startup path in a child so no browser, process working
// directory, signal handler or database from the user's installation is used.
func TestDesktopFirstLaunchRun(t *testing.T) {
	if configPath := os.Getenv("NEVR_TEST_FIRST_LAUNCH_CONFIG"); configPath != "" {
		if err := run(configPath, true, 0, "error"); err != nil {
			t.Fatal(err)
		}
		return
	}
	root := t.TempDir()
	dbPath := filepath.Join(root, "Player's local data", "NEVR-Anticheat", "evidence.db")
	configPath := filepath.Join(root, "desktop.toml")
	if err := os.WriteFile(configPath, fmt.Appendf(nil, "[general]\ndb_path = %q\n", dbPath), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDesktopFirstLaunchRun$")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "NEVR_TEST_FIRST_LAUNCH_CONFIG="+configPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			cancel()
			_ = cmd.Wait()
		}
	}()
	ready := make(chan string, 1)
	go func() {
		defer close(ready)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			const prefix = "NEVR-Anticheat desktop: open "
			if line, found := strings.CutPrefix(scanner.Text(), prefix); found {
				appURL, _, _ := strings.Cut(line, " ")
				ready <- appURL
				return
			}
		}
	}()
	var appURL string
	select {
	case appURL = <-ready:
	case <-ctx.Done():
		t.Fatal("desktop did not publish its URL before the startup deadline")
	}
	if appURL == "" {
		waitErr := cmd.Wait()
		waited = true
		t.Fatalf("desktop exited before startup: %v\n%s", waitErr, stderr.String())
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, endpoint := range []string{"", "api/health", "quit"} {
		resp, err := client.Get(appURL + endpoint)
		if err != nil {
			t.Fatalf("requesting %q: %v", endpoint, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("endpoint %q: status=%d, error=%v, body=%s", endpoint, resp.StatusCode, readErr, body)
		}
		if endpoint == "api/health" {
			var health healthResponse
			if err := json.Unmarshal(body, &health); err != nil || health.SchemaVersion != sqlite.SchemaVersion() || health.DatabasePath != dbPath {
				t.Fatalf("unexpected first-launch health: %+v, %v", health, err)
			}
		}
	}
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatalf("desktop did not shut down cleanly: %v\n%s", err, stderr.String())
	}
	if info, err := os.Stat(dbPath); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatalf("first launch did not retain its migrated database: info=%v, error=%v", info, err)
	}
}
