package adapter

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sessionLine renders a replay line for the given session at the given prefix time.
func sessionLine(t *testing.T, prefix string, s *EchoVRSessionResponse) string {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return prefix + "\t" + string(data)
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseReplayLineTime(t *testing.T) {
	got, err := ParseReplayLineTime("2025/12/04 03:38:22.060")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2025, 12, 4, 3, 38, 22, 60_000_000, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
	if _, err := ParseReplayLineTime("2025/12/04 03:38:22"); err != nil {
		t.Errorf("seconds-only prefix rejected: %v", err)
	}
	if _, err := ParseReplayLineTime("not a time"); err == nil {
		t.Error("garbage prefix accepted")
	}
}

// F57: the line prefix drives Timestamp/DeltaTime.
func TestReplayParser_LinePrefixTimestamps(t *testing.T) {
	dir := t.TempDir()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	lines := []string{
		sessionLine(t, "2025/12/04 03:38:22.000", twoTeamSession("R", []EchoVRPlayer{a}, nil)),
		sessionLine(t, "2025/12/04 03:38:22.067", twoTeamSession("R", []EchoVRPlayer{a}, nil)),
		sessionLine(t, "2025/12/04 03:38:22.267", twoTeamSession("R", []EchoVRPlayer{a}, nil)), // 200 ms hiccup
		sessionLine(t, "2025/12/04 03:38:23.267", twoTeamSession("R", []EchoVRPlayer{a}, nil)), // 1 s gap
	}
	path := filepath.Join(dir, "t.echoreplay")
	writeLines(t, path, lines)

	ctx, frames, diag, err := NewEchoReplayParser().ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 4 {
		t.Fatalf("got %d frames", len(frames))
	}
	wantTs := []float64{0, 0.067, 0.267, 1.267}
	wantDt := []float64{0, 0.067, 0.2, 1.0}
	for i, f := range frames {
		if math.Abs(f.Timestamp-wantTs[i]) > 1e-9 || math.Abs(f.DeltaTime-wantDt[i]) > 1e-9 {
			t.Errorf("frame %d: ts=%v dt=%v want %v/%v", i, f.Timestamp, f.DeltaTime, wantTs[i], wantDt[i])
		}
		if f.FrameIndex != i {
			t.Errorf("frame %d index = %d", i, f.FrameIndex)
		}
	}
	if !ctx.StartTime.Equal(time.Date(2025, 12, 4, 3, 38, 22, 0, time.UTC)) {
		t.Errorf("StartTime = %v", ctx.StartTime)
	}
	if diag.FramesMapped != 4 || diag.Snapshots != 4 || diag.FramesRejected != 0 {
		t.Errorf("diag = mapped %d snapshots %d rejected %d", diag.FramesMapped, diag.Snapshots, diag.FramesRejected)
	}
	if diag.MapperStats == nil || diag.MapperStats.Snapshots != 4 {
		t.Errorf("mapper stats not recorded: %+v", diag.MapperStats)
	}
	if !diag.PresenceTracked {
		t.Error("presence should be tracked on the replay path")
	}
}

func TestReplayParser_BadTimestampLineRejected(t *testing.T) {
	dir := t.TempDir()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	lines := []string{
		sessionLine(t, "garbage", twoTeamSession("R", []EchoVRPlayer{a}, nil)),
		sessionLine(t, "2025/12/04 03:38:22.000", twoTeamSession("R", []EchoVRPlayer{a}, nil)),
	}
	path := filepath.Join(dir, "t.echoreplay")
	writeLines(t, path, lines)
	_, frames, diag, err := NewEchoReplayParser().ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || diag.FramesRejected != 1 || diag.LinesBadTimestamp != 1 {
		t.Errorf("frames=%d rejected=%d badts=%d", len(frames), diag.FramesRejected, diag.LinesBadTimestamp)
	}
}

// F98: late joiners and team changes reach the match context.
func TestReplayParser_LateJoinersMergedIntoContext(t *testing.T) {
	dir := t.TempDir()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	b := testPlayer("B", 2, [3]float64{-1, 1.6, 10})
	lines := []string{
		sessionLine(t, "2025/12/04 03:38:22.000", twoTeamSession("R", []EchoVRPlayer{a}, nil)),
		sessionLine(t, "2025/12/04 03:38:22.067", twoTeamSession("R", []EchoVRPlayer{a}, []EchoVRPlayer{b})),
		sessionLine(t, "2025/12/04 03:38:22.134", twoTeamSession("R", []EchoVRPlayer{b}, []EchoVRPlayer{a})), // swap
	}
	path := filepath.Join(dir, "t.echoreplay")
	writeLines(t, path, lines)
	ctx, frames, _, err := NewEchoReplayParser().ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.PlayerIDs) != 2 {
		t.Errorf("roster = %v", ctx.PlayerIDs)
	}
	if ctx.TeamAssignments["echovr:1"] != "orange" || ctx.TeamAssignments["echovr:2"] != "blue" {
		t.Errorf("latest teams not applied: %v", ctx.TeamAssignments)
	}
	// Per-frame team survives the swap.
	if frames[1].Team != "blue" || frames[len(frames)-1].Team != "orange" {
		t.Errorf("per-frame teams: %q ... %q", frames[1].Team, frames[len(frames)-1].Team)
	}
}

// F183: mapping loss and no-team lines are visible in diagnostics.
func TestReplayParser_DiagnosticsCountMappingLoss(t *testing.T) {
	dir := t.TempDir()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	zero := testPlayer("Z", 3, [3]float64{})
	spec := testPlayer("S", 9, [3]float64{0, 5, 0})
	s := twoTeamSession("R", []EchoVRPlayer{a, zero}, nil)
	s.Teams = append(s.Teams, EchoVRTeam{TeamName: "SPECTATORS", Players: []EchoVRPlayer{spec}})
	lines := []string{
		"2025/12/04 03:38:21.900\t{\"sessionid\":\"R\",\"game_status\":\"pre_match\"}",
		sessionLine(t, "2025/12/04 03:38:22.000", s),
		sessionLine(t, "2025/12/04 03:38:22.067", s),
	}
	path := filepath.Join(dir, "t.echoreplay")
	writeLines(t, path, lines)
	_, frames, diag, err := NewEchoReplayParser().ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Errorf("frames = %d, want 2", len(frames))
	}
	if diag.SnapshotsNoTeams != 1 {
		t.Errorf("SnapshotsNoTeams = %d", diag.SnapshotsNoTeams)
	}
	if diag.PlayerFramesRejected != 2 || diag.RejectionsByField["body.position"] != 2 {
		t.Errorf("rejections = %d %v", diag.PlayerFramesRejected, diag.RejectionsByField)
	}
	if diag.SpectatorEntriesDropped != 2 || diag.PlayerEntriesSeen != 4 || diag.FramesMapped != 2 {
		t.Errorf("spectators=%d entries=%d mapped=%d", diag.SpectatorEntriesDropped, diag.PlayerEntriesSeen, diag.FramesMapped)
	}
	if diag.PlayerCount != 2 {
		t.Errorf("PlayerCount = %d (spectator must not be counted)", diag.PlayerCount)
	}
	report := diag.FormatReport()
	if !strings.Contains(report, "Player frames rejected:        2 (body.position=2)") {
		t.Errorf("report does not show rejections:\n%s", report)
	}
}

func TestReplayParser_DedupeOptIn(t *testing.T) {
	dir := t.TempDir()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	same := twoTeamSession("R", []EchoVRPlayer{a}, nil)
	lines := []string{
		sessionLine(t, "2025/12/04 03:38:22.000", same),
		sessionLine(t, "2025/12/04 03:38:22.067", same),
		sessionLine(t, "2025/12/04 03:38:22.134", same),
	}
	path := filepath.Join(dir, "t.echoreplay")
	writeLines(t, path, lines)

	_, frames, _, err := NewEchoReplayParser().ParseFile(path)
	if err != nil || len(frames) != 3 {
		t.Fatalf("default should keep all lines: %d frames, err %v", len(frames), err)
	}
	p := NewEchoReplayParser()
	p.SetDedupeIdentical(true)
	_, frames, diag, err := p.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || diag.SnapshotsDuplicate != 2 {
		t.Errorf("dedupe: frames=%d duplicates=%d", len(frames), diag.SnapshotsDuplicate)
	}
}

// F185: streaming API, ZIP entry selection and size bounds.
func TestReplayParser_StreamAndZipEntrySelection(t *testing.T) {
	dir := t.TempDir()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	lines := []string{
		sessionLine(t, "2025/12/04 03:38:22.000", twoTeamSession("R", []EchoVRPlayer{a}, nil)),
		sessionLine(t, "2025/12/04 03:38:22.067", twoTeamSession("R", []EchoVRPlayer{a}, nil)),
	}
	zipPath := filepath.Join(dir, "rec.echoreplay")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	// Decoys first: a directory, a macOS resource fork, a metadata file.
	if _, err := zw.Create("rec/"); err != nil {
		t.Fatal(err)
	}
	w, _ := zw.Create("__MACOSX/._rec.echoreplay")
	w.Write([]byte("resource fork junk"))
	w, _ = zw.Create("metadata.txt")
	w.Write([]byte("recorder=test\n"))
	w, _ = zw.Create("rec/rec.echoreplay")
	w.Write([]byte(strings.Join(lines, "\n") + "\n"))
	zw.Close()
	f.Close()

	var ticks []*ParsedTick
	p := NewEchoReplayParser()
	ctx, diag, err := p.ParseFileStream(zipPath, func(tick *ParsedTick) error {
		ticks = append(ticks, tick)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.MatchID != "R" || len(ticks) != 2 {
		t.Fatalf("ctx=%+v ticks=%d", ctx, len(ticks))
	}
	if ticks[1].FrameIndex != 1 || len(ticks[1].Frames) != 1 || !strings.HasPrefix(ticks[1].RawJSON, "{") {
		t.Errorf("tick = %+v", ticks[1])
	}
	if ticks[1].SampleTime.Sub(ticks[0].SampleTime) != 67*time.Millisecond {
		t.Errorf("sample times: %v %v", ticks[0].SampleTime, ticks[1].SampleTime)
	}
	if len(p.RawSessionByFrame()) != 0 {
		t.Error("streaming must not accumulate raw payloads")
	}
	if diag.FramesMapped != 2 {
		t.Errorf("diag mapped = %d", diag.FramesMapped)
	}

	// Callback errors abort parsing.
	stop := errors.New("stop")
	_, _, err = NewEchoReplayParser().ParseFileStream(zipPath, func(*ParsedTick) error { return stop })
	if !errors.Is(err, stop) {
		t.Errorf("callback error not propagated: %v", err)
	}

	// Size bound: the central-directory size rejects early...
	p2 := NewEchoReplayParser()
	p2.maxReplayBytes = 64
	if _, _, _, err := p2.ParseFile(zipPath); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected size-limit error, got %v", err)
	}
	// ...and the stream itself is bounded for archives whose header understates the size.
	p3 := NewEchoReplayParser()
	p3.maxReplayBytes = 64
	br := &boundedReader{r: strings.NewReader(strings.Join(lines, "\n") + "\n"), limit: p3.maxReplayBytes}
	if _, _, err := p3.parseReader(br, "rec.echoreplay", func(*ParsedTick) error { return nil }); !errors.Is(err, errReplayTooLarge) {
		t.Errorf("stream bound not enforced: %v", err)
	}
}

func TestSelectZipEntry(t *testing.T) {
	mk := func(name string, size uint64) *zip.File {
		zf := &zip.File{}
		zf.Name = name
		zf.UncompressedSize64 = size
		return zf
	}
	if got := selectZipEntry([]*zip.File{mk("dir/", 0), mk("__MACOSX/._x", 900), mk("big.bin", 5000), mk("x.echoreplay", 100)}); got == nil || got.Name != "x.echoreplay" {
		t.Errorf("extension preference failed: %v", got)
	}
	if got := selectZipEntry([]*zip.File{mk("a.dat", 10), mk("b.dat", 20)}); got == nil || got.Name != "b.dat" {
		t.Errorf("largest fallback failed: %v", got)
	}
	if got := selectZipEntry([]*zip.File{mk("dir/", 0), mk(".hidden", 5)}); got != nil {
		t.Errorf("expected nil, got %v", got.Name)
	}
}

// F186: oversize lines produce a clear error naming the limit.
func TestReplayParser_OversizeLineError(t *testing.T) {
	dir := t.TempDir()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	good := sessionLine(t, "2025/12/04 03:38:22.000", twoTeamSession("R", []EchoVRPlayer{a}, nil))
	huge := "2025/12/04 03:38:22.067\t" + strings.Repeat("x", 5000)
	path := filepath.Join(dir, "t.echoreplay")
	writeLines(t, path, []string{good, huge})

	p := NewEchoReplayParser()
	p.SetMaxLineBytes(4096)
	_, frames, _, err := p.ParseFile(path)
	if err == nil {
		t.Fatal("expected error for oversize line")
	}
	if !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "4096") {
		t.Errorf("error should name the line and the cap: %v", err)
	}
	if len(frames) != 1 {
		t.Errorf("frames parsed before the oversize line should be returned: %d", len(frames))
	}

	// Default cap comfortably holds a 1 MB line.
	big := twoTeamSession("R", []EchoVRPlayer{a}, nil)
	big.ClientName = strings.Repeat("n", 1024*1024)
	writeLines(t, path, []string{good, sessionLine(t, "2025/12/04 03:38:22.067", big)})
	if _, frames, _, err := NewEchoReplayParser().ParseFile(path); err != nil || len(frames) != 2 {
		t.Errorf("1MB line: frames=%d err=%v", len(frames), err)
	}
}

func TestReplayParser_ThreeTeamFixtureLine(t *testing.T) {
	data, err := os.ReadFile(fixtureDir + "echovr_session_three_teams.json")
	if err != nil {
		t.Fatal(err)
	}
	// A replay line is the fixture JSON on one line (no string value contains a newline).
	oneLine := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r", ""), "\n", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "t.echoreplay")
	writeLines(t, path, []string{
		"2025/12/04 03:38:22.000\t" + oneLine,
		fmt.Sprintf("2025/12/04 03:38:22.067\t%s", oneLine),
	})
	ctx, frames, diag, err := NewEchoReplayParser().ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 4 {
		t.Errorf("frames = %d, want 4 (2 ticks x 2 players)", len(frames))
	}
	for _, f := range frames {
		if f.PlayerID == "echovr:9001" {
			t.Error("spectator reached the pipeline")
		}
		if f.Disc == nil || !f.Disc.IsHeld || f.Disc.PossessorID != "echovr:2001" {
			t.Errorf("%s: disc should be held by echovr:2001: %+v", f.PlayerID, f.Disc)
		}
	}
	if len(ctx.PlayerIDs) != 2 || diag.SpectatorEntriesDropped != 2 {
		t.Errorf("roster=%v spectators=%d", ctx.PlayerIDs, diag.SpectatorEntriesDropped)
	}
}
