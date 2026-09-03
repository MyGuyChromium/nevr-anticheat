package bio

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func ctx() *model.MatchContext {
	return &model.MatchContext{MatchID: "m1", TickRate: 15, Physics: model.DefaultPhysics()}
}

// active returns a PlayerState that the extractor updated at frame fi.
func active(pid string, fi int) *model.PlayerState {
	return &model.PlayerState{
		PlayerID:      pid,
		Position:      model.Vec3{2, 1.6, 3},
		LeftHand:      model.Vec3{1.7, 1.9, 3.2},
		RightHand:     model.Vec3{2.3, 1.9, 2.8},
		LeftHandRot:   model.QuatIdentity(),
		RightHandRot:  model.QuatIdentity(),
		Rotation:      model.QuatIdentity(),
		FrameDt:       0.067,
		FrameCount:    fi + 1,
		LastFrameIdx:  fi,
		LastTimestamp: float64(fi) * 0.067,
	}
}

func players(ps ...*model.PlayerState) map[string]*model.PlayerState {
	m := make(map[string]*model.PlayerState, len(ps))
	for _, p := range ps {
		m[p.PlayerID] = p
	}
	return m
}

// ---- BIO_001 ----

func TestBio001_RequiresThreeSustainedFrames(t *testing.T) {
	d := NewBio001(map[string]any{"min_violation_frames": 2}) // floored to 3
	mc := ctx()
	var total int
	for fi := 0; fi < 2; fi++ {
		ps := active("p1", fi)
		ps.RightWristAngularRate = 120
		total += len(d.Evaluate(mc, players(ps), fi))
	}
	if total != 0 {
		t.Fatalf("2-frame blip must not fire, got %d events", total)
	}
	ps := active("p1", 2)
	ps.RightWristAngularRate = 120
	ev := d.Evaluate(mc, players(ps), 2)
	if len(ev) != 1 {
		t.Fatalf("third sustained frame should fire once, got %d", len(ev))
	}
	if ev[0].FrameRangeStart != 0 || ev[0].FrameRangeEnd != 2 {
		t.Errorf("causal range = %d-%d, want 0-2", ev[0].FrameRangeStart, ev[0].FrameRangeEnd)
	}
}

func TestBio001_ConfidenceGrowsWithStreakAndEvidenceIsWristRate(t *testing.T) {
	d := NewBio001(nil)
	mc := ctx()
	var events []model.DetectionEvent
	for fi := 0; fi < 12; fi++ {
		ps := active("p1", fi)
		ps.RightWristAngularRate = 60 // 20% over the 50 rad/s limit
		ps.RightHandSpeedStats.Update(7.0)
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	if len(events) < 2 {
		t.Fatalf("sustained violation should re-emit with cooldown, got %d events", len(events))
	}
	if !(events[1].Confidence > events[0].Confidence) {
		t.Errorf("confidence must grow with streak: %.3f then %.3f", events[0].Confidence, events[1].Confidence)
	}
	if events[0].Severity < 0.4 || events[0].Severity > 0.6 {
		t.Errorf("severity 20%% over the limit = %.3f, want ~0.5", events[0].Severity)
	}
	wr, ok := events[0].Evidence.(model.WristRotationEvidence)
	if !ok {
		t.Fatalf("evidence type %T", events[0].Evidence)
	}
	if math.Abs(wr.RunningMean-60) > 1e-9 || wr.MaxObserved != 60 {
		t.Errorf("evidence baseline must be wrist rate (rad/s): mean=%.2f max=%.2f", wr.RunningMean, wr.MaxObserved)
	}
}

func TestBio001_ImmuneRespawnGuard(t *testing.T) {
	d := NewBio001(nil)
	mc := ctx()
	for fi := 0; fi < 6; fi++ {
		ps := active("p1", fi)
		ps.IsImmune = true
		ps.LeftWristAngularRate = 150
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("immune player must not fire (frame %d)", fi)
		}
	}
}

func TestBio001_StalePlayerNotReScored(t *testing.T) {
	d := NewBio001(nil)
	mc := ctx()
	stale := active("p2", 0)
	stale.LeftWristAngularRate = 200 // frozen state from frame 0
	for fi := 1; fi < 10; fi++ {
		p1 := active("p1", fi)
		if ev := d.Evaluate(mc, players(p1, stale), fi); len(ev) != 0 {
			t.Fatalf("stale p2 must not be evaluated at frame %d", fi)
		}
	}
}

func TestBio001_UnreachableAt15Hz(t *testing.T) {
	d := NewBio001(nil)
	if d.Reachable(0.067) {
		t.Errorf("pi/0.067 = %.1f rad/s must be below the 50 rad/s threshold", d.SaturationRate(0.067))
	}
	if !d.Reachable(1.0 / 60) {
		t.Error("60 Hz sources should be able to exceed 50 rad/s")
	}
}

// ---- BIO_002 ----

func TestBio002_SeverityJustAboveLimitIsNotZero(t *testing.T) {
	d := NewBio002(nil)
	mc := ctx()
	var events []model.DetectionEvent
	for fi := 0; fi < 8; fi++ {
		ps := active("p1", fi)
		ps.LeftHandSpeed = 60
		ps.LeftHandRelativeSpeed = 55 // 10% over 50 m/s
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	if len(events) < 2 {
		t.Fatalf("expected repeated emissions, got %d", len(events))
	}
	if events[0].Severity < 0.2 {
		t.Errorf("severity at 55 m/s = %.4f, must not collapse to ~0", events[0].Severity)
	}
	if !(events[len(events)-1].Confidence > events[0].Confidence) {
		t.Errorf("confidence must grow with duration: %.3f -> %.3f", events[0].Confidence, events[len(events)-1].Confidence)
	}
	if events[len(events)-1].Confidence < 0.7 {
		t.Errorf("a sustained violation should reach the PAT_004 gate, got %.3f", events[len(events)-1].Confidence)
	}
}

func TestBio002_LegitHandSpeedSilent(t *testing.T) {
	d := NewBio002(nil)
	mc := ctx()
	for fi := 0; fi < 30; fi++ {
		ps := active("p1", fi)
		ps.LeftHandSpeed = 12
		ps.RightHandSpeed = 9
		ps.LeftHandRelativeSpeed = 10
		ps.RightHandRelativeSpeed = 7
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("legit hand speed fired at frame %d", fi)
		}
	}
}

func TestBio002_WorldTranslationIsNotBiomechanicalEvidence(t *testing.T) {
	d := NewBio002(nil)
	mc := ctx()
	for fi := 0; fi < 12; fi++ {
		ps := active("p1", fi)
		ps.Speed = 75
		ps.LeftHandSpeed = 75
		ps.RightHandSpeed = 75
		// Both controllers are stationary relative to the player.
		ps.LeftHandRelativeSpeed = 0
		ps.RightHandRelativeSpeed = 0
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("body translation produced BIO_002 at frame %d", fi)
		}
	}
}

// ---- BIO_003 ----

func botHands(ps *model.PlayerState, i int) {
	w := math.Sin(float64(i)*0.3) * 0.001
	ps.LeftHand = ps.Position.Add(model.Vec3{-0.3 + w, 0.3, 0.2})
	ps.RightHand = ps.Position.Add(model.Vec3{0.3 + w, 0.3, -0.2})
}

func humanHands(ps *model.PlayerState, i int) {
	j := model.Vec3{0.01 * math.Sin(float64(i)*0.73), 0.008 * math.Cos(float64(i)*0.51), 0.006 * math.Sin(float64(i)*1.13)}
	ps.LeftHand = ps.Position.Add(model.Vec3{-0.3, 0.3, 0.2}).Add(j)
	ps.RightHand = ps.Position.Add(model.Vec3{0.3, 0.3, -0.2}).Add(j.Scale(-1))
}

func TestBio003_ActivityGateIsWindowed(t *testing.T) {
	d := NewBio003(map[string]any{"jitter_window_frames": 30, "min_active_frames": 20, "min_consecutive_windows": 1})
	mc := ctx()
	fi := 0
	// 19 active frames with human jitter, then a long idle stretch with
	// resting (bot-like) controllers, then a single active frame.
	for ; fi < 19; fi++ {
		ps := active("p1", fi)
		ps.Speed = 5
		humanHands(ps, fi)
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("unexpected event at frame %d", fi)
		}
	}
	for ; fi < 200; fi++ {
		ps := active("p1", fi)
		ps.Speed = 0
		botHands(ps, fi)
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("idle window must not be evaluated (frame %d)", fi)
		}
	}
	ps := active("p1", fi)
	ps.Speed = 5
	botHands(ps, fi)
	if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
		t.Fatalf("one active frame must not arm a window of resting controllers")
	}
}

func TestBio003_BotPatternFiresWithRankedSeverity(t *testing.T) {
	d := NewBio003(map[string]any{"jitter_window_frames": 30, "min_active_frames": 20, "min_consecutive_windows": 1})
	mc := ctx()
	var events []model.DetectionEvent
	for fi := 0; fi < 60; fi++ {
		ps := active("p1", fi)
		ps.Speed = 5
		botHands(ps, fi)
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	if len(events) == 0 {
		t.Fatal("bot-like hands during active play must fire")
	}
	ev := events[0]
	zj := ev.Evidence.(model.ZeroJitterEvidence)
	if zj.ActiveFrames == 0 {
		t.Errorf("ActiveFrames evidence must be the window count, got 0")
	}
	want := math.Log10(zj.Threshold/zj.PositionVariance) / 2
	if math.Abs(ev.Severity-model.Clamp01(want)) > 1e-9 {
		t.Errorf("severity = %.3f, want log-ratio %.3f", ev.Severity, want)
	}
	if ev.Severity < 0.3 {
		t.Errorf("a window well below threshold should rank above 0.3, got %.3f", ev.Severity)
	}
	// Right-hand evidence in the same tick must report the window count too.
	for _, e := range events {
		if z := e.Evidence.(model.ZeroJitterEvidence); z.Hand == "right" && z.ActiveFrames == 0 {
			t.Errorf("right-hand ActiveFrames reported as 0")
		}
	}
}

func TestBio003_HumanJitterSilent(t *testing.T) {
	d := NewBio003(map[string]any{"jitter_window_frames": 30, "min_active_frames": 20, "min_consecutive_windows": 1})
	mc := ctx()
	for fi := 0; fi < 200; fi++ {
		ps := active("p1", fi)
		ps.Speed = 5
		humanHands(ps, fi)
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("human jitter fired at frame %d: %s", fi, ev[0].ObservedValue)
		}
	}
}

// ---- BIO_004 ----

func TestBio004_WindowedGateAndLogSeverity(t *testing.T) {
	d := NewBio004(map[string]any{"wobble_window_frames": 30, "min_active_frames": 20, "min_consecutive_windows": 1})
	mc := ctx()
	tiny := func(i int) model.Quat {
		h := float64(i) * 0.0001 / 2
		return model.Quat{math.Sin(h), 0, 0, math.Cos(h)}
	}
	// Idle stretch first: gate closed, nothing fires.
	for fi := 0; fi < 60; fi++ {
		ps := active("p1", fi)
		ps.Speed = 0
		ps.LeftHandRot, ps.RightHandRot = tiny(fi), tiny(fi)
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("idle player fired at frame %d", fi)
		}
	}
	var events []model.DetectionEvent
	for fi := 60; fi < 120; fi++ {
		ps := active("p1", fi)
		ps.Speed = 5
		ps.LeftHandRot, ps.RightHandRot = tiny(fi), tiny(fi)
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	if len(events) == 0 {
		t.Fatal("fixed aim during active play must fire")
	}
	zw := events[0].Evidence.(model.ZeroWobbleEvidence)
	want := model.Clamp01(math.Log10(zw.Threshold/zw.RotationVariance) / 2)
	if math.Abs(events[0].Severity-want) > 1e-9 {
		t.Errorf("severity = %.3f, want log-ratio %.3f", events[0].Severity, want)
	}
}
