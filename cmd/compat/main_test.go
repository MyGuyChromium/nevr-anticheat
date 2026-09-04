package main

import (
	"bytes"
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestRunReplayMode_SyntheticFixture(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay")
	out := captureStdout(t, func() { runReplayMode([]string{path, filepath.Join(t.TempDir(), "missing.echoreplay")}, 50) })
	if !strings.Contains(out, "=== REPLAY:") || !strings.Contains(out, "SYN-FIXTURE-001") {
		t.Errorf("replay mode output:\n%s", out)
	}
}

func TestRunSessionMode_Fixtures(t *testing.T) {
	normal := filepath.Join("..", "..", "tests", "fixtures", "echovr_session_normal.json")
	malformed := filepath.Join("..", "..", "tests", "fixtures", "echovr_session_malformed.json")
	out := captureStdout(t, func() { runSessionMode([]string{normal, malformed}, false) })
	if !strings.Contains(out, "echovr_session_normal.json") || !strings.Contains(out, "echovr_session_malformed.json") {
		t.Errorf("session mode output:\n%s", out)
	}
	out = captureStdout(t, func() { runSessionMode([]string{normal}, true) })
	if out == "" {
		t.Error("strict session mode printed nothing")
	}
}

func TestWritePhysicsAudit_SyntheticFixture(t *testing.T) {
	replayPath := filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay")
	outputPath := filepath.Join(t.TempDir(), "physics.csv")
	rows, err := writePhysicsAudit(replayPath, outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		t.Fatal("physics audit wrote no player frames")
	}
	f, err := os.Open(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != rows+1 || len(records[0]) != len(physicsAuditHeader) {
		t.Fatalf("CSV shape = %d rows x %d columns, want %d x %d", len(records), len(records[0]), rows+1, len(physicsAuditHeader))
	}
	want := map[string]bool{"reported_speed_mps": false, "independent_speed_mps": false, "extractor_delta_error_mps": false, "playspace_speed_mps": false, "possible_head_contact": false, "release_detected": false}
	for _, column := range records[0] {
		if _, ok := want[column]; ok {
			want[column] = true
		}
	}
	for column, found := range want {
		if !found {
			t.Errorf("missing audit column %q", column)
		}
	}
	indices := make(map[string]int, len(records[0]))
	for i, column := range records[0] {
		indices[column] = i
	}
	derived := 0
	for _, record := range records[1:] {
		if record[indices["derivation_status"]] != "derived" {
			continue
		}
		derived++
		delta, err := strconv.ParseFloat(record[indices["extractor_delta_error_mps"]], 64)
		if err != nil || delta > 1e-9 {
			t.Fatalf("independent/production velocity delta = %q, err=%v", record[indices["extractor_delta_error_mps"]], err)
		}
	}
	if derived == 0 {
		t.Fatal("fixture produced no independently derived velocity rows")
	}
}
