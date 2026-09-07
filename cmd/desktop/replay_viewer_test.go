package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
