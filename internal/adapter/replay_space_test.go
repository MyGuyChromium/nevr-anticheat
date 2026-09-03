package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Spark writes "<timestamp> <json>" with a space; the reference layout uses a tab.
func TestParseFileStream_AcceptsSpaceSeparatedLines(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "\t") {
		t.Fatal("fixture is expected to be tab separated")
	}
	spaced := strings.ReplaceAll(string(src), string('\t'), " ")
	path := filepath.Join(t.TempDir(), "spaced.echoreplay")
	if err := os.WriteFile(path, []byte(spaced), 0o644); err != nil {
		t.Fatal(err)
	}
	frames := 0
	_, diag, err := NewEchoReplayParser().ParseFileStream(path, func(tick *ParsedTick) error {
		frames += len(tick.Frames)
		return nil
	})
	if err != nil {
		t.Fatalf("space-separated replay rejected: %v", err)
	}
	if frames == 0 {
		t.Fatalf("no frames parsed; diagnostics: %+v", diag)
	}
}
