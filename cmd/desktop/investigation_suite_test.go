package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestInvestigationSuiteRoutes(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	if resp, out := upload(t, ts, true, map[string]string{"fixture.echoreplay": fixturePath}); resp.StatusCode != http.StatusOK || !out.Results[0].OK {
		t.Fatalf("upload status=%d out=%+v", resp.StatusCode, out)
	}
	const matchID = "SYN-FIXTURE-001"

	var investigation struct {
		Quality struct {
			Score float64 `json:"score"`
		} `json:"quality"`
		Timeline       []investigationPoint `json:"timeline"`
		ReviewProgress map[string]any       `json:"review_progress"`
		AnalysisRuns   []any                `json:"analysis_runs"`
	}
	if resp := getJSON(t, base+"/api/match/"+matchID+"/investigation", &investigation); resp.StatusCode != 200 || investigation.Quality.Score <= 0 || len(investigation.Timeline) == 0 || len(investigation.AnalysisRuns) == 0 {
		t.Fatalf("investigation status=%d body=%+v", resp.StatusCode, investigation)
	}
	var note map[string]any
	if resp := postJSONTest(t, base+"/api/match/"+matchID+"/notes", map[string]any{"kind": "note", "frame_index": 10, "body": "verified frame"}, &note); resp.StatusCode != 200 || note["note_id"] == "" {
		t.Fatalf("note status=%d body=%+v", resp.StatusCode, note)
	}

	if resp := postJSONTest(t, base+"/api/profiles", map[string]string{"name": "Test profile"}, nil); resp.StatusCode != 200 {
		t.Fatalf("profile status=%d", resp.StatusCode)
	}
	var profiles struct {
		Profiles []any `json:"profiles"`
	}
	if resp := getJSON(t, base+"/api/profiles", &profiles); resp.StatusCode != 200 || len(profiles.Profiles) != 1 {
		t.Fatalf("profiles status=%d body=%+v", resp.StatusCode, profiles)
	}
	if resp := postJSONTest(t, base+"/api/filters", map[string]string{"name": "Throws", "filter_json": `{"detector":"THROW_001"}`}, nil); resp.StatusCode != 200 {
		t.Fatalf("filter status=%d", resp.StatusCode)
	}
	if resp := postJSONTest(t, base+"/api/calibration/opportunities", map[string]any{
		"match_id": matchID, "player_id": "echovr:1001", "detector_id": "THROW_001",
		"opportunity_kind": "throw", "ground_truth": "negative", "frame_start": 5, "frame_end": 8,
	}, nil); resp.StatusCode != 200 {
		t.Fatalf("opportunity status=%d", resp.StatusCode)
	}

	var validation struct {
		Splits map[string][]string `json:"splits"`
	}
	if resp := getJSON(t, base+"/api/lab/validation", &validation); resp.StatusCode != 200 || len(validation.Splits) != 4 {
		t.Fatalf("validation status=%d body=%+v", resp.StatusCode, validation)
	}
	var probes struct {
		Probes []struct {
			ID      string `json:"id"`
			Quality struct {
				Gated bool `json:"gated"`
			} `json:"quality"`
		} `json:"probes"`
	}
	if resp := getJSON(t, base+"/api/lab/synthetic", &probes); resp.StatusCode != 200 || len(probes.Probes) != 6 {
		t.Fatalf("probes status=%d body=%+v", resp.StatusCode, probes)
	}
	for _, probe := range probes.Probes {
		if probe.ID == "timestamp-reversal" && !probe.Quality.Gated {
			t.Fatal("timestamp-reversal probe did not exercise the quality gate")
		}
	}
	var matrix struct {
		Rows []any `json:"rows"`
	}
	if resp := postJSONTest(t, base+"/api/lab/experiments", map[string]any{"MatchID": matchID, "DetectorID": "THROW_001", "Parameter": "base_tolerance", "Values": []float64{0, .5}}, &matrix); resp.StatusCode != 200 || len(matrix.Rows) != 2 {
		t.Fatalf("matrix status=%d body=%+v", resp.StatusCode, matrix)
	}

	response, err := http.Get(base + "/api/library")
	if err != nil {
		t.Fatal(err)
	}
	libraryBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var library evidenceLibrary
	if response.StatusCode != 200 || json.Unmarshal(libraryBody, &library) != nil || library.Version != 2 || len(library.Opportunities) != 1 || len(library.Notes) != 1 || len(library.Filters) != 1 {
		t.Fatalf("library status=%d body=%s", response.StatusCode, libraryBody)
	}
	response, err = http.Get(base + "/api/match/" + matchID + "/report")
	if err != nil {
		t.Fatal(err)
	}
	report, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(report), "NEVR investigation report") {
		t.Fatalf("report status=%d body=%s", response.StatusCode, report)
	}
	var queue struct {
		Items []analysisQueueItem `json:"items"`
	}
	if resp := getJSON(t, base+"/api/queue", &queue); resp.StatusCode != 200 || len(queue.Items) == 0 || queue.Items[0].Status != "complete" {
		t.Fatalf("queue status=%d body=%+v", resp.StatusCode, queue)
	}
	if runs, err := s.engine.Store().ListAnalysisRuns(t.Context(), matchID, 10); err != nil || len(runs) != 1 {
		t.Fatalf("analysis runs=%+v err=%v", runs, err)
	}
}
