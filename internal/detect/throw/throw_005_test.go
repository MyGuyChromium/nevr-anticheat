package throw

import (
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func mechanicsThrowState(frame int, speed, deviation float64) *model.PlayerState {
	ps := newState("p1", frame)
	ps.LastTimestamp = float64(frame) * .067
	ps.Observation = &model.ObservationContext{Source: "replay", Authority: "client_reported", TimeBasis: "capture", SessionID: "s1", FrameIndex: frame, Timestamp: ps.LastTimestamp}
	t := mkThrow(ps.PlayerID, frame, speed, 0)
	t.ReleasePosition = model.Vec3{1, 1, 1}
	t.ReleaseVelocity = model.Vec3{speed * math.Cos(deviation*math.Pi/180), 0, speed * math.Sin(deviation*math.Pi/180)}
	t.GoalPosition = model.Vec3{31, 1, 1}
	t.TargetPosition = &t.GoalPosition
	t.TargetDeviation = deviation
	t.ReleaseWindow = &model.ReleaseObservation{PlayerID: ps.PlayerID, FirstFreeFrame: frame, StartFrame: frame - 1, EndFrame: frame, StartTime: float64(frame-1) * .067, EndTime: ps.LastTimestamp, Source: ps.Observation.Clone(), HandCandidates: []string{"right"}}
	ps.LastThrow = &t
	return ps
}

func shotRecorder() (*Throw005, *[]model.MechanicsAssessment) {
	d := NewThrow005(map[string]any{"min_throws_for_pattern": 1, "max_mean_deviation": 999})
	d.SetWeight(1)
	d.SetAutoEnforce(true)
	records := []model.MechanicsAssessment{}
	d.SetMechanicsObserver(func(_, _ string, r model.MechanicsAssessment) { records = append(records, r.Clone()) })
	return d, &records
}

func TestThrow005PrecisionAndCorrelationNeverScore(t *testing.T) {
	d, records := shotRecorder()
	for frame := 1; frame <= 40; frame++ {
		ps := mechanicsThrowState(frame, 8+float64(frame%10), .01+float64(10-frame%10)*.001)
		if events := d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, frame); len(events) != 0 {
			t.Fatal("precision produced scored event")
		}
	}
	if len(*records) != 40 || d.DefaultEnforcementWeight() != 0 || d.AutoEnforce() {
		t.Fatal("policy or release count")
	}
	for _, r := range *records {
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
		if r.Result != model.MechanicsInconclusive || r.Kind != model.MechanicsShotTargeting {
			t.Fatalf("accuracy became accusation: %+v", r)
		}
	}
	last := (*records)[39]
	if last.Metrics["speed_direction_correlation"] > -.9 {
		t.Fatalf("descriptive negative correlation lost: %+v", last.Metrics)
	}
	if _, ok := (*records)[28].Metrics["speed_direction_correlation"]; ok {
		t.Fatal("correlation below30 supporting pairs")
	}
}

func TestThrow005IncludesMissesAndNonGoalReleases(t *testing.T) {
	d, records := shotRecorder()
	for frame := 1; frame <= 4; frame++ {
		ps := mechanicsThrowState(frame, 10, float64(frame)*40)
		ps.LastThrow.TargetPosition = nil
		if frame == 4 {
			ps.LastThrow.GoalPosition = model.Vec3{}
		}
		d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, frame)
	}
	r := (*records)[3]
	if r.Metrics["observed_release_count"] != 4 || r.Metrics["direction_sample_count"] != 3 || r.Metrics["max_reported_goal_direction_deg"] < 119 {
		t.Fatalf("misses dropped: %+v", r.Metrics)
	}
}

func TestThrow005ConstantUndefinedAndBoundedWindow(t *testing.T) {
	d, records := shotRecorder()
	for frame := 1; frame <= 70; frame++ {
		ps := mechanicsThrowState(frame, 12, float64(frame%9))
		d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, frame)
	}
	r := (*records)[69]
	if _, ok := r.Metrics["speed_direction_correlation"]; ok {
		t.Fatal("constant speed reported as zero correlation")
	}
	if len(d.histories["p1"].samples) != 50 || r.Metrics["observed_release_count"] != 50 {
		t.Fatal("unbounded history")
	}
}

func TestThrow005IndependentIdentityGapSourceAndUnknown(t *testing.T) {
	d, records := shotRecorder()
	ps := mechanicsThrowState(1, 12, 0)
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, 1)
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, 1)
	if len(*records) != 1 {
		t.Fatal("duplicate release counted")
	}
	ps = mechanicsThrowState(3, 12, 0)
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, 3)
	if (*records)[1].Metrics["observed_release_count"] != 1 {
		t.Fatal("history crossed gap")
	}
	ps = mechanicsThrowState(4, 12, 0)
	ps.Observation.SourceID = "other"
	ps.LastThrow.ReleaseWindow.Source = ps.Observation.Clone()
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, 4)
	if (*records)[2].Metrics["observed_release_count"] != 1 {
		t.Fatal("history crossed source change")
	}
	ps = mechanicsThrowState(5, 12, 0)
	ps.LastThrow.ReleaseWindow = nil
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": ps}, 5)
	if len(*records) != 3 {
		t.Fatal("legacy release without an interval invented an independent record")
	}
	d.SetMechanicsObserver(nil)
	d.Reset()
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": mechanicsThrowState(6, 12, 0)}, 6)
	if len(*records) != 3 {
		t.Fatal("detached callback leaked")
	}
}

func TestThrowMechanicsChecksShareCanonicalReleaseIdentity(t *testing.T) {
	shot, shotRecords := shotRecorder()
	flight, flightRecords := flightRecorder()
	first := flightState(1, 0)
	want := first.LastThrow.ReleaseWindow.EventID()
	shot.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": first}, 1)
	flight.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": first}, 1)
	for frame := 2; frame <= 9; frame++ {
		flight.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": flightState(frame, float64(frame-1)*10)}, frame)
	}
	if len(*shotRecords) != 1 || len(*flightRecords) != 1 || (*shotRecords)[0].EventID != want || (*flightRecords)[0].EventID != want {
		t.Fatalf("same release split across identities: shot=%+v flight=%+v", shotRecords, flightRecords)
	}
}
