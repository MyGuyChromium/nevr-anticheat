package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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
	candidates := replayViewerCandidates("windows", func(k string) string { return env[k] }, `C:\NEVR`)
	if got, want := candidates[0], env["NEVR_REPLAY_VIEWER"]; got != want {
		t.Fatalf("first viewer candidate = %q, want override %q", got, want)
	}
	wantSpark := filepath.Join(env["USERPROFILE"], "Documents", "Replay Viewer", "Replay Viewer.exe")
	if candidates[1] != wantSpark {
		t.Errorf("Spark install candidate = %q, want %q", candidates[1], wantSpark)
	}
	seenSibling := false
	for _, candidate := range candidates {
		if candidate == filepath.Join(`C:\NEVR`, "Replay Viewer.exe") {
			seenSibling = true
		}
	}
	if !seenSibling {
		t.Errorf("portable sibling viewer missing from %+v", candidates)
	}
}
