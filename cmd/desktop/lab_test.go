package main

import (
	"archive/zip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func testLabEvent(matchID string) model.DetectionEvent {
	return model.DetectionEvent{
		EventID: "LAB-FALSE-POSITIVE", DetectorID: "THROW_001", DetectorVersion: "1.5.0",
		MatchID: matchID, PlayerID: "echovr:1001", FrameIndex: 10, FrameRangeStart: 10, FrameRangeEnd: 10,
		Timestamp: .67, Severity: .8, Confidence: .9, IsShadow: true,
		Evidence:      model.ThrowEvidence{ReleaseSpeed: 19.91, EffectiveCap: 18.9, AlignedMovementSpeed: 1.2},
		ObservedValue: "release speed 19.91 m/s", ExpectedRange: "<= 18.90 m/s",
		CausalKey: model.CausalKey{PlayerID: "echovr:1001", AnomalyType: "THROW_001"},
	}
}

func TestDesktopRegressionComparisonSandboxHistoryRuntimeAndSupport(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	if resp, out := upload(t, ts, false, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != 200 || !out.Results[0].OK {
		t.Fatalf("upload=%d %+v", resp.StatusCode, out)
	}
	const matchID = "SYN-FIXTURE-001"
	event := testLabEvent(matchID)
	if err := s.engine.Store().StoreDetectionEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	var review map[string]any
	if resp := postJSONTest(t, base+"/api/event/"+event.EventID+"/review", map[string]string{"verdict": "no", "comment": "legal slap"}, &review); resp.StatusCode != 200 {
		t.Fatalf("review=%d %+v", resp.StatusCode, review)
	}

	// Reanalysis removes the synthetic false positive and snapshots the old output.
	if resp, out := upload(t, ts, true, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != 200 || !out.Results[0].OK {
		t.Fatalf("reupload=%d %+v", resp.StatusCode, out)
	}
	var comparison struct {
		Available bool           `json:"available"`
		Summary   map[string]int `json:"summary"`
	}
	if resp := getJSON(t, base+"/api/match/"+matchID+"/comparison", &comparison); resp.StatusCode != 200 || !comparison.Available || comparison.Summary["removed"] < 1 {
		t.Fatalf("comparison=%d %+v", resp.StatusCode, comparison)
	}
	var regression struct{ Total, Passed, Failed int }
	if resp := getJSON(t, base+"/api/lab/regression", &regression); resp.StatusCode != 200 || regression.Total != 1 || regression.Passed != 1 || regression.Failed != 0 {
		t.Fatalf("regression=%d %+v", resp.StatusCode, regression)
	}

	before, _ := s.engine.Store().GetMatchEvents(context.Background(), matchID)
	var preview struct {
		Persisted  bool           `json:"persisted"`
		DetectorID string         `json:"detector_id"`
		Summary    map[string]int `json:"summary"`
	}
	resp := postJSONTest(t, base+"/api/lab/thresholds/preview", map[string]any{"match_id": matchID, "detector_id": "THROW_001", "parameter": "base_tolerance", "value": 1.25}, &preview)
	if resp.StatusCode != 200 || preview.Persisted || preview.DetectorID != "THROW_001" {
		t.Fatalf("preview=%d %+v", resp.StatusCode, preview)
	}
	after, _ := s.engine.Store().GetMatchEvents(context.Background(), matchID)
	if len(before) != len(after) {
		t.Fatalf("sandbox mutated events: %d -> %d", len(before), len(after))
	}

	var history struct {
		PlayerID   string `json:"player_id"`
		MatchCount int    `json:"match_count"`
		Throws     int    `json:"throws"`
	}
	if resp := getJSON(t, base+"/api/player/echovr%3A1001/history", &history); resp.StatusCode != 200 || history.PlayerID != "echovr:1001" || history.MatchCount != 1 {
		t.Fatalf("history=%d %+v", resp.StatusCode, history)
	}

	watch := t.TempDir()
	var settings map[string]any
	if resp := postJSONTest(t, base+"/api/settings", map[string]any{"watch_folder": watch, "watch_enabled": true, "automatic_update_checks": false}, &settings); resp.StatusCode != 200 || settings["watch_enabled"] != true {
		t.Fatalf("settings=%d %+v", resp.StatusCode, settings)
	}
	copyPath := filepath.Join(watch, "new.echoreplay")
	data, _ := os.ReadFile(fixturePath)
	if err := os.WriteFile(copyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Second)
	_ = os.Chtimes(copyPath, old, old)
	var scan map[string]any
	if resp := postJSONTest(t, base+"/api/watch/scan", nil, &scan); resp.StatusCode != 200 {
		t.Fatalf("scan=%d %+v", resp.StatusCode, scan)
	}
	if scan["analyzed"] != float64(1) {
		t.Fatalf("watch scan did not analyze exactly one stable replay: %+v", scan)
	}

	// A replay already durably uploaded at shutdown is resumed on the next
	// launch (called directly here to keep the test deterministic).
	recoveryDir := filepath.Join(s.runtime.pendingDir, "upload-recovery")
	if err := os.MkdirAll(recoveryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	recoveryFile := filepath.Join(recoveryDir, "recover.echoreplay")
	if err := os.WriteFile(recoveryFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s.runtime.resumePending(context.Background())
	if _, err := os.Stat(recoveryFile); !os.IsNotExist(err) {
		t.Fatalf("recovered upload still exists: %v", err)
	}
	var recovery map[string]any
	if resp := getJSON(t, base+"/api/recovery", &recovery); resp.StatusCode != 200 || recovery["recovered"] != float64(1) || recovery["pending"] != float64(0) {
		t.Fatalf("recovery=%d %+v", resp.StatusCode, recovery)
	}

	badDir := filepath.Join(s.runtime.pendingDir, "upload-bad")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatal(err)
	}
	badFile := filepath.Join(badDir, "bad.echoreplay")
	if err := os.WriteFile(badFile, []byte("not a replay"), 0o600); err != nil {
		t.Fatal(err)
	}
	var discarded map[string]any
	if resp := postJSONTest(t, base+"/api/recovery/discard", nil, &discarded); resp.StatusCode != 200 || discarded["removed"] != float64(1) {
		t.Fatalf("discard=%d %+v", resp.StatusCode, discarded)
	}
	if _, err := os.Stat(badFile); !os.IsNotExist(err) {
		t.Fatalf("discarded upload still exists: %v", err)
	}

	var support map[string]any
	if resp := postJSONTest(t, base+"/api/maintenance/support-bundle", nil, &support); resp.StatusCode != 200 {
		t.Fatalf("support=%d %+v", resp.StatusCode, support)
	}
	path, _ := support["path"].(string)
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var joined strings.Builder
	for _, f := range zr.File {
		rd, _ := f.Open()
		_, _ = io.Copy(&joined, rd)
		_ = rd.Close()
	}
	if strings.Contains(joined.String(), "echovr:1001") || strings.Contains(joined.String(), watch) {
		t.Fatal("support bundle leaked identity or watch path")
	}
}

func TestDesktopUpdateCheckUsesEmbeddedRevision(t *testing.T) {
	newCommit := strings.Repeat("a", 40)
	oldCommit := strings.Repeat("b", 40)
	s, ts := newTestServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/git/ref/tags/windows-latest" {
			t.Fatalf("update request path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"object":{"sha":"`+newCommit+`"}}`)
	}))
	defer api.Close()
	s.runtime.updateURL = api.URL
	old := buildCommit
	buildCommit = oldCommit
	defer func() { buildCommit = old }()
	var status updateStatus
	if resp := getJSON(t, ts.URL+"/"+testToken+"/api/update", &status); resp.StatusCode != 200 || !status.Available || status.LatestCommit != newCommit {
		t.Fatalf("update=%d %+v", resp.StatusCode, status)
	}
}
