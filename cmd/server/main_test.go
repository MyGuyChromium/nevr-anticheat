package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func TestRunWithContextStartsAndShutsDownRealServer(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "server.db")
	configPath := filepath.Join(dir, "server.toml")
	configText := fmt.Sprintf("[general]\ndb_path = '%s'\nlog_level = 'error'\n[server]\nlisten = '127.0.0.1:0'\nmetrics = '127.0.0.1:0'\nallow_unauthenticated = true\n", dbPath)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- runWithContext(ctx, []string{"--config", configPath}, func(string) string { return "" })
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("server exit code = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down after context cancellation")
	}
	store, err := sqlite.NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopening migrated database: %v", err)
	}
	_ = store.Close()
}

func TestRunWithContextFailsClosedWithoutAuthentication(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "server.toml")
	configText := fmt.Sprintf("[general]\ndb_path = '%s'\nlog_level = 'error'\n[server]\nlisten = '127.0.0.1:0'\nmetrics = '127.0.0.1:0'\n", filepath.Join(dir, "server.db"))
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runWithContext(context.Background(), []string{"--config", configPath}, func(string) string { return "" }); code != 1 {
		t.Fatalf("server exit code = %d, want 1", code)
	}
}

func TestRunWithContextRejectsBadArguments(t *testing.T) {
	if code := runWithContext(context.Background(), []string{"--not-a-real-flag"}, os.Getenv); code != 2 {
		t.Fatalf("parse failure exit code = %d, want 2", code)
	}
}
