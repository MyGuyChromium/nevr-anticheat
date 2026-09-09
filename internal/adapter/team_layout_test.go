package adapter

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMappedTeamNameDoesNotInferRoleFromDisplayName(t *testing.T) {
	for _, name := range []string{"Blue Spectators", "Orange Spectators", "Not Blue", "Raptured and Friend", "Orange Boosters", "BLUE TEAM observer", "RED TEAM"} {
		for idx := 0; idx < 4; idx++ {
			if team, ok := MappedTeamName(name, idx); ok || team != "" {
				t.Errorf("display name %q at %d inferred a role: %q, %v", name, idx, team, ok)
			}
		}
	}
}

func TestMappedSessionTeamNameRequiresUnambiguousEchoLayout(t *testing.T) {
	tests := []struct {
		name  string
		teams []string
		want  []string
	}{
		{"custom names", []string{"Raptured and Friend", "Apollo", "SPECTATORS"}, []string{"blue", "orange", ""}},
		{"cross-colour display names", []string{"Orange Boosters", "Blue Jays", "SPECTATORS"}, []string{"blue", "orange", ""}},
		{"mixed custom and canonical", []string{"Apollo", "ORANGE TEAM", "SPECTATORS"}, []string{"blue", "orange", ""}},
		{"whitespace and case", []string{"Apollo", "orange team", " spectators "}, []string{"blue", "orange", ""}},
		{"legacy reordered labels", []string{"ORANGE TEAM", "BLUE TEAM", "SPECTATORS"}, []string{"orange", "blue", ""}},
		{"conflicting canonical label", []string{"ORANGE TEAM", "Apollo", "SPECTATORS"}, []string{"orange", "", ""}},
		{"no spectator sentinel", []string{"Apollo", "Artemis"}, []string{"", ""}},
		{"wrong third role", []string{"Apollo", "Artemis", "UNKNOWN"}, []string{"", "", ""}},
		{"spectator marker in playing slot", []string{"Blue Spectators", "Apollo", "SPECTATORS"}, []string{"", "", ""}},
		{"spectators at first slot", []string{"SPECTATORS", "ORANGE TEAM", "SPECTATORS"}, []string{"", "orange", ""}},
		{"additional unknown team", []string{"Apollo", "Artemis", "SPECTATORS", "Other"}, []string{"", "", "", ""}},
		{"legacy unnamed slots", []string{"", "", ""}, []string{"blue", "orange", ""}},
		{"legacy unknown first slot", []string{"RED TEAM", "ORANGE TEAM"}, []string{"", "orange"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := &EchoVRSessionResponse{}
			for _, name := range tc.teams {
				raw.Teams = append(raw.Teams, EchoVRTeam{TeamName: name})
			}
			for idx, want := range tc.want {
				got, ok := MappedSessionTeamName(raw, idx)
				if got != want || ok != (want != "") {
					t.Errorf("slot %d = %q,%v; want %q,%v", idx, got, ok, want, want != "")
				}
			}
			for _, idx := range []int{-1, len(raw.Teams)} {
				if got, ok := MappedSessionTeamName(raw, idx); ok || got != "" {
					t.Errorf("out-of-range slot %d = %q,%v", idx, got, ok)
				}
			}
		})
	}
	if got, ok := MappedSessionTeamName(nil, 0); ok || got != "" {
		t.Errorf("nil snapshot = %q,%v", got, ok)
	}
}

func customTeamSession() *EchoVRSessionResponse {
	blue := testPlayer("BluePlayer", 101, [3]float64{1, 1.6, -10})
	orange := testPlayer("OrangePlayer", 202, [3]float64{-1, 1.6, 10})
	spectator := testPlayer("Spectator", 303, [3]float64{0, 5, 0})
	blue.HoldingLeft, blue.HoldingRight = "disc", "none"
	orange.HoldingLeft, orange.HoldingRight = "none", "none"
	// A spectator claiming the disc must not create a conflict or become an
	// attachment candidate, even when real team display names contain colours.
	spectator.HoldingLeft, spectator.HoldingRight = "disc", "none"
	session := twoTeamSession("custom-team-match", []EchoVRPlayer{blue}, []EchoVRPlayer{orange})
	session.Teams[0].TeamName = "Orange Boosters"
	session.Teams[1].TeamName = "Blue Jays"
	session.Teams = append(session.Teams, EchoVRTeam{TeamName: "SPECTATORS", Players: []EchoVRPlayer{spectator}})
	return session
}

func TestMapperCustomTeamLayoutAgreesAcrossEvidenceAndDiagnostics(t *testing.T) {
	session := customTeamSession()
	result := NewMapper().MapSessionAt(session, time.Unix(0, 0))
	if len(result.Errors) != 0 || len(result.Frames) != 2 || result.SpectatorsDropped != 1 {
		t.Fatalf("unexpected mapped result: frames=%d excluded=%d errors=%v", len(result.Frames), result.SpectatorsDropped, result.Errors)
	}
	for id, team := range map[string]string{"echovr:101": "blue", "echovr:202": "orange"} {
		frame := frameByPlayer(result.Frames, id)
		if frame == nil || frame.Team != team || result.MatchCtx.TeamAssignments[id] != team {
			t.Fatalf("%s role mismatch: frame=%+v assignments=%v", id, frame, result.MatchCtx.TeamAssignments)
		}
		disc := frame.Disc
		if disc == nil || !disc.PossessionKnown || disc.SampledPlayerCount != 2 || disc.PossessionConflict || disc.PossessorID != "echovr:101" {
			t.Fatalf("disc evidence ignored playing roster or included spectator: %+v", disc)
		}
		if disc.Attachment == nil || disc.Attachment.State != "held" || disc.Attachment.HolderID != "echovr:101" {
			t.Fatalf("attachment disagrees with roster: %+v", disc.Attachment)
		}
	}
	if len(result.MatchCtx.PlayerIDs) != 2 || frameByPlayer(result.Frames, "echovr:303") != nil {
		t.Fatal("spectator leaked into mapped roster")
	}
	diag := NewDiagnosticReport()
	diag.RecordSession(session)
	if diag.PlayerEntriesSeen != 2 || diag.SpectatorEntriesDropped != 1 || diag.PlayerCount != 2 || diag.PossessionMultiplePlayers != 0 {
		t.Fatalf("diagnostics disagree with roster: players=%d excluded=%d unique=%d conflicts=%d", diag.PlayerEntriesSeen, diag.SpectatorEntriesDropped, diag.PlayerCount, diag.PossessionMultiplePlayers)
	}
}

func TestReplayParserCustomTeamNamesRemainDisplayNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-teams.echoreplay")
	session := customTeamSession()
	writeLines(t, path, []string{sessionLine(t, "2026/09/08 10:00:00.000", session)})
	parser := NewEchoReplayParser()
	seen := 0
	ctx, diag, err := parser.ParseFileStream(path, func(tick *ParsedTick) error {
		seen++
		if len(tick.Frames) != 2 || tick.Session.Teams[0].TeamName != "Orange Boosters" || tick.Session.Teams[1].TeamName != "Blue Jays" {
			t.Fatalf("custom display names or playing frames lost: %+v", tick)
		}
		if frameByPlayer(tick.Frames, "echovr:101").Team != "blue" || frameByPlayer(tick.Frames, "echovr:202").Team != "orange" {
			t.Fatal("custom display names were mistaken for team roles")
		}
		return nil
	})
	if err != nil || seen != 1 {
		t.Fatalf("parse custom-name replay: seen=%d err=%v", seen, err)
	}
	if len(ctx.PlayerIDs) != 2 || diag.PlayerEntriesSeen != 2 || diag.SpectatorEntriesDropped != 1 {
		t.Fatalf("parsed roster/diagnostic mismatch: context=%+v diagnostics=%+v", ctx, diag)
	}
}
