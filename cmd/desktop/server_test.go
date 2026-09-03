package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// TestDesktop_AnalyzeTwoSessions: a recording whose session id changes
// mid-file comes back as one upload entry carrying both matches (the
// single-match fields mirror the first analyzed one), both land in the
// history, and the stored check is per match.
func TestDesktop_AnalyzeTwoSessions(t *testing.T) {
	_, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	dir := t.TempDir()
	two := filepath.Join(dir, "rematch.echoreplay")
	if _, _, err := testutil.SplitReplaySessions(fixturePath, two, "SYN-FIXTURE-002", 10*time.Minute); err != nil {
		t.Fatal(err)
	}

	resp, out := upload(t, ts, false, map[string]string{"rematch.echoreplay": two})
	if resp.StatusCode != http.StatusOK || len(out.Results) != 1 {
		t.Fatalf("status %d, results %+v", resp.StatusCode, out.Results)
	}
	r := out.Results[0]
	if !r.OK || r.Error != "" || r.AlreadyStored || r.MatchID != "SYN-FIXTURE-001" || r.Match == nil || r.Match.MatchID != "SYN-FIXTURE-001" || r.Diagnostic != nil {
		t.Fatalf("entry %+v", r)
	}
	if len(r.Matches) != 2 {
		t.Fatalf("matches %+v", r.Matches)
	}
	for i, m := range r.Matches {
		id := "SYN-FIXTURE-00" + string(rune('1'+i))
		if !m.OK || m.AlreadyStored || m.Error != "" || m.MatchID != id || m.Match == nil || m.Match.MatchID != id {
			t.Fatalf("match %d: %+v", i, m)
		}
		v := m.Match
		if v.SourceFile != "rematch.echoreplay" || v.FramesProcessed != 60 || v.PlayerFrames != 240 || len(v.Players) != 4 ||
			v.StartTime == "" || v.DurationSeconds <= 0 || v.Replaced || len(v.Warnings) != 0 {
			t.Errorf("match %s view %+v", id, *v)
		}
		if v.Telemetry == nil || *v.Telemetry != (telemetryView{FramesInserted: 240, TicksInserted: 60}) {
			t.Errorf("match %s telemetry %+v (its ticks must not collide with the other match's)", id, v.Telemetry)
		}
		if v.Diagnostics == nil || v.Diagnostics.SessionChanges != 1 {
			t.Errorf("match %s diagnostics %+v", id, v.Diagnostics)
		}
	}
	if r.Matches[0].Match.StartTime == r.Matches[1].Match.StartTime {
		t.Errorf("both matches report the same start time %q", r.Matches[0].Match.StartTime)
	}

	var hist struct {
		Matches []matchListEntry `json:"matches"`
	}
	if resp := getJSON(t, base+"/api/matches", &hist); resp.StatusCode != http.StatusOK {
		t.Fatalf("matches status %d", resp.StatusCode)
	}
	ids := map[string]bool{}
	for _, m := range hist.Matches {
		ids[m.MatchID] = true
	}
	if len(hist.Matches) != 2 || !ids["SYN-FIXTURE-001"] || !ids["SYN-FIXTURE-002"] {
		t.Errorf("history %+v", hist.Matches)
	}

	// Both stored: the entry mirrors the first match's refusal.
	resp, out = upload(t, ts, false, map[string]string{"rematch.echoreplay": two})
	if resp.StatusCode != http.StatusOK || len(out.Results) != 1 {
		t.Fatalf("status %d, results %+v", resp.StatusCode, out.Results)
	}
	r = out.Results[0]
	if r.OK || !r.AlreadyStored || r.MatchID != "SYN-FIXTURE-001" || !strings.Contains(r.Error, "already stored") || r.Match != nil || len(r.Matches) != 2 {
		t.Fatalf("second upload %+v", r)
	}
	for i, m := range r.Matches {
		if m.OK || !m.AlreadyStored || m.Match != nil || !strings.Contains(m.Error, "already stored") {
			t.Errorf("second upload match %d: %+v", i, m)
		}
	}

	// First match stored, a new second one: the entry mirrors the analyzed match.
	mixed := filepath.Join(dir, "mixed.echoreplay")
	if _, _, err := testutil.SplitReplaySessions(fixturePath, mixed, "SYN-FIXTURE-003", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	resp, out = upload(t, ts, false, map[string]string{"mixed.echoreplay": mixed})
	if resp.StatusCode != http.StatusOK || len(out.Results) != 1 {
		t.Fatalf("status %d, results %+v", resp.StatusCode, out.Results)
	}
	r = out.Results[0]
	if !r.OK || r.AlreadyStored || r.Error != "" || r.MatchID != "SYN-FIXTURE-003" || r.Match == nil || len(r.Matches) != 2 ||
		!r.Matches[0].AlreadyStored || r.Matches[0].MatchID != "SYN-FIXTURE-001" || !r.Matches[1].OK || r.Matches[1].MatchID != "SYN-FIXTURE-003" {
		t.Errorf("mixed upload %+v", r)
	}

	// Force: both matches of the first file replaced.
	resp, out = upload(t, ts, true, map[string]string{"rematch.echoreplay": two})
	if resp.StatusCode != http.StatusOK || len(out.Results) != 1 || !out.Results[0].OK || len(out.Results[0].Matches) != 2 {
		t.Fatalf("forced upload: status %d %+v", resp.StatusCode, out)
	}
	for i, m := range out.Results[0].Matches {
		if !m.OK || m.Match == nil || !m.Match.Replaced || m.Match.Telemetry.FramesIgnored != 240 || m.Match.Telemetry.TicksIgnored != 60 {
			t.Errorf("forced match %d: %+v", i, m)
		}
	}
}

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
	if m.Summary == nil || m.Summary.Version != replay.SummaryVersion || m.Summary.MatchID != m.MatchID || m.Summary.Ticks != 120 || len(m.Summary.Players) != 4 {
		t.Errorf("match summary %+v", m.Summary)
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
		stored.BlueScore != m.BlueScore || stored.OrangeScore != m.OrangeScore || stored.Diagnostics != nil || stored.Telemetry != nil ||
		stored.Summary == nil || stored.Summary.MatchID != m.MatchID || len(stored.Summary.Players) != 4 {
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

func TestDesktop_MatchSummaryDownloads(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	if resp, out := upload(t, ts, false, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != http.StatusOK || len(out.Results) != 1 || !out.Results[0].OK {
		t.Fatalf("upload status=%d out=%+v", resp.StatusCode, out)
	}

	resp, err := http.Get(base + "/api/match/SYN-FIXTURE-001/summary.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var summary replay.MatchSummary
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "SYN-FIXTURE-001-summary.json") || json.Unmarshal(raw, &summary) != nil {
		t.Fatalf("JSON download: status=%d headers=%v body=%s", resp.StatusCode, resp.Header, raw)
	}
	if summary.MatchID != "SYN-FIXTURE-001" || summary.Ticks != 120 || len(summary.Players) != 4 {
		t.Errorf("JSON summary %+v", summary)
	}

	resp, err = http.Get(base + "/api/match/SYN-FIXTURE-001/export.csv")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	rows, csvErr := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "SYN-FIXTURE-001-players.csv") || csvErr != nil {
		t.Fatalf("CSV download: status=%d headers=%v parse=%v body=%s", resp.StatusCode, resp.Header, csvErr, raw)
	}
	if len(rows) != 5 || rows[0][0] != "player_id" || rows[1][1] != "BlueOne" {
		t.Errorf("CSV rows %+v", rows)
	}

	// Seed one observation so both the match/player (including shadow) and
	// scored-case visual evidence routes can be exercised. Default analysis
	// correctly produces no detections for the clean fixture.
	ev := model.DetectionEvent{
		EventID: "desktop-evidence", DetectorID: "MOV_001", DetectorVersion: "1.0.0",
		MatchID: "SYN-FIXTURE-001", PlayerID: "echovr:1001", FrameIndex: 10,
		FrameRangeStart: 9, FrameRangeEnd: 11, Timestamp: 10.0 / 15,
		Severity: 0.8, Confidence: 0.9, EnforcementWeight: 0.8,
		ObservedValue: "test movement", ExpectedRange: "normal movement",
		CausalKey: model.CausalKey{PlayerID: "echovr:1001", FrameStart: 9, FrameEnd: 11, AnomalyType: "speed"},
	}
	if _, err := s.engine.Store().StoreDetectionEvents(context.Background(), []model.DetectionEvent{ev}, "initial"); err != nil {
		t.Fatal(err)
	}
	var observations struct {
		Stats  []sqlite.DetectorObservationStats `json:"stats"`
		Notice string                            `json:"notice"`
	}
	if resp := getJSON(t, base+"/api/observations", &observations); resp.StatusCode != http.StatusOK ||
		len(observations.Stats) != 1 || observations.Stats[0].DetectorID != "MOV_001" || observations.Notice == "" {
		t.Fatalf("observations: status=%d body=%+v", resp.StatusCode, observations)
	}
	caseID := "RC-SYN-FIXTURE-001-echovr:1001"
	if err := s.engine.Store().StoreReviewCase(context.Background(), model.ReviewCase{
		CaseID: caseID, MatchID: ev.MatchID, PlayerID: ev.PlayerID, SuspicionScore: 70,
		Status: model.CaseStatusPending, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		base + "/api/match/SYN-FIXTURE-001/evidence/echovr:1001",
		base + "/api/case/" + caseID + "/evidence",
	} {
		resp, err = http.Get(path)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") ||
			!strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") ||
			!strings.Contains(string(raw), "NEVR evidence review") || !strings.Contains(string(raw), "desktop-evidence") {
			t.Fatalf("evidence page %s: status=%d headers=%v body=%s", path, resp.StatusCode, resp.Header, raw)
		}
	}

	for _, suffix := range []string{"summary.json", "export.csv", "evidence/player"} {
		if resp := getJSON(t, base+"/api/match/NOPE/"+suffix, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("unknown %s status %d", suffix, resp.StatusCode)
		}
	}
}

func TestDesktop_IndexIncludesFullMatchReport(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/" + testToken + "/")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(raw)
	for _, marker := range []string{"Every movement.", "LOCAL ENGINE", "offline-banner", "Full match report", "Player statistics", "Scoring timeline", "Throw log", "Download JSON", "Export player CSV", "Review replay", "Detector observations"} {
		if !strings.Contains(page, marker) {
			t.Errorf("desktop page does not contain %q", marker)
		}
	}
}

// TestDesktop_TokenRequired: every route lives under the per-run token;
// anything else is 404.
func TestDesktop_TokenRequired(t *testing.T) {
	_, ts := newTestServer(t)
	for _, path := range []string{"/", "/api/flagged", "/api/observations", "/api/matches", "/quit", "/wrongtoken/api/flagged", "/" + testToken + "/nope"} {
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

// TestDesktop_FailureDiagnostics: a file the parser refuses comes back with
// a diagnostic block (container, size, lines, first bytes, findings, hint)
// that describes what was uploaded; successful and already-stored entries
// carry none.
func TestDesktop_FailureDiagnostics(t *testing.T) {
	_, ts := newTestServer(t)
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	garbage := write("garbage.echoreplay", "this is not a replay\n")
	badTime := write("badtime.echoreplay", "yesterday\t{\"sessionid\":\"X\"}\n")
	empty := write("empty.echoreplay", "")
	legacy := write("config.json", `{"general": {}}`)

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	zf, err := zw.Create("notes/readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = zf.Write([]byte("hello\nworld"))
	_ = zw.Close()
	zipped := write("zipped.echoreplay", zbuf.String())

	resp, out := upload(t, ts, false, map[string]string{
		"garbage.echoreplay": garbage, "badtime.echoreplay": badTime, "empty.echoreplay": empty,
		"config.json": legacy, "zipped.echoreplay": zipped, "ok.echoreplay": fixturePath,
	})
	if resp.StatusCode != http.StatusOK || len(out.Results) != 6 {
		t.Fatalf("status %d results %+v", resp.StatusCode, out.Results)
	}
	byFile := map[string]analyzeEntry{}
	for _, r := range out.Results {
		byFile[r.File] = r
	}
	if r := byFile["ok.echoreplay"]; !r.OK || r.Diagnostic != nil {
		t.Errorf("good file %+v", r)
	}

	r := byFile["garbage.echoreplay"]
	d := r.Diagnostic
	if r.OK || r.Error == "" || d == nil {
		t.Fatalf("garbage %+v", r)
	}
	if d.Container != "text" || d.SizeBytes != 21 || d.Lines != 1 {
		t.Errorf("garbage container=%q size=%d lines=%d", d.Container, d.SizeBytes, d.Lines)
	}
	if d.HeadText != `this is not a replay\n` {
		t.Errorf("garbage head text %q", d.HeadText)
	}
	if !strings.HasPrefix(d.HeadHex, "74 68 69 73 20") || !strings.HasSuffix(d.HeadHex, " 0a") {
		t.Errorf("garbage head hex %q", d.HeadHex)
	}
	if len(d.Findings) != 1 || !strings.Contains(d.Findings[0], "no TAB") {
		t.Errorf("garbage findings %q", d.Findings)
	}
	if !strings.Contains(d.Hint, "YYYY/MM/DD HH:MM:SS.mmm<TAB>{json}") || !strings.Contains(d.Hint, "ZIP") {
		t.Errorf("garbage hint %q", d.Hint)
	}

	d = byFile["badtime.echoreplay"].Diagnostic
	if d == nil || d.Container != "text" || d.Lines != 1 {
		t.Fatalf("bad timestamp %+v", d)
	}
	joined := strings.Join(d.Findings, " | ")
	if !strings.Contains(joined, `prefix "yesterday" does not parse`) || !strings.Contains(joined, `no "teams" key`) || strings.Contains(joined, "sessionid") {
		t.Errorf("bad timestamp findings %q", d.Findings)
	}

	d = byFile["empty.echoreplay"].Diagnostic
	if d == nil || d.Container != "empty" || d.SizeBytes != 0 || d.Lines != 0 || d.HeadText != "" || d.HeadHex != "" ||
		len(d.Findings) != 1 || !strings.Contains(d.Findings[0], "empty") {
		t.Errorf("empty %+v", d)
	}

	d = byFile["config.json"].Diagnostic
	if d == nil || d.Container != "text" || d.Lines != 1 || !strings.Contains(strings.Join(d.Findings, " "), "starts with JSON") {
		t.Errorf("legacy json %+v", d)
	}

	d = byFile["zipped.echoreplay"].Diagnostic
	if d == nil || d.Container != "zip" || d.SizeBytes != int64(zbuf.Len()) || d.Lines != 2 ||
		len(d.ZipEntries) != 1 || !strings.HasPrefix(d.ZipEntries[0], "notes/readme.txt (") ||
		!strings.HasPrefix(d.HeadText, `PK\x03\x04`) || !strings.HasPrefix(d.HeadHex, "50 4b 03 04") {
		t.Errorf("zip %+v", d)
	}
	if joined := strings.Join(d.Findings, " | "); !strings.Contains(joined, `entry "notes/readme.txt"`) || !strings.Contains(joined, "no TAB") {
		t.Errorf("zip findings %q", d.Findings)
	}

	// A stored match refused without force is not a parse failure.
	_, out = upload(t, ts, false, map[string]string{"again.echoreplay": fixturePath})
	if len(out.Results) != 1 || !out.Results[0].AlreadyStored || out.Results[0].Diagnostic != nil {
		t.Errorf("already stored %+v", out.Results)
	}
}

func TestPrintableBytes(t *testing.T) {
	got := printableBytes([]byte("a\tb\nc\r\\\x00\xff\x7f"))
	if want := `a\tb\nc\r\\\x00\xff\x7f`; got != want {
		t.Errorf("printableBytes = %q, want %q", got, want)
	}
	if got := hexBytes([]byte("0123456789abcdefg")); got != "30 31 32 33 34 35 36 37 38 39 61 62 63 64 65 66\n67" {
		t.Errorf("hexBytes = %q", got)
	}
	if hexBytes(nil) != "" || printableBytes(nil) != "" {
		t.Error("empty input")
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
