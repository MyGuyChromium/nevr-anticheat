package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
)

func TestBuildMatchViewReviewAssessmentDoesNotChangeScore(t *testing.T) {
	s, _ := newTestServer(t)
	const playerID = "arbitrary-player"
	events := make([]model.DetectionEvent, 5)
	for i := range events {
		events[i] = model.DetectionEvent{PlayerID: playerID, DetectorID: "THROW_001", IsShadow: true}
	}
	mc := &model.MatchContext{MatchID: "review-test", PlayerIDs: []string{playerID, "no-signals"}}
	view := s.buildMatchView(matchData{ctx: mc, events: events})
	for _, player := range view.Players {
		want := model.AssessPlayerWithCoverage(player.PlayerID, events, nil)
		if !reflect.DeepEqual(player.Assessment, want) || player.Score != 0 || player.Level != "clean" || player.Detections != 0 {
			t.Fatalf("view assessment must not affect scoring: %+v, want %+v", player, want)
		}
		if player.PlayerID == playerID && (player.Assessment.Status != "review_needed" || player.ShadowDetections != 5) {
			t.Fatalf("shadow findings hidden: %+v", player)
		}
	}
	if len(view.Cases) != 0 || len(view.Players) != 2 {
		t.Fatalf("unexpected case or roster changes: %+v", view)
	}
}

func TestDesktopReviewAssessmentPersistsAcrossViewsAndExports(t *testing.T) {
	s, ts := newTestServer(t)
	base := ts.URL + "/" + testToken
	const matchID, playerID = "SYN-FIXTURE-001", "echovr:1001"
	resp, uploaded := upload(t, ts, false, map[string]string{"fixture.echoreplay": fixturePath})
	if resp.StatusCode != http.StatusOK || len(uploaded.Results) != 1 || !uploaded.Results[0].OK {
		t.Fatalf("upload status=%d out=%+v", resp.StatusCode, uploaded)
	}
	for _, player := range uploaded.Results[0].Match.Players {
		if player.Assessment.Status != model.ReviewStatusNoSignals || player.Assessment.SignalCount != 0 {
			t.Fatalf("fresh fixture unexpectedly has review findings: %+v", player)
		}
	}
	events := make([]model.DetectionEvent, 5)
	for i := range events {
		frame := 10 + i*10
		events[i] = model.DetectionEvent{
			EventID: fmt.Sprintf("review-assessment-%d", i), DetectorID: "THROW_001", DetectorVersion: "test",
			MatchID: matchID, PlayerID: playerID, FrameIndex: frame,
			FrameRangeStart: frame - 1, FrameRangeEnd: frame + 1, Timestamp: float64(frame) / 15,
			Severity: 0.7, Confidence: 0.8, EnforcementWeight: 0.8, IsShadow: true,
			ObservedValue: "test disc speed", ExpectedRange: "test configured cap",
			CausalKey: model.CausalKey{PlayerID: playerID, FrameStart: frame - 1, FrameEnd: frame + 1, AnomalyType: "disc_speed"},
		}
	}
	if _, err := s.engine.Store().StoreDetectionEvents(context.Background(), events, "initial"); err != nil {
		t.Fatal(err)
	}
	want := model.AssessPlayerEvents(playerID, events)
	var stored matchView
	if resp := getJSON(t, base+"/api/match/"+matchID, &stored); resp.StatusCode != http.StatusOK {
		t.Fatalf("stored match status=%d", resp.StatusCode)
	}
	matched := false
	for _, player := range stored.Players {
		if player.PlayerID == playerID {
			matched = true
			if !reflect.DeepEqual(player.Assessment, want) || player.Score != 0 || player.Level != "clean" || player.Detections != 0 {
				t.Fatalf("stored view mismatch or scoring changed: %+v", player)
			}
		}
	}
	if !matched || len(stored.Cases) != 0 {
		t.Fatal("expected player missing or a scored case was created")
	}
	var summary replay.MatchSummary
	if resp := getJSON(t, base+"/api/match/"+matchID+"/summary.json", &summary); resp.StatusCode != http.StatusOK {
		t.Fatalf("summary status=%d", resp.StatusCode)
	}
	for _, player := range summary.Players {
		if player.PlayerID == playerID && (player.Suspicion == nil || !reflect.DeepEqual(player.Suspicion.Assessment, want)) {
			t.Fatalf("cached summary did not project current events: %+v", player)
		}
	}
	readExport := func(path string) string {
		t.Helper()
		response, err := http.Get(base + "/api/match/" + matchID + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("export %s: status=%d err=%v body=%s", path, response.StatusCode, err, body)
		}
		return string(body)
	}
	rows, err := csv.NewReader(strings.NewReader(readExport("/export.csv"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	columns := make(map[string]int)
	for index, name := range rows[0] {
		columns[name] = index
	}
	for _, name := range []string{"review_status", "review_signals"} {
		if _, ok := columns[name]; !ok {
			t.Fatalf("CSV lacks %s: %+v", name, rows[0])
		}
	}
	for _, row := range rows[1:] {
		if row[columns["player_id"]] == playerID &&
			(row[columns["review_status"]] != "review_needed" || row[columns["review_signals"]] != "5" || row[columns["suspicion_score"]] != "0.00") {
			t.Fatalf("CSV assessment mismatch: %+v", row)
		}
	}
	report := readExport("/report")
	for _, marker := range []string{"<th>Assessment</th>", "<th>Scoring level</th>", "<td>Review needed</td>", "5 total (0 scored + 5 shadow)", "not verified fair play"} {
		if !strings.Contains(report, marker) {
			t.Errorf("printable report is missing %q", marker)
		}
	}

	// The replay's default analysis has no observations. Re-uploading replaces
	// the seeded findings and the cached summary must not keep their status.
	resp, uploaded = upload(t, ts, false, map[string]string{"fixture.echoreplay": fixturePath})
	if resp.StatusCode != http.StatusOK || len(uploaded.Results) != 1 || !uploaded.Results[0].OK {
		t.Fatalf("re-upload status=%d out=%+v", resp.StatusCode, uploaded)
	}
	if resp := getJSON(t, base+"/api/match/"+matchID+"/summary.json", &summary); resp.StatusCode != http.StatusOK {
		t.Fatalf("refreshed summary status=%d", resp.StatusCode)
	}
	for _, player := range summary.Players {
		if player.Suspicion == nil || player.Suspicion.Assessment.Status != model.ReviewStatusNoSignals || player.Suspicion.Assessment.SignalCount != 0 {
			t.Fatalf("re-analysis retained old findings: %+v", player)
		}
	}
}
