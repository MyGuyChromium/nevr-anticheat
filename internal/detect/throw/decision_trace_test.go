package throw

import (
	"math"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestThrow001DecisionReasonsAndObservationEquivalence(t *testing.T) {
	for _, tt := range []struct {
		name, reason string
		speed        float64
		wantEvents   int
	}{
		{"exact configured cap", "release_at_or_below_cap", 18.9, 0},
		{"over configured cap", "release_above_cap", 19.0, 1},
		{"sampled artifact band", "sampled_speed_artifact_band", 60, 1},
		{"missing speed", "release_speed_unusable", 0, 0},
		{"nonfinite speed", "release_speed_unusable", math.NaN(), 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := NewThrow001(nil)
			reasons := map[string]int{}
			d.SetDecisionObserver(func(detector, player string, frame int, code string) {
				if detector != "THROW_001" || player != "synthetic-player" || frame != 100 {
					t.Fatal("bad trace attribution")
				}
				reasons[code]++
			})
			states := withThrow("synthetic-player", 100, tt.speed, 0)
			got := d.Evaluate(testCtx(), states, 100)
			want := NewThrow001(nil).Evaluate(testCtx(), withThrow("synthetic-player", 100, tt.speed, 0), 100)
			if reasons[tt.reason] != 1 || len(got) != tt.wantEvents {
				t.Fatalf("reasons=%v events=%d", reasons, len(got))
			}
			for i := range got {
				got[i].EventID, want[i].EventID = "", ""
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("observer changed a detection decision")
			}
			if states["synthetic-player"].LastThrow.ReleaseSpeed != tt.speed && !math.IsNaN(tt.speed) {
				t.Fatal("observer mutated throw")
			}
		})
	}
}

func TestThrow003TracesContactAttributionAndBodyGuards(t *testing.T) {
	for _, tt := range []struct {
		name, reason string
		change       func(*model.ThrowEvent)
	}{
		{"possible headbutt", "possible_head_contact", func(e *model.ThrowEvent) { e.PossibleHeadContact = true }},
		{"slap with unknown hand", "release_hand_unavailable", func(e *model.ThrowEvent) { e.ThrowingHand = "unknown" }},
		{"untracked hand", "release_hand_unavailable", func(e *model.ThrowEvent) { e.HandKinematicsValid = false }},
		{"hand choice ambiguous", "release_hand_unavailable", func(e *model.ThrowEvent) { e.HandTracked = true; e.HandAttributionConfidence = 0 }},
		{"body translation", "body_translation_dominates", func(e *model.ThrowEvent) { e.PlayerVelocity = model.Vec3{0, 0, e.HandSpeed} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := NewThrow003(nil)
			reasons := map[string]int{}
			d.SetDecisionObserver(func(_, _ string, _ int, code string) { reasons[code]++ })
			states := withThrow("synthetic-player", 100, 10, 179)
			tt.change(states["synthetic-player"].LastThrow)
			if got := d.Evaluate(testCtx(), states, 100); len(got) != 0 {
				t.Fatalf("uncertain contact produced wrist-angle event: %+v", got)
			}
			if reasons[tt.reason] != 1 || len(reasons) != 1 {
				t.Fatalf("wrong actual guard: %v", reasons)
			}
		})
	}
}

func TestThrow002TracesUnavailablePreRelease(t *testing.T) {
	d := NewThrow002(nil)
	reasons := map[string]int{}
	d.SetDecisionObserver(func(_, _ string, _ int, code string) { reasons[code]++ })
	states := withThrow("synthetic-player", 100, 35, 0)
	if events := d.Evaluate(testCtx(), states, 100); len(events) != 0 {
		t.Fatal("missing pre-release snapshot produced signal")
	}
	states["synthetic-player"].LastThrow.PreReleaseFrames = []model.ThrowFrameSnapshot{{FrameIndex: 99, DiscMissing: true}}
	if events := d.Evaluate(testCtx(), states, 100); len(events) != 0 {
		t.Fatal("missing disc observation produced signal")
	}
	if reasons["pre_release_unavailable"] != 1 || reasons["pre_release_disc_unusable"] != 1 {
		t.Fatalf("missing branch trace: %v", reasons)
	}
}
