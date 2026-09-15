package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRequiresBuildManifest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestPreflightReadinessOutputRefusesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readiness.json")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := preflightReadinessOutput(path); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output error = %v", err)
	}
}

func TestWriteReadinessReportMakesSkippedControlsExplicit(t *testing.T) {
	report := readinessReport{
		Schema:           readinessReportSchema,
		ControlsExecuted: false,
		Controls:         make([]controlReadiness, 0),
		Limitations:      []string{"Paired synthetic controls were explicitly skipped for this readiness run."},
	}
	var output bytes.Buffer
	if err := writeReadinessReport("-", &output, report); err != nil {
		t.Fatal(err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &encoded); err != nil {
		t.Fatal(err)
	}
	if executed, ok := encoded["controls_executed"].(bool); !ok || executed {
		t.Fatalf("controls_executed = %#v", encoded["controls_executed"])
	}
	controls, ok := encoded["controls"].([]any)
	if !ok || len(controls) != 0 {
		t.Fatalf("controls = %#v; want an empty JSON array", encoded["controls"])
	}
}
