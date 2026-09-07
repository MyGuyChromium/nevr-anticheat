package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/regression"
)

func TestCLIUsageAndExistingDirectoryRemainUntouched(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{nil, {"bad"}, {"capture"}, {"check", "--manifest", "missing.json", "--out", dir}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 2 || stderr.Len() == 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("pre-existing directory changed: %+v %v", entries, err)
	}
}

func TestCLICaptureCheckRegressionAndHashRefusal(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "v1.json")
	source := filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay")
	execute := func(want int, args ...string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != want {
			t.Fatalf("exit=%d want=%d; stdout=%s stderr=%s", code, want, stdout.String(), stderr.String())
		}
	}
	execute(0, "capture", "--manifest", manifestPath, "--out", filepath.Join(dir, "capture"), source)
	execute(0, "check", "--manifest", manifestPath, "--out", filepath.Join(dir, "check"))
	before, _ := os.ReadFile(manifestPath)
	execute(2, "capture", "--manifest", manifestPath, "--out", filepath.Join(dir, "no-replace"), source)
	after, _ := os.ReadFile(manifestPath)
	if !bytes.Equal(before, after) {
		t.Fatal("capture replaced baseline")
	}
	manifest, err := regression.LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	match := &manifest.Cases[0].Expected[0]
	var player string
	for id := range match.PlayerFrames {
		player = id
		break
	}
	match.Signals = append(match.Signals, regression.Signal{MatchID: match.MatchID, PlayerID: player, DetectorID: "THROW_001", Frame: 10, Start: 10, End: 10, Shadow: true})
	changed := filepath.Join(dir, "expected-miss.json")
	if err := regression.WriteJSON(changed, manifest); err != nil {
		t.Fatal(err)
	}
	execute(1, "check", "--manifest", changed, "--out", filepath.Join(dir, "mismatch"))
	if _, err := os.Stat(filepath.Join(dir, "mismatch", "report.json")); err != nil {
		t.Fatal(err)
	}
	manifest.Cases[0].SHA256 = strings.Repeat("f", 64)
	tampered := filepath.Join(dir, "wrong-hash.json")
	if err := regression.WriteJSON(tampered, manifest); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "hash-refused")
	execute(2, "check", "--manifest", tampered, "--out", out)
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("hash preflight created outputs")
	}
}
