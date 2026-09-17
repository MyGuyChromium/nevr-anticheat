package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// A release while the client reports the unnamed post-goal state is the
// server taking the disc away, not a throw. A release with an unnamed status
// in a recording that never named one still counts (active fallback).
func TestSummaryBuilderUnnamedPostScoreGapIsNotAThrow(t *testing.T) {
	withPlayer := func(hold, status string) *adapter.EchoVRSessionResponse {
		return &adapter.EchoVRSessionResponse{GameStatus: status, Teams: []adapter.EchoVRTeam{{TeamName: "BLUE TEAM", Players: []adapter.EchoVRPlayer{{Name: "Alice", UserID: 101, HoldingLeft: hold, HoldingRight: "none"}}}}}
	}
	for _, unnamed := range []string{"", "unknown"} {
		b := NewSummaryBuilder(nil)
		b.Add(withPlayer("none", "playing"), 0, 0)
		b.Add(withPlayer("disc", "score"), 1, 1)
		b.Add(withPlayer("disc", unnamed), 2, 2)
		b.Add(withPlayer("none", unnamed), 3, 3)
		b.Add(withPlayer("none", "round_start"), 4, 4)
		if s := b.Finish(); len(s.Throws) != 0 {
			t.Errorf("unnamed status %q after a goal produced throws: %+v", unnamed, s.Throws)
		}

		b = NewSummaryBuilder(nil)
		b.Add(withPlayer("disc", unnamed), 0, 0)
		b.Add(withPlayer("none", unnamed), 1, 1)
		if s := b.Finish(); len(s.Throws) != 1 {
			t.Errorf("a recording that never names a status (%q) must keep counting releases, got %+v", unnamed, s.Throws)
		}
	}
}

// Synthetic goal cycle through the whole replay path: playing -> score ->
// (unnamed) -> round_start -> playing. The unnamed ticks must reach the
// pipeline as an inactive phase.
func TestAnalyzeReplayTreatsUnnamedPostScoreStatusAsInactive(t *testing.T) {
	statuses := []string{}
	add := func(status string, n int) {
		for i := 0; i < n; i++ {
			statuses = append(statuses, status)
		}
	}
	add("playing", 20)
	add("score", 10)
	add("", 9)
	add("round_start", 10)
	add("playing", 20)

	var lines strings.Builder
	start := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for frame, status := range statuses {
		var session adapter.EchoVRSessionResponse
		if err := json.Unmarshal([]byte(remapSession(t, frame, "none")), &session); err != nil {
			t.Fatal(err)
		}
		session.GameStatus = status
		session.GameClock = 100 - float64(frame)*.05
		session.Disc.Velocity = [3]float64{}
		encoded, err := json.Marshal(session)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&lines, "%s\t%s\n", start.Add(time.Duration(frame)*50*time.Millisecond).Format("2006/01/02 15:04:05.000"), encoded)
	}
	path := filepath.Join(t.TempDir(), "synthetic-goal-cycle.echoreplay")
	if err := os.WriteFile(path, []byte(lines.String()), 0600); err != nil {
		t.Fatal(err)
	}
	engine := newTestEngine(t)
	result, err := engine.AnalyzeFile(context.Background(), path, false)
	if err != nil || result.PersistError() != nil {
		t.Fatalf("analysis failed: %v / %v", err, result.PersistError())
	}
	frames, err := engine.Store().GetMatchFrames(context.Background(), result.MatchCtx.MatchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != len(statuses) {
		t.Fatalf("stored %d frames, want %d", len(frames), len(statuses))
	}
	gap, active := 0, 0
	for _, f := range frames {
		want := map[string]string{"playing": "playing", "score": "round_over", "": model.PhasePostScoreGap, "round_start": "round_start"}[statuses[f.FrameIndex]]
		if f.GamePhase != want {
			t.Fatalf("frame %d (%q): phase %q, want %q", f.FrameIndex, statuses[f.FrameIndex], f.GamePhase, want)
		}
		if f.GamePhase == model.PhasePostScoreGap {
			gap++
		}
		if result.MatchCtx.IsActivePhase(f.GamePhase) {
			active++
		}
	}
	if gap != 9 || active != 40 {
		t.Fatalf("gap frames=%d active frames=%d, want 9 and 40", gap, active)
	}
}
