package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSparkReplayClipRequiresExactIncidentFrame(t *testing.T) {
	s, _ := newTestServer(t)
	stopRuntimeForTest(t, s)
	if _, err := s.engine.AnalyzeFileAll(context.Background(), fixturePath, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.engine.Store().DB().Exec(`DELETE FROM match_ticks WHERE match_id = ? AND frame_index = 10`, "SYN-FIXTURE-001"); err != nil {
		t.Fatal(err)
	}
	event := testLabEvent("SYN-FIXTURE-001")
	clip, err := buildSparkReplayClip(context.Background(), s.engine.Store(), s.clipDir, event)
	if !errors.Is(err, errRawReplayUnavailable) || clip != nil {
		t.Fatalf("clip claimed exact incident despite missing selected frame: %+v, %v", clip, err)
	}
	if files, _ := os.ReadDir(s.clipDir); len(files) != 0 {
		t.Fatal("unavailable incident left an unrelated clip")
	}
}

func TestSparkReplayClipOverflowCannotSelectUnrelatedFrames(t *testing.T) {
	s, _ := newTestServer(t)
	stopRuntimeForTest(t, s)
	if _, err := s.engine.AnalyzeFileAll(context.Background(), fixturePath, true); err != nil {
		t.Fatal(err)
	}
	event := testLabEvent("SYN-FIXTURE-001")
	event.FrameIndex = int(^uint(0) >> 1)
	event.FrameRangeStart, event.FrameRangeEnd = event.FrameIndex, event.FrameIndex
	if clip, err := buildSparkReplayClip(context.Background(), s.engine.Store(), s.clipDir, event); !errors.Is(err, errRawReplayUnavailable) || clip != nil {
		t.Fatalf("overflow frame result = %+v, %v", clip, err)
	}
}

func TestSparkReplayClipInvalidRawRemovesPartialClip(t *testing.T) {
	s, _ := newTestServer(t)
	stopRuntimeForTest(t, s)
	if _, err := s.engine.AnalyzeFileAll(context.Background(), fixturePath, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.engine.Store().DB().Exec(`UPDATE match_ticks SET raw_json = 'invalid' WHERE match_id = ? AND frame_index = 10`, "SYN-FIXTURE-001"); err != nil {
		t.Fatal(err)
	}
	if clip, err := buildSparkReplayClip(context.Background(), s.engine.Store(), s.clipDir, testLabEvent("SYN-FIXTURE-001")); err == nil || clip != nil {
		t.Fatalf("corrupt clip accepted: %+v, %v", clip, err)
	}
	if files, _ := os.ReadDir(s.clipDir); len(files) != 0 {
		t.Fatal("corrupt replay left a partially playable clip")
	}
}

func TestSparkReplayViewerPreservesExactPathAsSingleArgument(t *testing.T) {
	viewer := filepath.Join(t.TempDir(), "Spark viewer", "Replay Viewer.exe")
	relativeClip := filepath.Join("relative clips", "semi; dollar$ apostrophe' clip.echoreplay")
	wantClip, err := filepath.Abs(relativeClip)
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	got, err := launchSparkReplayViewerWith(relativeClip, []string{viewer}, func(cmd *exec.Cmd) error {
		called++
		if !reflect.DeepEqual(cmd.Args, []string{viewer, wantClip}) || cmd.Dir != filepath.Dir(viewer) {
			t.Fatalf("viewer command changed clip or split arguments: %+v", cmd)
		}
		return nil // Never starts a real viewer.
	})
	if err != nil || got != viewer || called != 1 {
		t.Fatalf("viewer launch = %q, %v, calls=%d", got, err, called)
	}
}

// "Open in Spark" used to run a file named "Replay Viewer.exe" found next to
// nevr-desktop.exe, which for the portable build is usually Downloads or the
// Desktop. Mutation: put the executable-directory candidates back, or drop the
// insideAnyDir check, and a planted file is resolved.
func TestReplayViewerIsNeverResolvedFromTheAppFolder(t *testing.T) {
	appDir := t.TempDir()
	planted := filepath.Join(appDir, "Replay Viewer.exe")
	nested := filepath.Join(appDir, "Replay Viewer", "Replay Viewer.exe")
	if err := os.MkdirAll(filepath.Dir(nested), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{planted, nested} {
		if err := os.WriteFile(path, []byte("not the viewer"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	env := map[string]string{"USERPROFILE": home}
	getenv := func(k string) string { return env[k] }
	for _, candidate := range replayViewerCandidates("windows", getenv) {
		if strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(appDir)) {
			t.Fatalf("the app folder is a viewer candidate: %s", candidate)
		}
	}
	// PATH (or a relative lookup) pointing back into the app folder is refused too.
	lookPath := func(name string) (string, error) { return filepath.Join(appDir, name), nil }
	if got := resolveReplayViewers(replayViewerCandidates("windows", getenv), "", []string{appDir}, lookPath); len(got) != 0 {
		t.Fatalf("resolved a viewer from the app folder: %v", got)
	}

	// Spark's real install location still resolves, and so does an explicit override.
	installed := filepath.Join(home, "Documents", "Replay Viewer", "Replay Viewer.exe")
	if err := os.MkdirAll(filepath.Dir(installed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("viewer"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := resolveReplayViewers(replayViewerCandidates("windows", getenv), "", []string{appDir}, lookPath)
	if len(got) != 1 || got[0] != installed {
		t.Fatalf("installed viewer: %v", got)
	}
	env["NEVR_REPLAY_VIEWER"] = planted
	got = resolveReplayViewers(replayViewerCandidates("windows", getenv), planted, []string{appDir}, lookPath)
	if len(got) == 0 || got[0] != planted {
		t.Fatalf("explicit override was not honoured: %v", got)
	}
}
