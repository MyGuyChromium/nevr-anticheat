package state

import (
	"fmt"
	"math"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// All rates sample this SAME analytic trajectory: 0.4 s straight, then a
// constant-speed circular turn until turnEnd, then straight. Neither angular
// velocity nor spatial path depends on sample count. The first held sample
// follows the common last-free timestamp t=1. This is not exploit ground truth.
func catchAnalyticFlight(rate int, jitter bool, omegaDeg, turnEnd float64) []map[string]*model.PlayerState {
	pose := func(t float64) (model.Vec3, model.Vec3) {
		const speed, onset = 10.0, .4
		p, v := model.Vec3{3 + speed*t, 2, 1}, model.Vec3{speed, 0, 0}
		if t <= onset || omegaDeg == 0 {
			return p, v
		}
		omega := omegaDeg * math.Pi / 180
		duration := math.Min(t, turnEnd) - onset
		angle := omega * duration
		p = model.Vec3{3 + speed*onset + speed*math.Sin(angle)/omega, 2 + speed*(1-math.Cos(angle))/omega, 1}
		v = model.Vec3{speed * math.Cos(angle), speed * math.Sin(angle), 0}
		if t > turnEnd {
			p = p.Add(v.Scale(t - turnEnd))
		}
		return p, v
	}
	last, velocity := pose(1)
	hand := last.Add(velocity.Normalized().Scale(.9))
	ticks := make([]map[string]*model.PlayerState, 0, rate+3)
	for i := 0; i < rate+3; i++ {
		tm := float64(i) / float64(rate)
		if jitter && i > 0 && i < rate {
			tm += .1 / float64(rate) * math.Sin(float64(i)*.73)
		}
		p, v := pose(tm)
		holder := ""
		if i > rate {
			p, v, holder = hand, model.Vec3{}, "receiver"
		}
		players := map[string]*model.PlayerState{}
		for _, id := range []string{"receiver", "other"} {
			left := hand
			if id == "other" {
				left = model.Vec3{3, 2, 20}
			}
			head, bounce := left.Add(model.Vec3{0, 1.2, 0}), 0
			players[id] = &model.PlayerState{PlayerID: id, LastFrameIdx: i + 1, FrameCount: i + 1, LastTimestamp: 10 + tm,
				Position: left.Add(model.Vec3{0, 1, 0}), HeadPosition: &head,
				LeftHand: left, RightHand: left.Add(model.Vec3{0, 0, .5}), HasDisc: holder == id,
				CurrentDisc: &model.DiscState{Position: p, Velocity: v, Speed: v.Magnitude(), PossessorID: holder, IsHeld: holder != "",
					PossessionKnown: true, SampledPlayerCount: 2, BounceCount: &bounce}}
		}
		ticks = append(ticks, players)
	}
	return ticks
}

func TestState008SameContinuousTrajectoryAcrossRatesAndJitter(t *testing.T) {
	var reference map[string]float64
	for _, rate := range []int{10, 15, 20, 30, 60, 120} {
		for _, jitter := range []bool{false, true} {
			t.Run(fmt.Sprintf("hz%d_jitter%t", rate, jitter), func(t *testing.T) {
				d := NewState008(nil)
				var reasons []string
				d.SetDecisionObserver(func(_, _ string, _ int, r string) { reasons = append(reasons, r) })
				ticks := catchAnalyticFlight(rate, jitter, 100, 1)
				events := catchRun(d, ticks)
				if len(events) != 1 {
					t.Fatalf("same analytic flight produced %d events; reasons=%v", len(events), reasons)
				}
				if !events[0].IsShadow || events[0].EnforcementWeight != 0 || events[0].AutoEnforce || events[0].DetectorVersion != "0.2.0" {
					t.Fatalf("unsafe event metadata: %+v", events[0])
				}
				e := events[0].Evidence.(model.StateEvidence)
				m := e.Metrics
				if m["baseline_samples"] < 4 || m["baseline_duration_s"]+1e-9 < .2 || m["correction_samples"] < 2 || m["correction_duration_s"]+1e-9 < .12 {
					t.Fatalf("physical durations or independent evidence floors bypassed: %+v", m)
				}
				if math.Abs(m["observed_max_turn_rate_deg_s"]-100) > 1e-6 || math.Abs(m["observed_max_secant_turn_rate_deg_s"]-100) > 1e-6 {
					t.Fatalf("same physical turning changed rate: %+v", m)
				}
				if len(e.CatchTrajectory) > catchHistoryLimit || e.CatchTrajectory[len(e.CatchTrajectory)-1].Timestamp != 11 {
					t.Fatal("history/evidence bound or held exclusion failed")
				}
				if reference == nil {
					reference = m
				}
				for _, key := range []string{"max_lateral_deviation_m", "actual_hand_miss_m", "expected_hand_miss_m", "miss_improvement_m", "baseline_speed_mps"} {
					if math.Abs(m[key]-reference[key]) > 1e-6 {
						t.Fatalf("resampling changed geometric %s: got%v reference%v", key, m[key], reference[key])
					}
				}
			})
		}
	}
}

func TestState008TemporalGuardsAcrossSamplingRates(t *testing.T) {
	for _, rate := range []int{10, 15, 20, 30, 60, 120} {
		for _, tc := range []struct {
			name       string
			omega, end float64
		}{
			{"straight", 0, 1}, {"brief_correction", 100, .48}, {"single_large_impulse", 1000, .41}, {"below_turn_rate", 20, 1},
		} {
			t.Run(fmt.Sprintf("%s_hz%d", tc.name, rate), func(t *testing.T) {
				for _, jitter := range []bool{false, true} {
					if events := catchRun(NewState008(nil), catchAnalyticFlight(rate, jitter, tc.omega, tc.end)); len(events) != 0 {
						t.Fatalf("excluded analytic flight emitted at jitter%t: %+v", jitter, events)
					}
				}
			})
		}
	}
}

func TestState008TimeWeightedReferenceAndEveryPositionResidual(t *testing.T) {
	samples := []catchSample{
		{timestamp: 1, velocity: model.Vec3{9.9, 0, 0}, position: model.Vec3{1, 1, 1}},
		{timestamp: 1.01, velocity: model.Vec3{10.1, 0, 0}, position: model.Vec3{1.1, 1, 1}},
		{timestamp: 1.21, velocity: model.Vec3{10.1, 0, 0}, position: model.Vec3{3.12, 1, 1}},
	}
	v, residual, reason := catchFitReference(samples, .2)
	if reason != "" || math.Abs(v[0]-2.12/.21) > 1e-10 || residual > .002 {
		t.Fatalf("reference was not duration weighted: v%v residual%v reason%s", v, residual, reason)
	}
	samples[1].position[1] += .3
	if _, _, reason := catchFitReference(samples, .2); reason != "catch_baseline_unstable" {
		t.Fatalf("intermediate excursion hidden by agreeing endpoints: %s", reason)
	}
	samples[1].position[1] -= .3
	samples[1].timestamp, samples[2].timestamp = 1.001, 1.002
	if _, _, reason := catchFitReference(samples, .2); reason != "catch_baseline_low_information" {
		t.Fatalf("negligible information accepted: %s", reason)
	}
}

func TestState008SecantUsesMidpointSeparation(t *testing.T) {
	// The secants' endpoints are .08 s apart, but their midpoints are
	// (.02+.08)/2=.05 s apart. Five degrees therefore means 100 deg/s.
	a, b := model.Vec3{1, 0, 0}, model.Vec3{math.Cos(5 * math.Pi / 180), math.Sin(5 * math.Pi / 180), 0}
	if rate := catchSecantTurnRate(a, b, .02, .08); math.Abs(rate-100) > 1e-8 {
		t.Fatalf("midpoint angular rate=%v", rate)
	}
}

func TestState008IndependentDurationAndSampleFloors(t *testing.T) {
	// The old 20 Hz fixture has four reference samples in only .15 s.
	// Extra apparent curvature cannot replace the missing physical duration.
	if events := catchRun(NewState008(nil), catchTestFlight(.05, true)); len(events) != 0 {
		t.Fatal("sample count bypassed baseline duration")
	}
	for _, params := range []map[string]any{
		{"baseline_duration_s": .8}, {"baseline_samples": 12}, {"min_correction_duration_s": .8}, {"min_correction_samples": 8},
	} {
		if events := catchRun(NewState008(params), catchAnalyticFlight(10, false, 100, 1)); len(events) != 0 {
			t.Fatalf("independent requirement bypassed: %v", params)
		}
	}
}

func TestState008FinalizedCatchDiagnosticsNeverManufactureEvents(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func([]map[string]*model.PlayerState) []map[string]*model.PlayerState
		outcome    model.CatchReviewOutcome
		confirmed  bool
		wantEvents int
	}{
		{"observation", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState { return s }, model.CatchReviewObservation, true, 1},
		{"straight exclusion", func(_ []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			return catchAnalyticFlight(30, false, 0, 1)
		}, model.CatchReviewExcluded, true, 0},
		{"end before confirmation", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState { return s[:len(s)-1] }, model.CatchReviewUnconfirmed, false, 0},
		{"bad confirmation tracking", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			s[len(s)-1]["other"].HeadPosition = nil
			return s
		}, model.CatchReviewInsufficientData, true, 0},
		{"confirmation gap", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			for _, p := range s[len(s)-1] {
				p.LastFrameIdx++
			}
			return s
		}, model.CatchReviewInsufficientData, false, 0},
		{"different holder", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			for _, p := range s[len(s)-1] {
				p.HasDisc = p.PlayerID == "other"
				p.CurrentDisc.PossessorID = "other"
			}
			return s
		}, model.CatchReviewUnconfirmed, false, 0},
		{"not enough reference", func(s []map[string]*model.PlayerState) []map[string]*model.PlayerState {
			s = s[:4]
			for i := 2; i < 4; i++ {
				for _, p := range s[i] {
					p.HasDisc = p.PlayerID == "receiver"
					p.CurrentDisc.IsHeld, p.CurrentDisc.PossessorID = true, "receiver"
				}
			}
			return s
		}, model.CatchReviewInsufficientData, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewState008(nil)
			var records []model.CatchReviewRecord
			d.SetCatchObserver(func(id, player string, r model.CatchReviewRecord) {
				if id != "STATE_008" || player != "receiver" {
					t.Fatalf("wrong diagnostic attribution: %s/%s", id, player)
				}
				if err := r.Validate(); err != nil {
					t.Fatalf("invalid diagnostic: %v %+v", err, r)
				}
				records = append(records, r.Clone())
				// An observer cannot corrupt the retained event's metrics.
				r.Metrics["max_lateral_deviation_m"] = 999
			})
			ticks := tc.mutate(catchAnalyticFlight(30, false, 100, 1))
			events := catchRun(d, ticks)
			if flushed := d.FlushTracks(ctx(), 100); len(flushed) != 0 {
				t.Fatal("diagnostic flush emitted an event")
			}
			d.FlushTracks(ctx(), 100)
			if len(events) != tc.wantEvents || len(records) != 1 {
				t.Fatalf("events%d records%d", len(events), len(records))
			}
			r := records[0]
			if r.Outcome != tc.outcome || r.Confirmed != tc.confirmed || r.FrameIndex != r.LastFreeFrame+1 {
				t.Fatalf("wrong final diagnostic: %+v", r)
			}
			if len(events) == 1 && events[0].Evidence.(model.StateEvidence).Metrics["max_lateral_deviation_m"] == 999 {
				t.Fatal("observer mutated event evidence")
			}
		})
	}
	// Free-only data is not a catch, including at match end.
	d := NewState008(nil)
	called := false
	d.SetCatchObserver(func(_, _ string, _ model.CatchReviewRecord) { called = true })
	catchRun(d, catchAnalyticFlight(30, false, 100, 1)[:31])
	d.FlushTracks(ctx(), 31)
	if called {
		t.Fatal("free-only trajectory became a catch diagnostic")
	}
}

func TestState008PendingDiagnosticUsesCurrentObserverOnceAcrossChunks(t *testing.T) {
	d := NewState008(nil)
	first, second := 0, 0
	d.SetCatchObserver(func(_, _ string, _ model.CatchReviewRecord) { first++ })
	ticks := catchAnalyticFlight(30, false, 100, 1)
	catchRun(d, ticks[:len(ticks)-1])
	d.SetCatchObserver(nil)
	d.SetCatchObserver(func(_, _ string, r model.CatchReviewRecord) {
		second++
		if !r.Confirmed || r.FrameIndex != 32 {
			t.Fatalf("wrong first-held identity: %+v", r)
		}
	})
	catchRun(d, ticks[len(ticks)-1:])
	d.FlushTracks(ctx(), 33)
	if first != 0 || second != 1 {
		t.Fatalf("chunk callbacks %d/%d", first, second)
	}
}
