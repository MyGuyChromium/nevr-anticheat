package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func uploadBlindArtifactTest(t *testing.T, url, name, consent string, payload []byte) (int, sqlite.BlindArtifact) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	part.Write(payload)
	if consent != "" {
		mw.WriteField("consent", consent)
	}
	mw.Close()
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var a sqlite.BlindArtifact
	json.NewDecoder(resp.Body).Decode(&a)
	return resp.StatusCode, a
}
func TestBlindReviewAPIKeepsBallotsPrivateUntilExplicitReveal(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken + "/api/blind-review"
	if resp, out := upload(t, ts, true, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != 200 || !out.Results[0].OK {
		t.Fatal("fixture upload failed")
	}
	status, artifact := uploadBlindArtifactTest(t, base+"/artifacts", "trial.html", "true", []byte("<script>window.leak=true</script>"))
	if status != 201 || len(artifact.SHA256) != 64 {
		t.Fatalf("artifact: %d %+v", status, artifact)
	}
	resp, err := http.Get(base + "/artifacts/" + artifact.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/octet-stream" || resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("active artifact served inline: %+v", resp.Header)
	}
	var session sqlite.BlindReviewSession
	b := sqlite.BlindReviewBinding{MatchID: "SYN-FIXTURE-001", PlayerID: "echovr:1001", DetectorID: "THROW_001", Kind: sqlite.OpportunityThrow, FrameStart: 10, FrameEnd: 12, EvidenceMethod: "controlled_reproduction", ArtifactSHA256: artifact.SHA256, LegalContext: "normal"}
	if resp := postJSONTest(t, base+"/sessions", b, &session); resp.StatusCode != 201 || session.SessionID == "" {
		t.Fatalf("create: %d %+v", resp.StatusCode, session)
	}
	reveal := base + "/sessions/" + session.SessionID + "/reveal"
	ballot := base + "/sessions/" + session.SessionID + "/ballots"
	if resp := postJSONTest(t, reveal, map[string]any{}, nil); resp.StatusCode != 409 {
		t.Fatal("revealed before votes")
	}
	for i, reviewer := range []string{"Alice", "Bob"} {
		var out sqlite.BlindReviewSession
		if resp := postJSONTest(t, ballot, map[string]any{"reviewer_id": reviewer, "ground_truth": "negative", "comment": "reviewed source", "pre_reveal_attestation": true}, &out); resp.StatusCode != 201 || out.BallotCount != i+1 || len(out.Ballots) != 0 || out.Consensus != "" {
			t.Fatalf("vote leaked: %d %+v", resp.StatusCode, out)
		}
	}
	var listed struct {
		Sessions []sqlite.BlindReviewSession `json:"sessions"`
	}
	if resp := getJSON(t, base+"/sessions", &listed); resp.StatusCode != 200 || len(listed.Sessions) != 1 || len(listed.Sessions[0].Ballots) != 0 {
		t.Fatal("list leaked ballots")
	}
	var revealed struct {
		Session sqlite.BlindReviewSession `json:"session"`
	}
	if resp := postJSONTest(t, reveal, map[string]any{}, &revealed); resp.StatusCode != 200 || revealed.Session.Consensus != "negative" || len(revealed.Session.Ballots) != 2 {
		t.Fatalf("reveal: %d %+v", resp.StatusCode, revealed)
	}
	dashboard, err := s.buildCalibrationDashboard(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	metric, _ := detectorMetric(dashboard, "THROW_001")
	if metric.Overall.IndependentEvidence != 1 || metric.Eligible || dashboard.ProductionValidated || dashboard.SealedHoldout {
		t.Fatalf("bound review metrics: %+v", metric)
	}
}
func TestBlindReviewAPIRejectsMissingConsentAndArbitrarySources(t *testing.T) {
	_, ts := newTestServer(t)
	base := ts.URL + "/" + testToken + "/api/blind-review"
	for _, consent := range []string{"", "false", "TRUE", "true\n"} {
		status, _ := uploadBlindArtifactTest(t, base+"/artifacts", "trial.bin", consent, []byte("x"))
		if status != 400 {
			t.Fatalf("consent %q accepted with %d", consent, status)
		}
	}
	if resp := postJSONTest(t, base+"/sessions", map[string]any{"evidence_url": "https://example.invalid/private"}, nil); resp.StatusCode != 400 {
		t.Fatal("remote evidence source accepted")
	}
	if resp := postJSONTest(t, base+"/sessions", map[string]any{"evidence_path": "C:/private/file"}, nil); resp.StatusCode != 400 {
		t.Fatal("server file path accepted")
	}
}

func TestBlindReviewCreationRequiresExactAnalyzingExecutable(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken + "/api/blind-review"
	if resp, out := upload(t, ts, true, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != 200 || !out.Results[0].OK {
		t.Fatal("fixture upload failed")
	}
	runs, err := s.engine.Store().ListAnalysisRuns(t.Context(), "SYN-FIXTURE-001", 1)
	if err != nil || len(runs) != 1 {
		t.Fatal("missing analysis run")
	}
	exact, err := runningExecutableSHA256()
	if err != nil || len(exact) != 64 || runs[0].ExecutableSHA256 != exact {
		t.Fatalf("writer did not persist actual executable: %+v %v", runs, err)
	}
	status, a := uploadBlindArtifactTest(t, base+"/artifacts", "trial.bin", "true", []byte("independent evidence"))
	if status != 201 {
		t.Fatal("artifact upload failed")
	}
	b := sqlite.BlindReviewBinding{MatchID: "SYN-FIXTURE-001", PlayerID: "echovr:1001", DetectorID: "THROW_001", Kind: sqlite.OpportunityThrow, FrameStart: 10, FrameEnd: 12, EvidenceMethod: "controlled_reproduction", ArtifactSHA256: a.SHA256, LegalContext: "normal"}
	for _, hash := range []string{"", strings.Repeat("0", 64)} {
		run := runs[0]
		run.ExecutableSHA256 = hash
		if _, err := s.engine.Store().StoreAnalysisRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
		if resp := postJSONTest(t, base+"/sessions", b, nil); resp.StatusCode != 409 {
			t.Fatalf("same version/build but missing/different executable accepted: %d", resp.StatusCode)
		}
	}
	// Re-analysis by the actual running binary establishes identity again.
	if resp, out := upload(t, ts, true, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != 200 || !out.Results[0].OK {
		t.Fatal("re-analysis failed")
	}
	if resp := postJSONTest(t, base+"/sessions", b, nil); resp.StatusCode != 201 {
		t.Fatalf("exact re-analysis rejected: %d", resp.StatusCode)
	}
}
func TestBlindReviewMatchPickerOmitsFindingsAndSourcePaths(t *testing.T) {
	s, ts := newTestServer(t)
	if resp, out := upload(t, ts, true, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != 200 || !out.Results[0].OK {
		t.Fatal("fixture upload failed")
	}
	m, err := s.engine.Store().GetStoredMatch(t.Context(), "SYN-FIXTURE-001")
	if err != nil {
		t.Fatal(err)
	}
	m.Context.ReplayFile = "C:\\private\\sensitive\\trial.echoreplay"
	if err := s.engine.Store().StoreMatchContext(t.Context(), m.Context, m.FrameCount); err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if resp := getJSON(t, ts.URL+"/"+testToken+"/api/blind-review/matches", &out); resp.StatusCode != 200 {
		t.Fatal("picker failed")
	}
	raw := string(out["matches"])
	for _, word := range []string{"sensitive", "private", "score", "events", "observations", "flagged", "label", "THROW_"} {
		if strings.Contains(raw, word) {
			t.Fatalf("picker exposed %q: %s", word, raw)
		}
	}
	if !strings.Contains(raw, "trial.echoreplay") || !strings.Contains(raw, "min_frame") {
		t.Fatal("picker missing safe source/window bounds")
	}
}
