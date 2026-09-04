package tests

import (
	"bufio"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// syntheticReplay is a 120-line .echoreplay in the real recorder layout:
// "YYYY/MM/DD HH:MM:SS.mmm\t{session}" at 67 ms, three teams (BLUE TEAM,
// ORANGE TEAM, SPECTATORS), a possession -> release -> flight sequence by
// BlueOne, a "score" line with blue_points 0 -> 2 and the disc at the +Z
// goal, a two-line round_start reset, and a punch (OrangeOne's stun stat
// increments while BlueTwo is stunned). Generated deterministically; no
// real player data.
const syntheticReplay = "fixtures/synthetic_session.echoreplay"

// realReplayEnv names an optional real .echoreplay for local runs.
const realReplayEnv = "NEVR_REAL_REPLAY"

func TestReplayFixture_ParserAndPipeline(t *testing.T) {
	parser := adapter.NewEchoReplayParser()
	mc, frames, diag, err := parser.ParseFile(syntheticReplay)
	if err != nil {
		t.Fatal(err)
	}
	if mc.MatchID != "SYN-FIXTURE-001" || mc.GameMode != "Echo_Arena" || mc.Map != "mpl_arena_a" {
		t.Errorf("match context %+v", mc)
	}
	if diag.FramesRejected != 0 {
		t.Errorf("%d lines rejected", diag.FramesRejected)
	}
	// Four rostered players; the spectator is dropped, not mapped.
	if len(mc.PlayerIDs) != 4 {
		t.Fatalf("roster %v", mc.PlayerIDs)
	}
	if mc.TeamAssignments["echovr:1001"] != "blue" || mc.TeamAssignments["echovr:2001"] != "orange" {
		t.Errorf("teams %v", mc.TeamAssignments)
	}
	if len(mc.PlayerNames) != 4 || mc.PlayerNames["echovr:1001"] != "BlueOne" {
		t.Errorf("names %v", mc.PlayerNames)
	}
	if _, ok := mc.TeamAssignments["echovr:3001"]; ok {
		t.Error("spectator was rostered")
	}
	if parser.Mapper().Stats().SpectatorsDropped != 120 {
		t.Errorf("spectators dropped = %d, want 120", parser.Mapper().Stats().SpectatorsDropped)
	}
	if len(frames) != 480 {
		t.Fatalf("%d frames, want 120 lines x 4 players", len(frames))
	}
	// Timestamps come from the line prefix: 67 ms spacing.
	for _, f := range frames {
		if f.DeltaTime != 0 && (f.DeltaTime < 0.066 || f.DeltaTime > 0.068) {
			t.Fatalf("frame %d dt %.4f", f.FrameIndex, f.DeltaTime)
		}
	}
	// Game phases from the recorder: playing, score, round_start.
	phases := map[string]int{}
	for _, f := range frames {
		if f.PlayerID == "echovr:1001" {
			phases[f.GamePhase]++
		}
	}
	if phases["playing"] != 117 || phases["round_over"] != 1 || phases["round_start"] != 2 {
		t.Errorf("phases %v", phases)
	}
	// Score increment reaches the frames.
	if frames[0].BlueScore != 0 || frames[len(frames)-1].BlueScore != 2 {
		t.Errorf("blue score %d -> %d", frames[0].BlueScore, frames[len(frames)-1].BlueScore)
	}

	// Pipeline: every frame valid, the throw is detected, the goal side is
	// learned from the score, the punch increments reach PlayerState, and
	// no production-enabled detector fires on a legitimate session.
	h := testutil.NewHarness(t).WithEnabledDetectors().WithMatchContext(mc)
	// This fixture predates raw velocity fidelity: it moves poses while every
	// player.velocity remains zero, which is intentionally the MOV_006 signal.
	// Exclude that new detector from this older fixture's broad clean assertion.
	walking := h.Config().Detectors["MOV_006"]
	walking.Enabled = false
	h.Config().Detectors["MOV_006"] = walking
	p, _ := h.NewPipeline()
	result, err := p.ProcessMatch(t.Context(), mc, frames)
	if err != nil {
		t.Fatal(err)
	}
	if result.InvalidFrames != 0 || result.FramesProcessed != 120 {
		t.Fatalf("processed %d, invalid %d: %v", result.FramesProcessed, result.InvalidFrames, result.InvalidFrameReasons)
	}
	thrower := p.Players()["echovr:1001"]
	if thrower == nil || thrower.ThrowCount != 1 {
		t.Fatalf("BlueOne throw count = %v", thrower)
	}
	if th := thrower.LastThrow; th == nil || th.ReleaseSpeed < 13.9 || th.ReleaseSpeed > 14.1 || th.TargetPosition == nil {
		t.Errorf("throw = %+v", thrower.LastThrow)
	}
	if z, known := p.Extractor().TeamGoalZ(mc, "blue"); !known || z <= 0 {
		t.Errorf("blue goal side not learned from the score: z=%.1f known=%v", z, known)
	}
	if ps := p.Players()["echovr:2001"]; ps == nil || ps.PrevStuns != 1 {
		t.Errorf("OrangeOne stun stat did not reach PlayerState: %+v", ps)
	}
	if ps := p.Players()["echovr:1002"]; ps == nil || !ps.IsStunned || ps.StunStartFrame != 90 {
		t.Errorf("BlueTwo stun start not tracked (want stunned from frame 90): stunned=%v start=%d", ps.IsStunned, ps.StunStartFrame)
	}
	if len(result.DetectionEvents) != 0 {
		for _, ev := range result.DetectionEvents {
			t.Errorf("legit session flagged: %s %s frame %d %s", ev.DetectorID, ev.PlayerID, ev.FrameIndex, ev.ObservedValue)
		}
	}
}

// TestReplayFixture_LineLimit (F237): an oversized line is a whole-file
// parse error that names the limit, not a silently truncated match.
func TestReplayFixture_LineLimit(t *testing.T) {
	parser := adapter.NewEchoReplayParser()
	parser.SetMaxLineBytes(1024)
	_, _, _, err := parser.ParseFile(syntheticReplay)
	if err == nil {
		t.Fatal("expected a line-limit error")
	}
	if !errors.Is(err, bufio.ErrTooLong) || !strings.Contains(err.Error(), "1024-byte line limit") {
		t.Errorf("error = %v", err)
	}
}

// TestReplayFixture_StreamMatchesParseFile: ParseFileStream delivers the
// same ticks (frame indices, raw payload per tick) as ParseFile.
func TestReplayFixture_StreamMatchesParseFile(t *testing.T) {
	full := adapter.NewEchoReplayParser()
	_, frames, _, err := full.ParseFile(syntheticReplay)
	if err != nil {
		t.Fatal(err)
	}
	stream := adapter.NewEchoReplayParser()
	ticks := 0
	streamed := 0
	_, _, err = stream.ParseFileStream(syntheticReplay, func(tick *adapter.ParsedTick) error {
		if tick.MatchID != "SYN-FIXTURE-001" || tick.FrameIndex != ticks || len(tick.Frames) != 4 || tick.RawJSON == "" {
			t.Errorf("tick %d: %+v", ticks, tick)
		}
		ticks++
		streamed += len(tick.Frames)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 120 || streamed != len(frames) {
		t.Errorf("streamed %d ticks / %d frames, ParseFile gave %d frames", ticks, streamed, len(frames))
	}
	if len(full.RawSessionByFrame()) != 120 {
		t.Errorf("raw payloads captured for %d ticks", len(full.RawSessionByFrame()))
	}
}

// TestReplay_RealFileIfConfigured parses a real recording when
// NEVR_REAL_REPLAY points at one. It is skipped (visibly) otherwise: no
// machine-specific path is hardwired.
func TestReplay_RealFileIfConfigured(t *testing.T) {
	path := os.Getenv(realReplayEnv)
	if path == "" {
		t.Skipf("set %s=<path to an .echoreplay> to run the real-replay check", realReplayEnv)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s=%q: %v", realReplayEnv, path, err)
	}
	parser := adapter.NewEchoReplayParser()
	mc, frames, diag, err := parser.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real replay: match=%s mode=%s players=%d frames=%d rejected=%d", mc.MatchID, mc.GameMode, len(mc.PlayerIDs), len(frames), diag.FramesRejected)
	if len(frames) < 1000 || len(mc.PlayerIDs) < 2 {
		t.Errorf("expected a full match (>1000 frames, >=2 players), got %d frames / %d players", len(frames), len(mc.PlayerIDs))
	}
	h := testutil.NewHarness(t).WithEnabledDetectors().WithMatchContext(mc)
	p, _ := h.NewPipeline()
	result, err := p.ProcessMatch(t.Context(), mc, frames)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("processed=%d invalid=%d (%v) events=%d", result.FramesProcessed, result.InvalidFrames, result.InvalidFrameReasons, len(result.DetectionEvents))
}
