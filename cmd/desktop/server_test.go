package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

const (
	fixturePath = "../../tests/fixtures/synthetic_session.echoreplay"
	testToken   = "0123456789abcdef0123456789abcdef"
)

func newTestServer(t *testing.T) (*server, *httptest.Server) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.General.LogLevel = "error"
	store, err := sqlite.NewStore(filepath.Join(t.TempDir(), "desktop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s := newServer(replay.NewEngine(cfg, store), testToken)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

// upload POSTs the named files (client name -> local path) to /api/analyze.
func upload(t *testing.T, ts *httptest.Server, force bool, files map[string]string) (*http.Response, analyzeResponse) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if force {
		_ = mw.WriteField("force", "1")
	}
	for name, path := range files {
		part, err := mw.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(data)
	}
	_ = mw.Close()
	resp, err := http.Post(ts.URL+"/"+testToken+"/api/analyze", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out analyzeResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decoding %s: %v", raw, err)
		}
	}
	return resp, out
}

func getJSON(t *testing.T, url string, v any) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if v != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatalf("decoding %s: %v", raw, err)
		}
	}
	return resp
}

// TestDesktop_AnalyzeFixture: the synthetic replay goes through the API and
// comes back as a match card: id, four rostered players with teams and
// names, no review cases; a second upload is refused until force is set;
// the match then shows up in the history and can be re-opened.
func TestDesktop_AnalyzeFixture(t *testing.T) {
	_, ts := newTestServer(t)
	base := ts.URL + "/" + testToken

	resp, out := upload(t, ts, false, map[string]string{"synthetic_session.echoreplay": fixturePath})
	if resp.StatusCode != http.StatusOK || len(out.Results) != 1 {
		t.Fatalf("status %d, results %+v", resp.StatusCode, out.Results)
	}
	r := out.Results[0]
	if !r.OK || r.Error != "" || r.MatchID != "SYN-FIXTURE-001" || r.Match == nil {
		t.Fatalf("result %+v", r)
	}
	m := r.Match
	if m.MatchID != "SYN-FIXTURE-001" || m.SourceFile != "synthetic_session.echoreplay" || m.GameMode != "Echo_Arena" || m.Map != "mpl_arena_a" {
		t.Errorf("match header %+v", *m)
	}
	if m.StartTime == "" || m.DurationSeconds <= 0 || m.AnalyzedAt == "" {
		t.Errorf("times start=%q duration=%v analyzed=%q", m.StartTime, m.DurationSeconds, m.AnalyzedAt)
	}
	if m.FramesProcessed != 120 || m.InvalidFrames != 0 || m.PlayerFrames != 480 {
		t.Errorf("frames %d/%d/%d", m.FramesProcessed, m.InvalidFrames, m.PlayerFrames)
	}
	if len(m.Players) != 4 {
		t.Fatalf("players %+v", m.Players)
	}
	teams := map[string]int{}
	for _, p := range m.Players {
		teams[p.Team]++
		if p.Frames != 120 || p.Name == "" || p.Level == "" {
			t.Errorf("player %+v", p)
		}
	}
	if teams["blue"] != 2 || teams["orange"] != 2 {
		t.Errorf("teams %v", teams)
	}
	if m.Players[0].Team != "blue" || m.Players[0].Name != "BlueOne" {
		t.Errorf("first player %+v", m.Players[0])
	}
	if len(m.Cases) != 0 {
		t.Errorf("fixture must not flag anyone: %+v", m.Cases)
	}
	for _, p := range m.Players {
		if p.Level != "clean" || p.Score != 0 {
			t.Errorf("fixture player scored: %+v", p)
		}
	}
	if m.Diagnostics == nil || m.Diagnostics.FramesMapped != 480 || m.Diagnostics.SpectatorEntriesDropped != 120 || !strings.Contains(m.Diagnostics.Report, "Diagnostic Report") {
		t.Errorf("diagnostics %+v", m.Diagnostics)
	}
	if m.Telemetry == nil || m.Telemetry.FramesInserted != 480 || m.Telemetry.TicksInserted != 120 {
		t.Errorf("telemetry %+v", m.Telemetry)
	}
	if !m.HasScore {
		t.Errorf("fixture carries team scores: %+v", *m)
	}
	if m.Replaced || len(m.Warnings) != 0 {
		t.Errorf("replaced=%v warnings=%v", m.Replaced, m.Warnings)
	}

	// Second upload without force: already stored.
	resp, out = upload(t, ts, false, map[string]string{"again.echoreplay": fixturePath})
	if resp.StatusCode != http.StatusOK || len(out.Results) != 1 {
		t.Fatalf("status %d, results %+v", resp.StatusCode, out.Results)
	}
	r = out.Results[0]
	if r.OK || !r.AlreadyStored || r.MatchID != "SYN-FIXTURE-001" || !strings.Contains(r.Error, "already stored") {
		t.Errorf("second upload %+v", r)
	}

	// With force: replaced.
	resp, out = upload(t, ts, true, map[string]string{"synthetic_session.echoreplay": fixturePath})
	if resp.StatusCode != http.StatusOK || !out.Force || len(out.Results) != 1 || !out.Results[0].OK {
		t.Fatalf("forced upload: status %d %+v", resp.StatusCode, out)
	}
	if fm := out.Results[0].Match; !fm.Replaced || fm.Telemetry.FramesIgnored != 480 {
		t.Errorf("forced match %+v", *fm)
	}

	// History lists it; the stored view matches the fresh one.
	var hist struct {
		Matches []matchListEntry `json:"matches"`
	}
	if resp := getJSON(t, base+"/api/matches", &hist); resp.StatusCode != http.StatusOK {
		t.Fatalf("matches status %d", resp.StatusCode)
	}
	if len(hist.Matches) != 1 || hist.Matches[0].MatchID != "SYN-FIXTURE-001" || len(hist.Matches[0].Players) != 4 ||
		hist.Matches[0].Players[0].Name != "BlueOne" || hist.Matches[0].AnalyzedAt == "" || hist.Matches[0].SourceFile != "synthetic_session.echoreplay" {
		t.Errorf("history %+v", hist.Matches)
	}
	var stored matchView
	if resp := getJSON(t, base+"/api/match/SYN-FIXTURE-001", &stored); resp.StatusCode != http.StatusOK {
		t.Fatalf("match status %d", resp.StatusCode)
	}
	if stored.MatchID != m.MatchID || len(stored.Players) != 4 || stored.PlayerFrames != 480 || stored.FramesProcessed != 120 ||
		stored.Players[0].Name != "BlueOne" || stored.Players[0].Team != "blue" || stored.HasScore != m.HasScore ||
		stored.BlueScore != m.BlueScore || stored.OrangeScore != m.OrangeScore || stored.Diagnostics != nil || stored.Telemetry != nil {
		t.Errorf("stored view %+v", stored)
	}
	if resp := getJSON(t, base+"/api/match/NOPE", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown match status %d", resp.StatusCode)
	}

	var flagged flaggedResponse
	if resp := getJSON(t, base+"/api/flagged", &flagged); resp.StatusCode != http.StatusOK {
		t.Fatalf("flagged status %d", resp.StatusCode)
	}
	if len(flagged.SingleMatch) != 0 || len(flagged.CrossMatch) != 0 {
		t.Errorf("flagged %+v", flagged)
	}
}

// TestDesktop_TokenRequired: every route lives under the per-run token;
// anything else is 404.
func TestDesktop_TokenRequired(t *testing.T) {
	_, ts := newTestServer(t)
	for _, path := range []string{"/", "/api/flagged", "/api/matches", "/quit", "/wrongtoken/api/flagged", "/" + testToken + "/nope"} {
		if resp := getJSON(t, ts.URL+path, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, resp.StatusCode)
		}
	}
	resp, err := http.Post(ts.URL+"/api/analyze", "multipart/form-data; boundary=x", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /api/analyze without token: status %d", resp.StatusCode)
	}
	resp = getJSON(t, ts.URL+"/"+testToken+"/", nil)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("index: status %d type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp := getJSON(t, ts.URL+"/"+testToken+"/api/flagged", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("flagged with token: status %d", resp.StatusCode)
	}
}

// TestDesktop_BadUploads: wrong types and unreadable files are reported per
// file, an empty form is a 400, and one bad file does not stop the others.
func TestDesktop_BadUploads(t *testing.T) {
	_, ts := newTestServer(t)
	dir := t.TempDir()
	txt := filepath.Join(dir, "notes.txt")
	garbage := filepath.Join(dir, "garbage.echoreplay")
	notReplay := filepath.Join(dir, "config.json")
	for path, content := range map[string]string{txt: "hello", garbage: "this is not a replay\n", notReplay: `{"general": {}}`} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resp, out := upload(t, ts, false, map[string]string{
		"notes.txt": txt, "garbage.echoreplay": garbage, "config.json": notReplay, "ok.echoreplay": fixturePath,
	})
	if resp.StatusCode != http.StatusOK || len(out.Results) != 4 {
		t.Fatalf("status %d results %+v", resp.StatusCode, out.Results)
	}
	byFile := map[string]analyzeEntry{}
	for _, r := range out.Results {
		byFile[r.File] = r
	}
	if r := byFile["notes.txt"]; r.OK || !strings.Contains(r.Error, "unsupported file type") {
		t.Errorf("txt %+v", r)
	}
	if r := byFile["garbage.echoreplay"]; r.OK || r.Error == "" {
		t.Errorf("garbage %+v", r)
	}
	if r := byFile["config.json"]; r.OK || r.Error == "" {
		t.Errorf("non-replay json %+v", r)
	}
	if r := byFile["ok.echoreplay"]; !r.OK || r.MatchID != "SYN-FIXTURE-001" {
		t.Errorf("good file %+v", r)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("force", "1")
	_ = mw.Close()
	resp, err := http.Post(ts.URL+"/"+testToken+"/api/analyze", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty upload: status %d", resp.StatusCode)
	}
}

func TestDesktop_Quit(t *testing.T) {
	s, ts := newTestServer(t)
	if resp := getJSON(t, ts.URL+"/"+testToken+"/quit", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("quit status %d", resp.StatusCode)
	}
	select {
	case <-s.Done():
	default:
		t.Error("quit did not close Done")
	}
	if resp := getJSON(t, ts.URL+"/"+testToken+"/quit", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("second quit status %d", resp.StatusCode)
	}
}

func TestUploadName(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Users\me\match.echoreplay`: "match.echoreplay",
		"../../etc/passwd.json":        "passwd.json",
		"":                             "upload",
		"..":                           "upload",
		`bad<name>.echoreplay`:         "bad_name_.echoreplay",
	} {
		if got := uploadName(in); got != want {
			t.Errorf("uploadName(%q) = %q, want %q", in, got, want)
		}
	}
	if !isTrue("1") || !isTrue("true") || !isTrue(" on ") || isTrue("") || isTrue("0") {
		t.Error("isTrue")
	}
}
