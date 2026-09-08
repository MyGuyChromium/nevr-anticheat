package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestInvalidServerFlagOverridesFailBeforeOpeningDatabase(t *testing.T) {
	for _, args := range [][]string{
		{"--max-frame-rate", "0"}, {"--idle-timeout", "0"},
		{"--max-connections", "-1"}, {"--max-message-bytes", "0"},
		{"--max-matches", "0"}, {"--max-players", "0"},
		{"--stale-match-after", "-1s"}, {"--persist-interval", "1ns"},
	} {
		t.Run(args[0], func(t *testing.T) {
			dir := t.TempDir()
			dbPath, configPath := filepath.Join(dir, "must-not-create.db"), filepath.Join(dir, "config.toml")
			body := fmt.Sprintf("[general]\ndb_path = '%s'\n[server]\nlisten = '127.0.0.1:0'\nmetrics = '127.0.0.1:0'\n", dbPath)
			if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			argv := append([]string{"--config", configPath}, args...)
			if code := runWithContext(context.Background(), argv, func(string) string { return "test-secret" }); code != 1 {
				t.Fatalf("invalid override exit=%d, want 1", code)
			}
			if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
				t.Fatalf("invalid settings opened database: %v", err)
			}
		})
	}
}
