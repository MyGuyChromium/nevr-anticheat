package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestLogStartup_DeprecatedKeyProducesWarningLine: a file that still sets a
// deprecated key yields one Warn line naming it, and the effective table is
// only written when a writer is given.
func TestLogStartup_DeprecatedKeyProducesWarningLine(t *testing.T) {
	cfg := mustLoad(t, "[general]\nmode = \"online\"\n")

	var logs, table bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	LogStartup(logger, cfg, nil)
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "general.mode is deprecated") {
		t.Fatalf("deprecated key not logged at Warn:\n%s", out)
	}
	if strings.Contains(out, "effective detector configuration") {
		t.Fatalf("table announced although no writer was given:\n%s", out)
	}

	logs.Reset()
	LogStartup(logger, cfg, &table)
	if !strings.Contains(logs.String(), "level=INFO") || !strings.Contains(logs.String(), "effective detector configuration") {
		t.Fatalf("table not announced at Info:\n%s", logs.String())
	}
	if !strings.HasPrefix(table.String(), "DETECTOR") || !strings.Contains(table.String(), "MOV_001") {
		t.Fatalf("effective table not written:\n%s", table.String())
	}

	// A clean config warns about nothing.
	logs.Reset()
	LogStartup(logger, DefaultConfig(), nil)
	if logs.Len() != 0 {
		t.Fatalf("default config produced startup output:\n%s", logs.String())
	}
}
