package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func TestLogToFileDecision(t *testing.T) {
	for _, tc := range []struct {
		gui, console, force, want bool
	}{
		{gui: false, want: false},                              // plain `go build`: console
		{gui: true, want: true},                                // windowed release: file
		{gui: true, console: true, want: false},                // windowed release debugging from a terminal
		{gui: false, force: true, want: true},                  // dev build opting in
		{gui: true, console: true, force: true, want: true},    // explicit file wins
		{gui: false, console: true, force: false, want: false}, // --console is a no-op on a console build
	} {
		if got := logToFile(tc.gui, tc.console, tc.force); got != tc.want {
			t.Errorf("logToFile(gui=%v, console=%v, force=%v) = %v, want %v", tc.gui, tc.console, tc.force, got, tc.want)
		}
	}
	if guiSubsystem != "false" {
		t.Fatalf("a plain build must default to console logging, guiSubsystem=%q", guiSubsystem)
	}
}

func TestRotatingLogRotatesBySizeAndKeepsABoundedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "app.log")
	log, err := openRotatingLog(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := func(i int) string { return fmt.Sprintf("line %02d %s\n", i, strings.Repeat("x", 30)) } // 39 bytes
	for i := range 12 {
		if n, err := log.Write([]byte(line(i))); err != nil || n != len(line(i)) {
			t.Fatalf("write %d: n=%d err=%v", i, n, err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	var all string
	for _, name := range []string{path + ".2", path + ".1", path} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(name), err)
		}
		if len(data) > 100 {
			t.Fatalf("%s is %d bytes, over the 100 byte limit", filepath.Base(name), len(data))
		}
		if !strings.HasSuffix(string(data), "\n") || strings.Count(string(data), "\n") != len(data)/39 {
			t.Fatalf("%s holds a split line: %q", filepath.Base(name), data)
		}
		all += string(data)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("more than 2 old generations were kept: %v", err)
	}
	// The newest lines survive, in order; only the oldest generations are dropped.
	if !strings.HasSuffix(all, line(10)+line(11)) || !strings.Contains(all, line(6)) || strings.Contains(all, line(0)) {
		t.Fatalf("unexpected retained history:\n%s", all)
	}

	// Reopening appends; nothing already logged is lost on the next launch.
	log, err = openRotatingLog(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Write([]byte("next launch\n")); err != nil {
		t.Fatal(err)
	}
	_ = log.Close()
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), line(11)) || !strings.HasSuffix(string(data), "next launch\n") {
		t.Fatalf("reopen did not append: %q", data)
	}
}

func TestFileLoggingCapturesStdoutAndStderrAndRestoresThem(t *testing.T) {
	dataDir := t.TempDir()
	prevOut, prevErr := os.Stdout, os.Stderr
	logging, err := startFileLogging(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "to stdout")
	fmt.Fprintln(os.Stderr, `{"level":"WARN","msg":"to stderr"}`)
	logging.stop()
	if os.Stdout != prevOut || os.Stderr != prevErr {
		t.Fatal("standard streams were not restored")
	}
	if want := filepath.Join(dataDir, appLogDirName, appLogFileName); logging.path != want {
		t.Fatalf("log path = %s, want %s", logging.path, want)
	}
	data, err := os.ReadFile(logging.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "to stdout\n") || !strings.Contains(string(data), `"msg":"to stderr"`) {
		t.Fatalf("log file = %q", data)
	}
	if _, err := os.Stat(filepath.Join(dataDir, appLogDirName, crashLogName)); err != nil {
		t.Fatalf("crash log was not prepared: %v", err)
	}
	var nilLogging *fileLogging
	nilLogging.stop() // a console run has none
}

func TestStartupFailureExplainsANewerDatabase(t *testing.T) {
	newer := fmt.Errorf("opening store: %w", fmt.Errorf("running migrations: %w", &sqlite.NewerSchemaError{Applied: 99, Supported: 20}))
	failure := describeStartupFailure(newer, `C:\data\nevr-anticheat.db`, `C:\data\logs\nevr-desktop.log`)
	for _, want := range []string{"newer NEVR-Anticheat", "schema version 99", "supports up to 20", "Install the latest", `C:\data\nevr-anticheat.db`, `C:\data\logs\nevr-desktop.log`, "untouched"} {
		if !strings.Contains(failure.Title+"\n"+failure.Message, want) {
			t.Errorf("newer-schema message lacks %q:\n%s\n%s", want, failure.Title, failure.Message)
		}
	}
	if failure.Title != "NEVR-Anticheat needs an update" {
		t.Errorf("title = %q", failure.Title)
	}
	generic := describeStartupFailure(errors.New("listening: address in use"), "", "")
	if generic.Title != "NEVR-Anticheat could not start" || generic.Message != "listening: address in use" {
		t.Errorf("generic failure = %+v", generic)
	}
}

// The whole startup path: an older build pointed at a newer database refuses
// it, says why on the startup error surface (the log file of a windowed
// build), and leaves the database alone.
func TestRunRefusesANewerDatabaseAndSaysWhy(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "data", "evidence.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	future := sqlite.SchemaVersion() + 3
	if _, err := store.DB().Exec(`INSERT INTO schema_migrations(version, description) VALUES(?, 'written by a newer build')`, future); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "desktop.toml")
	if err := os.WriteFile(configPath, fmt.Appendf(nil, "[general]\ndb_path = %q\n", dbPath), 0o600); err != nil {
		t.Fatal(err)
	}

	err = run(runOptions{configPath: configPath, noBrowser: true, logLevel: "error", logToFile: true})
	if !errors.Is(err, sqlite.ErrNewerSchema) {
		t.Fatalf("run error = %v, want ErrNewerSchema", err)
	}
	logged, readErr := os.ReadFile(filepath.Join(filepath.Dir(dbPath), appLogDirName, appLogFileName))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, want := range []string{"NEVR-Anticheat needs an update", "written by a newer NEVR-Anticheat", "Install the latest"} {
		if !strings.Contains(string(logged), want) {
			t.Errorf("log lacks %q:\n%s", want, logged)
		}
	}
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if applied, err := sqlite.AppliedSchemaVersion(raw); err != nil || applied != future {
		t.Fatalf("refused database changed: version=%d err=%v", applied, err)
	}
}
