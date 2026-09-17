package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDesktopSingleInstanceAsksTheRunningAppToShowItsWindow(t *testing.T) {
	db := filepath.Join(t.TempDir(), "evidence.db")
	first, running := claimDesktopInstance(db, true)
	if first == nil || running {
		t.Fatalf("first claim = %#v, %v", first, running)
	}
	defer first.close()
	shown := make(chan struct{}, 4)
	first.publish(func() { shown <- struct{}{} })

	second, running := claimDesktopInstance(db, true)
	if second != nil || !running {
		t.Fatalf("second claim = %#v, %v; want the running instance", second, running)
	}
	select {
	case <-shown:
	default:
		t.Fatal("the running instance was not asked to show its window")
	}
	// --no-browser only finds out that the app runs; it opens nothing.
	if third, running := claimDesktopInstance(db, false); third != nil || !running || len(shown) != 0 {
		t.Fatalf("no-browser claim = %#v, %v, shown=%d", third, running, len(shown))
	}
	other, otherRunning := claimDesktopInstance(filepath.Join(t.TempDir(), "other.db"), true)
	if other == nil || otherRunning {
		t.Fatalf("separate database claim = %#v, %v", other, otherRunning)
	}
	_ = other.close()
}

func TestAppWindowCandidatesWindows(t *testing.T) {
	env := map[string]string{
		"ProgramFiles":      `C:\Program Files`,
		"ProgramFiles(x86)": `C:\Program Files (x86)`,
		"LOCALAPPDATA":      `C:\Users\tester\AppData\Local`,
	}
	getenv := func(k string) string { return env[k] }
	url := "http://127.0.0.1:1234/token/"
	candidates := appWindowCandidates("windows", url, getenv)
	if len(candidates) != 8 {
		t.Fatalf("got %d candidates: %+v", len(candidates), candidates)
	}
	if got, want := candidates[0].command, filepath.Join(env["ProgramFiles(x86)"], "Microsoft", "Edge", "Application", "msedge.exe"); got != want {
		t.Errorf("first candidate = %q, want %q", got, want)
	}
	wantArgs := []string{"--app=" + url, "--new-window", "--window-size=1280,900"}
	for _, candidate := range candidates {
		if !reflect.DeepEqual(candidate.args, wantArgs) {
			t.Errorf("%q args = %q, want %q", candidate.command, candidate.args, wantArgs)
		}
	}
}

func TestAppWindowCandidatesSkipMissingAndKeepMacApps(t *testing.T) {
	getenv := func(string) string { return "" }
	win := appWindowCandidates("windows", "http://local/", getenv)
	if len(win) != 2 || win[0].command != "msedge.exe" || win[1].command != "chrome.exe" {
		t.Errorf("empty Windows environment candidates: %+v", win)
	}

	mac := appWindowCandidates("darwin", "http://local/", getenv)
	if len(mac) != 2 || mac[0].command != "open" || mac[1].command != "open" ||
		!strings.Contains(strings.Join(mac[0].args, " "), "Microsoft Edge") ||
		!strings.Contains(strings.Join(mac[1].args, " "), "Google Chrome") {
		t.Errorf("macOS candidates: %+v", mac)
	}
}

func TestReplayViewerCandidatesPreferSparkInstallAndOverride(t *testing.T) {
	env := map[string]string{
		"NEVR_REPLAY_VIEWER": `D:\Portable\Replay Viewer.exe`,
		"USERPROFILE":        `C:\Users\tester`,
		"OneDrive":           `C:\Users\tester\OneDrive`,
		"ProgramFiles":       `C:\Program Files`,
	}
	candidates := replayViewerCandidates("windows", func(k string) string { return env[k] })
	if got, want := candidates[0], env["NEVR_REPLAY_VIEWER"]; got != want {
		t.Fatalf("first viewer candidate = %q, want override %q", got, want)
	}
	wantSpark := filepath.Join(env["USERPROFILE"], "Documents", "Replay Viewer", "Replay Viewer.exe")
	if candidates[1] != wantSpark {
		t.Errorf("Spark install candidate = %q, want %q", candidates[1], wantSpark)
	}
}
