package tests

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
)

// writeNDJSON writes raw NDJSON replay lines to a file.
func writeNDJSON(t *testing.T, dir, name string, lines []string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating file: %v", err)
	}
	for _, line := range lines {
		f.WriteString(line + "\n")
	}
	f.Close()
	return path
}

// writeZipNDJSON wraps NDJSON lines in a ZIP archive.
func writeZipNDJSON(t *testing.T, dir, zipName, innerName string, lines []string) string {
	t.Helper()
	path := filepath.Join(dir, zipName)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating zip: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create(innerName)
	if err != nil {
		t.Fatalf("creating zip entry: %v", err)
	}
	for _, line := range lines {
		w.Write([]byte(line + "\n"))
	}
	zw.Close()
	f.Close()
	return path
}

// A valid minimal Echo VR session line.
const validSessionLine = `2025/12/04 03:38:22.060	{"disc":{"position":[1.0,2.0,3.0],"forward":[0,0,1],"left":[1,0,0],"up":[0,1,0],"velocity":[5.0,1.0,0.0],"bounce_count":0},"sessionid":"TEST-001","game_clock_display":"10:00","game_status":"playing","match_type":"Echo_Arena","map_name":"mpl_arena_a","blue_points":0,"orange_points":0,"teams":[{"players":[{"name":"Player1","userid":1001,"playerid":0,"body":{"position":[5.0,1.6,0.0],"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]},"velocity":[1.0,0.0,0.0],"lhand":{"pos":[4.7,1.9,0.2],"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]},"rhand":{"pos":[5.3,1.9,-0.2],"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]},"head":{"position":[5.0,1.6,0.0],"forward":[0,0,1],"left":[-1,0,0],"up":[0,1,0]},"stunned":false,"invulnerable":false,"possession":false,"blocking":false,"ping":50,"stats":{"goals":0,"stuns":2,"assists":0,"saves":0}}]},{"players":[]}]}`

func TestReplayParser_RawNDJSON(t *testing.T) {
	dir := t.TempDir()
	lines := []string{validSessionLine, validSessionLine, validSessionLine}
	path := writeNDJSON(t, dir, "test.echoreplay", lines)

	parser := adapter.NewEchoReplayParser()
	matchCtx, frames, diag, err := parser.ParseFile(path)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if matchCtx == nil {
		t.Fatal("matchCtx is nil")
	}
	if matchCtx.MatchID != "TEST-001" {
		t.Errorf("matchID = %q, want TEST-001", matchCtx.MatchID)
	}
	if len(frames) != 3 {
		t.Errorf("got %d frames, want 3", len(frames))
	}
	if diag.FramesRejected != 0 {
		t.Errorf("got %d rejected frames, want 0", diag.FramesRejected)
	}
	if frames[0].Position[0] != 5.0 {
		t.Errorf("position X = %f, want 5.0", frames[0].Position[0])
	}
	if frames[0].EstimatedPingMs != 50 {
		t.Errorf("ping = %f, want 50", frames[0].EstimatedPingMs)
	}
	t.Logf("Parsed %d frames, match=%s, players=%d", len(frames), matchCtx.MatchID, len(matchCtx.PlayerIDs))
}

func TestReplayParser_ZIPCompressed(t *testing.T) {
	dir := t.TempDir()
	lines := []string{validSessionLine, validSessionLine}
	path := writeZipNDJSON(t, dir, "test.echoreplay", "inner.echoreplay", lines)

	parser := adapter.NewEchoReplayParser()
	matchCtx, frames, _, err := parser.ParseFile(path)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if matchCtx == nil {
		t.Fatal("matchCtx is nil")
	}
	if len(frames) != 2 {
		t.Errorf("got %d frames, want 2", len(frames))
	}
	t.Logf("ZIP: parsed %d frames from %s", len(frames), path)
}

func TestReplayParser_MalformedLines(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"no tab separator here",
		"2025/01/01 00:00:00.000\t{invalid json!!!}",
		"2025/01/01 00:00:00.000\t",   // empty JSON
		validSessionLine,               // one valid line
		"",                             // empty line
		"2025/01/01 00:00:00.000\t{}", // empty object (no teams)
	}
	path := writeNDJSON(t, dir, "malformed.echoreplay", lines)

	parser := adapter.NewEchoReplayParser()
	matchCtx, frames, diag, err := parser.ParseFile(path)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if matchCtx == nil {
		t.Fatal("should have parsed at least one valid frame")
	}
	if len(frames) != 1 {
		t.Errorf("got %d frames, want 1 (only one valid line)", len(frames))
	}
	if diag.FramesRejected < 2 {
		t.Errorf("got %d rejected, want >= 2", diag.FramesRejected)
	}
	t.Logf("Malformed: %d frames, %d rejected", len(frames), diag.FramesRejected)
}

func TestReplayParser_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := writeNDJSON(t, dir, "empty.echoreplay", []string{})

	parser := adapter.NewEchoReplayParser()
	_, _, _, err := parser.ParseFile(path)
	if err == nil {
		t.Fatal("expected error for empty replay")
	}
	t.Logf("Empty file error: %v", err)
}

func TestReplayParser_RealFileIfAvailable(t *testing.T) {
	// Test against the real replay file if it exists
	path := "/tmp/rec_2025-12-03_21-38-17.echoreplay"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("real replay file not available at " + path)
	}

	parser := adapter.NewEchoReplayParser()
	matchCtx, frames, diag, err := parser.ParseFile(path)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	t.Logf("Real replay results:")
	t.Logf("  Match:    %s", matchCtx.MatchID)
	t.Logf("  Mode:     %s", matchCtx.GameMode)
	t.Logf("  Map:      %s", matchCtx.Map)
	t.Logf("  Players:  %d", len(matchCtx.PlayerIDs))
	t.Logf("  Frames:   %d", len(frames))
	t.Logf("  Rejected: %d", diag.FramesRejected)

	if len(frames) < 1000 {
		t.Errorf("expected >1000 frames from real replay, got %d", len(frames))
	}
	if len(matchCtx.PlayerIDs) < 2 {
		t.Errorf("expected >2 players, got %d", len(matchCtx.PlayerIDs))
	}
}

func TestReplayParser_RealZIPIfAvailable(t *testing.T) {
	// Test against the original ZIP-compressed file
	path := "/mnt/c/Users/colli/Downloads/rec_2025-12-03_21-38-17.echoreplay"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("real ZIP replay not available at " + path)
	}

	parser := adapter.NewEchoReplayParser()
	matchCtx, frames, diag, err := parser.ParseFile(path)
	if err != nil {
		t.Fatalf("parse error on ZIP: %v", err)
	}

	t.Logf("Real ZIP replay results:")
	t.Logf("  Match:    %s", matchCtx.MatchID)
	t.Logf("  Players:  %d", len(matchCtx.PlayerIDs))
	t.Logf("  Frames:   %d", len(frames))
	t.Logf("  Rejected: %d", diag.FramesRejected)

	if len(frames) < 1000 {
		t.Errorf("expected >1000 frames from real replay, got %d", len(frames))
	}
}
