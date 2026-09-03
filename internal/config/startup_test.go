package config

import (
	"bytes"
	"encoding/json"
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

	// log_format = "text": the fixed-width block goes to the writer.
	logs.Reset()
	cfg.General.LogFormat = "text"
	LogStartup(logger, cfg, &table)
	if !strings.Contains(logs.String(), "level=INFO") || !strings.Contains(logs.String(), "effective detector configuration") {
		t.Fatalf("table not announced at Info:\n%s", logs.String())
	}
	if !strings.HasPrefix(table.String(), "DETECTOR") || !strings.Contains(table.String(), "MOV_001") {
		t.Fatalf("effective table not written:\n%s", table.String())
	}
	if strings.Contains(logs.String(), "msg=\"effective detector\"") {
		t.Fatalf("text format must not also emit per-detector records:\n%s", logs.String())
	}

	// A clean config warns about nothing.
	logs.Reset()
	LogStartup(logger, DefaultConfig(), nil)
	if logs.Len() != 0 {
		t.Fatalf("default config produced startup output:\n%s", logs.String())
	}
}

// TestLogStartup_JSONFormatKeepsStderrJSON: nevr-server writes JSON logs to
// stderr, so the effective table must not be a plain-text block in that
// stream; every detector becomes one JSON record and the writer stays empty.
func TestLogStartup_JSONFormatKeepsStderrJSON(t *testing.T) {
	cfg := mustLoad(t, "[general]\nlog_format = \"json\"\n[detector.MOV_001]\nmode = \"review\"\n")

	var logs, table bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	LogStartup(logger, cfg, &table)

	if table.Len() != 0 {
		t.Fatalf("json format wrote a plain-text block to the table writer:\n%s", table.String())
	}
	records := 0
	var mov001 map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("stderr line is not JSON: %q (%v)", line, err)
		}
		if rec["msg"] == "effective detector" {
			records++
			if rec["id"] == "MOV_001" {
				mov001 = rec
			}
		}
	}
	if want := len(KnownDetectorIDs()); records != want {
		t.Fatalf("%d effective detector records, want %d", records, want)
	}
	if mov001 == nil {
		t.Fatal("no record for MOV_001")
	}
	if mov001["mode"] != "review" || mov001["enabled"] != true || mov001["shadow"] != false {
		t.Fatalf("MOV_001 record wrong: %v", mov001)
	}
	params, ok := mov001["params"].(map[string]any)
	if !ok || params["max_legitimate_speed"] != 55.0 {
		t.Fatalf("MOV_001 params not nested in the record: %v", mov001["params"])
	}
	if _, present := mov001["params_from_constructor"]; present {
		t.Fatalf("every default param comes from config; got %v", mov001["params_from_constructor"])
	}
}
