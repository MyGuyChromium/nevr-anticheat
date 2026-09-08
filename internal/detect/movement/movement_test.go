package movement

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func ctx() *model.MatchContext {
	return &model.MatchContext{MatchID: "m1", TickRate: 15, Physics: model.DefaultPhysics()}
}

func active(pid string, fi int) *model.PlayerState {
	return &model.PlayerState{
		PlayerID:        pid,
		IsBoostingKnown: true,
		Position:        model.Vec3{2, 1.6, 3},
		Rotation:        model.QuatIdentity(),
		FrameDt:         0.067,
		FrameCount:      fi + 1,
		LastFrameIdx:    fi,
		LastTimestamp:   float64(fi) * 0.067,
	}
}

func players(ps ...*model.PlayerState) map[string]*model.PlayerState {
	m := make(map[string]*model.PlayerState, len(ps))
	for _, p := range ps {
		m[p.PlayerID] = p
	}
	return m
}

// productionMov001 mirrors configs/default.toml.
func productionMov001() *Mov001 {
	return NewMov001(map[string]any{"max_legitimate_speed": 55.0, "sustained_speed_window": 30})
}

// ---- MOV_001 ----

func TestMov001_StalePlayerNotReScored(t *testing.T) {
	d := productionMov001()
	mc := ctx()
	var stale *model.PlayerState
	for fi := 0; fi < 200; fi++ {
		p1 := active("p1", fi)
		p1.Speed = 5
		ps := players(p1)
		if fi <= 50 {
			p2 := active("p2", fi)
			p2.Speed = 5
			if fi == 50 {
				p2.Speed = 65 // one-frame jump, then the player leaves
				stale = p2
			}
			ps["p2"] = p2
		} else {
			ps["p2"] = stale
		}
		if ev := d.Evaluate(mc, ps, fi); len(ev) != 0 {
			t.Fatalf("stale p2 produced %d events at frame %d (%s)", len(ev), fi, ev[0].ObservedValue)
		}
	}
}

func TestMov001_SingleFrameGlitchesDoNotFireBurst(t *testing.T) {
	d := productionMov001()
	mc := ctx()
	for fi := 0; fi < 120; fi++ {
		ps := active("p1", fi)
		ps.Speed = 5
		if fi == 40 || fi == 50 {
			ps.Speed = 4.0 / 0.067 // 4 m glitch in one frame = 59.7 m/s
		}
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("two single-frame glitches fired at frame %d: %s", fi, ev[0].ObservedValue)
		}
	}
}

func TestMov001_BurstBelowPhysicsCapSilent(t *testing.T) {
	d := productionMov001()
	mc := ctx()
	for fi := 0; fi < 120; fi++ {
		ps := active("p1", fi)
		ps.Speed = 5
		if fi >= 40 && fi < 46 {
			ps.Speed = 52 // legit per the 55 m/s cap
		}
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("52 m/s burst under the 55 m/s cap fired at frame %d", fi)
		}
	}
}

func TestMov001_SustainedBurstFiresOnce(t *testing.T) {
	d := productionMov001()
	mc := ctx()
	var events []model.DetectionEvent
	for fi := 0; fi < 150; fi++ {
		ps := active("p1", fi)
		ps.Speed = 5
		if fi >= 40 && fi < 46 {
			ps.Speed = 62
		}
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	if len(events) != 1 {
		t.Fatalf("one 6-frame burst over the cap should yield exactly one event, got %d", len(events))
	}
	if events[0].CausalKey.AnomalyType != "oscillating_speed" {
		t.Errorf("anomaly = %s", events[0].CausalKey.AnomalyType)
	}
	m := events[0].Evidence.(model.MovementEvidence).Metrics
	// Fires as soon as min_burst_frames (5) samples exceed the physics cap.
	if m["burst_threshold"] != 55 || m["frames_above_threshold"] < 5 {
		t.Errorf("metrics = %v", m)
	}
}

func TestMov001_SustainedSpeedHackFires(t *testing.T) {
	d := productionMov001()
	mc := ctx()
	var events []model.DetectionEvent
	for fi := 0; fi < 60; fi++ {
		ps := active("p1", fi)
		ps.Speed = 80
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	if len(events) == 0 {
		t.Fatal("sustained 80 m/s must fire")
	}
	if events[0].Severity < 0.9 || events[0].Confidence < 0.9 {
		t.Errorf("severity/confidence = %.2f/%.2f", events[0].Severity, events[0].Confidence)
	}
}

// ---- MOV_006 ----

func TestMov006_CoherentPhysicalWalkFiresOncePerBurst(t *testing.T) {
	d := NewMov006(nil)
	mc := ctx()
	var events []model.DetectionEvent
	for fi := 0; fi < 8; fi++ {
		ps := active("p1", fi)
		ps.PlayspaceValid = true
		ps.PlayspaceTrackedHands = 2
		ps.PlayspaceSpeed = 1.4
		ps.PlayspaceDistance = 0.7
		ps.PlayspaceRigCoherence = 0.95
		ps.ReportedVelocity = model.Vec3{0, 0, 2}
		ps.Speed = 3.4
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	if len(events) != 1 {
		t.Fatalf("coherent walk emitted %d events, want 1", len(events))
	}
	if event := events[0]; event.DetectorID != "MOV_006" || event.CausalKey.AnomalyType != "playspace_walking" || event.AutoEnforce {
		t.Errorf("event = %+v", event)
	}
	metrics := events[0].Evidence.(model.MovementEvidence).Metrics
	if metrics["tracked_hands"] != 2 || metrics["reported_game_speed"] != 2 || metrics["sustained_frames"] != 6 || metrics["sustained_seconds"] < 0.3 {
		t.Errorf("metrics = %v", metrics)
	}

	// A clean frame closes the burst; a later sustained burst is a separate
	// reviewable incident rather than being suppressed for the whole match.
	clean := active("p1", 8)
	d.Evaluate(mc, players(clean), 8)
	for fi := 9; fi < 16; fi++ {
		ps := active("p1", fi)
		ps.PlayspaceValid, ps.PlayspaceTrackedHands = true, 1
		ps.PlayspaceSpeed, ps.PlayspaceDistance, ps.PlayspaceRigCoherence = 1.2, 0.6, 0.8
		ps.Speed = 1.2
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	if len(events) != 2 {
		t.Fatalf("second walk burst not emitted: %d events", len(events))
	}
}

func TestMov006_FrozenRemotePoseWithStaleGameVelocityIsSilent(t *testing.T) {
	d := NewMov006(nil)
	for fi := 0; fi < 12; fi++ {
		ps := active("p1", fi)
		ps.PlayspaceValid, ps.PlayspaceTrackedHands = true, 2
		ps.PlayspaceSpeed, ps.PlayspaceDistance, ps.PlayspaceRigCoherence = 4.99, 2.46, 1
		ps.ReportedVelocity = model.Vec3{0, 0, 4.99}
		ps.Speed = 0 // real replay pattern: head and both hands are byte-for-byte frozen
		if events := d.Evaluate(ctx(), players(ps), fi); len(events) != 0 {
			t.Fatalf("frozen remote pose emitted at frame %d: %+v", fi, events)
		}
	}
}

func TestMov006_LegalLeanAndShortBurstAreSilent(t *testing.T) {
	tests := []struct {
		name     string
		frames   int
		distance float64
	}{
		{name: "ordinary lean stays inside displacement gate", frames: 12, distance: 0.45},
		{name: "quick lunge stays inside duration gate", frames: 4, distance: 0.7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewMov006(nil)
			for fi := 0; fi < tt.frames; fi++ {
				ps := active("p1", fi)
				ps.PlayspaceValid, ps.PlayspaceTrackedHands = true, 2
				ps.PlayspaceSpeed, ps.PlayspaceDistance, ps.PlayspaceRigCoherence = 1.4, tt.distance, 0.95
				if events := d.Evaluate(ctx(), players(ps), fi); len(events) != 0 {
					t.Fatalf("legal movement emitted at frame %d: %+v", fi, events)
				}
			}
		})
	}
}

func TestMov006_StackingAndUnreliableTrackingAreSilent(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*model.PlayerState)
	}{
		{"stacking residual is zero", func(ps *model.PlayerState) { ps.PlayspaceSpeed = 0 }},
		{"too little displacement", func(ps *model.PlayerState) { ps.PlayspaceDistance = 0.1 }},
		{"hands not tracked", func(ps *model.PlayerState) { ps.PlayspaceTrackedHands = 0 }},
		{"incoherent rig", func(ps *model.PlayerState) { ps.PlayspaceRigCoherence = 0.1 }},
		{"high ping", func(ps *model.PlayerState) { ps.EstimatedPingMs = 300 }},
		{"invalid reconstruction", func(ps *model.PlayerState) { ps.PlayspaceValid = false }},
		{"frozen observed pose", func(ps *model.PlayerState) { ps.Speed = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewMov006(nil)
			for fi := 0; fi < 6; fi++ {
				ps := active("p1", fi)
				ps.PlayspaceValid, ps.PlayspaceTrackedHands = true, 2
				ps.PlayspaceSpeed, ps.PlayspaceDistance, ps.PlayspaceRigCoherence = 1.2, 0.7, 0.9
				ps.Speed = 1.2
				tt.alter(ps)
				if events := d.Evaluate(ctx(), players(ps), fi); len(events) != 0 {
					t.Fatalf("unexpected event at frame %d: %+v", fi, events)
				}
			}
		})
	}
}

// ---- MOV_002 ----

func productionMov002(extra map[string]any) *Mov002 {
	params := map[string]any{"teleport_threshold": 8.0, "velocity_mismatch_factor": 3.0}
	for k, v := range extra {
		params[k] = v
	}
	return NewMov002(params)
}

// runTeleports moves p1 by jump metres on each frame in jumpAt, with an
// optional second player state supplied by other(fi).
func runTeleports(d *Mov002, jumpAt map[int]float64, other func(fi int) *model.PlayerState, frames int) []model.DetectionEvent {
	mc := ctx()
	var events []model.DetectionEvent
	z := 0.0
	for fi := 0; fi < frames; fi++ {
		p1 := active("p1", fi)
		if j, ok := jumpAt[fi]; ok {
			z += j
		}
		p1.Position = model.Vec3{2, 1.6, z}
		ps := players(p1)
		if other != nil {
			if o := other(fi); o != nil {
				ps[o.PlayerID] = o
			}
		}
		events = append(events, d.Evaluate(mc, ps, fi)...)
	}
	return events
}

func TestMov002_StalePlayerDoesNotSilenceDetection(t *testing.T) {
	d := productionMov002(map[string]any{"min_incidents": 1})
	stale := active("p2", 0)
	stale.PrevBlueScore = 3 // differs from p1's score forever
	jumps := map[int]float64{30: 10, 100: 10, 170: 10, 240: 10, 310: 10}
	events := runTeleports(d, jumps, func(int) *model.PlayerState { return stale }, 400)
	if len(events) != 5 {
		t.Fatalf("expected 5 teleport events with a stale second player present, got %d", len(events))
	}
}

func TestMov002_RepeatedTeleportsBySameCheaterAreNotSuppressed(t *testing.T) {
	d := productionMov002(map[string]any{"min_incidents": 1})
	events := runTeleports(d, map[int]float64{100: 10, 130: 10}, nil, 200)
	if len(events) != 2 {
		t.Fatalf("one player teleporting twice inside the cluster window must yield 2 events, got %d", len(events))
	}
}

func TestMov002_MultiPlayerClusterSuppressed(t *testing.T) {
	d := productionMov002(map[string]any{"min_incidents": 1})
	other := func(fi int) *model.PlayerState {
		p2 := active("p2", fi)
		z := 20.0
		if fi >= 100 {
			z = 30 // p2 jumps 10 m on the same frame as p1 (round reset)
		}
		p2.Position = model.Vec3{-2, 1.6, z}
		return p2
	}
	events := runTeleports(d, map[int]float64{100: 10, 130: 10}, other, 200)
	if len(events) != 0 {
		t.Fatalf("two distinct players teleporting inside the cluster window is a round reset, got %d events", len(events))
	}
}

func TestMov002_SeverityAndDisplacementCap(t *testing.T) {
	d := productionMov002(map[string]any{"min_incidents": 1})
	events := runTeleports(d, map[int]float64{50: 10}, nil, 60)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Severity < 0.45 || events[0].Severity > 0.55 {
		t.Errorf("10 m teleport (midpoint of 8..12) severity = %.3f, want ~0.5", events[0].Severity)
	}
	// Above max_displacement is treated as a game-event reset.
	d2 := productionMov002(map[string]any{"min_incidents": 1})
	if ev := runTeleports(d2, map[int]float64{50: 20}, nil, 60); len(ev) != 0 {
		t.Errorf("20 m jump exceeds the default 12 m cap and must be dropped, got %d", len(ev))
	}
	// Raising the cap makes long-range teleports detectable.
	d3 := productionMov002(map[string]any{"min_incidents": 1, "max_displacement": 40.0})
	if ev := runTeleports(d3, map[int]float64{50: 20}, nil, 60); len(ev) != 1 {
		t.Errorf("with max_displacement 40 the 20 m jump must fire, got %d", len(ev))
	}
}

func TestMov002_DeterministicAcrossRuns(t *testing.T) {
	run := func() string {
		d := productionMov002(map[string]any{"min_incidents": 1})
		stale := active("zz", 0)
		stale.PrevBlueScore = 9
		events := runTeleports(d, map[int]float64{30: 10, 90: 10}, func(int) *model.PlayerState { return stale }, 120)
		out := ""
		for _, e := range events {
			out += e.PlayerID + "@" + e.ObservedValue + ";"
		}
		return out
	}
	first := run()
	for i := 0; i < 5; i++ {
		if got := run(); got != first {
			t.Fatalf("run %d differs:\n%s\n%s", i, first, got)
		}
	}
	if first == "" {
		t.Fatal("expected events")
	}
}

// ---- MOV_003 ----

func velocities(d *Mov003, vels []model.Vec3) []model.DetectionEvent {
	mc := ctx()
	var events []model.DetectionEvent
	for fi, v := range vels {
		ps := active("p1", fi)
		ps.Velocity = v
		ps.Speed = v.Magnitude()
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	return events
}

func TestMov003_TrueReversalFiresFlipFlopDoesNot(t *testing.T) {
	fwd := model.Vec3{20, 0, 0}
	back := model.Vec3{-20, 0, 0}
	real := velocities(NewMov003(nil), []model.Vec3{fwd, fwd, fwd, back, back, back, back, back})
	if len(real) != 1 {
		t.Fatalf("instant reversal with a held heading must fire exactly once, got %d", len(real))
	}
	if real[0].FrameRangeStart != 2 {
		t.Errorf("causal start = %d, want the frame before the reversal (2)", real[0].FrameRangeStart)
	}
	jitter := velocities(NewMov003(nil), []model.Vec3{fwd, back, fwd, back, fwd, back, fwd, back})
	if len(jitter) != 0 {
		t.Fatalf("(v, -v, v, -v) position jitter must not fire, got %d", len(jitter))
	}
}

func TestMov003_CollisionAndSlowReversalsSkipped(t *testing.T) {
	d := NewMov003(nil)
	mc := ctx()
	fwd := model.Vec3{20, 0, 0}
	back := model.Vec3{-20, 0, 0}
	vels := []model.Vec3{fwd, fwd, back, back, back, back}
	for fi, v := range vels {
		p1 := active("p1", fi)
		p1.Velocity, p1.Speed = v, v.Magnitude()
		p2 := active("p2", fi)
		p2.Position = p1.Position.Add(model.Vec3{1, 0, 0}) // body contact
		if ev := d.Evaluate(mc, players(p1, p2), fi); len(ev) != 0 {
			t.Fatalf("reversal next to another player must be treated as a collision")
		}
	}
	slow := velocities(NewMov003(nil), []model.Vec3{{5, 0, 0}, {5, 0, 0}, {-5, 0, 0}, {-5, 0, 0}, {-5, 0, 0}, {-5, 0, 0}})
	if len(slow) != 0 {
		t.Fatalf("reversal below min_speed must not fire")
	}
}

// ---- MOV_004 ----

func TestMov004_ComparesBoostGainNotAbsoluteSpeed(t *testing.T) {
	d := NewMov004(nil) // cap 5 + margin 1.5
	mc := ctx()
	speeds := []struct {
		speed float64
		boost bool
	}{{20, false}, {20, false}, {22, true}, {24, true}, {24, true}, {23, false}}
	for fi, s := range speeds {
		ps := active("p1", fi)
		ps.Speed, ps.IsBoosting = s.speed, s.boost
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("a +4 m/s boost while cruising at 20 m/s must not fire (frame %d)", fi)
		}
	}
	cheat := []struct {
		speed float64
		boost bool
	}{{20, false}, {26, true}, {31, true}, {31, true}, {30, false}}
	var events []model.DetectionEvent
	for i, s := range cheat {
		fi := 10 + i
		ps := active("p1", fi)
		ps.Speed, ps.IsBoosting = s.speed, s.boost
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	if len(events) != 1 {
		t.Fatalf("a +11 m/s boost gain must fire once, got %d", len(events))
	}
	if events[0].FrameRangeStart != 11 {
		t.Errorf("causal start = %d, want boost start frame 11", events[0].FrameRangeStart)
	}
	if m := events[0].Evidence.(model.MovementEvidence).Metrics; m["boost_gain"] != 11 {
		t.Errorf("boost_gain = %v, want 11", m["boost_gain"])
	}
}

// ---- MOV_005 ----

func TestMov005_ContinuousBoostIsOneActivation(t *testing.T) {
	d := NewMov005(map[string]any{"min_sequences": 0})
	mc := ctx()
	for fi := 0; fi < 40; fi++ {
		ps := active("p1", fi)
		ps.IsBoosting = fi >= 5 && fi < 17 // one 12-frame boost
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("one continuous boost must not read as boost spam (frame %d): %s", fi, ev[0].ObservedValue)
		}
		if fi == 16 && d.consecutiveBoosts["p1"] != 1 {
			t.Fatalf("consecutive activations = %d during a single boost, want 1", d.consecutiveBoosts["p1"])
		}
	}
}

func TestMov005_TapSpamFiresAtProductionDefaults(t *testing.T) {
	d := NewMov005(nil)
	mc := ctx()
	var events []model.DetectionEvent
	for fi := 0; fi < 300; fi++ {
		ps := active("p1", fi)
		c := fi % 60
		ps.IsBoosting = c < 40 && c%5 < 2 // eight taps then a 20-frame pause
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	if len(events) == 0 {
		t.Fatal("repeated tap sequences must fire")
	}
	m := events[0].Evidence.(model.MovementEvidence).Metrics
	if m["violation_sequences"] < 2 {
		t.Errorf("expected >= 2 closed violation sequences, got %v", m["violation_sequences"])
	}
}

func TestMov003_FrameGapDiscardsPendingReversal(t *testing.T) {
	d := NewMov003(nil)
	mc := ctx()
	fwd := model.Vec3{20, 0, 0}
	back := model.Vec3{-20, 0, 0}
	seq := []struct {
		fi int
		v  model.Vec3
	}{{0, fwd}, {1, fwd}, {2, back}, {3, back}, {10, back}, {11, back}, {12, back}}
	for _, s := range seq {
		ps := active("p1", s.fi)
		ps.Velocity, ps.Speed = s.v, s.v.Magnitude()
		if ev := d.Evaluate(mc, players(ps), s.fi); len(ev) != 0 {
			t.Fatalf("a reversal whose confirmation spans a frame gap must not fire (frame %d)", s.fi)
		}
	}
}

func TestMov002_ScoreSampleSkipsNeverUpdatedPlaceholder(t *testing.T) {
	d := productionMov002(map[string]any{"min_incidents": 1})
	mc := ctx()
	placeholder := &model.PlayerState{PlayerID: "aaa"} // listed in PlayerIDs, never sent a frame
	z := 0.0
	var events []model.DetectionEvent
	for fi := 0; fi < 80; fi++ {
		p1 := active("p1", fi)
		p1.PrevBlueScore = 0
		if fi >= 40 {
			p1.PrevBlueScore = 2 // goal at frame 40 -> reset teleports must be suppressed
		}
		if fi == 45 {
			z += 10
		}
		p1.Position = model.Vec3{2, 1.6, z}
		events = append(events, d.Evaluate(mc, players(p1, placeholder), fi)...)
	}
	if len(events) != 0 {
		t.Fatalf("goal cooldown must be armed from a player with frames, got %d events", len(events))
	}
}
