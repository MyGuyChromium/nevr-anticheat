package main

import (
	"encoding/json"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestTeamSideOf(t *testing.T) {
	tests := []struct {
		idx  int
		name string
		want string
	}{
		{0, "BLUE TEAM", "blue"},
		{1, "ORANGE TEAM", "orange"},
		{1, "blue", "blue"}, // name wins over index
		{0, "Orange", "orange"},
		{2, "SPECTATORS", ""},
		{0, "SPECTATORS", ""},
		{0, "", "blue"},
		{1, "", "orange"},
		{2, "", ""},
		{0, "MODERATORS", ""},
	}
	for _, tt := range tests {
		if got := teamSideOf(tt.idx, tt.name); got != tt.want {
			t.Errorf("teamSideOf(%d, %q) = %q, want %q", tt.idx, tt.name, got, tt.want)
		}
	}
}

func TestBuildRosterAndFilterFrames(t *testing.T) {
	var s adapter.EchoVRSessionResponse
	if err := json.Unmarshal([]byte(fakeSessionJSONTeams("s", "playing", 2, 1, 2)), &s); err != nil {
		t.Fatal(err)
	}
	r := buildRoster(&s)
	if r.PlayerCount != 3 || len(r.Spectators) != 2 {
		t.Fatalf("roster = %+v", r)
	}
	if r.Teams["echovr:1000"] != "blue" || r.Teams["echovr:1001"] != "blue" || r.Teams["echovr:2000"] != "orange" {
		t.Errorf("teams = %v", r.Teams)
	}
	if r.Spectators[0] != "echovr:9000" || r.Spectators[1] != "echovr:9001" {
		t.Errorf("spectators = %v", r.Spectators)
	}

	frames := []model.PlayerTelemetryFrame{
		{PlayerID: "echovr:9000"},
		{PlayerID: "echovr:2000"},
		{PlayerID: "echovr:1000"},
		{PlayerID: "echovr:9001", Team: "blue"}, // a mislabelled spectator is still dropped
		{PlayerID: "echovr:unknown"},
	}
	kept, dropped := filterFrames(frames, r)
	if dropped != 3 || len(kept) != 2 {
		t.Fatalf("kept=%d dropped=%d", len(kept), dropped)
	}
	if kept[0].PlayerID != "echovr:1000" || kept[0].Team != "blue" || kept[1].PlayerID != "echovr:2000" || kept[1].Team != "orange" {
		t.Errorf("kept = %+v (want sorted by player id with teams filled)", kept)
	}
	if !r.teamsCopyEqual(kept) {
		t.Errorf("teamsCopy mismatch")
	}
}

func (r sessionRoster) teamsCopyEqual(frames []model.PlayerTelemetryFrame) bool {
	c := r.teamsCopy()
	for _, f := range frames {
		if c[f.PlayerID] != f.Team {
			return false
		}
	}
	return true
}

func TestValidateSession_SpectatorOnlyIsInvalid(t *testing.T) {
	var s adapter.EchoVRSessionResponse
	if err := json.Unmarshal([]byte(fakeSessionJSONTeams("s", "playing", 0, 0, 3)), &s); err != nil {
		t.Fatal(err)
	}
	if err := validateSession(&s); err == nil {
		t.Error("a lobby with only spectators must not validate as a match")
	}
}

func TestSessionMatchesMatchID(t *testing.T) {
	m := DiscoveredMatch{MatchID: "AAAAAAAA-1111-2222-3333-444444444444.node1", LabelID: "aaaaaaaa-1111-2222-3333-444444444444"}
	if !sessionMatchesMatchID("aaaaaaaa-1111-2222-3333-444444444444", m) {
		t.Error("uuid part should match case-insensitively")
	}
	if !sessionMatchesMatchID("{AAAAAAAA-1111-2222-3333-444444444444}", m) {
		t.Error("braced uuid should match")
	}
	if sessionMatchesMatchID("bbbbbbbb-1111-2222-3333-444444444444", m) {
		t.Error("different uuid must not match")
	}
	if sessionMatchesMatchID("", m) {
		t.Error("empty session id must not match")
	}
	if !sessionMatchesMatchID("plain-id", DiscoveredMatch{MatchID: "plain-id"}) {
		t.Error("match id without node suffix should match")
	}
}
