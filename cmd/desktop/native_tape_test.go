package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	capturepb "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func nativeDesktopFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic-native.tape")
	if err := testutil.WriteNativeTapeFixture(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// Real HTTP upload and SQLite persistence with the real native codec, but only
// synthetic stationary telemetry. Spark launch is stubbed; this is not native
// viewer acceptance or independent detector-accuracy validation.
func TestDesktopNativeTapeImportDuplicateReopenAndClip(t *testing.T) {
	s, ts := newTestServer(t)
	path := nativeDesktopFixture(t)
	var matchID string
	for pass := 0; pass < 2; pass++ {
		resp, out := upload(t, ts, false, map[string]string{"synthetic-native.TAPE": path})
		if resp.StatusCode != http.StatusOK || len(out.Results) != 1 || !out.Results[0].OK {
			t.Fatalf("native upload %d: HTTP %d %+v", pass, resp.StatusCode, out)
		}
		m := out.Results[0].Match
		if m == nil || m.Source != "tape" || m.NativeCapture == nil || m.NativeCapture.CaptureID != testutil.NativeTapeCaptureID || m.NativeCapture.ContainerIntegrity != "verified_footer_and_checksum" || m.SourceFile != "synthetic-native.TAPE" || m.FramesProcessed != testutil.NativeTapeFrames || len(m.Players) != 1 || m.Players[0].PlayerID != testutil.NativeTapePlayerID {
			t.Fatalf("native match metadata: %+v", m)
		}
		if pass == 1 && (m.MatchID != matchID || !m.Replaced || out.Results[0].AlreadyStored) {
			t.Fatalf("duplicate upload did not refresh the same capture: %+v", out.Results[0])
		}
		matchID = m.MatchID
	}
	ctx := context.Background()
	if _, err := s.engine.Store().StoreMatchLabel(ctx, matchID, "suspected", "Synthetic workflow note; not a gameplay finding", "tester", "test", "test-config"); err != nil {
		t.Fatal(err)
	}
	raw, err := s.engine.Store().GetMatchRawTicks(ctx, matchID, 0, testutil.NativeTapeFrames)
	if err != nil || len(raw) != testutil.NativeTapeFrames {
		t.Fatalf("native ticks not persisted: %d %v", len(raw), err)
	}

	// Close and reopen the actual disposable database, not merely a fresh view.
	dbPath, cfg := s.engine.Store().Path(), s.engine.Config()
	stopRuntimeForTest(t, s)
	ts.Close()
	if err := s.engine.Store().Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	next := newServer(replay.NewEngine(cfg, reopened), testToken)
	t.Cleanup(func() { stopRuntimeForTest(t, next) })
	next.clipDir = t.TempDir()
	nextHTTP := httptest.NewServer(next.Handler())
	t.Cleanup(nextHTTP.Close)
	base := nextHTTP.URL + "/" + testToken
	var stored matchView
	if resp := getJSON(t, base+"/api/match/"+matchID, &stored); resp.StatusCode != http.StatusOK || stored.Source != "tape" || stored.NativeCapture == nil || stored.NativeCapture.CaptureID != testutil.NativeTapeCaptureID || stored.NativeCapture.ContainerIntegrity != "verified_footer_and_checksum" || stored.FramesProcessed != testutil.NativeTapeFrames || stored.CalibrationComment != "Synthetic workflow note; not a gameplay finding" {
		t.Fatalf("native stored review after reopen: HTTP %d %+v", resp.StatusCode, stored)
	}
	resp, err := http.Get(base + "/api/match/" + matchID + "/export.csv")
	if err != nil {
		t.Fatal(err)
	}
	rows, csvErr := csv.NewReader(resp.Body).ReadAll()
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || csvErr != nil || len(rows) != 2 || rows[1][0] != testutil.NativeTapePlayerID {
		t.Fatalf("native CSV export: HTTP %d rows=%v err=%v", resp.StatusCode, rows, csvErr)
	}
	launched := ""
	next.launchReplay = func(path string) (string, error) { launched = path; return "synthetic-viewer-stub", nil }
	var clip struct {
		OK       bool   `json:"ok"`
		Message  string `json:"message"`
		ClipFile string `json:"clip_file"`
		Derived  bool   `json:"derived_from_native"`
		Frames   int    `json:"frames"`
	}
	resp = postAPI(t, base+"/api/match/"+matchID+"/replay/frame/5", nil, &clip)
	if resp.StatusCode != http.StatusOK || !clip.OK || !clip.Derived || clip.Frames != testutil.NativeTapeFrames || launched != clip.ClipFile || !strings.Contains(clip.Message, "derived viewing clip") {
		t.Fatalf("native Spark clip: HTTP %d %+v", resp.StatusCode, clip)
	}
	clipBytes, err := os.ReadFile(clip.ClipFile)
	if err != nil || !strings.Contains(string(clipBytes), "Synthetic Native Player") || len(strings.Split(strings.TrimSpace(string(clipBytes)), "\n")) != testutil.NativeTapeFrames {
		t.Fatalf("derived viewing clip content: %v", err)
	}
}

func TestDesktopNativeTapeCorruptionDoesNotReplaceStoredMatch(t *testing.T) {
	s, ts := newTestServer(t)
	path := nativeDesktopFixture(t)
	_, initial := upload(t, ts, false, map[string]string{"good.tape": path})
	if len(initial.Results) != 1 || !initial.Results[0].OK {
		t.Fatalf("fixture upload: %+v", initial)
	}
	matchID := initial.Results[0].MatchID
	before, err := s.engine.Store().GetMatchRawTicks(context.Background(), matchID, 0, testutil.NativeTapeFrames)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "cut.tape")
	if err := os.WriteFile(bad, bytes[:len(bytes)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	resp, out := upload(t, ts, false, map[string]string{"cut.tape": bad})
	if resp.StatusCode != http.StatusOK || len(out.Results) != 1 || out.Results[0].OK || out.Results[0].Error == "" || out.Results[0].Diagnostic == nil {
		t.Fatalf("truncated native upload reported success: HTTP %d %+v", resp.StatusCode, out)
	}
	d := out.Results[0].Diagnostic
	if d.Lines != 0 || !strings.Contains(d.Hint, "complete native .tape") || strings.Contains(d.Hint, "timestamp and a tab") {
		t.Fatalf("binary failure presented as malformed text: %+v", d)
	}
	after, err := s.engine.Store().GetMatchRawTicks(context.Background(), matchID, 0, testutil.NativeTapeFrames)
	if err != nil || len(after) != len(before) {
		t.Fatalf("corrupt input changed stored ticks: before=%d after=%d err=%v", len(before), len(after), err)
	}
	for index, value := range before {
		if after[index] != value {
			t.Fatalf("stored native tick %d changed after corrupt upload", index)
		}
	}
}

func TestDesktopNativeTapeWatchAndRecovery(t *testing.T) {
	s, _ := newTestServer(t)
	stopRuntimeForTest(t, s)
	watch := t.TempDir()
	path := filepath.Join(watch, "watched.tape")
	if err := testutil.WriteNativeTapeFixture(path); err != nil {
		t.Fatal(err)
	}
	s.runtime.settings.WatchFolder = watch
	if count, err := s.runtime.scanWatchFolder(context.Background()); err != nil || count != 0 {
		t.Fatalf("unstable native file analyzed: %d %v", count, err)
	}
	old := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if count, err := s.runtime.scanWatchFolder(context.Background()); err != nil || count != 1 {
		t.Fatalf("stable native watch: %d %v", count, err)
	}
	if count, err := s.runtime.scanWatchFolder(context.Background()); err != nil || count != 0 {
		t.Fatalf("unchanged native watch repeated: %d %v", count, err)
	}
	pending := filepath.Join(s.runtime.pendingDir, "recovered.tape")
	if err := testutil.WriteNativeTapeFixture(pending); err != nil {
		t.Fatal(err)
	}
	s.runtime.resumePending(context.Background())
	if s.runtime.recovered != 1 || s.runtime.recoveryErr != "" {
		t.Fatalf("native recovery: count=%d error=%s", s.runtime.recovered, s.runtime.recoveryErr)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("successfully recovered native input still pending: %v", err)
	}
}

func TestDesktopNativeTapeDiagnosticDoesNotMisidentifyZip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-native.tape")
	if err := os.WriteFile(path, []byte("PK\x03\x04not-a-native-capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := diagnoseUpload(path)
	if d.Container != "zip" || !strings.Contains(strings.Join(d.Findings, " "), "not a native tape") {
		t.Fatalf("diagnostic=%+v", d)
	}
	// Diagnostic probes leave the source bytes untouched.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "PK\x03\x04not-a-native-capture" {
		t.Fatal("diagnostic modified the source")
	}
}

func TestDesktopNativeTapeClipKeepsExactNativeTimesWithoutPlayerRows(t *testing.T) {
	s, _ := newTestServer(t)
	stopRuntimeForTest(t, s)
	ctx := context.Background()
	created := time.Date(2026, 2, 3, 4, 5, 6, 123456789, time.UTC)
	raw := make(map[int]string)
	wantTimes := make(map[int]time.Time)
	// Adapt only this synthetic recording to cover a non-zero first offset,
	// fractional header time, and an event-only tick without normalized players.
	mc, _, err := adapter.NewEchoReplayParser().ParseFileStream(nativeDesktopFixture(t), func(tick *adapter.ParsedTick) error {
		var record struct {
			Native adapter.TapeRawRecord `json:"_nevr_tape"`
		}
		if err := json.Unmarshal([]byte(tick.RawJSON), &record); err != nil {
			return err
		}
		header, frame := new(capturepb.CaptureHeader), new(capturepb.Frame)
		if err := proto.Unmarshal(record.Native.Header, header); err != nil {
			return err
		}
		if err := proto.Unmarshal(record.Native.Frame, frame); err != nil {
			return err
		}
		header.CreatedAt = timestamppb.New(created)
		frame.TimestampOffsetMs += 1700
		if tick.FrameIndex == 5 {
			frame.GetEchoArena().Players = nil
		}
		var err error
		if record.Native.Header, err = proto.Marshal(header); err != nil {
			return err
		}
		if record.Native.Frame, err = proto.Marshal(frame); err != nil {
			return err
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return err
		}
		derived, err := adapter.NewTapeRawDecoder().Decode(string(encoded))
		if err != nil {
			return err
		}
		raw[tick.FrameIndex] = derived.RawJSON
		wantTimes[tick.FrameIndex] = created.Add(time.Duration(frame.TimestampOffsetMs) * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately stale cache timing and no player rows: the clip must read
	// every timestamp from the preserved native record, never invent 15 Hz.
	mc.StartTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.engine.Store().StoreMatchContext(ctx, mc, len(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.engine.Store().StoreTelemetryFramesWithRaw(ctx, mc.MatchID, nil, raw); err != nil {
		t.Fatal(err)
	}
	event := model.DetectionEvent{MatchID: mc.MatchID, DetectorID: "synthetic-workflow", FrameIndex: 5}
	clip, err := buildSparkReplayClip(ctx, s.engine.Store(), s.clipDir, event)
	if err != nil {
		t.Fatal(err)
	}
	if !clip.DerivedFromNative || clip.Frames != testutil.NativeTapeFrames {
		t.Fatalf("native clip metadata: %+v", clip)
	}
	data, err := os.ReadFile(clip.Path)
	if err != nil {
		t.Fatal(err)
	}
	for idx, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			t.Fatalf("clip line %d has no timestamp", idx)
		}
		stamp, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil || !stamp.Equal(wantTimes[idx]) || parts[1] != raw[idx] {
			t.Fatalf("clip changed time or native record at %d: stamp=%v want=%v err=%v", idx, stamp, wantTimes[idx], err)
		}
	}
	seen := 0
	reimported, _, err := adapter.NewEchoReplayParser().ParseFileStream(clip.Path, func(tick *adapter.ParsedTick) error {
		if !tick.SampleTime.Equal(wantTimes[tick.FrameIndex]) || !adapter.IsTapeRawJSON(tick.RawJSON) {
			t.Fatalf("derived clip reimport lost native time/provenance: %+v", tick)
		}
		if tick.FrameIndex == 5 && len(tick.Frames) != 0 {
			t.Fatal("event-only tick invented player observations")
		}
		seen++
		return nil
	})
	if err != nil || seen != testutil.NativeTapeFrames || reimported == nil || reimported.Source != "tape" || reimported.NativeCapture == nil || reimported.NativeCapture.CaptureID != testutil.NativeTapeCaptureID || reimported.NativeCapture.ContainerIntegrity != "not_verified_from_records" {
		t.Fatalf("native clip reimport: ticks=%d context=%+v err=%v", seen, reimported, err)
	}
	if _, err := s.engine.Store().DB().Exec(`UPDATE match_ticks SET raw_json = '{"_nevr_tape":null}' WHERE match_id = ? AND frame_index = 5`, mc.MatchID); err != nil {
		t.Fatal(err)
	}
	if bad, err := buildSparkReplayClip(ctx, s.engine.Store(), s.clipDir, event); err == nil || bad != nil {
		t.Fatalf("invalid native timing fell back to invented playback: %+v %v", bad, err)
	}
	if files, err := os.ReadDir(s.clipDir); err != nil || len(files) != 1 {
		t.Fatalf("failed native export left a partial clip: %d %v", len(files), err)
	}
}
