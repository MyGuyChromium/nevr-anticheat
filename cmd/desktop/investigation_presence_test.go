package main

import (
	"encoding/json"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

func TestInvestigationTimelineKeepsMissingSpeedDistinctFromZero(t *testing.T) {
	s, _ := newTestServer(t)
	const matchID = "SYN-UI-SPEED-PRESENCE"
	mc := &model.MatchContext{MatchID: matchID, PlayerIDs: []string{"fixture-player"},
		TeamAssignments: map[string]string{"fixture-player": "blue"}, Physics: model.DefaultPhysics(), TickRate: 15}
	frames := testutil.NewFrameBuilder("fixture-player").NormalIdlePlayer(3)
	frames[0].ReportedVelocity, frames[0].Disc = nil, nil
	frames[1].ReportedVelocity = &model.Vec3{}
	frames[1].Disc = &model.DiscState{Speed: 0}
	frames[2].ReportedVelocity = &model.Vec3{3, 4, 0}
	frames[2].Disc = &model.DiscState{Speed: 12.5}
	if err := s.engine.Store().StoreMatchContext(t.Context(), mc, len(frames)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.engine.Store().StoreTelemetryFrames(t.Context(), matchID, frames); err != nil {
		t.Fatal(err)
	}
	doc, err := s.investigationDocument(t.Context(), matchID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Timeline []map[string]json.RawMessage `json:"timeline"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Timeline) != 3 {
		t.Fatalf("timeline points=%d, want 3", len(response.Timeline))
	}
	wants := [][2]string{{"null", "null"}, {"0", "0"}, {"5", "12.5"}}
	for i, want := range wants {
		for j, key := range []string{"game_speed", "disc_speed"} {
			if got := string(response.Timeline[i][key]); got != want[j] {
				t.Errorf("point %d %s=%s, want %s", i, key, got, want[j])
			}
		}
	}
}
