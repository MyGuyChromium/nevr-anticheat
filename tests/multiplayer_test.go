package tests

import (
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

// eventSignature is the part of a DetectionEvent that must be reproducible
// across runs (EventIDs are random, timestamps of storage are not).
type eventSignature struct {
	Detector, Player     string
	Frame, Start, End    int
	Severity, Confidence float64
	Observed             string
	Shadow               bool
	Merged               int
}

func signatures(events []model.DetectionEvent) []eventSignature {
	out := make([]eventSignature, 0, len(events))
	for _, ev := range events {
		out = append(out, eventSignature{
			Detector: ev.DetectorID, Player: ev.PlayerID, Frame: ev.FrameIndex,
			Start: ev.FrameRangeStart, End: ev.FrameRangeEnd,
			Severity: ev.Severity, Confidence: ev.Confidence,
			Observed: ev.ObservedValue, Shadow: ev.IsShadow, Merged: ev.MergedCount,
		})
	}
	return out
}

// teleporter is the 2x4 cheater: 11 m jumps every 30 frames.
func teleporter(fb *testutil.FrameBuilder) []model.PlayerTelemetryFrame {
	return fb.TeleportCheat(450, 30, 11.0)
}

// TestMultiPlayer_OnlyTheCheaterIsFlagged: a 4v4 match with seven legit
// players and one teleporting cheater through the production-enabled
// detector set. Only the cheater gets events, every frame is processed and
// TeamAssignments reach PlayerState.Team.
func TestMultiPlayer_OnlyTheCheaterIsFlagged(t *testing.T) {
	const cheaterSlot = 5 // orange2
	mc, frames := testutil.TwoByFour("match-2x4", 450, cheaterSlot, teleporter)
	cheater := testutil.TwoByFourPlayerID(cheaterSlot)
	if len(mc.PlayerIDs) != 8 || mc.TeamAssignments[cheater] != "orange" || mc.TeamAssignments["blue1"] != "blue" {
		t.Fatalf("roster %v teams %v", mc.PlayerIDs, mc.TeamAssignments)
	}

	h := testutil.NewHarness(t).WithEnabledDetectors().WithMatchContext(mc)
	p, _ := h.NewPipeline()
	result, err := p.ProcessMatch(t.Context(), mc, frames)
	if err != nil {
		t.Fatal(err)
	}
	if result.InvalidFrames != 0 || result.FramesProcessed != 450 {
		t.Fatalf("processed %d / invalid %d (%v)", result.FramesProcessed, result.InvalidFrames, result.InvalidFrameReasons)
	}
	for _, pid := range mc.PlayerIDs {
		ps := p.Players()[pid]
		if ps == nil {
			t.Fatalf("no state for %s", pid)
		}
		if ps.Team != mc.TeamAssignments[pid] {
			t.Errorf("%s: PlayerState.Team %q, want %q", pid, ps.Team, mc.TeamAssignments[pid])
		}
		if ps.FrameCount != 450 {
			t.Errorf("%s: %d frames seen", pid, ps.FrameCount)
		}
	}
	flagged := map[string]int{}
	for _, ev := range result.DetectionEvents {
		flagged[ev.PlayerID]++
		if ev.PlayerID != cheater {
			t.Errorf("legit player %s flagged by %s at frame %d: %s", ev.PlayerID, ev.DetectorID, ev.FrameIndex, ev.ObservedValue)
		}
	}
	if flagged[cheater] == 0 {
		t.Fatal("the teleporting cheater was not flagged")
	}
	for pid, sc := range result.PlayerScores {
		if pid != cheater && sc.TotalScore > 0 {
			t.Errorf("legit player %s scored %.1f", pid, sc.TotalScore)
		}
	}
	if result.PlayerScores[cheater].TotalScore <= 0 {
		t.Errorf("cheater has no score")
	}
}

// TestMultiPlayer_RoundResetIsSuppressed (F74): when all eight players
// jump 10 m on the same frame (a round reset with the phase still
// reported as playing) MOV_002's distinct-teleporter cluster suppression
// must swallow it, at production params.
func TestMultiPlayer_RoundResetIsSuppressed(t *testing.T) {
	mb := testutil.NewMatchBuilder("match-reset")
	for i := 0; i < 8; i++ {
		fb := testutil.NewFrameBuilder(testutil.TwoByFourPlayerID(i)).WithStartPos(testutil.TwoByFourStart(i))
		frames := fb.NormalMovingPlayer(300, 3.0+float64(i%4))
		// Every reset moves everyone by 10 m; five resets so one player
		// alone would be over min_incidents.
		for _, at := range []int{60, 100, 140, 180, 220} {
			delta := 10.0
			if frames[at].Position[2] > 0 {
				delta = -10.0
			}
			testutil.ShiftFrom(frames, at, model.Vec3{0, 0, delta})
		}
		mb.AddPlayer(testutil.TwoByFourTeam(i), frames)
	}
	mc, frames := mb.Build()
	hr := testutil.NewHarness(t).WithDetectors("MOV_002").WithMatchContext(mc).Run(t, frames)
	hr.AssertAllFramesValid(300)
	hr.AssertDetectorNotFired("MOV_002")

	// Control: the same five jumps on ONE player are a cheater.
	solo := testutil.NewFrameBuilder("solo").WithStartPos(testutil.TwoByFourStart(0)).NormalMovingPlayer(300, 3.0)
	for _, at := range []int{60, 100, 140, 180, 220} {
		delta := 10.0
		if solo[at].Position[2] > 0 {
			delta = -10.0
		}
		testutil.ShiftFrom(solo, at, model.Vec3{0, 0, delta})
	}
	hr = testutil.NewHarness(t).WithDetectors("MOV_002").Run(t, solo)
	hr.AssertDetectorFiredN("MOV_002", 1) // the 5th jump reaches min_incidents
}

// TestMultiPlayer_State007PunchRangeWithOpponents (STATE_007, telemetry
// dependent, enabled explicitly): a punch landing on an opponent 16 m from
// the puncher's hands is impossible; the third and fourth such punches
// fire (min_incidents 3). The same punches at 1 m are silent, and the
// victim is never flagged.
func TestMultiPlayer_State007PunchRangeWithOpponents(t *testing.T) {
	mc, frames := testutil.PunchScenario(400, 16.0)
	hr := testutil.NewHarness(t).WithDetectors("STATE_007").WithMatchContext(mc).Run(t, frames)
	hr.AssertAllFramesValid(400)
	hr.AssertDetectorFiredN("STATE_007", 2)
	hr.AssertNoDetectionsFor("victim")
	for _, ev := range hr.DetectorEvents("STATE_007") {
		if ev.PlayerID != "puncher" {
			t.Errorf("event names %s", ev.PlayerID)
		}
		if ev.FrameIndex < 260 {
			t.Errorf("fired at frame %d before the 3rd punch (frame 260)", ev.FrameIndex)
		}
	}
	hr.AssertMinSeverity("STATE_007", 0.6)
	hr.AssertMinConfidence("STATE_007", 0.4)

	mc, frames = testutil.PunchScenario(400, 1.0)
	hr = testutil.NewHarness(t).WithDetectors("STATE_007").WithMatchContext(mc).Run(t, frames)
	hr.AssertNoDetections()
}

// TestMultiPlayer_TeamFilterNeedsOpponent: STATE_007 attributes a punch
// only to an opponent whose stun started in the attribution window; a
// teammate stunned at the same moment is never the victim.
func TestMultiPlayer_TeamFilterNeedsOpponent(t *testing.T) {
	mc, frames := testutil.PunchScenario(400, 16.0)
	for i := range frames {
		frames[i].Team = "blue"
	}
	mc.TeamAssignments["victim"] = "blue"
	hr := testutil.NewHarness(t).WithDetectors("STATE_007").WithMatchContext(mc).Run(t, frames)
	hr.AssertNoDetections()
}

// TestMultiPlayer_Deterministic: the same telemetry through two fresh
// pipelines yields identical event lists and scores (event ordering across
// players is deterministic).
func TestMultiPlayer_Deterministic(t *testing.T) {
	mc, frames := testutil.TwoByFour("match-det", 450, 2, func(fb *testutil.FrameBuilder) []model.PlayerTelemetryFrame {
		tele := fb.TeleportCheat(240, 30, 10.0)
		aim := fb.After(tele).AimbotThrows(3)
		return testutil.Concat(tele, aim, fb.After(aim).SpeedHackFrames(90, 75))
	})
	run := func() ([]eventSignature, map[string]float64) {
		h := testutil.NewHarness(t).WithEnabledDetectors().WithMatchContext(mc)
		p, _ := h.NewPipeline()
		res, err := p.ProcessMatch(t.Context(), mc, frames)
		if err != nil {
			t.Fatal(err)
		}
		scores := map[string]float64{}
		for pid, sc := range res.PlayerScores {
			scores[pid] = sc.TotalScore
		}
		return signatures(res.DetectionEvents), scores
	}
	ev1, sc1 := run()
	ev2, sc2 := run()
	if len(ev1) == 0 {
		t.Fatal("no events to compare")
	}
	if !reflect.DeepEqual(ev1, ev2) {
		t.Fatalf("event lists differ:\n%v\n%v", ev1, ev2)
	}
	if !reflect.DeepEqual(sc1, sc2) {
		t.Fatalf("scores differ: %v vs %v", sc1, sc2)
	}
	seen := map[string]bool{}
	for _, e := range ev1 {
		seen[e.Player] = true
	}
	if len(seen) != 1 || !seen[testutil.TwoByFourPlayerID(2)] {
		t.Errorf("flagged players %v, want only the cheater", seen)
	}
	t.Logf("%d events, cheater score %.1f", len(ev1), sc1[testutil.TwoByFourPlayerID(2)])
}

// TestMultiPlayer_SharedDiscFollowsTheThrower: with the disc shared across
// every frame of a tick (as the adapter emits), the throw detectors track
// the thrower's disc even when another player sorts first.
func TestMultiPlayer_SharedDiscFollowsTheThrower(t *testing.T) {
	// "orange3" sorts after every blue player; without a shared disc the
	// sorted-first player's idle disc would be picked in flight.
	mc, frames := testutil.TwoByFour("match-disc", 300, 6, func(fb *testutil.FrameBuilder) []model.PlayerTelemetryFrame {
		return fb.AcceleratingDisc(3)
	})
	shared := 0
	for _, f := range frames {
		if f.FrameIndex == 5 && f.Disc != nil && f.Disc.IsHeld && f.Disc.PossessorID == "orange3" {
			shared++
		}
	}
	if shared != 8 {
		t.Fatalf("held disc shared on %d of 8 frames of tick 5", shared)
	}
	hr := testutil.NewHarness(t).WithDetectors("THROW_008").WithMatchContext(mc).Run(t, frames)
	hr.AssertDetectorFiredN("THROW_008", 3)
	hr.AssertNoDetectionsFor("blue1")
	for i := 0; i < 8; i++ {
		if pid := testutil.TwoByFourPlayerID(i); pid != "orange3" {
			hr.AssertNoDetectionsFor(pid)
		}
	}
}
