package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// A "False positive" label passes the Regression Lab when the detector no
// longer fires on that play. If the match's current events cannot be READ, the
// lab knows nothing about the detector and must say so instead of reporting
// the label as fixed.
func TestTeethRegressionLabDoesNotPassLabelsWhenEventsCannotBeLoaded(t *testing.T) {
	s, base := teethUploadFixture(t)
	event := testLabEvent(teethMatchID)
	if err := s.engine.Store().StoreDetectionEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if resp := postJSONTest(t, base+"/api/event/"+event.EventID+"/review", map[string]string{"verdict": "no", "comment": "legal slap"}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("review status=%d", resp.StatusCode)
	}

	var lab struct{ Total, Passed, Failed int }
	if resp := getJSON(t, base+"/api/lab/regression", &lab); resp.StatusCode != http.StatusOK || lab.Total != 1 || lab.Passed != 0 || lab.Failed != 1 {
		t.Fatalf("precondition: the false positive still fires, so the label must fail: status=%d %+v", resp.StatusCode, lab)
	}

	// One unreadable row is enough to make GetMatchEvents fail for the match.
	// The labelled false-positive event itself is untouched and still fires.
	if _, err := s.engine.Store().DB().Exec(`INSERT INTO detection_events
		(event_id, detector_id, match_id, player_id, frame_index, severity, confidence)
		VALUES ('LAB-UNREADABLE', 'THROW_001', ?, 'echovr:1001', 'not-a-frame', 0.5, 0.5)`, teethMatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.engine.Store().GetMatchEvents(context.Background(), teethMatchID); err == nil {
		t.Fatal("precondition: the damaged row did not make the match's events unreadable")
	}

	resp, err := http.Get(base + "/api/lab/regression")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		var wrong struct{ Total, Passed, Failed int }
		_ = json.Unmarshal(body, &wrong)
		t.Fatalf("a read failure was presented as detector behaviour: %+v (the false positive still fires; nothing passed)", wrong)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want 500", resp.StatusCode, body)
	}
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &failure); err != nil || failure.Error == "" {
		t.Fatalf("the failure carries no explanation: %s", body)
	}
}
