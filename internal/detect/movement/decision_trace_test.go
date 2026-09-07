package movement

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func qualifyingPlayspaceFrame(fi int) *model.PlayerState {
	ps := active("synthetic-player", fi)
	ps.PlayspaceValid = true
	ps.PlayspaceTrackedHands = 2
	ps.PlayspaceSpeed = 1.4
	ps.PlayspaceDistance = 0.7
	ps.PlayspaceRigCoherence = 0.95
	ps.ReportedVelocity = model.Vec3{0, 0, 2}
	ps.Speed = 3.4
	return ps
}

func TestMov006_DisconnectedShortBurstsDoNotBecomeSustained(t *testing.T) {
	d := NewMov006(nil)
	// Four observations cannot establish the required five-frame burst. A
	// missing player/phase/rejected sample must not supply elapsed evidence.
	for _, fi := range []int{0, 1, 2, 3, 100, 101, 102, 103} {
		if events := d.Evaluate(ctx(), players(qualifyingPlayspaceFrame(fi)), fi); len(events) != 0 {
			t.Fatalf("disconnected short bursts generated a sustained event at frame %d: %+v", fi, events)
		}
	}
}

func TestMov006_ActualLegalMotionGuards(t *testing.T) {
	for _, tt := range []struct {
		name, reason string
		change       func(*model.PlayerState)
	}{
		{"small lean", "playspace_distance_below_gate", func(ps *model.PlayerState) { ps.PlayspaceDistance = 0.45 }},
		{"game locomotion including stacking", "playspace_speed_below_gate", func(ps *model.PlayerState) { ps.PlayspaceSpeed = 0 }},
		{"untracked hand", "tracked_hands_unavailable", func(ps *model.PlayerState) { ps.PlayspaceTrackedHands = 0 }},
		{"controller-only swing", "rig_coherence_below_gate", func(ps *model.PlayerState) { ps.PlayspaceRigCoherence = 0.1 }},
		{"frozen pose", "pose_speed_below_gate", func(ps *model.PlayerState) { ps.Speed = 0 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := NewMov006(nil)
			reasons := map[string]int{}
			d.SetDecisionObserver(func(_, _ string, _ int, code string) { reasons[code]++ })
			for fi := 0; fi < 15; fi++ {
				ps := qualifyingPlayspaceFrame(fi)
				tt.change(ps)
				if events := d.Evaluate(ctx(), players(ps), fi); len(events) != 0 {
					t.Fatalf("guarded synthetic motion emitted %+v", events)
				}
			}
			if reasons[tt.reason] != 15 || len(reasons) != 1 {
				t.Fatalf("expected exact visited guard %s, got %v", tt.reason, reasons)
			}
		})
	}
}
