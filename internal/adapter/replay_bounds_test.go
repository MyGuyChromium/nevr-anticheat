package adapter

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// allocatedDuring reports the bytes allocated (not retained: allocated) while
// fn runs. It is process-wide, so budgets leave headroom for the runtime.
func allocatedDuring(fn func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// floodLine is one snapshot with n minimal but mappable players on team.
func floodLine(prefix, session, team string, n int) string {
	var b strings.Builder
	b.WriteString(prefix + "\t" + `{"sessionid":"` + session + `","teams":[{"team":"BLUE TEAM","players":[`)
	if team != "BLUE TEAM" {
		b.WriteString(`]},{"team":"ORANGE TEAM","players":[]},{"team":"` + team + `","players":[`)
	}
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"userid":%d,"body":{"position":[1,1.5,%d]}}`, 100000+i, i%50+1)
	}
	b.WriteString(`]}]}`)
	return b.String()
}

// TestReplayRejectsTenThousandPlayerSnapshot: the measured attack was a
// 30-line replay with 10,000 players per snapshot (800 KB zipped) that took
// 3.7 GB of heap and wrote a 1.9 GB database. Every snapshot of that file must
// now be refused before any per-player work, with a sentence a moderator can
// read, within a fixed allocation budget.
func TestReplayRejectsTenThousandPlayerSnapshot(t *testing.T) {
	ok := sessionLine(t, "2026/03/01 12:00:00.000", twoTeamSession("FLOOD", []EchoVRPlayer{testPlayer("A", 1, [3]float64{1, 1.6, -10})}, nil))
	hostile := floodLine("2026/03/01 12:00:00.033", "FLOOD", "BLUE TEAM", 10_000)
	if len(hostile) > 600<<10 {
		t.Fatalf("hostile line is %d bytes; keep this input small", len(hostile))
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "flood-plain.echoreplay")
	writeLines(t, plain, []string{ok, hostile, hostile, hostile})
	zipped := filepath.Join(dir, "flood-zip.echoreplay")
	zf, err := os.Create(zipped)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	w, err := zw.Create("flood.echoreplay")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(strings.Join([]string{ok, hostile, hostile, hostile}, "\n") + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zf.Close()

	for _, path := range []string{plain, zipped} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			ticks, frames := 0, 0
			var parseErr error
			allocated := allocatedDuring(func() {
				_, _, parseErr = NewEchoReplayParser().ParseFileStream(path, func(tick *ParsedTick) error {
					ticks++
					frames += len(tick.Frames)
					return nil
				})
			})
			if !errors.Is(parseErr, ErrTooManySnapshotPlayers) {
				t.Fatalf("err = %v, want ErrTooManySnapshotPlayers", parseErr)
			}
			if ticks != 1 || frames != 1 {
				t.Fatalf("delivered %d ticks / %d frames; only the first, honest snapshot may be mapped", ticks, frames)
			}
			for _, want := range []string{"line 2", "too many players in one snapshot", "more than 64 player entries in one team", "was not imported"} {
				if !strings.Contains(parseErr.Error(), want) {
					t.Errorf("error %q does not say %q", parseErr, want)
				}
			}
			// The roster is bounded while the line is decoded: 65 players are
			// built, not 10,000. A plain json.Unmarshal of this one line
			// allocated 41 MB before anything was mapped; the budget covers
			// the scanner's buffer for the 480 KB line and the ZIP reader.
			const budget = 12 << 20
			if allocated > budget {
				t.Fatalf("refusing the snapshot allocated %d bytes, budget %d", allocated, budget)
			}
			t.Logf("refused with %d bytes allocated", allocated)
		})
	}
}

func TestReplayRosterLimits(t *testing.T) {
	parse := func(t *testing.T, lines []string) (int, error) {
		t.Helper()
		ticks := 0
		_, _, err := NewEchoReplayParser().parseReader(strings.NewReader(strings.Join(lines, "\n")+"\n"), "limits.echoreplay",
			func(*ParsedTick) error { ticks++; return nil })
		return ticks, err
	}

	t.Run("exactly at the snapshot limit is accepted", func(t *testing.T) {
		ticks, err := parse(t, []string{floodLine("2026/03/01 12:00:00.000", "EDGE", "BLUE TEAM", MaxSnapshotPlayers)})
		if err != nil || ticks != 1 {
			t.Fatalf("ticks=%d err=%v", ticks, err)
		}
		_, err = parse(t, []string{floodLine("2026/03/01 12:00:00.000", "EDGE", "BLUE TEAM", MaxSnapshotPlayers+1)})
		if !errors.Is(err, ErrTooManySnapshotPlayers) || !strings.Contains(err.Error(), "line 1: too many players in one snapshot: 33 players on the two teams (limit 32") {
			t.Fatalf("one over the limit: %v", err)
		}
	})

	t.Run("spectator flood", func(t *testing.T) {
		// One team over the decode bound, and a gallery that only exceeds the
		// snapshot total together with the players.
		_, err := parse(t, []string{floodLine("2026/03/01 12:00:00.000", "SPEC", "SPECTATORS", MaxSnapshotPlayerEntries+1)})
		if !errors.Is(err, ErrTooManySnapshotPlayers) || !strings.Contains(err.Error(), "player entries in one team") {
			t.Fatalf("err = %v", err)
		}
		_, err = parse(t, []string{
			sessionLine(t, "2026/03/01 12:00:00.000", &EchoVRSessionResponse{SessionID: "SPEC", Teams: []EchoVRTeam{
				{TeamName: "BLUE TEAM", Players: []EchoVRPlayer{testPlayer("A", 1, [3]float64{1, 1.6, -10})}},
				{TeamName: "ORANGE TEAM"},
				{TeamName: "SPECTATORS", Players: make([]EchoVRPlayer, MaxSnapshotPlayerEntries)},
			}}),
		})
		if !errors.Is(err, ErrTooManySnapshotPlayers) || !strings.Contains(err.Error(), "65 player entries including spectators (limit 64)") {
			t.Fatalf("err = %v", err)
		}
		var teams []string
		for i := 0; i <= maxSnapshotTeams; i++ {
			teams = append(teams, `{"team":"T","players":[]}`)
		}
		if _, err = parse(t, []string{"2026/03/01 12:00:00.000	" + `{"sessionid":"TEAMS","teams":[` + strings.Join(teams, ",") + `]}`}); !errors.Is(err, ErrTooManySnapshotPlayers) || !strings.Contains(err.Error(), "more than 16 teams") {
			t.Fatalf("team flood: %v", err)
		}
		if ticks, err := parse(t, []string{
			sessionLine(t, "2026/03/01 12:00:00.000", &EchoVRSessionResponse{SessionID: "SPEC", Teams: []EchoVRTeam{
				{TeamName: "BLUE TEAM", Players: []EchoVRPlayer{testPlayer("A", 1, [3]float64{1, 1.6, -10})}},
				{TeamName: "ORANGE TEAM"},
				{TeamName: "SPECTATORS", Players: make([]EchoVRPlayer, MaxSnapshotPlayerEntries-1)},
			}}),
		}); err != nil || ticks != 1 {
			t.Fatalf("a full spectator gallery within the limit: ticks=%d err=%v", ticks, err)
		}
	})

	t.Run("distinct players per match", func(t *testing.T) {
		line := func(i int, session string) string {
			at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(i) * 33 * time.Millisecond)
			p := testPlayer(fmt.Sprintf("P%d", i), int64(1000+i), [3]float64{1, 1.6, -10})
			return sessionLine(t, at.Format("2006/01/02 15:04:05.000"), twoTeamSession(session, []EchoVRPlayer{p}, nil))
		}
		var lines []string
		for i := 0; i <= MaxMatchPlayers; i++ {
			lines = append(lines, line(i, "CHURN"))
		}
		ticks, err := parse(t, lines)
		if !errors.Is(err, ErrTooManyMatchPlayers) || ticks != MaxMatchPlayers {
			t.Fatalf("ticks=%d err=%v, want %d ticks then ErrTooManyMatchPlayers", ticks, err, MaxMatchPlayers)
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("line %d", MaxMatchPlayers+1)) || !strings.Contains(err.Error(), "different player ids") {
			t.Errorf("error is not readable: %q", err)
		}
		// The limit is per match: a new session id starts a new roster.
		lines[MaxMatchPlayers] = line(MaxMatchPlayers, "CHURN-2")
		if ticks, err := parse(t, lines); err != nil || ticks != MaxMatchPlayers+1 {
			t.Fatalf("new session: ticks=%d err=%v", ticks, err)
		}
	})

	t.Run("frames per match", func(t *testing.T) {
		a, b := testPlayer("A", 1, [3]float64{1, 1.6, -10}), testPlayer("B", 2, [3]float64{-1, 1.6, 10})
		var lines []string
		for i := 0; i < 6; i++ {
			session := "LONG"
			if i >= 3 {
				session = "LONG-2"
			}
			at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(i) * 33 * time.Millisecond)
			lines = append(lines, sessionLine(t, at.Format("2006/01/02 15:04:05.000"), twoTeamSession(session, []EchoVRPlayer{a}, []EchoVRPlayer{b})))
		}
		run := func(limit int) (int, error) {
			p := NewEchoReplayParser()
			p.maxNormalizedFrames = limit
			ticks := 0
			_, _, err := p.parseReader(strings.NewReader(strings.Join(lines, "\n")+"\n"), "long.echoreplay", func(*ParsedTick) error { ticks++; return nil })
			return ticks, err
		}
		// Six frames per match: a limit of six passes because it is per match.
		if ticks, err := run(6); err != nil || ticks != 6 {
			t.Fatalf("limit 6: ticks=%d err=%v", ticks, err)
		}
		ticks, err := run(5)
		if !errors.Is(err, ErrReplayTooManyFrames) || ticks != 2 {
			t.Fatalf("limit 5: ticks=%d err=%v", ticks, err)
		}
		if !strings.Contains(err.Error(), "line 3") || !strings.Contains(err.Error(), "more than 5 player frames") {
			t.Errorf("error is not readable: %q", err)
		}
		if NewEchoReplayParser().maxNormalizedFrames != maxTapeNormalizedFrames {
			t.Error("the replay frame limit is not the native tape limit")
		}
	})
}

// A refused snapshot must leave no trace: the mapper is as if it never saw it.
func TestMapperLimitErrorLeavesStateUntouched(t *testing.T) {
	m := NewMapper()
	m.EnforceRosterLimits()
	var crowd []EchoVRPlayer
	for i := 0; i <= MaxSnapshotPlayers; i++ {
		crowd = append(crowd, testPlayer(fmt.Sprintf("C%d", i), int64(500+i), [3]float64{1, 1.6, float64(i + 1)}))
	}
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	refused := m.MapSessionAt(twoTeamSession("S", crowd, nil), t0)
	if !errors.Is(refused.LimitError, ErrTooManySnapshotPlayers) || len(refused.Frames) != 0 || refused.MatchCtx != nil || len(refused.Errors) != 1 {
		t.Fatalf("refused snapshot: %+v", refused)
	}
	if !m.FirstSampleTime().IsZero() || m.Stats().Snapshots != 0 || len(m.matchPlayers) != 0 {
		t.Fatalf("refused snapshot changed mapper state: first=%v stats=%+v roster=%d", m.FirstSampleTime(), m.Stats(), len(m.matchPlayers))
	}
	okResult := m.MapSessionAt(twoTeamSession("S", crowd[:2], nil), t0.Add(time.Second))
	if okResult.LimitError != nil || len(okResult.Frames) != 2 || okResult.Frames[0].FrameIndex != 0 || okResult.Frames[0].Timestamp != 0 {
		t.Fatalf("first accepted snapshot: %+v", okResult.Frames)
	}

	// Without EnforceRosterLimits (live ingest and native tape bring their own
	// limits) nothing is refused.
	if r := NewMapper().MapSessionAt(twoTeamSession("S", crowd, nil), t0); r.LimitError != nil || len(r.Frames) != len(crowd) {
		t.Fatalf("unlimited mapper: err=%v frames=%d", r.LimitError, len(r.Frames))
	}
}

// TestMapperDropsRepeatedPlayerIDs: storage keys a frame by (match, player,
// frame index). Two entries with one id used to yield two frames, of which
// INSERT OR IGNORE silently kept one and a forced re-analysis could store
// neither. The first entry wins everywhere and the repeats are counted.
func TestMapperDropsRepeatedPlayerIDs(t *testing.T) {
	first := testPlayer("Twin", 777, [3]float64{1, 1.6, -10})
	second := testPlayer("Twin-again", 777, [3]float64{5, 1.6, 10})
	second.HoldingLeft, second.HoldingRight = "disc", "none"
	other := testPlayer("Other", 778, [3]float64{-3, 1.6, 4})
	anon1, anon2 := testPlayer("", 0, [3]float64{2, 1.6, 2}), testPlayer("", 0, [3]float64{3, 1.6, 3})

	m := NewMapper()
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	var warnings int
	for i := 0; i < 3; i++ {
		res := m.MapSessionAt(twoTeamSession("DUP", []EchoVRPlayer{first, anon1}, []EchoVRPlayer{second, other, anon2}), t0.Add(time.Duration(i)*33*time.Millisecond))
		if len(res.Frames) != 3 || res.DuplicatePlayersDropped != 2 {
			t.Fatalf("snapshot %d: %d frames, %d dropped, want 3 and 2", i, len(res.Frames), res.DuplicatePlayersDropped)
		}
		twin := frameByPlayer(res.Frames, "echovr:777")
		if twin == nil || twin.Position[0] != 1 || twin.Team != "blue" {
			t.Fatalf("snapshot %d: the first entry did not win: %+v", i, twin)
		}
		if anon := frameByPlayer(res.Frames, "name:"); anon == nil || anon.Position[0] != 2 {
			t.Fatalf("snapshot %d: anonymous players: %+v", i, anon)
		}
		if got := res.MatchCtx.TeamAssignments["echovr:777"]; got != "blue" || len(res.MatchCtx.PlayerIDs) != 3 || res.MatchCtx.PlayerNames["echovr:777"] != "Twin" {
			t.Fatalf("snapshot %d: context follows the repeat: team=%q ids=%v names=%v", i, got, res.MatchCtx.PlayerIDs, res.MatchCtx.PlayerNames)
		}
		// The dropped entry's held disc must not make it the holder.
		if twin.Disc == nil || twin.Disc.IsHeld || twin.Disc.SampledPlayerCount != 3 {
			t.Fatalf("snapshot %d: disc state built from a dropped entry: %+v", i, twin.Disc)
		}
		for _, w := range res.Warnings {
			if strings.Contains(w.Message, "same player id") {
				warnings++
			}
		}
	}
	if got := m.Stats().DuplicatePlayerEntries; got != 6 || warnings != 1 {
		t.Fatalf("stats count %d repeats (want 6), %d warnings (want 1)", got, warnings)
	}

	// The parser folds the count into the diagnostic report.
	var lines []string
	for i := 0; i < 4; i++ {
		at := t0.Add(time.Duration(i) * 33 * time.Millisecond)
		lines = append(lines, sessionLine(t, at.Format("2006/01/02 15:04:05.000"), twoTeamSession("DUP", []EchoVRPlayer{first}, []EchoVRPlayer{second, other})))
	}
	perTick := map[int]int{}
	_, diag, err := NewEchoReplayParser().parseReader(strings.NewReader(strings.Join(lines, "\n")+"\n"), "dup.echoreplay", func(tick *ParsedTick) error {
		perTick[tick.FrameIndex] += len(tick.Frames)
		return nil
	})
	if err != nil || len(perTick) != 4 || perTick[0] != 2 {
		t.Fatalf("parse: err=%v ticks=%v", err, perTick)
	}
	if diag.DuplicatePlayerEntries != 4 || diag.MapperStats == nil || diag.MapperStats.DuplicatePlayerEntries != 4 || diag.FramesMapped != 8 {
		t.Fatalf("diagnostics: dup=%d mapped=%d stats=%+v", diag.DuplicatePlayerEntries, diag.FramesMapped, diag.MapperStats)
	}
	if !strings.Contains(diag.FormatReport(), "Duplicate player ids dropped:  4") {
		t.Error("the text report does not mention the dropped duplicates")
	}
}

// decodeReplaySession repeats the shape of EchoVRSessionResponse.UnmarshalJSON
// so it can bound the rosters while decoding. This keeps the two in step: any
// document must decode to the same value, or fail, through both.
func TestReplaySessionDecodeMatchesPlainDecode(t *testing.T) {
	docs := []string{
		`{}`, `null`, `{"teams":null}`, `{"teams":[]}`, `{"teams":[null,{}]}`,
		`{"teams":[{"team":"BLUE TEAM","players":null,"stats":{"points":2.0}},{"team":"ORANGE TEAM","players":[]}]}`,
		`{"sessionid":"S","blue_points":5.0,"orange_points":3,"teams":[{"players":[null,{"userid":7,"name":"n","stats":{"goals":1.0}}]}]}`,
		`{"TEAMS":[{"PLAYERS":[{"USERID":9}]}],"Blue_Points":2}`,
		`{"teams":[{"players":[{"userid":1}]}],"teams":[{"players":[{"userid":2},{"userid":3}]}]}`,
		`{"blue_points":1.5}`, `{"teams":{}}`, `{"teams":[{"players":{}}]}`, `{"teams":[{"players":[{"userid":"x"}]}]}`, `{"teams":[7]}`, `[`,
	}
	fixture, err := os.ReadFile("../../tests/fixtures/synthetic_session.echoreplay")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(fixture), "\n") {
		if tab := strings.IndexByte(line, '\t'); tab >= 0 && i%10 == 0 {
			docs = append(docs, strings.TrimSpace(line[tab+1:]))
		}
	}
	if len(docs) < 20 {
		t.Fatalf("only %d documents; the fixture did not contribute", len(docs))
	}
	for _, doc := range docs {
		var plain, bounded EchoVRSessionResponse
		plainErr := json.Unmarshal([]byte(doc), &plain)
		boundedErr := decodeReplaySession([]byte(doc), &bounded)
		if (plainErr == nil) != (boundedErr == nil) {
			t.Errorf("%.80s: plain err=%v, bounded err=%v", doc, plainErr, boundedErr)
			continue
		}
		if plainErr == nil && !reflect.DeepEqual(plain, bounded) {
			t.Errorf("%.80s:\n plain   %+v\n bounded %+v", doc, plain, bounded)
		}
	}
}
