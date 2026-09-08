package movement

import (
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestBoostUnknownCannotSeedOrCompleteIntervals(t *testing.T) {
	for _, id := range []string{"MOV_004", "MOV_005"} {
		t.Run(id, func(t *testing.T) {
			var d detect.Detector
			if id == "MOV_004" {
				d = NewMov004(nil)
			} else {
				d = NewMov005(map[string]any{"max_consecutive": 1, "max_boosts_per_window": 1, "min_sequences": 0})
			}
			for fi := 0; fi < 40; fi++ {
				ps := active("p1", fi)
				ps.IsBoostingKnown = fi%2 == 1
				ps.IsBoosting = fi%2 == 1
				ps.Speed = 5
				if ps.IsBoosting {
					ps.Speed = 30
				}
				if events := d.Evaluate(ctx(), players(ps), fi); len(events) != 0 {
					t.Fatalf("unknown off-states created boost interval: %+v", events)
				}
			}
		})
	}
}

func TestMov004UnknownEndAndGapDiscardPeak(t *testing.T) {
	for _, mode := range []string{"unknown", "gap", "source"} {
		t.Run(mode, func(t *testing.T) {
			d := NewMov004(nil)
			for fi := 0; fi < 3; fi++ {
				ps := active("p1", fi)
				ps.Speed = 5 + float64(fi)*10
				ps.IsBoosting = fi > 0
				ps.Observation = &model.ObservationContext{Source: "s", Authority: "client_reported", TimeBasis: "fixture", SessionID: "m", FrameIndex: fi, Timestamp: ps.LastTimestamp}
				d.Evaluate(ctx(), players(ps), fi)
			}
			ps := active("p1", 3)
			ps.Speed = 25
			ps.Observation = &model.ObservationContext{Source: "s", Authority: "client_reported", TimeBasis: "fixture", SessionID: "m", FrameIndex: 3, Timestamp: ps.LastTimestamp}
			switch mode {
			case "unknown":
				ps.IsBoostingKnown = false
			case "gap":
				ps.LastFrameIdx = 5
				ps.LastTimestamp = .335
			case "source":
				ps.Observation.Source = "other"
			}
			if events := d.Evaluate(ctx(), players(ps), ps.LastFrameIdx); len(events) != 0 {
				t.Fatalf("untrusted ending evaluated prior peak: %+v", events)
			}
			if d.wasBoosting["p1"] || len(d.peakSpeed) != 0 || len(d.baselineSpeed) != 0 {
				t.Fatal("untrusted ending retained boost peak")
			}
		})
	}
}

func TestBoostInitialHeldStateIsNotAnActivation(t *testing.T) {
	d4, d5 := NewMov004(nil), NewMov005(nil)
	for fi := 0; fi < 4; fi++ {
		ps := active("p1", fi)
		ps.IsBoosting = fi < 3
		ps.Speed = 5 + 10*float64(fi)
		if len(d4.Evaluate(ctx(), players(ps), fi)) != 0 || len(d5.Evaluate(ctx(), players(ps), fi)) != 0 {
			t.Fatal("initial already-boosting state emitted")
		}
	}
	if len(d5.boostTimestamps["p1"]) != 0 || d5.consecutiveBoosts["p1"] != 0 {
		t.Fatal("initial already-boosting state counted activation")
	}
}
