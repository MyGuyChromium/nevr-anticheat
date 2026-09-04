package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func postJSONTest(t *testing.T, path string, payload any, out any) *http.Response {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(path, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s: %v; body=%s", path, err, raw)
		}
	}
	return resp
}

func TestDesktopCalibrationPhysicsDiagnosticsAndArchive(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	if resp, out := upload(t, ts, false, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != http.StatusOK || len(out.Results) != 1 || !out.Results[0].OK {
		t.Fatalf("upload status=%d out=%+v", resp.StatusCode, out)
	}
	const matchID = "SYN-FIXTURE-001"
	const playerID = "echovr:1001"

	var label sqlite.MatchLabel
	resp := postJSONTest(t, base+"/api/match/"+matchID+"/label", map[string]string{
		"label": sqlite.MatchLabelKnownClean, "comment": "manually reviewed",
	}, &label)
	if resp.StatusCode != http.StatusOK || label.MatchID != matchID || label.Label != sqlite.MatchLabelKnownClean {
		t.Fatalf("label status=%d body=%+v", resp.StatusCode, label)
	}
	var calibration struct {
		MatchLabels      map[string]int `json:"match_labels"`
		LabeledMatches   int            `json:"labeled_matches"`
		UnlabeledMatches int            `json:"unlabeled_matches"`
	}
	if resp := getJSON(t, base+"/api/calibration", &calibration); resp.StatusCode != http.StatusOK || calibration.MatchLabels[sqlite.MatchLabelKnownClean] != 1 || calibration.LabeledMatches != 1 || calibration.UnlabeledMatches != 0 {
		t.Fatalf("calibration status=%d body=%+v", resp.StatusCode, calibration)
	}

	var stored matchView
	if resp := getJSON(t, base+"/api/match/"+matchID, &stored); resp.StatusCode != http.StatusOK {
		t.Fatalf("match status=%d", resp.StatusCode)
	}
	if stored.CalibrationLabel != sqlite.MatchLabelKnownClean || stored.TelemetryHealth == nil || stored.TelemetryHealth.Snapshots == 0 || stored.Storage == nil || stored.Storage.RawTicks == 0 {
		t.Fatalf("stored metadata = label=%q health=%+v storage=%+v", stored.CalibrationLabel, stored.TelemetryHealth, stored.Storage)
	}

	physicsURL := base + "/api/match/" + matchID + "/physics/frame/10?player=" + url.QueryEscape(playerID)
	var inspection physicsInspection
	if resp := getJSON(t, physicsURL, &inspection); resp.StatusCode != http.StatusOK || inspection.PlayerID != playerID || inspection.FocusFrame != 10 || len(inspection.Frames) == 0 || !inspection.RawTickPresent || inspection.Telemetry == nil {
		t.Fatalf("physics status=%d body=%+v", resp.StatusCode, inspection)
	}
	focusAuditOK := false
	for _, frame := range inspection.Frames {
		if frame.Focus && frame.IndependentAudit.Valid && frame.IndependentAudit.ExtractorVelocityDelta < 1e-9 {
			focusAuditOK = true
		}
	}
	if !focusAuditOK {
		t.Fatalf("focus frame did not pass independent velocity audit: %+v", inspection.Frames)
	}

	diagnosticURL := base + "/api/match/" + matchID + "/diagnostic/frame/10?player=" + url.QueryEscape(playerID)
	diagnosticResp, err := http.Get(diagnosticURL)
	if err != nil {
		t.Fatal(err)
	}
	bundle, _ := io.ReadAll(diagnosticResp.Body)
	diagnosticResp.Body.Close()
	if diagnosticResp.StatusCode != http.StatusOK || diagnosticResp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("diagnostic status=%d headers=%v body=%s", diagnosticResp.StatusCode, diagnosticResp.Header, bundle)
	}
	zr, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 3 {
		t.Fatalf("diagnostic entries=%d", len(zr.File))
	}
	var uncompressed bytes.Buffer
	for _, file := range zr.File {
		r, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		_, _ = io.Copy(&uncompressed, r)
		_ = r.Close()
	}
	joined := uncompressed.String()
	if strings.Contains(joined, "BlueOne") || strings.Contains(joined, playerID) {
		t.Fatal("diagnostic ZIP leaked a player name or identifier")
	}

	before, err := s.engine.Store().GetMatchTickCount(context.Background(), matchID)
	if err != nil || before == 0 {
		t.Fatalf("raw ticks before=%d, %v", before, err)
	}
	var archive rawArchiveResult
	resp = postJSONTest(t, base+"/api/match/"+matchID+"/archive", map[string]any{"prune_raw": false}, &archive)
	if resp.StatusCode != http.StatusOK || !archive.OK || archive.TicksArchived != before || archive.TicksPruned != 0 {
		t.Fatalf("archive status=%d result=%+v", resp.StatusCode, archive)
	}
	resp = postJSONTest(t, base+"/api/match/"+matchID+"/archive", map[string]any{"prune_raw": true, "confirmation": "wrong"}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad prune confirmation status=%d", resp.StatusCode)
	}
	resp = postJSONTest(t, base+"/api/match/"+matchID+"/archive", map[string]any{"prune_raw": true, "confirmation": matchID}, &archive)
	if resp.StatusCode != http.StatusOK || archive.TicksPruned != int64(before) {
		t.Fatalf("prune status=%d result=%+v", resp.StatusCode, archive)
	}
	if count, _ := s.engine.Store().GetMatchTickCount(context.Background(), matchID); count != 0 {
		t.Fatalf("raw tick count after prune=%d", count)
	}
	var restore struct {
		TicksRestored int `json:"ticks_restored"`
	}
	resp = postJSONTest(t, base+"/api/match/"+matchID+"/restore-raw", map[string]any{}, &restore)
	if resp.StatusCode != http.StatusOK || restore.TicksRestored != before {
		t.Fatalf("restore status=%d result=%+v", resp.StatusCode, restore)
	}
	if count, _ := s.engine.Store().GetMatchTickCount(context.Background(), matchID); count != before {
		t.Fatalf("raw tick count after restore=%d, want %d", count, before)
	}
}
