package state

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// The synthetic positive contains a coherent, steadily curving FREE disc,
// followed by a confirmed catch. It is a regression fixture, not a model of a
// validated exploit or evidence of detector accuracy against real players.
func catchTestFlight(dt float64, curved bool) []map[string]*model.PlayerState {
	const free = 10
	positions, velocities := make([]model.Vec3, free), make([]model.Vec3, free)
	positions[0] = model.Vec3{3, 2, 1}
	for i := 0; i < free; i++ {
		angle := 0.0
		if curved && i >= 4 {
			angle = float64(i-3) * 8 * math.Pi / 180
		}
		velocities[i] = model.Vec3{10 * math.Cos(angle), 10 * math.Sin(angle), 0}
		if i > 0 {
			positions[i] = positions[i-1].Add(velocities[i-1].Add(velocities[i]).Scale(dt / 2))
		}
	}
	hand := positions[free-1].Add(velocities[free-1].Normalized().Scale(.9))
	var ticks []map[string]*model.PlayerState
	for i := 0; i < free+2; i++ {
		position, velocity, holder := hand, model.Vec3{}, "receiver"
		if i < free {
			position, velocity, holder = positions[i], velocities[i], ""
		}
		players := map[string]*model.PlayerState{}
		for _, id := range []string{"receiver", "other"} {
			left := hand
			if id == "other" {
				left = model.Vec3{3, 2, 20}
			}
			head := left.Add(model.Vec3{0, 1.2, 0})
			bounce := 0
			players[id] = &model.PlayerState{
				PlayerID: id, LastFrameIdx: i + 1, LastTimestamp: 10 + float64(i)*dt, FrameCount: i + 1,
				Position: left.Add(model.Vec3{0, 1, 0}), HeadPosition: &head,
				LeftHand: left, RightHand: left.Add(model.Vec3{0, 0, .5}), HasDisc: holder == id,
				CurrentDisc: &model.DiscState{Position: position, Velocity: velocity, Speed: velocity.Magnitude(),
					PossessorID: holder, IsHeld: holder != "", PossessionKnown: true, SampledPlayerCount: 2, BounceCount: &bounce},
			}
		}
		ticks = append(ticks, players)
	}
	return ticks
}

func catchRun(d *State008, ticks []map[string]*model.PlayerState) []model.DetectionEvent {
	var events []model.DetectionEvent
	for _, players := range ticks {
		frame := players["receiver"].LastFrameIdx
		events = append(events, d.Evaluate(ctx(), players, frame)...)
	}
	return events
}

func catchDiscEach(ticks []map[string]*model.PlayerState, index int, change func(*model.DiscState)) {
	for _, p := range ticks[index] {
		change(p.CurrentDisc)
	}
}

func TestState008CurvedFreeFlightNeedsConfirmedCatchAndStaysShadow(t *testing.T) {
	d := NewState008(nil)
	d.SetWeight(1)
	d.SetAutoEnforce(true)
	// Even direct mutation of the embedded base cannot promote the emission.
	d.BaseDetector.Weight, d.BaseDetector.IsAutoEnforce = 1, true
	var reasons []string
	d.SetDecisionObserver(func(_, _ string, _ int, reason string) { reasons = append(reasons, reason) })
	ticks := catchTestFlight(1.0/15, true)
	if events := catchRun(d, ticks[:11]); len(events) != 0 {
		t.Fatalf("unconfirmed catch produced an event: %+v", events)
	}
	events := catchRun(d, ticks[11:])
	if len(events) != 1 {
		t.Fatalf("expected one receiver observation, got %d; branches=%v", len(events), reasons)
	}
	ev := events[0]
	if ev.PlayerID != "receiver" || ev.DetectorID != "STATE_008" || !ev.IsShadow || ev.AutoEnforce || ev.EnforcementWeight != 0 || d.AutoEnforce() || d.DefaultEnforcementWeight() != 0 {
		t.Fatalf("observation attribution/enforcement = %+v", ev)
	}
	evidence := ev.Evidence.(model.StateEvidence)
	if evidence.Metrics["correction_samples"] < 2 || evidence.Metrics["miss_improvement_m"] < .5 || evidence.Metrics["confirmed_catch_frame"] != 12 {
		t.Fatalf("insufficient evidence: %+v", evidence)
	}
	if len(evidence.CatchTrajectory) != 10 || evidence.CatchTrajectory[9].FrameIndex != 10 ||
		!strings.Contains(evidence.Attribution, "cause and actor unverified") || len(evidence.Limitations) < 3 {
		t.Fatalf("missing free-flight path or limitations: %+v", evidence)
	}
	inputs, approaches := 0, 0
	for _, reason := range reasons {
		if reason == "catch_inputs_ready" {
			inputs++
		}
		if reason == "catch_approach_evaluated" {
			approaches++
		}
	}
	if inputs != 24 || approaches != 1 {
		t.Fatalf("coverage inputs=%d approaches=%d, want 24/1", inputs, approaches)
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("invalid event: %v", err)
	}
	if _, err := json.Marshal(evidence); err != nil {
		t.Fatalf("unserializable evidence: %v", err)
	}
	if extra := catchRun(d, []map[string]*model.PlayerState{ticks[11]}); len(extra) != 0 {
		t.Fatal("duplicate frame repeated the observation")
	}
}

func TestState008AbstainsOnLegalAndUnavailableWindows(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]map[string]*model.PlayerState) []map[string]*model.PlayerState
	}{
		{"straight legal catch", func(_ []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			return catchTestFlight(1.0/15, false)
		}},
		{"no catch", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState { return s[:10] }},
		{"one held sample", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState { return s[:11] }},
		{"bounce then catch", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			for i := 5; i < len(s); i++ {
				catchDiscEach(s, i, func(d *model.DiscState) { *d.BounceCount = 1 })
			}
			return s
		}},
		{"bounce counter missing", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			catchDiscEach(s, 5, func(d *model.DiscState) { d.BounceCount = nil })
			return s
		}},
		{"unknown possession", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			catchDiscEach(s, 5, func(d *model.DiscState) { d.PossessionKnown = false })
			return s
		}},
		{"holder conflict", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			catchDiscEach(s, 10, func(d *model.DiscState) { d.PossessionConflict = true })
			return s
		}},
		{"two holders", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			s[10]["other"].HasDisc = true
			return s
		}},
		{"missing other player", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			delete(s[5], "other")
			return s
		}},
		{"stale other player", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			s[5]["other"].LastFrameIdx--
			return s
		}},
		{"missing other hand", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			s[5]["other"].RightHand = model.Vec3{}
			return s
		}},
		{"missing other head", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			s[5]["other"].HeadPosition = nil
			return s
		}},
		{"missing disc", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			s[5]["receiver"].CurrentDisc = nil
			return s
		}},
		{"inconsistent disc copies", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			s[5]["other"].CurrentDisc.Position[0] += .01
			return s
		}},
		{"inconsistent timestamps", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			s[5]["other"].LastTimestamp += .01
			return s
		}},
		{"nonfinite disc", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			catchDiscEach(s, 5, func(d *model.DiscState) { d.Velocity[0] = math.NaN() })
			return s
		}},
		{"overflowing disc vector", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			catchDiscEach(s, 5, func(d *model.DiscState) { d.Velocity[0] = math.MaxFloat64 })
			return s
		}},
		{"scalar vector speed disagreement", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			catchDiscEach(s, 5, func(d *model.DiscState) { d.Speed += 5 })
			return s
		}},
		{"sample gap", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			return append(s[:5], s[6:]...)
		}},
		{"interval jitter", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			for _, p := range s[5] {
				p.LastTimestamp = s[4]["receiver"].LastTimestamp + .01
			}
			return s
		}},
		{"duplicate timestamp", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			for _, p := range s[5] {
				p.LastTimestamp = s[4]["receiver"].LastTimestamp
			}
			return s
		}},
		{"position jump only", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			catchDiscEach(s, 5, func(d *model.DiscState) { d.Position[1] += 2 })
			return s
		}},
		{"velocity turn without positional support", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			straight := catchTestFlight(1.0/15, false)
			for i := range s {
				position := straight[i]["receiver"].CurrentDisc.Position
				catchDiscEach(s, i, func(d *model.DiscState) { d.Position = position })
			}
			return s
		}},
		{"fast tracking spike", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			s[5]["other"].RightHand[0] += 6
			return s
		}},
		{"high ping", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			s[5]["receiver"].IsHighPing = true
			return s
		}},
		{"uncertain held confirmation", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			s[11]["receiver"].HasDisc = false
			s[11]["other"].HasDisc = true
			catchDiscEach(s, 11, func(d *model.DiscState) { d.PossessorID = "other" })
			return s
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ticks := tc.mutate(catchTestFlight(1.0/15, true))
			if events := catchRun(NewState008(nil), ticks); len(events) != 0 {
				t.Fatalf("expected abstention, got %+v", events)
			}
		})
	}
}

func TestState008LegalAttachmentAndMovingReceiver(t *testing.T) {
	t.Run("held attachment cannot manufacture an anomaly", func(t *testing.T) {
		ticks := catchTestFlight(1.0/15, false)
		for i := 10; i < len(ticks); i++ {
			catchDiscEach(ticks, i, func(d *model.DiscState) {
				d.Position = model.Vec3{70, 50, 40}
				d.Velocity = model.Vec3{50, 20, 10}
				d.Speed = d.Velocity.Magnitude()
			})
		}
		if events := catchRun(NewState008(nil), ticks); len(events) != 0 {
			t.Fatalf("attachment created candidate: %+v", events)
		}
	})
	t.Run("held attachment cannot change free-flight evidence", func(t *testing.T) {
		original := catchRun(NewState008(nil), catchTestFlight(1.0/15, true))
		ticks := catchTestFlight(1.0/15, true)
		for i := 10; i < len(ticks); i++ {
			catchDiscEach(ticks, i, func(d *model.DiscState) {
				d.Position = model.Vec3{70, 50, 40}
				d.Velocity = model.Vec3{50, 20, 10}
				d.Speed = d.Velocity.Magnitude()
			})
		}
		modified := catchRun(NewState008(nil), ticks)
		if len(original) != 1 || len(modified) != 1 || !reflect.DeepEqual(original[0].Evidence, modified[0].Evidence) {
			t.Fatal("held attachment changed pre-catch measurement")
		}
	})
	t.Run("receiver intercepts an unchanged trajectory", func(t *testing.T) {
		ticks := catchTestFlight(1.0/15, false)
		for i, players := range ticks {
			delta := model.Vec3{0, float64(11-i) * .1, 0}
			p := players["receiver"]
			p.Position, p.LeftHand, p.RightHand = p.Position.Add(delta), p.LeftHand.Add(delta), p.RightHand.Add(delta)
			head := p.HeadPosition.Add(delta)
			p.HeadPosition = &head
		}
		if events := catchRun(NewState008(nil), ticks); len(events) != 0 {
			t.Fatal("moving receiver made straight flight suspicious")
		}
	})
}

func TestState008SweptSlapAndHeadbuttBetweenSamples(t *testing.T) {
	for _, part := range []string{"hand", "head"} {
		t.Run(part, func(t *testing.T) {
			ticks := catchTestFlight(1.0/15, true)
			// The other player's relevant pose crosses the disc path between
			// samples 5 and 6; neither sampled endpoint is within 0.65 m.
			for i := 0; i < len(ticks); i++ {
				p := ticks[i]["other"]
				near := ticks[5]["receiver"].CurrentDisc.Position
				p.Position = near.Add(model.Vec3{0, 2, 0})
				p.LeftHand = near.Add(model.Vec3{0, 2, 1})
				p.RightHand = near.Add(model.Vec3{0, 2, 2})
				head := near.Add(model.Vec3{0, 2.2, 0})
				p.HeadPosition = &head
				if i == 4 || i == 5 {
					endpoint := ticks[i]["receiver"].CurrentDisc.Position.Add(model.Vec3{0, 0, 1})
					if i == 5 {
						endpoint[2] -= 2
					}
					if part == "hand" {
						p.LeftHand = endpoint
					} else {
						p.HeadPosition = &endpoint
					}
				}
			}
			var contact bool
			d := NewState008(nil)
			d.SetDecisionObserver(func(_, _ string, _ int, r string) { contact = contact || r == "catch_possible_contact" })
			if ev := catchRun(d, ticks); len(ev) != 0 || !contact {
				t.Fatalf("swept %s contact not excluded: events=%d, contact=%t", part, len(ev), contact)
			}
		})
	}
}

func TestState008ShortRegrabHasNoUsableBaseline(t *testing.T) {
	ticks := catchTestFlight(1.0/15, true)
	for i := 0; i < 4; i++ {
		ticks[i]["receiver"].HasDisc = true
		catchDiscEach(ticks, i, func(d *model.DiscState) { d.IsHeld, d.PossessorID = true, "receiver" })
	}
	if ev := catchRun(NewState008(nil), ticks); len(ev) != 0 {
		t.Fatalf("rapid regrab was judged: %+v", ev)
	}
}

func TestState008SingleDeflectionIsNotSustainedCorrection(t *testing.T) {
	ticks := catchTestFlight(1.0/15, true)
	// One 16-degree deflection followed by straight flight is coherent in
	// position/velocity, but is not sustained free-flight correction.
	angle := 16.0 * math.Pi / 180
	velocity := model.Vec3{10 * math.Cos(angle), 10 * math.Sin(angle), 0}
	for i := 4; i < 10; i++ {
		previous := ticks[i-1]["receiver"].CurrentDisc
		position := previous.Position.Add(previous.Velocity.Add(velocity).Scale(1.0 / 30))
		catchDiscEach(ticks, i, func(d *model.DiscState) { d.Position, d.Velocity, d.Speed = position, velocity, 10 })
	}
	var nonsustained bool
	d := NewState008(nil)
	d.SetDecisionObserver(func(_, _ string, _ int, reason string) {
		nonsustained = nonsustained || reason == "catch_correction_not_sustained"
	})
	if events := catchRun(d, ticks); len(events) != 0 || !nonsustained {
		t.Fatalf("single deflection accepted or wrong guard: events=%d, sustained guard=%t", len(events), nonsustained)
	}
}

func TestState008UsesMeasuredVariableIntervals(t *testing.T) {
	ticks := catchTestFlight(1.0/15, true)
	now := ticks[0]["receiver"].LastTimestamp
	for i := 1; i < len(ticks); i++ {
		dt := []float64{.06, .08, .07}[i%3]
		now += dt
		for _, p := range ticks[i] {
			p.LastTimestamp = now
		}
		if i < 10 {
			previous, current := ticks[i-1]["receiver"].CurrentDisc, ticks[i]["receiver"].CurrentDisc
			position := previous.Position.Add(previous.Velocity.Add(current.Velocity).Scale(dt / 2))
			catchDiscEach(ticks, i, func(d *model.DiscState) { d.Position = position })
		}
	}
	last := ticks[9]["receiver"].CurrentDisc
	hand := last.Position.Add(last.Velocity.Normalized().Scale(.9))
	for i, players := range ticks {
		p := players["receiver"]
		p.LeftHand, p.RightHand, p.Position = hand, hand.Add(model.Vec3{0, 0, .5}), hand.Add(model.Vec3{0, 1, 0})
		head := hand.Add(model.Vec3{0, 1.2, 0})
		p.HeadPosition = &head
		if i >= 10 {
			catchDiscEach(ticks, i, func(d *model.DiscState) { d.Position = hand })
		}
	}
	events := catchRun(NewState008(nil), ticks)
	if len(events) != 1 {
		t.Fatalf("coherent variable-interval flight produced %d events", len(events))
	}
	evidence := events[0].Evidence.(model.StateEvidence)
	if evidence.Metrics["max_fit_error_m"] > 1e-10 {
		t.Fatalf("measured intervals were not used for integration: %+v", evidence.Metrics)
	}
}

func TestState008TransformsRatesBoundsAndReset(t *testing.T) {
	for _, dt := range []float64{.05, 1.0 / 15, .1} {
		t.Run(strings.ReplaceAll(fmtFloat(dt), ".", "_"), func(t *testing.T) {
			d := NewState008(nil)
			ticks := catchTestFlight(dt, true)
			base := catchRun(d, ticks)
			if len(base) != 1 {
				t.Fatalf("dt=%v produced %d events", dt, len(base))
			}
			d.Reset()
			again := catchRun(d, ticks)
			if len(again) != 1 || !reflect.DeepEqual(base[0].Evidence, again[0].Evidence) {
				t.Fatal("reset/replay changed deterministic evidence")
			}
			transform := func(v model.Vec3) model.Vec3 { return model.Vec3{-v[1] + 30, v[0] - 8, v[2] + 11} }
			rotate := func(v model.Vec3) model.Vec3 { return model.Vec3{-v[1], v[0], v[2]} }
			for _, players := range ticks {
				for _, p := range players {
					p.Position, p.LeftHand, p.RightHand = transform(p.Position), transform(p.LeftHand), transform(p.RightHand)
					h := transform(*p.HeadPosition)
					p.HeadPosition = &h
					p.CurrentDisc.Position, p.CurrentDisc.Velocity = transform(p.CurrentDisc.Position), rotate(p.CurrentDisc.Velocity)
				}
			}
			transformed := catchRun(NewState008(nil), ticks)
			if len(transformed) != 1 {
				t.Fatal("rigid transform hid the observation")
			}
			a, b := base[0].Evidence.(model.StateEvidence).Metrics, transformed[0].Evidence.(model.StateEvidence).Metrics
			for _, key := range []string{"max_lateral_deviation_m", "actual_hand_miss_m", "expected_hand_miss_m", "miss_improvement_m"} {
				if math.Abs(a[key]-b[key]) > 1e-8 {
					t.Fatalf("transform changed %s: %v -> %v", key, a[key], b[key])
				}
			}
		})
	}
	d := NewState008(map[string]any{"baseline_samples": -100, "min_correction_samples": -20, "max_sample_gap_s": math.NaN(), "max_window_s": math.Inf(1)})
	if d.baselineSamples < 4 || d.minCorrectionSamples < 2 || math.IsNaN(d.maxSampleGap) || math.IsInf(d.maxWindow, 0) {
		t.Fatal("unsafe config defeated sampling floors")
	}
	tick := catchTestFlight(1.0/15, false)[0]
	for i := 0; i < 5000; i++ {
		for _, p := range tick {
			p.LastFrameIdx = i
			p.LastTimestamp = 10 + float64(i)/15
			p.CurrentDisc.Position[0] += 10.0 / 15
		}
		d.Evaluate(ctx(), tick, i)
		if len(d.history) > catchHistoryLimit {
			t.Fatal("unbounded history")
		}
	}
	d.Reset()
	if d.previous != nil || len(d.history) != 0 || d.baseline != nil || d.pending != nil || d.pendingHolder != "" {
		t.Fatal("reset retained match state")
	}
}

func fmtFloat(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
