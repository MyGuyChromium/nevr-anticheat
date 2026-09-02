package state

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func ctx() *model.MatchContext {
	return &model.MatchContext{MatchID: "m1", TickRate: 15, Physics: model.DefaultPhysics()}
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

// ---- STATE_001 ----

func grabAt(d *State001, dist, speed float64) []model.DetectionEvent {
	mc := ctx()
	p0 := active("p1", 10)
	d.Evaluate(mc, players(p0), 10)
	p1 := active("p1", 11)
	p1.HasDisc = true
	p1.Speed = speed
	p1.CurrentDisc = &model.DiscState{Position: p1.RightHand.Add(model.Vec3{dist, 0, 0})}
	return d.Evaluate(mc, players(p1), 11)
}

func TestState001_FastPlayersAreNotExempt(t *testing.T) {
	if ev := grabAt(NewState001(nil), 5.9, 0); len(ev) != 1 {
		t.Fatalf("5.9 m grab at rest must fire, got %d", len(ev))
	}
	ev := grabAt(NewState001(nil), 5.9, 12)
	if len(ev) != 1 {
		t.Fatalf("5.9 m grab at 12 m/s must still fire (window must not close), got %d", len(ev))
	}
	m := ev[0].Evidence.(model.StateEvidence).Metrics
	if m["closing_credit_m"] > 1.0 {
		t.Errorf("one frame of latency at 12 m/s should credit ~0.8 m, got %.2f", m["closing_credit_m"])
	}
	if ev := grabAt(NewState001(nil), 1.0, 20); len(ev) != 0 {
		t.Fatalf("1 m grab is legitimate, got %d events", len(ev))
	}
	if ev := grabAt(NewState001(nil), 9.0, 0); len(ev) != 0 {
		t.Fatalf("9 m raw distance at rest is a desync artifact, got %d events", len(ev))
	}
}

// ---- STATE_002 ----

func stunFor(d *State002, startFrame int, frames int, dtSec float64) []model.DetectionEvent {
	mc := ctx()
	var events []model.DetectionEvent
	for fi := startFrame; fi <= startFrame+frames; fi++ {
		ps := active("p1", fi)
		ps.LastTimestamp = float64(fi) * dtSec
		ps.IsStunned = fi < startFrame+frames
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	return events
}

func TestState002_MeasuresSeconds(t *testing.T) {
	d := NewState002(map[string]any{"min_stun_frames": 20, "min_incidents": 2})
	// A poll stall: a 3 s stun packed into 15 frames (0.2 s per frame).
	for i := 0; i < 3; i++ {
		if ev := stunFor(d, 100*i, 15, 0.2); len(ev) != 0 {
			t.Fatalf("3 s stun over 15 stalled frames is legitimate, got %d events", len(ev))
		}
	}
	d2 := NewState002(map[string]any{"min_stun_frames": 20, "min_incidents": 2})
	first := stunFor(d2, 0, 5, 0.067) // 0.33 s
	second := stunFor(d2, 100, 5, 0.067)
	if len(first) != 0 || len(second) != 1 {
		t.Fatalf("two 0.33 s stuns must fire on the second: %d/%d", len(first), len(second))
	}
	m := second[0].Evidence.(model.StateEvidence).Metrics
	if m["recovery_seconds"] > 0.4 || m["min_stun_seconds"] < 1.3 || m["expected_stun_seconds"] != 3.0 {
		t.Errorf("metrics = %v", m)
	}
}

func TestState002_ConfigureHonoursAlias(t *testing.T) {
	d := NewState002(nil)
	_ = d.Configure(map[string]any{"min_incidents_to_surface": 7})
	if d.minIncidents != 7 {
		t.Errorf("min_incidents_to_surface alias ignored by Configure: %d", d.minIncidents)
	}
}

// ---- STATE_004 ----

func TestState004_EscalatesWithMeaningfulSeverity(t *testing.T) {
	d := NewState004(nil)
	mc := ctx()
	var events []model.DetectionEvent
	for fi := 0; fi < 900; fi++ {
		ps := active("p1", fi)
		ps.IsImmune = true
		ps.Speed = 3
		events = append(events, d.Evaluate(mc, players(ps), fi)...)
	}
	if len(events) < 3 {
		t.Fatalf("a 60 s streak should re-fire with escalation, got %d events", len(events))
	}
	if events[0].Severity < 0.1 {
		t.Errorf("first event severity = %.4f, must not be score-inert", events[0].Severity)
	}
	last := events[len(events)-1]
	if !(last.Severity > events[0].Severity) || last.Severity < 0.9 {
		t.Errorf("severity must escalate: first %.3f, last %.3f", events[0].Severity, last.Severity)
	}
}

func TestState004_ShortImmunitySilent(t *testing.T) {
	d := NewState004(nil)
	mc := ctx()
	for fi := 0; fi < 200; fi++ {
		ps := active("p1", fi)
		ps.IsImmune = fi < 100 // 100 frames < 225
		ps.Speed = 3
		if ev := d.Evaluate(mc, players(ps), fi); len(ev) != 0 {
			t.Fatalf("respawn immunity fired at frame %d", fi)
		}
	}
}

// ---- STATE_005 ----

func TestState005_MeasuresSeconds(t *testing.T) {
	d := NewState005(map[string]any{"min_cooldown_frames": 60, "min_violations": 1})
	mc := ctx()
	shield := func(fi int, on bool, dtSec float64) []model.DetectionEvent {
		ps := active("p1", fi)
		ps.LastTimestamp = float64(fi) * dtSec
		ps.ShieldActive = on
		return d.Evaluate(mc, players(ps), fi)
	}
	// Stalled polling: shield off for 30 frames that span 6 s (>= 4 s minimum).
	shield(0, true, 0.2)
	shield(1, false, 0.2)
	for fi := 2; fi < 31; fi++ {
		shield(fi, false, 0.2)
	}
	if ev := shield(31, true, 0.2); len(ev) != 0 {
		t.Fatalf("6 s cooldown over stalled frames is legitimate, got %d events", len(ev))
	}
	// Real bypass: 5 frames = 0.33 s.
	shield(40, false, 0.067)
	for fi := 41; fi < 45; fi++ {
		shield(fi, false, 0.067)
	}
	ev := shield(45, true, 0.067)
	if len(ev) != 1 {
		t.Fatalf("0.33 s shield cooldown must fire, got %d", len(ev))
	}
	if m := ev[0].Evidence.(model.StateEvidence).Metrics; m["expected_cooldown_seconds"] != 5.0 {
		t.Errorf("metrics = %v", m)
	}
}

// ---- STATE_006 ----

func TestState006_NeverAutoEnforces(t *testing.T) {
	if NewState006(nil).AutoEnforce() {
		t.Fatal("suspended STATE_006 must not advertise auto-enforce")
	}
}

// ---- STATE_007 ----

type punchScene struct {
	d  *State007
	mc *model.MatchContext
}

func newPunchScene(params map[string]any) *punchScene {
	return &punchScene{d: NewState007(params), mc: ctx()}
}

// frame builds puncher p1 at the origin area and victims from the given specs.
type victimSpec struct {
	id      string
	team    string
	pos     model.Vec3
	stunned bool
}

func (s *punchScene) frame(fi int, punches int, team string, victims ...victimSpec) []model.DetectionEvent {
	p1 := active("p1", fi)
	p1.Team = team
	p1.PrevStuns = punches
	p1.Speed = 2
	ps := players(p1)
	for _, v := range victims {
		o := active(v.id, fi)
		o.Team = v.team
		o.Position = v.pos
		o.IsStunned = v.stunned
		ps[v.id] = o
	}
	return s.d.Evaluate(s.mc, ps, fi)
}

func TestState007_WorksWithoutTeamData(t *testing.T) {
	s := newPunchScene(map[string]any{"min_incidents": 1})
	s.frame(10, 0, "", victimSpec{"p2", "", model.Vec3{22, 1.6, 3}, false})
	ev := s.frame(11, 1, "", victimSpec{"p2", "", model.Vec3{22, 1.6, 3}, true})
	if len(ev) != 1 {
		t.Fatalf("20 m punch with empty teams must fire, got %d", len(ev))
	}
}

func TestState007_TeammatesExcludedWhenTeamsKnown(t *testing.T) {
	s := newPunchScene(map[string]any{"min_incidents": 1})
	s.frame(10, 0, "blue", victimSpec{"p2", "blue", model.Vec3{22, 1.6, 3}, false})
	for fi := 11; fi < 15; fi++ {
		if ev := s.frame(fi, 1, "blue", victimSpec{"p2", "blue", model.Vec3{22, 1.6, 3}, true}); len(ev) != 0 {
			t.Fatalf("a teammate can not be the victim")
		}
	}
}

func TestState007_VictimIsTheNewlyStunnedOpponent(t *testing.T) {
	s := newPunchScene(map[string]any{"min_incidents": 1})
	far := victimSpec{"far", "orange", model.Vec3{20, 1.6, 3}, true} // stunned long ago by someone else
	near := victimSpec{"near", "orange", model.Vec3{3, 1.6, 3}, false}
	for fi := 0; fi < 10; fi++ {
		s.frame(fi, 0, "blue", far, near)
	}
	// Stat increments at frame 10; the victim's stunned flag lands one poll later.
	if ev := s.frame(10, 1, "blue", far, near); len(ev) != 0 {
		t.Fatalf("no candidate yet at the stat frame, got %d events", len(ev))
	}
	near.stunned = true
	ev := s.frame(11, 1, "blue", far, near)
	if len(ev) != 0 {
		t.Fatalf("the 1 m punch on the newly stunned opponent must not fire, got %d (%s)", len(ev), ev[0].ObservedValue)
	}
	if s.d.incidents["p1"] != 0 {
		t.Errorf("stale far-away stunned opponent was attributed as victim")
	}
}

func TestState007_UnattributedPunchExpires(t *testing.T) {
	s := newPunchScene(map[string]any{"min_incidents": 1})
	far := victimSpec{"far", "orange", model.Vec3{20, 1.6, 3}, true}
	s.frame(0, 0, "blue", far)
	for fi := 1; fi < 8; fi++ {
		if ev := s.frame(fi, 1, "blue", far); len(ev) != 0 {
			t.Fatalf("no stun transition near the punch: must not fire (frame %d)", fi)
		}
	}
	if len(s.d.pending) != 0 {
		t.Errorf("pending punches must expire after the attribution window, %d left", len(s.d.pending))
	}
}

func TestState007_ConfigureHonoursAliases(t *testing.T) {
	d := NewState007(nil)
	_ = d.Configure(map[string]any{"min_incidents_to_surface": 9})
	if d.minIncidents != 9 {
		t.Errorf("min_incidents_to_surface alias ignored: %d", d.minIncidents)
	}
	d5 := NewState005(nil)
	_ = d5.Configure(map[string]any{"min_violations_to_surface": 4, "cooldown_bypass_threshold_frames": 33})
	if d5.minViolations != 4 || d5.minCooldownFrames != 33 {
		t.Errorf("STATE_005 aliases ignored: %d/%d", d5.minViolations, d5.minCooldownFrames)
	}
	d4 := NewState004(nil)
	_ = d4.Configure(map[string]any{"immunity_threshold_frames": 100})
	if d4.maxImmuneFrames != 100 {
		t.Errorf("STATE_004 alias ignored: %d", d4.maxImmuneFrames)
	}
}
