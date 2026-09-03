package pattern

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func ctx() *model.MatchContext {
	return &model.MatchContext{MatchID: "current", TickRate: 15, Physics: model.DefaultPhysics()}
}

func active(pid string, fi int) *model.PlayerState {
	return &model.PlayerState{
		PlayerID:      pid,
		Position:      model.Vec3{2, 1.6, 3},
		LeftHand:      model.Vec3{1.7, 1.9, 3.2},
		RightHand:     model.Vec3{2.3, 1.9, 2.8},
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

// ---- PAT_001 ----

// throwSchedule feeds frames 0..n-1; throwAt[frame] marks a new throw.
func throwSchedule(d *Pat001, n int, throwAt map[int]bool, startCount int) ([]model.DetectionEvent, int) {
	mc := ctx()
	count := startCount
	var events []model.DetectionEvent
	for fi := 0; fi < n; fi++ {
		if throwAt[fi] {
			count++
		}
		ps := active("p1", fi)
		ps.ThrowCount = count
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	return events, count
}

func TestPat001_NoPhantomThrowsAfterDetection(t *testing.T) {
	// The shipped min_throw_count is 12; this test pins 8 so the schedule
	// below fires at the 8th throw.
	d := NewPat001(map[string]any{"min_throw_count": 8})
	throwAt := map[int]bool{}
	for i := 1; i <= 8; i++ {
		throwAt[i*30] = true // perfectly regular: fires at the 8th throw
	}
	events, _ := throwSchedule(d, 241, throwAt, 0)
	if len(events) != 1 {
		t.Fatalf("regular throws should fire exactly once, got %d", len(events))
	}
	// Hold the count with no new throws for 40 frames.
	mc := ctx()
	for fi := 241; fi < 281; fi++ {
		ps := active("p1", fi)
		ps.ThrowCount = 8
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("phantom throw cascade at frame %d: %s", fi, ev[0].ObservedValue)
		}
	}
}

func TestPat001_MoreThanFiftyIrregularThrowsNeverFire(t *testing.T) {
	d := NewPat001(nil)
	throwAt := map[int]bool{}
	fi := 5
	for i := 0; i < 55; i++ {
		throwAt[fi] = true
		fi += 20 + (i*7)%15 // irregular gaps 20..34 frames
	}
	events, count := throwSchedule(d, fi+120, throwAt, 0)
	if count != 55 {
		t.Fatalf("schedule produced %d throws", count)
	}
	if len(events) != 0 {
		t.Fatalf("irregular timing with >50 throws must never fire, got %d (%s)", len(events), events[0].ObservedValue)
	}
}

func TestPat001_UsesThrowCountBaselineNotBufferLength(t *testing.T) {
	d := NewPat001(nil)
	// Player observed mid-match with 30 throws already on the counter.
	events, _ := throwSchedule(d, 10, nil, 30)
	if len(events) != 0 || len(d.throwFrames["p1"]) != 0 {
		t.Fatalf("pre-existing throw count must not be appended as throws")
	}
}

// ---- PAT_002 ----

func yaw(deg float64) model.Quat {
	h := deg * math.Pi / 360
	return model.Quat{0, math.Sin(h), 0, math.Cos(h)}
}

func TestPat002_LeftHandedThrowerNotFlaggedByIdleRightHand(t *testing.T) {
	d := NewPat002(nil)
	mc := ctx()
	for i := 0; i < 12; i++ {
		fi := i * 30
		ps := active("p1", fi)
		ps.ThrowCount = i + 1
		// Left-hand releases spread over 30 cm; right hand rests on the body.
		ps.LastThrow = &model.ThrowEvent{
			ThrowingHand:   "left",
			HandPosition:   ps.Position.Add(model.Vec3{-0.4 + 0.1*float64(i%4), 0.3 + 0.1*float64(i%3), 0.2}),
			PlayerPosition: ps.Position,
			ReleaseSpeed:   10,
		}
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("left-handed thrower flagged by idle right hand at throw %d", i+1)
		}
	}
}

func TestPat002_UnknownThrowingHandIsNotSampled(t *testing.T) {
	d := NewPat002(map[string]any{"min_throw_count": 3})
	mc := ctx()
	for i := 0; i < 10; i++ {
		fi := i * 30
		ps := active("p1", fi)
		ps.ThrowCount = i + 1
		ps.LastThrow = &model.ThrowEvent{
			ThrowingHand:   "unknown",
			PlayerPosition: ps.Position,
			ReleaseSpeed:   12,
		}
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("unknown hand produced release-point evidence at throw %d", i+1)
		}
	}
	if got := len(d.releasePositions["p1"]); got != 0 {
		t.Fatalf("sampled %d unknown-hand release positions", got)
	}
}

func TestPat002_BodyFrameReleasePointFiresWhenFacingChanges(t *testing.T) {
	// The shipped min_throw_count is 12; 8 keeps the ten-throw schedule
	// below firing exactly once.
	d := NewPat002(map[string]any{"min_throw_count": 8})
	mc := ctx()
	rel := model.Vec3{0.4, 0.2, 0.3} // fixed body-relative macro release point
	var events []model.DetectionEvent
	for i := 0; i < 10; i++ {
		fi := i * 30
		ps := active("p1", fi)
		ps.Rotation = yaw(float64(i) * 37)
		ps.ThrowCount = i + 1
		ps.LastThrow = &model.ThrowEvent{
			ThrowingHand:   "right",
			HandPosition:   ps.Position.Add(rel.Rotate(ps.Rotation)),
			PlayerPosition: ps.Position,
			ReleaseSpeed:   12,
		}
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	if len(events) != 1 {
		t.Fatalf("fixed body-frame release point must fire once regardless of facing, got %d", len(events))
	}
	m := events[0].Evidence.(model.PatternEvidence).Metrics
	if m["stddev_3d"] > 1e-6 {
		t.Errorf("body-frame spread = %g, want ~0", m["stddev_3d"])
	}
}

// ---- PAT_003 ----

type stubHistory struct{ events []model.DetectionEvent }

func (s stubHistory) GetPlayerDetections(string, int) ([]model.DetectionEvent, error) {
	return s.events, nil
}

func history(detID string, shadow bool, conf float64, matches int) []model.DetectionEvent {
	var out []model.DetectionEvent
	for i := 0; i < matches; i++ {
		out = append(out, model.DetectionEvent{
			DetectorID: detID, MatchID: "m" + string(rune('a'+i)), PlayerID: "p1",
			Confidence: conf, IsShadow: shadow,
		})
	}
	return out
}

func runPat003(params map[string]any, hist []model.DetectionEvent) []model.DetectionEvent {
	if params == nil {
		params = map[string]any{}
	}
	params["history_provider"] = stubHistory{hist}
	d := NewPat003(params)
	return d.Evaluate(ctx(), players(active("p1", 0)), 0)
}

func TestPat003_IgnoresShadowAndMetaHistory(t *testing.T) {
	if ev := runPat003(nil, history("PAT_001", true, 0.8, 3)); len(ev) != 0 {
		t.Fatalf("shadow history must not be laundered into a scored event, got %d", len(ev))
	}
	if ev := runPat003(nil, history("PAT_003", false, 0.9, 3)); len(ev) != 0 {
		t.Fatalf("PAT_003 must not fire on its own history, got %d", len(ev))
	}
	if ev := runPat003(nil, history("PAT_004", false, 0.9, 3)); len(ev) != 0 {
		t.Fatalf("PAT_004 history is not cross-match evidence, got %d", len(ev))
	}
	ev := runPat003(nil, history("THROW_001", false, 0.8, 3))
	if len(ev) != 1 {
		t.Fatalf("scored THROW_001 in 3 matches must fire, got %d", len(ev))
	}
	if ev[0].CausalKey.AnomalyType != "cross_match_THROW_001" {
		t.Errorf("anomaly = %s", ev[0].CausalKey.AnomalyType)
	}
}

func TestPat003_TrustedListAndSteepness(t *testing.T) {
	hist := history("THROW_001", false, 0.8, 3)
	if ev := runPat003(map[string]any{"trusted_detectors": []any{"MOV_001"}}, hist); len(ev) != 0 {
		t.Fatalf("untrusted detector history must be ignored, got %d", len(ev))
	}
	sharp := runPat003(map[string]any{"sigmoid_steepness": 4.0}, history("THROW_001", false, 0.8, 3))
	flat := runPat003(map[string]any{"sigmoid_steepness": 0.5}, history("THROW_001", false, 0.8, 3))
	if len(sharp) != 1 || len(flat) != 1 {
		t.Fatalf("expected one event each: %d/%d", len(sharp), len(flat))
	}
	if sharp[0].Severity == flat[0].Severity {
		t.Errorf("sigmoid_steepness has no effect: %.3f == %.3f", sharp[0].Severity, flat[0].Severity)
	}
}

// ---- PAT_004 ----

func TestPat004_SortedCategoryEvidence(t *testing.T) {
	d := NewPat004(nil)
	d.RecordDetection("p1", "throw")
	d.RecordDetection("p1", "bio")
	d.RecordDetection("p1", "movement")
	ev := d.Evaluate(ctx(), players(active("p1", 5)), 5)
	if len(ev) != 1 {
		t.Fatalf("expected one composite event, got %d", len(ev))
	}
	if ev[0].ObservedValue != "multi_cheat: 3 categories [bio, movement, throw]" {
		t.Errorf("observed = %q", ev[0].ObservedValue)
	}
}

// ---- PAT_005 ----

func reach(d *Pat005, n int, hands func(ps *model.PlayerState)) []model.DetectionEvent {
	mc := ctx()
	var events []model.DetectionEvent
	for fi := 0; fi < n; fi++ {
		ps := active("p1", fi)
		ps.Position = model.Vec3{2, 1.6, 30}
		hands(ps)
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	return events
}

func TestPat005_TrackingLossNeverFires(t *testing.T) {
	ev := reach(NewPat005(nil), 100, func(ps *model.PlayerState) {
		ps.LeftHand, ps.RightHand = model.Vec3{}, model.Vec3{}
	})
	if len(ev) != 0 {
		t.Fatalf("zero hand vectors are tracking loss, got %d events (%s)", len(ev), ev[0].ObservedValue)
	}
	ev = reach(NewPat005(nil), 100, func(ps *model.PlayerState) {
		ps.LeftHand = model.Vec3{}
		ps.RightHand = ps.Position.Add(model.Vec3{3, 0, 0})
	})
	if len(ev) != 0 {
		t.Fatalf("one untracked hand must skip the frame, got %d events", len(ev))
	}
}

func TestPat005_SustainedReachFiresWithGrowingConfidence(t *testing.T) {
	ev := reach(NewPat005(nil), 100, func(ps *model.PlayerState) {
		ps.LeftHand = ps.Position.Add(model.Vec3{-3, 0.3, 0.2})
		ps.RightHand = ps.Position.Add(model.Vec3{3, 0.3, -0.2})
	})
	if len(ev) != 3 {
		t.Fatalf("3 m reach for 100 frames should emit at 30/60/90 frames, got %d", len(ev))
	}
	if ev[0].Severity < 0.5 {
		t.Errorf("severity for a 3 m reach = %.3f, must not be pinned near 0", ev[0].Severity)
	}
	if !(ev[2].Confidence > ev[0].Confidence) {
		t.Errorf("confidence must grow with duration: %.3f -> %.3f", ev[0].Confidence, ev[2].Confidence)
	}
	if m := ev[2].Evidence.(model.PatternEvidence).Metrics; m["consecutive_frames"] != 90 {
		t.Errorf("consecutive_frames = %v, want 90 (counter must keep growing)", m["consecutive_frames"])
	}
}

func TestPat005_LegitimateReachSilent(t *testing.T) {
	ev := reach(NewPat005(nil), 200, func(ps *model.PlayerState) {
		ps.LeftHand = ps.Position.Add(model.Vec3{-1.0, 0.8, 0.6}) // 1.41 m
		ps.RightHand = ps.Position.Add(model.Vec3{1.2, 0.6, 0.6}) // 1.47 m
	})
	if len(ev) != 0 {
		t.Fatalf("a 1.5 m reach is legitimate, got %d events", len(ev))
	}
}
