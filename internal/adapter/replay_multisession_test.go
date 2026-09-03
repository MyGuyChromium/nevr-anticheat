package adapter

import (
	"archive/zip"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// C0 (fix pass 2): a session-id change mid-file starts a new match (new
// context, fresh time base and frame index) instead of being merged into the
// first one; ParsedTick.MatchID/MatchCtx/NewMatch follow it.
func TestReplayParser_SessionChangeStartsNewMatch(t *testing.T) {
	dir := t.TempDir()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	b := testPlayer("B", 2, [3]float64{-1, 1.6, 10})
	lines := []string{
		sessionLine(t, "2025/12/04 03:38:22.000", twoTeamSession("R1", []EchoVRPlayer{a}, nil)),
		sessionLine(t, "2025/12/04 03:38:22.067", twoTeamSession("R1", []EchoVRPlayer{a}, nil)),
		// Rematch ~11 minutes later under a new session id with a new roster.
		sessionLine(t, "2025/12/04 03:50:00.000", twoTeamSession("R2", []EchoVRPlayer{a}, []EchoVRPlayer{b})),
		sessionLine(t, "2025/12/04 03:50:00.067", twoTeamSession("R2", []EchoVRPlayer{a}, []EchoVRPlayer{b})),
	}
	path := filepath.Join(dir, "two.echoreplay")
	writeLines(t, path, lines)

	var ticks []*ParsedTick
	p := NewEchoReplayParser()
	ctx, diag, err := p.ParseFileStream(path, func(tick *ParsedTick) error {
		ticks = append(ticks, tick)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ticks) != 4 {
		t.Fatalf("ticks = %d", len(ticks))
	}
	wantID := []string{"R1", "R1", "R2", "R2"}
	wantIdx := []int{0, 1, 0, 1}
	wantNew := []bool{true, false, true, false}
	wantTs := []float64{0, 0.067, 0, 0.067}
	for i, tk := range ticks {
		if tk.MatchID != wantID[i] || tk.FrameIndex != wantIdx[i] || tk.NewMatch != wantNew[i] {
			t.Errorf("tick %d: id=%q idx=%d new=%v, want %q/%d/%v", i, tk.MatchID, tk.FrameIndex, tk.NewMatch, wantID[i], wantIdx[i], wantNew[i])
		}
		if tk.MatchCtx == nil || tk.MatchCtx.MatchID != wantID[i] {
			t.Errorf("tick %d: MatchCtx = %+v", i, tk.MatchCtx)
		}
		for _, f := range tk.Frames {
			if math.Abs(f.Timestamp-wantTs[i]) > 1e-9 || f.FrameIndex != wantIdx[i] {
				t.Errorf("tick %d frame %s: ts=%v idx=%d, want %v/%d", i, f.PlayerID, f.Timestamp, f.FrameIndex, wantTs[i], wantIdx[i])
			}
		}
	}
	// Player A's first frame of R2 must not carry a delta from R1.
	if f := ticks[2].Frames[0]; f.DeltaTime != 0 {
		t.Errorf("first frame of the second match has DeltaTime %v", f.DeltaTime)
	}
	if ticks[0].MatchCtx == ticks[2].MatchCtx {
		t.Error("both matches share one context")
	}

	// The returned context is the first match's, untouched by the rematch.
	if ctx == nil || ctx.MatchID != "R1" || len(ctx.PlayerIDs) != 1 {
		t.Errorf("returned ctx = %+v", ctx)
	}
	if !ctx.StartTime.Equal(time.Date(2025, 12, 4, 3, 38, 22, 0, time.UTC)) {
		t.Errorf("first StartTime = %v", ctx.StartTime)
	}
	matches := p.Matches()
	if len(matches) != 2 || matches[0] != ctx || matches[1] != ticks[2].MatchCtx {
		t.Fatalf("Matches() = %v", matches)
	}
	second := matches[1]
	if second.MatchID != "R2" || len(second.PlayerIDs) != 2 || second.ReplayFile != "two.echoreplay" {
		t.Errorf("second ctx = %+v", second)
	}
	if !second.StartTime.Equal(time.Date(2025, 12, 4, 3, 50, 0, 0, time.UTC)) {
		t.Errorf("second StartTime = %v (must be the rematch's first sample)", second.StartTime)
	}
	if diag.SessionChanges != 1 || !strings.Contains(diag.FormatReport(), "Session id changes (matches):  1 (2 matches in file)") {
		t.Errorf("SessionChanges = %d\n%s", diag.SessionChanges, diag.FormatReport())
	}
	if diag.MapperStats == nil || diag.MapperStats.Snapshots != 4 || diag.MapperStats.FramesMapped != 6 {
		t.Errorf("mapper stats must span both matches: %+v", diag.MapperStats)
	}

	// ParseFile cannot represent two matches: it stops with an explicit error
	// and still returns the first match.
	fp := NewEchoReplayParser()
	fctx, frames, _, err := fp.ParseFile(path)
	if !errors.Is(err, ErrMultipleSessions) {
		t.Fatalf("ParseFile error = %v, want ErrMultipleSessions", err)
	}
	if !strings.Contains(err.Error(), `"R2"`) || !strings.Contains(err.Error(), `"R1"`) || !strings.Contains(err.Error(), "03:50:00.000") {
		t.Errorf("error should name both sessions and the line time: %v", err)
	}
	if fctx == nil || fctx.MatchID != "R1" || len(frames) != 2 {
		t.Errorf("ParseFile ctx=%+v frames=%d", fctx, len(frames))
	}
	for _, f := range frames {
		if f.PlayerID == "echovr:2" {
			t.Error("second match's frames leaked into ParseFile's result")
		}
	}
}

// Snapshots without a session id belong to the current match; a repeated id
// is not a change.
func TestReplayParser_EmptySessionIDInheritsMatch(t *testing.T) {
	dir := t.TempDir()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	lines := []string{
		sessionLine(t, "2025/12/04 03:38:22.000", twoTeamSession("R1", []EchoVRPlayer{a}, nil)),
		sessionLine(t, "2025/12/04 03:38:22.067", twoTeamSession("", []EchoVRPlayer{a}, nil)),
		sessionLine(t, "2025/12/04 03:38:22.134", twoTeamSession("R1", []EchoVRPlayer{a}, nil)),
	}
	path := filepath.Join(dir, "t.echoreplay")
	writeLines(t, path, lines)
	p := NewEchoReplayParser()
	ctx, frames, diag, err := p.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 || ctx.MatchID != "R1" || len(p.Matches()) != 1 || diag.SessionChanges != 0 {
		t.Errorf("frames=%d ctx=%s matches=%d changes=%d", len(frames), ctx.MatchID, len(p.Matches()), diag.SessionChanges)
	}
	if frames[2].FrameIndex != 2 || math.Abs(frames[2].Timestamp-0.134) > 1e-9 {
		t.Errorf("third frame = idx %d ts %v", frames[2].FrameIndex, frames[2].Timestamp)
	}
}

// NewMatch is carried to the first delivered tick when the new match's first
// snapshots produce no frames.
func TestReplayParser_NewMatchFlagSurvivesRejectedFirstSnapshot(t *testing.T) {
	dir := t.TempDir()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	zero := testPlayer("Z", 3, [3]float64{})
	lines := []string{
		sessionLine(t, "2025/12/04 03:38:22.000", twoTeamSession("R1", []EchoVRPlayer{a}, nil)),
		sessionLine(t, "2025/12/04 03:40:00.000", twoTeamSession("R2", []EchoVRPlayer{zero}, nil)), // every player rejected
		sessionLine(t, "2025/12/04 03:40:00.067", twoTeamSession("R2", []EchoVRPlayer{a}, nil)),
	}
	path := filepath.Join(dir, "t.echoreplay")
	writeLines(t, path, lines)
	var ticks []*ParsedTick
	_, _, err := NewEchoReplayParser().ParseFileStream(path, func(tick *ParsedTick) error {
		ticks = append(ticks, tick)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ticks) != 2 || !ticks[0].NewMatch || !ticks[1].NewMatch || ticks[1].MatchID != "R2" || ticks[1].FrameIndex != 1 {
		t.Errorf("ticks = %+v", ticks)
	}
}

// L0: a UTF-8 BOM before the first prefix must not reject the first sample.
func TestReplayParser_LeadingBOM(t *testing.T) {
	dir := t.TempDir()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	lines := []string{
		"\xef\xbb\xbf" + sessionLine(t, "2025/12/04 03:38:22.000", twoTeamSession("R", []EchoVRPlayer{a}, nil)),
		sessionLine(t, "2025/12/04 03:38:22.067", twoTeamSession("R", []EchoVRPlayer{a}, nil)),
	}
	path := filepath.Join(dir, "t.echoreplay")
	writeLines(t, path, lines)
	ctx, frames, diag, err := NewEchoReplayParser().ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || diag.FramesRejected != 0 || diag.LinesBadTimestamp != 0 {
		t.Errorf("frames=%d rejected=%d badts=%d", len(frames), diag.FramesRejected, diag.LinesBadTimestamp)
	}
	if !ctx.StartTime.Equal(time.Date(2025, 12, 4, 3, 38, 22, 0, time.UTC)) {
		t.Errorf("StartTime = %v, want the first line", ctx.StartTime)
	}
	if _, err := ParseReplayLineTime("\ufeff2025/12/04 03:38:22.000"); err != nil {
		t.Errorf("BOM-prefixed prefix rejected: %v", err)
	}
}

// L2: a small .json metadata entry must not beat a large .txt payload.
func TestSelectZipEntry_JSONAndTxtDecidedBySize(t *testing.T) {
	mk := func(name string, size uint64) *zip.File {
		zf := &zip.File{}
		zf.Name = name
		zf.UncompressedSize64 = size
		return zf
	}
	if got := selectZipEntry([]*zip.File{mk("info.json", 300), mk("frames.txt", 900000)}); got == nil || got.Name != "frames.txt" {
		t.Errorf("large .txt payload lost to small .json: %v", got)
	}
	if got := selectZipEntry([]*zip.File{mk("notes.txt", 300), mk("frames.json", 900000)}); got == nil || got.Name != "frames.json" {
		t.Errorf("large .json payload lost to small .txt: %v", got)
	}
	// Explicit replay extensions still win regardless of size.
	if got := selectZipEntry([]*zip.File{mk("frames.txt", 900000), mk("rec.ndjson", 100)}); got == nil || got.Name != "rec.ndjson" {
		t.Errorf(".ndjson should outrank .txt: %v", got)
	}

	// End to end: the payload named frames.txt next to a metadata json.
	dir := t.TempDir()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	zipPath := filepath.Join(dir, "rec.echoreplay")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("info.json")
	w.Write([]byte(`{"recorder":"test"}`))
	w, _ = zw.Create("frames.txt")
	w.Write([]byte(sessionLine(t, "2025/12/04 03:38:22.000", twoTeamSession("R", []EchoVRPlayer{a}, nil)) + "\n"))
	zw.Close()
	f.Close()
	if _, frames, _, err := NewEchoReplayParser().ParseFile(zipPath); err != nil || len(frames) != 1 {
		t.Errorf("frames.txt payload: frames=%d err=%v", len(frames), err)
	}
}
