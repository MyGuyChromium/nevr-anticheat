package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Spark writes timestamp, session JSON and bones JSON separated by tabs; the
// bones document must be split off before decoding the session.
func TestParseFileStream_IgnoresTrailingBonesDocument(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay"))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(src), "\n"), "\n") {
		b.WriteString(line)
		b.WriteString("\t{\"user_bones\":[[0.1,0.2,0.3]],\"bone_count\":1}\n")
	}
	path := filepath.Join(t.TempDir(), "bones.echoreplay")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	frames := 0
	_, diag, err := NewEchoReplayParser().ParseFileStream(path, func(tick *ParsedTick) error {
		frames += len(tick.Frames)
		return nil
	})
	if err != nil {
		t.Fatalf("replay with bones rejected: %v", err)
	}
	if frames == 0 || diag.LinesWithBones == 0 {
		t.Fatalf("frames=%d linesWithBones=%d", frames, diag.LinesWithBones)
	}
}
