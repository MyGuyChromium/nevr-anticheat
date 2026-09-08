package throw

import (
	"math"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func flightRecorder() (*Throw006, *[]model.MechanicsAssessment) {
	d := NewThrow006(map[string]any{"max_cumulative_change": 50.0, "post_release_frames": 8, "min_distance_from_thrower": 0.0})
	d.SetWeight(1)
	d.SetAutoEnforce(true)
	records := []model.MechanicsAssessment{}
	d.SetMechanicsObserver(func(_, _ string, r model.MechanicsAssessment) { records = append(records, r.Clone()) })
	return d, &records
}

func flightState(frame int, angle float64) *model.PlayerState {
	ps := mechanicsThrowState(frame, 12, 0)
	if frame != 1 {
		ps.LastThrow = nil
	}
	bounce := 0
	a := angle * math.Pi / 180
	ps.CurrentDisc = &model.DiscState{Attachment: &model.DiscAttachment{State: "free"}, BounceCount: &bounce, SampledPlayerCount: 1,
		Position: model.Vec3{1 + float64(frame-1)*.8, 1, 1}, Velocity: model.Vec3{12 * math.Cos(a), 0, 12 * math.Sin(a)}, Speed: 12}
	ps.DiscAttachment = ps.CurrentDisc.Attachment.Clone()
	return ps
}

func TestThrow006BoundedSampledBendIsUnscoredDiagnostic(t *testing.T) {
	d, records := flightRecorder()
	for frame := 1; frame <= 9; frame++ {
		if ev := d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": flightState(frame, float64(frame-1)*10)}, frame); len(ev) != 0 {
			t.Fatal("scored flight event")
		}
	}
	if len(*records) != 1 || d.AutoEnforce() || d.DefaultEnforcementWeight() != 0 {
		t.Fatalf("policy/records %+v", records)
	}
	r := (*records)[0]
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if r.Result != model.MechanicsAnomaly || r.Reason != "trajectory_sampled_bend_anomaly" || r.FrameIndex != 1 || r.Metrics["above_filter_sample_count"] != 8 {
		t.Fatalf("diagnostic %+v", r)
	}
	if len(r.RawSamples) > model.MaxMechanicsRawSamples || len(d.activeThrows) != 0 {
		t.Fatal("unbounded/unclosed flight")
	}
	for _, key := range []string{"goal_alignment", "alignment_improvement", "correction_confidence"} {
		if _, ok := r.Metrics[key]; ok {
			t.Fatal("unverified goal model survived")
		}
	}
}

func TestThrow006InterruptedFlightAlwaysInconclusive(t *testing.T) {
	for name, change := range map[string]func(*model.PlayerState){
		"unknown": func(p *model.PlayerState) { p.CurrentDisc.Attachment = nil; p.DiscAttachment = nil },
		"held": func(p *model.PlayerState) {
			p.CurrentDisc.Attachment = &model.DiscAttachment{State: "held", HolderID: "p2", HandCandidates: []string{"left"}}
			p.DiscAttachment = p.CurrentDisc.Attachment.Clone()
		},
		"bounce":         func(p *model.PlayerState) { *p.CurrentDisc.BounceCount = 1 },
		"missing bounce": func(p *model.PlayerState) { p.CurrentDisc.BounceCount = nil },
		"collision":      func(p *model.PlayerState) { p.CurrentDisc.Velocity = model.Vec3{-12, 0, 0} },
		"source":         func(p *model.PlayerState) { p.Observation.SourceID = "other" },
		"time gap":       func(p *model.PlayerState) { p.LastTimestamp += 1; p.Observation.Timestamp = p.LastTimestamp },
		"missing roster": func(p *model.PlayerState) { p.CurrentDisc.SampledPlayerCount = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			d, records := flightRecorder()
			for frame := 1; frame <= 7; frame++ {
				d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": flightState(frame, float64(frame-1)*10)}, frame)
			}
			p := flightState(8, 70)
			change(p)
			d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": p}, 8)
			if len(*records) != 1 || (*records)[0].Result != model.MechanicsInconclusive || len(d.activeThrows) != 0 {
				t.Fatalf("interruption became positive %+v", records)
			}
			d.FlushTracks(testCtx(), 9)
			if len(*records) != 1 {
				t.Fatal("flush duplicated cancelled flight")
			}
		})
	}
}

func TestThrow006InitialUnknownGapAndStaleCopyAbstain(t *testing.T) {
	d, records := flightRecorder()
	p := flightState(1, 0)
	p.LastThrow.ReleaseWindow = nil
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": p}, 1)
	if len(*records) != 0 || len(d.activeThrows) != 0 {
		t.Fatal("legacy event without an interval invented a release identity")
	}
	d, records = flightRecorder()
	p = flightState(1, 0)
	stale := flightState(1, 0)
	stale.PlayerID = "old"
	stale.FrameCount = 2
	stale.LastFrameIdx = -100
	stale.CurrentDisc.IsHeld = true
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": p, "old": stale}, 1)
	if len(d.activeThrows) != 1 {
		t.Fatal("stale copy interrupted known free release")
	}
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": flightState(3, 20)}, 3)
	if len(*records) != 1 || (*records)[0].Reason != "trajectory_sample_gap" {
		t.Fatal("flight bridged frame gap")
	}
}

func TestThrow006EOFRegrabAndDedupDoNotScoreOrLeak(t *testing.T) {
	d, records := flightRecorder()
	for frame := 1; frame <= 7; frame++ {
		p := flightState(frame, float64(frame-1)*10)
		d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": p}, frame)
		d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": p}, frame)
	}
	if ev := d.FlushTracks(testCtx(), 7); len(ev) != 0 {
		t.Fatal("EOF emitted score")
	}
	d.FlushTracks(testCtx(), 7)
	if len(*records) != 1 || (*records)[0].Reason != "trajectory_end_of_stream" || (*records)[0].Result != model.MechanicsInconclusive {
		t.Fatalf("EOF %+v", records)
	}
	d, records = flightRecorder()
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": flightState(1, 0)}, 1)
	p := flightState(2, 10)
	p.LastThrow = mechanicsThrowState(2, 12, 0).LastThrow
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": p}, 2)
	if len(*records) != 1 || (*records)[0].Reason != "trajectory_release_replaced" || d.activeThrows["p1"].releaseFrame != 2 {
		t.Fatal("releases merged")
	}
	d.SetMechanicsObserver(nil)
	d.Reset()
	d.FlushTracks(testCtx(), 3)
	if len(*records) != 1 {
		t.Fatal("reset/detach emitted callback")
	}
}

func TestThrow006ChunkAndRawOwnershipEquivalent(t *testing.T) {
	run := func(mutate bool) []model.MechanicsAssessment {
		d, records := flightRecorder()
		for frame := 1; frame <= 9; frame++ {
			p := flightState(frame, float64(frame-1)*10)
			d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": p}, frame)
			if mutate {
				p.CurrentDisc.Position[0] = 999
				p.Observation.Source = "mutated"
			}
		}
		return *records
	}
	if !reflect.DeepEqual(run(false), run(true)) {
		t.Fatal("raw samples or source alias input")
	}
}

func TestThrow006ProvisionalControlsBoundHistories(t *testing.T) {
	d := NewThrow006(map[string]any{"post_release_frames": 999999, "min_trajectory_change": math.NaN(), "max_cumulative_change": math.Inf(1), "min_distance_from_thrower": -1.0})
	if d.postReleaseFrames != 15 || d.minTrajectoryChange != 8 || d.maxCumulativeChange != 130 || d.minDistFromThrower != 2 {
		t.Fatalf("invalid controls escaped safe fallbacks: %+v", d)
	}
	_ = d.Configure(map[string]any{"post_release_frames": 20, "min_trajectory_change": 9.0, "min_distance_from_thrower": 3.0})
	if d.postReleaseFrames != 20 || d.minTrajectoryChange != 9 || d.minDistFromThrower != 3 {
		t.Fatal("valid controls ignored")
	}
}
