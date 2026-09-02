package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
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
