package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// TestRunAnalyze_TwoSessions: a recording whose session id changes mid-file
// is analyzed as two matches. The command prints the file's diagnostics
// once and one summary block per match, stores both without any tick
// colliding, refuses both on a re-run and replaces both with --force.
func TestRunAnalyze_TwoSessions(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.toml")
	db := filepath.ToSlash(filepath.Join(dir, "app.db"))
	if err := os.WriteFile(cfgPath, []byte("[general]\ndb_path = \""+db+"\"\nlog_level = \"error\"\n[detector.MOV_006]\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	replayPath := filepath.Join(dir, "rematch.echoreplay")
	src := filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay")
	if _, _, err := testutil.SplitReplaySessions(src, replayPath, "SYN-FIXTURE-002", 10*time.Minute); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() { runAnalyze(cfgPath, replayPath, false) })
	if !strings.Contains(out, "Parsed 480 player-frames from "+replayPath) || strings.Count(out, "Session id changes (matches):  1 (2 matches in file)") != 1 {
		t.Errorf("file header and diagnostics should appear once:\n%s", out)
	}
	if strings.Count(out, "Match: ") != 2 || !strings.Contains(out, "Match: SYN-FIXTURE-001\n") || !strings.Contains(out, "Match: SYN-FIXTURE-002\n") {
		t.Errorf("analyze output should hold one summary block per match:\n%s", out)
	}
	if n := strings.Count(out, "Stored 240 telemetry frames (0 already present), 60 raw ticks (0 already present)"); n != 2 {
		t.Errorf("each match stores its own 60 ticks with nothing already present (%d blocks):\n%s", n, out)
	}
	if strings.Count(out, "Frames: 60 processed, 0 invalid") != 2 || strings.Contains(out, "already stored") {
		t.Errorf("analyze output:\n%s", out)
	}

	out = captureStdout(t, func() { runAnalyze(cfgPath, replayPath, false) })
	for _, want := range []string{"Match SYN-FIXTURE-001 is already stored", "Match SYN-FIXTURE-002 is already stored"} {
		if !strings.Contains(out, want) {
			t.Errorf("second analyze lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Match: ") || !strings.Contains(out, "Parsed 480 player-frames from "+replayPath) {
		t.Errorf("second analyze without --force should refuse both matches and still report the file it read:\n%s", out)
	}

	out = captureStdout(t, func() { runAnalyze(cfgPath, replayPath, true) })
	if strings.Count(out, "Cleared previous analysis for") != 2 || strings.Count(out, "Match: ") != 2 ||
		strings.Count(out, "Stored 0 telemetry frames (240 already present), 0 raw ticks (60 already present)") != 2 {
		t.Errorf("forced analyze output:\n%s", out)
	}
}

// captureStdout runs fn and returns what it printed to stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestParseWindow(t *testing.T) {
	cases := map[string]time.Duration{"7d": 7 * 24 * time.Hour, "36h": 36 * time.Hour, "2h30m": 2*time.Hour + 30*time.Minute}
	for in, want := range cases {
		got, err := parseWindow(in)
		if err != nil || got != want {
			t.Errorf("parseWindow(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "x", "3w"} {
		if _, err := parseWindow(bad); err == nil {
			t.Errorf("parseWindow(%q) accepted", bad)
		}
	}
}

func TestIsEchoReplay(t *testing.T) {
	if !isEchoReplay("rec.echoreplay") || !isEchoReplay("REC.ECHOREPLAY") || isEchoReplay("match.json") {
		t.Error("extension detection")
	}
}

func TestSortedHelpers(t *testing.T) {
	scores := map[string]model.SuspicionScore{"b": {PlayerID: "b", TotalScore: 5, EventCount: 1}, "a": {PlayerID: "a"}, "c": {PlayerID: "c", TotalScore: 70, EventCount: 3}}
	if got := sortedPlayerIDs(scores); strings.Join(got, ",") != "a,b,c" {
		t.Errorf("sortedPlayerIDs = %v", got)
	}
	if got := sortedKeys(map[string]int{"z": 1, "m": 2}); strings.Join(got, ",") != "m,z" {
		t.Errorf("sortedKeys = %v", got)
	}
	out := captureStdout(t, func() { printPlayerScores(scores) })
	if strings.Contains(out, "Player a") || !strings.Contains(out, "Player b: score=5.0") || !strings.Contains(out, "Player c: score=70.0 level=high_risk") {
		t.Errorf("printPlayerScores output:\n%s", out)
	}
}

func TestMultiFlag(t *testing.T) {
	var m multiFlag
	_ = m.Set("MOV_001:yes")
	_ = m.Set("THROW_001:no")
	if m.String() != "MOV_001:yes,THROW_001:no" {
		t.Errorf("multiFlag = %q", m.String())
	}
}

// TestOpenApp_DefaultsAndConfigFile: the app opens with the built-in
// defaults (empty path) and with a config file; the scorer, physics and
// level table come from the config.
func TestOpenApp_DefaultsAndConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.toml")
	db := filepath.ToSlash(filepath.Join(dir, "app.db"))
	if err := os.WriteFile(cfgPath, []byte("[general]\ndb_path = \""+db+"\"\n[scoring]\nreview_threshold = 45\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := openApp(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	defer a.store.Close()
	if a.levels().HighRisk != 45 || a.scorerConfig().Levels.HighRisk != 45 {
		t.Errorf("review threshold did not reach the level table: %+v", a.levels())
	}
	if a.physics().GoalZ != model.DefaultPhysics().GoalZ {
		t.Errorf("physics = %+v", a.physics())
	}
	if a.crossMatchConfig().Levels.HighRisk != 45 || a.analysisOptions().Levels.HighRisk != 45 {
		t.Error("cross-match / analysis options do not share the level table")
	}
	if p := a.newPipeline(); p == nil {
		t.Error("newPipeline returned nil")
	}
	if _, err := openApp(filepath.Join(dir, "missing.toml")); err == nil {
		t.Error("missing config accepted")
	}
}

// TestRunAnalyze_SyntheticReplay runs the analyze command end to end on
// the committed synthetic .echoreplay: parse, detect, store telemetry and
// raw ticks, then refuse a re-run without --force.
func TestRunAnalyze_SyntheticReplay(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.toml")
	db := filepath.ToSlash(filepath.Join(dir, "app.db"))
	if err := os.WriteFile(cfgPath, []byte("[general]\ndb_path = \""+db+"\"\nlog_level = \"error\"\n[detector.MOV_006]\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	replayPath := filepath.Join("..", "..", "tests", "fixtures", "synthetic_session.echoreplay")
	out := captureStdout(t, func() { runAnalyze(cfgPath, replayPath, false) })
	for _, want := range []string{"Match: SYN-FIXTURE-001", "Frames: 120 processed, 0 invalid", "Stored 480 telemetry frames", "120 raw ticks"} {
		if !strings.Contains(out, want) {
			t.Errorf("analyze output lacks %q:\n%s", want, out)
		}
	}
	out = captureStdout(t, func() { runAnalyze(cfgPath, replayPath, false) })
	if !strings.Contains(out, "already stored") {
		t.Errorf("second analyze should refuse without --force:\n%s", out)
	}
	out = captureStdout(t, func() { runAnalyze(cfgPath, replayPath, true) })
	if !strings.Contains(out, "Cleared previous analysis") || !strings.Contains(out, "480 already present") {
		t.Errorf("forced analyze output:\n%s", out)
	}
	out = captureStdout(t, func() { runReprocessMatch(cfgPath, "SYN-FIXTURE-001") })
	if !strings.Contains(out, "SYN-FIXTURE-001") {
		t.Errorf("reprocess output:\n%s", out)
	}
	out = captureStdout(t, func() { runFlagged(cfgPath) })
	if out == "" {
		t.Error("flagged printed nothing")
	}
	// Report commands on a store without moderator decisions or events.
	out = captureStdout(t, func() { runPlayerHistory(cfgPath, "echovr:1001") })
	if !strings.Contains(out, "echovr:1001") {
		t.Errorf("player-history output:\n%s", out)
	}
	out = captureStdout(t, func() { runCrossMatchAnalysis(cfgPath) })
	if !strings.Contains(out, "Cross-match aggregation complete") {
		t.Errorf("cross-match output:\n%s", out)
	}
	for _, since := range []string{"", "7d"} {
		out = captureStdout(t, func() { runCalibrationReport(cfgPath, since) })
		if !strings.Contains(out, "DETECTOR CALIBRATION") || !strings.Contains(out, "No moderator decisions") {
			t.Errorf("calibration report (since %q):\n%s", since, out)
		}
	}
	out = captureStdout(t, func() { runObservationReport(cfgPath, "", false) })
	if !strings.Contains(out, "DETECTOR OBSERVATIONS") || !strings.Contains(out, "No detection events stored yet") {
		t.Errorf("observation report:\n%s", out)
	}
	out = captureStdout(t, func() { runObservationReport(cfgPath, "7d", true) })
	var observations observationReport
	if err := json.Unmarshal([]byte(out), &observations); err != nil || observations.Since == nil || observations.Stats == nil || observations.Notice == "" {
		t.Errorf("JSON observation report: %+v, %v; raw=%s", observations, err, out)
	}
	out = captureStdout(t, func() { runReprocessPlayer(cfgPath, "echovr:1001") })
	if !strings.Contains(out, "SYN-FIXTURE-001") {
		t.Errorf("reprocess-player output:\n%s", out)
	}
}
