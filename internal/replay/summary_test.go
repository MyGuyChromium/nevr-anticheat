package replay

import (
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestSummaryBuilderMatchReport(t *testing.T) {
	const aliceID = "echovr:101"
	mc := &model.MatchContext{
		MatchID: "MATCH-1", GameMode: "Echo_Arena", Map: "mpl_arena_a", IsPrivate: true,
		StartTime: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC), Duration: 4 * time.Second,
		TeamAssignments: map[string]string{aliceID: "blue", "echovr:202": "orange"},
		PlayerNames:     map[string]string{aliceID: "Alice", "echovr:202": "Bob"},
	}
	player := func(name string, userID int64, holds bool, scored bool) adapter.EchoVRPlayer {
		p := adapter.EchoVRPlayer{Name: name, UserID: userID, Level: 50, Ping: 40}
		p.HoldingLeft, p.HoldingRight = "none", "none"
		if holds {
			p.HoldingLeft = "disc"
		}
		if scored {
			p.Stats = adapter.EchoVRPlayerStats{Points: 2, Goals: 1, Saves: 1, Passes: 3, Possession: 2.5, ShotsOnGoal: 1}
		}
		return p
	}
	session := func(holds, scored bool, speed float64) *adapter.EchoVRSessionResponse {
		return &adapter.EchoVRSessionResponse{
			SessionID: "MATCH-1", MatchType: "Echo_Arena", MapName: "mpl_arena_a", GameStatus: "playing",
			GameClockDisplay: "09:59", BluePoints: map[bool]int{false: 0, true: 2}[scored],
			Disc: &adapter.EchoVRDisc{Velocity: [3]float64{speed, 0, 0}},
			Teams: []adapter.EchoVRTeam{
				{TeamName: "BLUE TEAM", Players: []adapter.EchoVRPlayer{player("Alice", 101, holds, scored)}},
				{TeamName: "ORANGE TEAM", Players: []adapter.EchoVRPlayer{player("Bob", 202, false, false)}},
			},
		}
	}

	b := NewSummaryBuilder(mc)
	b.Add(session(true, false, 0), 0, 0)
	b.Add(session(false, false, 0), 1, 1) // release; speed arrives on the next tick
	b.Add(session(false, false, 18.5), 2, 2)
	goal := session(false, true, 0)
	goal.LastScore = &adapter.EchoVRLastScore{
		Team: "blue", PointAmount: 2, GoalType: "INSIDE SHOT", DiscSpeed: 18.5,
		DistanceThrown: 12.25, PersonScored: "Alice", AssistScored: "[INVALID]",
	}
	b.Add(goal, 3, 3)
	s := b.Finish()

	if s.Version != SummaryVersion || s.MatchID != mc.MatchID || s.Ticks != 4 || s.DurationSeconds != 4 || !s.HasScore || s.BlueScore != 2 || s.OrangeScore != 0 {
		t.Fatalf("summary header: %+v", s)
	}
	if s.ThrowSource != "holding" || len(s.Throws) != 1 || s.Throws[0].PlayerID != aliceID || s.Throws[0].Speed != 18.5 || !s.Throws[0].Goal {
		t.Errorf("throws: source=%q events=%+v", s.ThrowSource, s.Throws)
	}
	if len(s.Goals) != 1 || !s.Goals[0].Described || s.Goals[0].ScorerID != aliceID || s.Goals[0].Points != 2 || s.Goals[0].Distance != 12.25 {
		t.Errorf("goals: %+v", s.Goals)
	}
	blue := s.Teams["blue"]
	if blue == nil || blue.Score != 2 || blue.Players != 1 || blue.Stats.Points != 2 || blue.Stats.Goals != 1 || blue.Throws.Count != 1 || blue.Throws.MaxSpeed != 18.5 || blue.Throws.Goals != 1 {
		t.Errorf("blue totals: %+v", blue)
	}
	if len(s.Players) != 2 || s.Players[0].Name != "Alice" || s.Players[0].PingAvg != 40 || s.Players[0].Stats.Passes != 3 {
		t.Errorf("players: %+v", s.Players)
	}

	s.ApplySuspicion(mc,
		map[string]model.SuspicionScore{aliceID: {PlayerID: aliceID, TotalScore: 65}},
		[]model.DetectionEvent{
			{PlayerID: aliceID, DetectorID: "THROW_001"},
			{PlayerID: aliceID, DetectorID: "THROW_001", IsShadow: true},
			{PlayerID: aliceID, DetectorID: "BIO_002", IsShadow: true},
		}, model.DefaultLevelTable())
	if su := s.Players[0].Suspicion; su == nil || su.Score != 65 || su.Level != "high_risk" || su.Detections != 1 || su.ShadowDetections != 2 ||
		len(su.TopDetectors) != 2 || su.TopDetectors[0] != "THROW_001 (2)" {
		t.Errorf("Alice suspicion: %+v", su)
	}
	if got := s.FlaggedPlayers(); len(got) != 1 || got[0] != aliceID {
		t.Errorf("flagged players: %v", got)
	}

	rows, err := csv.NewReader(strings.NewReader(string(s.PlayersCSV()))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0][0] != "player_id" || rows[1][0] != aliceID || rows[1][1] != "Alice" || rows[1][25] != "65.00" || rows[1][26] != "high_risk" {
		t.Errorf("CSV rows: %v", rows)
	}
}

func TestSummaryBuilderDoesNotCountRoundTransitionAsThrow(t *testing.T) {
	b := NewSummaryBuilder(nil)
	withPlayer := func(hold string, status string) *adapter.EchoVRSessionResponse {
		return &adapter.EchoVRSessionResponse{GameStatus: status, Teams: []adapter.EchoVRTeam{{TeamName: "BLUE TEAM", Players: []adapter.EchoVRPlayer{{Name: "Alice", UserID: 101, HoldingLeft: hold, HoldingRight: "none"}}}}}
	}
	b.Add(withPlayer("disc", "playing"), 0, 0)
	b.Add(withPlayer("none", "round_over"), 1, 1)
	if s := b.Finish(); len(s.Throws) != 0 {
		t.Errorf("round transition produced throws: %+v", s.Throws)
	}
}
