package throw

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestReleaseDirectionRejectsContradictoryMeasurements(t *testing.T) {
	for name, mutate := range map[string]func(*model.ThrowEvent){
		"angle contradicts vectors":                func(te *model.ThrowEvent) { te.HandVelocity = model.Vec3{5, 0, 0} },
		"angle outside geometric range":            func(te *model.ThrowEvent) { te.ReleaseAngle = 181 },
		"hand speed contradicts vector":            func(te *model.ThrowEvent) { te.HandSpeed = 50 },
		"relative speed contradicts vector":        func(te *model.ThrowEvent) { te.HandRelativeSpeed = 50 },
		"zero disc direction":                      func(te *model.ThrowEvent) { te.ReleaseVelocity = model.Vec3{} },
		"finite vector with overflowing magnitude": func(te *model.ThrowEvent) { te.HandVelocity = model.Vec3{math.MaxFloat64, 0, 0} },
		"unknown hand token":                       func(te *model.ThrowEvent) { te.ThrowingHand = "LEFT" },
		"NaN thrower confidence":                   func(te *model.ThrowEvent) { te.Attribution.Confidence = math.NaN() },
		"negative thrower confidence":              func(te *model.ThrowEvent) { te.Attribution.Confidence = -.1 },
		"zero thrower confidence":                  func(te *model.ThrowEvent) { te.Attribution.Confidence = 0 },
		"inflated thrower confidence":              func(te *model.ThrowEvent) { te.Attribution.Confidence = 2 },
		"NaN hand confidence":                      func(te *model.ThrowEvent) { te.HandAttributionConfidence = math.NaN() },
		"negative hand confidence":                 func(te *model.ThrowEvent) { te.HandAttributionConfidence = -.1 },
		"inflated hand confidence":                 func(te *model.ThrowEvent) { te.HandAttributionConfidence = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			players := angleObservedFixture()
			mutate(players["p1"].LastThrow)
			if events := NewThrow003(nil).Evaluate(testCtx(), players, 11); len(events) != 0 {
				t.Fatalf("contradictory measurement produced finding: %+v", events)
			}
		})
	}
	// Positive control retains the configured 177-degree comparison and does
	// not turn this integrity check into a new game-physics threshold.
	if events := NewThrow003(nil).Evaluate(testCtx(), angleObservedFixture(), 11); len(events) != 1 {
		t.Fatalf("consistent sampled release was lost: %d", len(events))
	}
}

func TestReleaseDirectionValidatesSuppliedConfirmationWithoutReleaseSource(t *testing.T) {
	for _, missingWindow := range []bool{false, true} {
		players := angleObservedFixture()
		ps := players["p1"]
		if missingWindow {
			ps.LastThrow.ReleaseWindow = nil
		} else {
			ps.LastThrow.ReleaseWindow.Source = nil
		}
		ps.Observation.FrameIndex = 900
		if events := NewThrow003(nil).Evaluate(testCtx(), players, 11); len(events) != 0 {
			t.Fatal("absent release source bypassed contradictory supplied confirmation")
		}
	}
}

func TestShotReviewRejectsUnboundReleaseContext(t *testing.T) {
	for name, mutate := range map[string]func(*model.PlayerState){
		"release from different recorder": func(ps *model.PlayerState) { ps.LastThrow.ReleaseWindow.Source.SourceID = "other" },
		"release from different epoch":    func(ps *model.PlayerState) { ps.LastThrow.ReleaseWindow.Source.SourceEpoch++ },
		"confirmation wrong frame":        func(ps *model.PlayerState) { ps.Observation.FrameIndex++ },
		"confirmation wrong time":         func(ps *model.PlayerState) { ps.Observation.Timestamp++ },
		"same frame different instant": func(ps *model.PlayerState) {
			ps.LastTimestamp++
			ps.Observation.Timestamp = ps.LastTimestamp
		},
		"missing confirmation source":      func(ps *model.PlayerState) { ps.Observation = nil },
		"release observation interval gap": func(ps *model.PlayerState) { ps.LastThrow.ReleaseWindow.StartFrame -= 2 },
		"release delayed many frames": func(ps *model.PlayerState) {
			observed := 20
			ps.LastFrameIdx, ps.LastTimestamp = observed, float64(observed)*.067
			ps.LastThrow.ObservedFrameIndex = &observed
			ps.Observation.FrameIndex, ps.Observation.Timestamp = observed, ps.LastTimestamp
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, records := shotRecorder()
			ps := mechanicsThrowState(5, 12, 10)
			mutate(ps)
			d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, ps.LastFrameIdx)
			if len(*records) != 1 || (*records)[0].Result != model.MechanicsInconclusive ||
				(*records)[0].Metrics["observed_release_count"] != 0 || len(d.histories["p1"].samples) != 0 {
				t.Fatalf("unbound release entered supporting statistics: %+v", records)
			}
		})
	}
	for _, delayed := range []bool{false, true} {
		d, records := shotRecorder()
		ps := mechanicsThrowState(5, 12, 10)
		if delayed {
			observed := 6
			ps.LastFrameIdx, ps.LastTimestamp = observed, float64(observed)*.067
			ps.LastThrow.ObservedFrameIndex = &observed
			ps.Observation.FrameIndex, ps.Observation.Timestamp = observed, ps.LastTimestamp
		}
		d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, ps.LastFrameIdx)
		if len(*records) != 1 || (*records)[0].Metrics["observed_release_count"] != 1 {
			t.Fatal("valid immediate/delayed confirmation excluded")
		}
	}
}

func TestFlightReviewRejectsContradictoryOwnership(t *testing.T) {
	for name, mutate := range map[string]func(*model.PlayerState){
		"player possession":             func(ps *model.PlayerState) { ps.HasDisc = true },
		"held copy":                     func(ps *model.PlayerState) { ps.CurrentDisc.IsHeld = true },
		"possessor id":                  func(ps *model.PlayerState) { ps.CurrentDisc.PossessorID = "p1" },
		"conflicting possession":        func(ps *model.PlayerState) { ps.CurrentDisc.PossessionConflict = true },
		"possible sampled head contact": func(ps *model.PlayerState) { ps.LegalContext.PossibleHeadContact = true },
	} {
		t.Run(name, func(t *testing.T) {
			d, records := flightRecorder()
			for frame := 1; frame <= 9; frame++ {
				ps := flightState(frame, float64(frame-1)*10)
				if frame == 8 {
					mutate(ps)
				}
				d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, frame)
			}
			if len(*records) != 1 || (*records)[0].Result != model.MechanicsInconclusive {
				t.Fatalf("uncertain contact retained anomaly: %+v", records)
			}
		})
	}
}
