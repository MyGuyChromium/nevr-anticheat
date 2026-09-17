package state

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Frame-count thresholds are durations of game time. They must mean the same
// seconds whatever capture rate the recording measured (real recordings
// arrive at about 15, 20 and 30 Hz), and a missing rate must change nothing.
func TestFrameCountThresholdsMeanTheSameSecondsAtEveryCaptureRate(t *testing.T) {
	stun := NewState002(map[string]any{"min_stun_frames": 20})
	shield := NewState005(map[string]any{"min_cooldown_frames": 60})
	for _, rate := range []float64{0, 15, 15.2, 20, 30.3, 120, math.Inf(1), math.NaN(), -5} {
		mc := &model.MatchContext{TickRate: rate, Physics: model.DefaultPhysics()}
		if got := stun.minimumSeconds(mc); math.Abs(got-20.0/15) > 1e-12 {
			t.Errorf("TickRate %v: minimum stun %.4f s, want %.4f s", rate, got, 20.0/15)
		}
		if got := shield.minimumSeconds(mc); math.Abs(got-4) > 1e-12 {
			t.Errorf("TickRate %v: minimum cooldown %.4f s, want 4 s", rate, got)
		}
	}
	if got := stun.minimumSeconds(nil); math.Abs(got-20.0/15) > 1e-12 {
		t.Errorf("nil context: minimum stun %.4f s", got)
	}
}

// A 1.0 s stun is 30 frames on a 30 Hz recording. It is longer than the
// 20-frame threshold counted in frames but shorter than the 1.33 s the
// threshold stands for, so it must still count as short, exactly as the same
// 1.0 s stun does on a 15 Hz recording.
func TestState002ShortStunIsShortAtEveryCaptureRate(t *testing.T) {
	for _, hz := range []float64{15, 20, 30} {
		d := NewState002(map[string]any{"min_stun_frames": 20, "min_incidents": 2})
		mc := &model.MatchContext{MatchID: "m", TickRate: hz, Physics: model.DefaultPhysics()}
		frames := int(hz) // 1.0 s
		var events []model.DetectionEvent
		for round := 0; round < 2; round++ {
			start := round * 200
			for fi := start; fi <= start+frames+1; fi++ {
				ps := active("p1", fi)
				ps.LastTimestamp = float64(fi) / hz
				ps.IsStunned = fi > start && fi <= start+frames
				events = append(events, d.Evaluate(mc, players(ps), fi)...)
			}
		}
		if len(events) != 1 {
			t.Errorf("%v Hz: two 1.0 s stuns produced %d events, want 1 (same verdict at every rate)", hz, len(events))
		}
	}
}
