package throw

import (
	"math"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestReleaseDirectionRejectsContradictoryRelativeVector(t *testing.T) {
	players := angleObservedFixture()
	te := players["p1"].LastThrow
	// Preserve the scalar magnitude: checking only |relative velocity| misses
	// a contradictory direction even though hand and body vectors are known.
	te.HandRelativeVelocity = te.HandRelativeVelocity.Scale(-1)
	if got := NewThrow003(nil).Evaluate(testCtx(), players, 11); len(got) != 0 {
		t.Fatal("contradictory body-relative vector survived the evidence boundary")
	}
	if got := NewThrow003(nil).Evaluate(testCtx(), angleObservedFixture(), 11); len(got) != 1 {
		t.Fatal("consistent candidate lost")
	}
}

func TestShotReviewRejectsOverflowingDirectionGeometry(t *testing.T) {
	for name, mutate := range map[string]func(*model.ThrowEvent){
		"velocity norm": func(te *model.ThrowEvent) {
			te.ReleaseVelocity = model.Vec3{0, 1e200, 0}
		},
		"goal displacement norm": func(te *model.ThrowEvent) {
			te.GoalPosition = model.Vec3{1, 1e200, 1}
		},
		"degenerate velocity": func(te *model.ThrowEvent) {
			te.ReleaseVelocity = model.Vec3{0, 1e-15, 0}
		},
		"degenerate goal displacement": func(te *model.ThrowEvent) {
			te.ReleasePosition = model.Vec3{}
			te.GoalPosition = model.Vec3{0, 1e-15, 0}
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, records := shotRecorder()
			ps := mechanicsThrowState(1, 12, 0)
			mutate(ps.LastThrow)
			d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, 1)
			if len(*records) != 1 || (*records)[0].Metrics["observed_release_count"] != 1 {
				t.Fatal("independent finite reported speed was lost")
			}
			if (*records)[0].Metrics["direction_sample_count"] != 0 {
				t.Fatalf("nonrepresentable geometry invented a direction: %+v", (*records)[0].Metrics)
			}
		})
	}
	d, records := shotRecorder()
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": mechanicsThrowState(1, 12, 45)}, 1)
	if len(*records) != 1 || (*records)[0].Metrics["direction_sample_count"] != 1 ||
		!near((*records)[0].Metrics["mean_reported_goal_direction_deg"], 45, 1e-10) {
		t.Fatal("ordinary measured direction was lost")
	}
	// Large is not itself unavailable: these two norms and their product are
	// finite, so their actual orthogonal direction remains reviewable.
	d, records = shotRecorder()
	ps := mechanicsThrowState(1, 12, 0)
	ps.LastThrow.ReleaseVelocity = model.Vec3{0, 1e154, 0}
	ps.LastThrow.GoalPosition = model.Vec3{1e154, 1, 1}
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, 1)
	if (*records)[0].Metrics["direction_sample_count"] != 1 || (*records)[0].Metrics["mean_reported_goal_direction_deg"] != 90 {
		t.Fatal("finite, nondegenerate angle computation was over-restricted")
	}
}

func TestFlightReviewRejectsNonrepresentableReleaseDistance(t *testing.T) {
	for name, mutate := range map[string]func(*model.PlayerState, int){
		"nonfinite release origin": func(ps *model.PlayerState, frame int) {
			if frame == 1 {
				ps.LastThrow.ReleasePosition[0] = math.Inf(1)
			}
		},
		"finite origin with overflowing displacement": func(ps *model.PlayerState, frame int) {
			if frame == 1 {
				ps.LastThrow.ReleasePosition[0] = -math.MaxFloat64
				ps.CurrentDisc.Position[0] = -math.MaxFloat64
			} else {
				ps.CurrentDisc.Position[0] = math.MaxFloat64
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, records := flightRecorder()
			for frame := 1; frame <= 9; frame++ {
				ps := flightState(frame, float64(frame-1)*10)
				mutate(ps, frame)
				d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, frame)
			}
			if len(*records) != 1 || (*records)[0].Result != model.MechanicsInconclusive ||
				(*records)[0].Metrics["tracked_free_samples"] != 0 {
				t.Fatalf("invalid distance qualified as an inspected bend: %+v", records)
			}
		})
	}
}

func TestReleaseAndFlightRigidCoordinateInvariance(t *testing.T) {
	// Exact axis permutation and sign changes form a rigid rotation. No
	// Galilean boost is asserted: native velocity inheritance remains unknown.
	rotate := func(v model.Vec3) model.Vec3 { return model.Vec3{-v[2], v[0], -v[1]} }
	translate := func(v model.Vec3) model.Vec3 { return rotate(v).Add(model.Vec3{100, -80, 70}) }
	basePlayers, movedPlayers := angleObservedFixture(), angleObservedFixture()
	te := movedPlayers["p1"].LastThrow
	te.HandVelocity, te.ReleaseVelocity = rotate(te.HandVelocity), rotate(te.ReleaseVelocity)
	te.PlayerVelocity, te.HandRelativeVelocity = rotate(te.PlayerVelocity), rotate(te.HandRelativeVelocity)
	te.ReleasePosition, te.HandPosition, te.PlayerPosition = translate(te.ReleasePosition), translate(te.HandPosition), translate(te.PlayerPosition)
	for i := range te.ReleaseWindow.PlayerMovement {
		m := &te.ReleaseWindow.PlayerMovement[i]
		m.Position = translate(m.Position)
		if m.ReportedVelocity != nil {
			*m.ReportedVelocity = rotate(*m.ReportedVelocity)
		}
	}
	base, moved := NewThrow003(nil).Evaluate(testCtx(), basePlayers, 11), NewThrow003(nil).Evaluate(testCtx(), movedPlayers, 11)
	if len(base) != 1 || len(moved) != 1 || !near(base[0].Confidence, moved[0].Confidence, 1e-12) || base[0].Severity != moved[0].Severity {
		t.Fatal("rigid-coordinate change altered a supported release comparison")
	}
	runFlight := func(transform bool) model.MechanicsAssessment {
		d, records := flightRecorder()
		for frame := 1; frame <= 9; frame++ {
			ps := flightState(frame, float64(frame-1)*10)
			if transform {
				ps.CurrentDisc.Position, ps.CurrentDisc.Velocity = translate(ps.CurrentDisc.Position), rotate(ps.CurrentDisc.Velocity)
				if ps.LastThrow != nil {
					ps.LastThrow.ReleasePosition, ps.LastThrow.ReleaseVelocity = translate(ps.LastThrow.ReleasePosition), rotate(ps.LastThrow.ReleaseVelocity)
				}
			}
			d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, frame)
		}
		if len(*records) != 1 || (*records)[0].Result != model.MechanicsAnomaly {
			t.Fatal("supported sampled bend missing")
		}
		return (*records)[0]
	}
	a, b := runFlight(false), runFlight(true)
	if a.Result != b.Result || a.Reason != b.Reason || !reflect.DeepEqual(a.Metrics, b.Metrics) {
		t.Fatal("rigid-coordinate change altered free-flight measurements")
	}
}

func TestFlightReviewRetryEquivalenceAndConflictingRetryInvalidation(t *testing.T) {
	run := func(retry bool, mutate func(*model.PlayerState)) model.MechanicsAssessment {
		d, records := flightRecorder()
		for frame := 1; frame <= 9; frame++ {
			ps := flightState(frame, float64(frame-1)*10)
			if got := d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, frame); len(got) != 0 {
				t.Fatal("review-only free-flight observation emitted a scored event")
			}
			if retry {
				if frame == 5 && mutate != nil {
					mutate(ps)
				}
				d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, frame)
			}
		}
		if len(*records) != 1 {
			t.Fatalf("expected one bounded terminal record, got %d", len(*records))
		}
		return (*records)[0]
	}
	base, retried := run(false, nil), run(true, nil)
	if base.Result != model.MechanicsAnomaly || !reflect.DeepEqual(base, retried) {
		t.Fatal("exact sample retries altered a nonvacuous bend observation")
	}
	for name, mutate := range map[string]func(*model.PlayerState){
		"velocity":        func(ps *model.PlayerState) { ps.CurrentDisc.Velocity[0]++ },
		"position":        func(ps *model.PlayerState) { ps.CurrentDisc.Position[0]++ },
		"contact counter": func(ps *model.PlayerState) { *ps.CurrentDisc.BounceCount++ },
		"source":          func(ps *model.PlayerState) { ps.Observation.SourceID = "other-recorder" },
		"timestamp": func(ps *model.PlayerState) {
			ps.LastTimestamp += .01
			ps.Observation.Timestamp = ps.LastTimestamp
		},
		"ownership": func(ps *model.PlayerState) { ps.HasDisc = true },
	} {
		t.Run(name, func(t *testing.T) {
			r := run(true, mutate)
			if r.Result != model.MechanicsInconclusive || r.Metrics["tracked_free_samples"] >= base.Metrics["tracked_free_samples"] {
				t.Fatalf("conflicting retry failed to interrupt comparison: reason=%s metrics=%v", r.Reason, r.Metrics)
			}
		})
	}
}

func TestShotReviewRetainedSupportDoesNotAliasInputsOrObserver(t *testing.T) {
	run := func(mutateInput, mutateObserver bool) model.MechanicsAssessment {
		d := NewThrow005(nil)
		var records []model.MechanicsAssessment
		d.SetMechanicsObserver(func(_, _ string, r model.MechanicsAssessment) {
			records = append(records, r.Clone())
			if mutateObserver {
				for i := range r.RawSamples {
					raw := &r.RawSamples[i]
					if raw.DiscPosition != nil {
						(*raw.DiscPosition)[0] = 999
					}
					if raw.SampledSpeedMPS != nil {
						*raw.SampledSpeedMPS = 999
					}
					if raw.InferredReferenceGoal != nil {
						(*raw.InferredReferenceGoal)[0] = 999
					}
				}
			}
		})
		for frame := 1; frame <= 2; frame++ {
			ps := mechanicsThrowState(frame, 12, float64(frame)*10)
			d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, frame)
			if mutateInput {
				ps.LastThrow.ReleasePosition[0] = 999
				ps.LastThrow.ReleaseVelocity[0] = 999
				ps.LastThrow.GoalPosition[0] = 999
				ps.LastThrow.ReleaseWindow.Source.SourceID = "mutated"
				ps.Observation.SourceID = "mutated"
			}
		}
		if len(records) != 2 || records[1].Metrics["observed_release_count"] != 2 || records[1].Metrics["direction_sample_count"] != 2 {
			t.Fatal("test did not retain two supported releases")
		}
		return records[1]
	}
	base := run(false, false)
	for _, mode := range [][2]bool{{true, false}, {false, true}, {true, true}} {
		if got := run(mode[0], mode[1]); !reflect.DeepEqual(base, got) {
			t.Fatalf("retained shot statistics/evidence alias input=%t observer=%t", mode[0], mode[1])
		}
	}
}
