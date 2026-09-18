package state

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestState008DegradedRetriesAreNotNewPossessionSamples(t *testing.T) {
	for name, degrade := range map[string]func(*model.PlayerState){
		"missing head":         func(p *model.PlayerState) { p.HeadPosition = nil },
		"missing bounce":       func(p *model.PlayerState) { p.CurrentDisc.BounceCount = nil },
		"nonfinite velocity":   func(p *model.PlayerState) { p.CurrentDisc.Velocity[0] = math.NaN() },
		"overflowed magnitude": func(p *model.PlayerState) { p.CurrentDisc.Velocity[0] = math.MaxFloat64 },
	} {
		t.Run(name, func(t *testing.T) {
			d := NewState008(nil)
			var records []model.CatchReviewRecord
			d.SetCatchObserver(func(_, _ string, r model.CatchReviewRecord) { records = append(records, r.Clone()) })
			ticks := catchTestFlight(1.0/15, true)
			for _, tick := range ticks {
				for _, p := range tick {
					degrade(p)
				}
			}
			if events := catchRun(d, ticks[:11]); len(events) != 0 || d.pendingReview == nil {
				t.Fatal("degraded acquisition must be visible but cannot be a trajectory candidate")
			}
			for retry := 0; retry < 3; retry++ {
				if events := catchRun(d, ticks[10:11]); len(events) != 0 || len(records) != 0 || d.pendingReview == nil {
					t.Fatal("an identical degraded retry terminated or confirmed pending possession")
				}
			}
			if events := catchRun(d, ticks[11:]); len(events) != 0 {
				t.Fatal("degraded motion created a trajectory event")
			}
			d.FlushTracks(ctx(), 12)
			if len(records) != 1 || !records[0].Confirmed || records[0].Outcome != model.CatchReviewInsufficientData || records[0].Reason == "catch_sample_gap" {
				t.Fatalf("fresh identity confirmation lost the motion limitation: %+v", records)
			}
			if err := records[0].Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestState008ChangedDegradedRetryInterruptsPossession(t *testing.T) {
	for name, mutate := range map[string]func(*model.PlayerState){
		"disc":          func(p *model.PlayerState) { p.CurrentDisc.Position[0] += .01 },
		"body":          func(p *model.PlayerState) { p.Position[0] += .01 },
		"left hand":     func(p *model.PlayerState) { p.LeftHand[0] += .01 },
		"right hand":    func(p *model.PlayerState) { p.RightHand[0] += .01 },
		"speed":         func(p *model.PlayerState) { p.CurrentDisc.Speed += .01 },
		"head restored": func(p *model.PlayerState) { h := model.Vec3{1, 2, 3}; p.HeadPosition = &h },
		"different nan": func(p *model.PlayerState) { p.CurrentDisc.Velocity[0] = math.Float64frombits(0x7ff800000000002a) },
		"quality":       func(p *model.PlayerState) { p.IsHighPing = true },
		"bounce":        func(p *model.PlayerState) { *p.CurrentDisc.BounceCount++ },
		"source":        func(p *model.PlayerState) { p.Observation.SourceID = "changed" },
		"attachment": func(p *model.PlayerState) {
			p.DiscAttachment.HandCandidates = []string{"left"}
			p.CurrentDisc.Attachment = p.DiscAttachment.Clone()
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := NewState008(nil)
			var records []model.CatchReviewRecord
			d.SetCatchObserver(func(_, _ string, r model.CatchReviewRecord) { records = append(records, r.Clone()) })
			ticks := catchTestFlight(1.0/15, true)
			for _, tick := range ticks {
				for _, p := range tick {
					p.HeadPosition = nil
					p.CurrentDisc.Velocity[0] = math.NaN()
				}
			}
			catchRun(d, ticks[:11])
			if d.pendingReview == nil {
				t.Fatal("positive diagnostic control missing")
			}
			for _, p := range ticks[10] {
				mutate(p)
			}
			if events := catchRun(d, ticks[10:]); len(events) != 0 {
				t.Fatal("changed retry became a trajectory event")
			}
			d.FlushTracks(ctx(), 12)
			if len(records) != 1 || records[0].Confirmed || records[0].Outcome != model.CatchReviewInsufficientData {
				t.Fatalf("changed degraded retry did not preserve interruption: %+v", records)
			}
		})
	}
}

func TestState008RetryAfterInterruptionCannotReuseOldIdentity(t *testing.T) {
	d := NewState008(nil)
	ticks := catchTestFlight(1.0/15, true)
	var records []model.CatchReviewRecord
	d.SetCatchObserver(func(_, _ string, r model.CatchReviewRecord) { records = append(records, r.Clone()) })
	catchRun(d, ticks[:11])
	if d.pendingReview == nil {
		t.Fatal("positive control missing pending catch")
	}
	if events := d.Evaluate(ctx(), nil, 12); len(events) != 0 {
		t.Fatal("empty roster created event")
	}
	if events := catchRun(d, ticks[10:]); len(events) != 0 {
		t.Fatal("retry recovered old trajectory after a known interruption")
	}
	d.FlushTracks(ctx(), 12)
	if len(records) != 1 || records[0].Confirmed {
		t.Fatalf("interruption lost: %+v", records)
	}
}

func TestState008RigidCoordinateTransformPreservesEvidence(t *testing.T) {
	transforms := []struct {
		name   string
		rotate func(model.Vec3) model.Vec3
		offset model.Vec3
	}{
		{"translated", func(v model.Vec3) model.Vec3 { return v }, model.Vec3{103, -207, 41}},
		{"quarter turn", func(v model.Vec3) model.Vec3 { return model.Vec3{-v[1], v[0], v[2]} }, model.Vec3{20, 20, 20}},
		{"oblique", func(v model.Vec3) model.Vec3 {
			c, s := math.Cos(.731), math.Sin(.731)
			return model.Vec3{c*v[0] + s*v[2], v[1], -s*v[0] + c*v[2]}
		}, model.Vec3{-90, 71, 29}},
	}
	for _, curved := range []bool{false, true} {
		base := catchRun(NewState008(nil), catchTestFlight(1.0/15, curved))
		if curved && len(base) != 1 {
			t.Fatal("positive control missing")
		}
		if !curved && len(base) != 0 {
			t.Fatal("straight control unexpectedly emitted")
		}
		for _, transform := range transforms {
			t.Run(transform.name, func(t *testing.T) {
				ticks := catchTestFlight(1.0/15, curved)
				point := func(v model.Vec3) model.Vec3 { return transform.rotate(v).Add(transform.offset) }
				for _, tick := range ticks {
					for _, p := range tick {
						p.Position, p.LeftHand, p.RightHand = point(p.Position), point(p.LeftHand), point(p.RightHand)
						*p.HeadPosition = point(*p.HeadPosition)
						p.CurrentDisc.Position = point(p.CurrentDisc.Position)
						p.CurrentDisc.Velocity = transform.rotate(p.CurrentDisc.Velocity)
						p.CurrentDisc.Speed = p.CurrentDisc.Velocity.Magnitude()
					}
				}
				got := catchRun(NewState008(nil), ticks)
				if len(got) != len(base) {
					t.Fatalf("coordinate frame changed outcome: %d vs %d", len(got), len(base))
				}
				if len(got) == 0 {
					return
				}
				wantMetrics, gotMetrics := base[0].Evidence.(model.StateEvidence).Metrics, got[0].Evidence.(model.StateEvidence).Metrics
				if len(wantMetrics) != len(gotMetrics) {
					t.Fatal("transformation changed evidence fields")
				}
				for key, want := range wantMetrics {
					if math.Abs(want-gotMetrics[key]) > 1e-8 {
						t.Fatalf("%s changed with coordinates: %.12g vs %.12g", key, want, gotMetrics[key])
					}
				}
			})
		}
	}
}

func TestState001RigidCoordinatesPreserveDescriptiveMetrics(t *testing.T) {
	run := func(transform bool) model.MechanicsAssessment {
		d, records := grabReviewRecorder()
		for i, state := range []string{"free", "held", "held"} {
			p := grabReviewFrame(i, state)
			if transform {
				point := func(v model.Vec3) model.Vec3 { return model.Vec3{-v[1] + 103, v[0] - 207, v[2] + 41} }
				p.Position, p.LeftHand, p.RightHand = point(p.Position), point(p.LeftHand), point(p.RightHand)
				p.CurrentDisc.Position = point(p.CurrentDisc.Position)
			}
			assertGrabUnscored(t, d, d.Evaluate(ctx(), players(p), i))
		}
		if len(*records) != 1 {
			t.Fatal("positive review control missing")
		}
		assertGrabRecord(t, (*records)[0], "grab_geometry_unverified", true)
		return (*records)[0]
	}
	a, b := run(false), run(true)
	if a.EventID != b.EventID || len(a.Metrics) != len(b.Metrics) {
		t.Fatal("rigid transform changed source identity or evidence fields")
	}
	for key, want := range a.Metrics {
		if math.Abs(want-b.Metrics[key]) > 1e-9 {
			t.Fatalf("%s changed under rigid transform: %.12g vs %.12g", key, want, b.Metrics[key])
		}
	}
}

func TestCatchEndOfStreamCannotBridgeIntoLaterInput(t *testing.T) {
	t.Run("trajectory", func(t *testing.T) {
		d := NewState008(nil)
		ticks := catchTestFlight(1.0/15, true)
		if len(catchRun(NewState008(nil), ticks)) != 1 {
			t.Fatal("positive control missing")
		}
		catchRun(d, ticks[:10])
		d.FlushTracks(ctx(), 10)
		if events := catchRun(d, ticks[10:]); len(events) != 0 {
			t.Fatal("finalized free flight bridged into later catch")
		}
		d.Reset()
		if len(catchRun(d, ticks)) != 1 {
			t.Fatal("new complete sequence failed after finalization")
		}
	})
	t.Run("grab", func(t *testing.T) {
		d, records := grabReviewRecorder()
		d.Evaluate(ctx(), players(grabReviewFrame(0, "free")), 0)
		d.FlushTracks(ctx(), 0)
		for i := 1; i <= 2; i++ {
			assertGrabUnscored(t, d, d.Evaluate(ctx(), players(grabReviewFrame(i, "held")), i))
		}
		if len(*records) != 0 {
			t.Fatal("finalized free sample bridged into later grab")
		}
		for i, state := range []string{"free", "held", "held"} {
			assertGrabUnscored(t, d, d.Evaluate(ctx(), players(grabReviewFrame(i+3, state)), i+3))
		}
		if len(*records) != 1 {
			t.Fatal("new complete acquisition did not recover")
		}
		assertGrabRecord(t, (*records)[0], "grab_geometry_unverified", true)
	})
}

func TestState001NegativeFrameWithoutProvenanceCannotSeedReview(t *testing.T) {
	d, records := grabReviewRecorder()
	for i, state := range []string{"free", "held", "held"} {
		p := grabReviewFrame(i-1, state)
		p.LastTimestamp = float64(i) * .067
		p.Observation = nil
		assertGrabUnscored(t, d, d.Evaluate(ctx(), players(p), i-1))
	}
	if len(*records) != 0 {
		t.Fatal("negative-frame history produced an invalid acquisition diagnostic")
	}
	for i, state := range []string{"free", "held", "held"} {
		assertGrabUnscored(t, d, d.Evaluate(ctx(), players(grabReviewFrame(i+3, state)), i+3))
	}
	if len(*records) != 1 {
		t.Fatal("valid acquisition did not recover")
	}
	assertGrabRecord(t, (*records)[0], "grab_geometry_unverified", true)
}

func TestState008InvalidGeometryCannotBecomeFiniteEvidence(t *testing.T) {
	for valueName, value := range map[string]float64{"nan": math.NaN(), "positive inf": math.Inf(1), "negative inf": math.Inf(-1), "finite overflow": math.MaxFloat64} {
		for fieldName, mutate := range map[string]func(*model.PlayerState, float64){
			"disc position": func(p *model.PlayerState, v float64) { p.CurrentDisc.Position[0] = v },
			"disc velocity": func(p *model.PlayerState, v float64) { p.CurrentDisc.Velocity[0] = v },
			"disc speed":    func(p *model.PlayerState, v float64) { p.CurrentDisc.Speed = v },
			"body":          func(p *model.PlayerState, v float64) { p.Position[0] = v },
			"head":          func(p *model.PlayerState, v float64) { p.HeadPosition[0] = v },
			"hand":          func(p *model.PlayerState, v float64) { p.LeftHand[0] = v },
		} {
			for _, index := range []int{5, 10, 11} {
				t.Run(fmt.Sprintf("%s/%s/at_%d", valueName, fieldName, index), func(t *testing.T) {
					d := NewState008(nil)
					var records []model.CatchReviewRecord
					d.SetCatchObserver(func(_, _ string, r model.CatchReviewRecord) { records = append(records, r.Clone()) })
					ticks := catchTestFlight(1.0/15, true)
					for _, p := range ticks[index] {
						mutate(p, value)
					}
					if events := catchRun(d, ticks); len(events) != 0 {
						t.Fatal("invalid geometry produced trajectory candidate")
					}
					d.FlushTracks(ctx(), 12)
					if len(records) != 1 {
						t.Fatalf("observed acquisition lost: %+v", records)
					}
					if err := records[0].Validate(); err != nil {
						t.Fatal(err)
					}
					if _, err := json.Marshal(records); err != nil {
						t.Fatal(err)
					}
					if events := catchRun(d, catchTestFlight(1.0/15, true)); len(events) != 1 {
						t.Fatal("new valid stream did not recover")
					}
				})
			}
		}
	}
}

func TestState008SourceAndRosterMutationsClosePendingReview(t *testing.T) {
	for name, mutate := range map[string]func(map[string]*model.PlayerState){
		"source epoch": func(rows map[string]*model.PlayerState) {
			for _, p := range rows {
				p.Observation.SourceEpoch++
			}
		},
		"source recorder": func(rows map[string]*model.PlayerState) {
			for _, p := range rows {
				p.Observation.SourcePlayerID = "different-recorder"
			}
		},
		"source authority": func(rows map[string]*model.PlayerState) {
			for _, p := range rows {
				p.Observation.Authority = "different-authority"
			}
		},
		"session": func(rows map[string]*model.PlayerState) {
			for _, p := range rows {
				p.Observation.SessionID = "different-session"
			}
		},
		"time basis": func(rows map[string]*model.PlayerState) {
			for _, p := range rows {
				p.Observation.TimeBasis = "different-clock"
			}
		},
		"duplicate identity":     func(rows map[string]*model.PlayerState) { rows["other"].PlayerID = "receiver" },
		"replaced roster":        func(rows map[string]*model.PlayerState) { rows["other"].PlayerID = "replacement" },
		"missing peer":           func(rows map[string]*model.PlayerState) { delete(rows, "other") },
		"missing holder":         func(rows map[string]*model.PlayerState) { rows["receiver"].PlayerID = "replacement" },
		"unknown attachment":     func(rows map[string]*model.PlayerState) { rows["receiver"].DiscAttachment = nil },
		"conflicting possession": func(rows map[string]*model.PlayerState) { rows["other"].CurrentDisc.PossessionConflict = true },
	} {
		t.Run(name, func(t *testing.T) {
			d := NewState008(nil)
			var records []model.CatchReviewRecord
			d.SetCatchObserver(func(_, _ string, r model.CatchReviewRecord) { records = append(records, r.Clone()) })
			ticks := catchTestFlight(1.0/15, true)
			catchRun(d, ticks[:11])
			if d.pending == nil || d.pendingReview == nil {
				t.Fatal("positive candidate control missing")
			}
			mutate(ticks[11])
			if events := d.Evaluate(ctx(), ticks[11], 12); len(events) != 0 {
				t.Fatal("changed source/roster/attachment confirmed old candidate")
			}
			d.FlushTracks(ctx(), 12)
			if len(records) != 1 || records[0].Confirmed {
				t.Fatalf("interruption diagnostic lost: %+v", records)
			}
			if err := records[0].Validate(); err != nil {
				t.Fatal(err)
			}
			if events := catchRun(d, catchTestFlight(1.0/15, true)); len(events) != 1 {
				t.Fatal("new valid stream did not recover")
			}
		})
	}
}

func TestState008RetainedEvidenceOwnsMutableCallerInputs(t *testing.T) {
	baseline := catchRun(NewState008(nil), catchTestFlight(1.0/15, true))
	if len(baseline) != 1 {
		t.Fatal("positive control missing")
	}
	d := NewState008(nil)
	ticks := catchTestFlight(1.0/15, true)
	for _, tick := range ticks[:11] {
		if events := d.Evaluate(ctx(), tick, tick["receiver"].LastFrameIdx); len(events) != 0 {
			t.Fatal("premature confirmation")
		}
		for _, p := range tick {
			p.Observation.SourceID = "caller-mutated"
			p.DiscAttachment.HandCandidates = append(p.DiscAttachment.HandCandidates, "bad")
			p.CurrentDisc.Attachment.State = "unknown"
			p.CurrentDisc.Position[0] = 999
			*p.CurrentDisc.BounceCount = 999
			p.LeftHand[0] = 999
			p.HeadPosition[0] = 999
		}
	}
	got := catchRun(d, ticks[11:])
	if len(got) != 1 || !reflect.DeepEqual(got[0].Evidence, baseline[0].Evidence) {
		t.Fatal("caller mutation corrupted retained free-flight evidence or pending holder")
	}
}

func TestState001NonfiniteGeometryIsMissingNeverNumericEvidence(t *testing.T) {
	for name, value := range map[string]float64{"nan": math.NaN(), "inf": math.Inf(1), "overflow": math.MaxFloat64} {
		t.Run(name, func(t *testing.T) {
			d, records := grabReviewRecorder()
			for i, state := range []string{"free", "held", "held"} {
				p := grabReviewFrame(i, state)
				p.CurrentDisc.Position[0] = value
				p.LeftHand[0] = -value
				assertGrabUnscored(t, d, d.Evaluate(ctx(), players(p), i))
			}
			if len(*records) != 1 {
				t.Fatal("explicit sampled possession silently lost with bad geometry")
			}
			assertGrabRecord(t, (*records)[0], "grab_geometry_unverified", true)
			if _, err := json.Marshal((*records)[0]); err != nil {
				t.Fatal(err)
			}
			for key, v := range (*records)[0].Metrics {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					t.Fatalf("nonfinite metric %s", key)
				}
			}
		})
	}
}
