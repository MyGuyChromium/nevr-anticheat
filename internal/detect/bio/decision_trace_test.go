package bio

import (
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestBio001TraceRequiresObservedSustainedWristMotion(t *testing.T) {
	for _, tt := range []struct {
		name       string
		frames     []int
		rate       float64
		wantEvents int
	}{
		{"short flick or slap-like transient", []int{0, 1}, 100, 0},
		{"disconnected spikes", []int{0, 1, 10, 11}, 100, 0},
		{"three consecutive observations", []int{0, 1, 2}, 100, 1},
		{"sub-threshold flick", []int{0, 1, 2}, 30, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, plain := NewBio001(nil), NewBio001(nil)
			reasons := map[string]int{}
			d.SetDecisionObserver(func(_, _ string, _ int, code string) { reasons[code]++ })
			var got, want []model.DetectionEvent
			for _, fi := range tt.frames {
				ps := active("synthetic-player", fi)
				ps.FrameDt = 1.0 / 60
				ps.RightWristAngularRate = tt.rate
				got = append(got, d.Evaluate(ctx(), players(ps), fi)...)
				want = append(want, plain.Evaluate(ctx(), players(ps), fi)...)
			}
			if len(got) != tt.wantEvents || reasons["wrist_sustained_candidate"] != tt.wantEvents {
				t.Fatalf("events=%d reasons=%v", len(got), reasons)
			}
			for i := range got {
				got[i].EventID, want[i].EventID = "", ""
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("branch observer changed wrist event evidence")
			}
			if reasons["wrist_at_or_below_threshold"] < len(tt.frames) {
				t.Fatal("quiet hand's separate checks missing")
			}
		})
	}
}

func TestBio001TraceNamesActualRespawnAndTimingGuards(t *testing.T) {
	d := NewBio001(nil)
	reasons := map[string]int{}
	d.SetDecisionObserver(func(_, _ string, _ int, code string) { reasons[code]++ })
	for fi := 0; fi < 3; fi++ {
		ps := active("synthetic-player", fi)
		ps.RightWristAngularRate = 100
		switch fi {
		case 0:
			ps.IsStunned = true
		case 1:
			ps.FrameDt = .001
		case 2:
			ps.IsImmune = true
		}
		if got := d.Evaluate(ctx(), players(ps), fi); len(got) != 0 {
			t.Fatal("guarded frame emitted a wrist event")
		}
	}
	if len(reasons) != 3 || reasons["player_stunned"] != 1 || reasons["wrist_interval_too_short"] != 1 || reasons["player_immune"] != 1 {
		t.Fatalf("actual guards=%v", reasons)
	}
}
