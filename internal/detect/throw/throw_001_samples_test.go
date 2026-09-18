package throw

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func speedTestState(frame int, speed float64) *model.PlayerState {
	ps := newState("p1", frame)
	ps.FrameCount, ps.LastTimestamp = frame+1, float64(frame)*.05
	ps.Observation = &model.ObservationContext{Source: "echoreplay", SourceID: "private://path?token=do-not-persist", Authority: "client_reported", TimeBasis: "recording", SessionID: "throw-test", SourcePlayerID: "p1", FrameIndex: frame, Timestamp: ps.LastTimestamp}
	bounce := 0
	ps.CurrentDisc = &model.DiscState{Attachment: &model.DiscAttachment{State: "free"}, Position: model.Vec3{float64(frame), 1, 1}, Velocity: model.Vec3{speed, 0, 0}, Speed: speed, BounceCount: &bounce, SampledPlayerCount: 1, PossessionKnown: true}
	ps.DiscAttachment = ps.CurrentDisc.Attachment.Clone()
	return ps
}

func speedTestRelease(first *model.PlayerState, observed int) *model.ThrowEvent {
	t := mkThrow("p1", first.LastFrameIdx, first.CurrentDisc.Speed, 0)
	t.Timestamp, t.ReleasePosition = first.LastTimestamp, first.CurrentDisc.Position
	t.ObservedFrameIndex = &observed
	t.ReleaseWindow = &model.ReleaseObservation{PlayerID: "p1", FirstFreeFrame: t.FrameIndex, StartFrame: t.FrameIndex - 1, EndFrame: t.FrameIndex, StartTime: t.Timestamp - .05, EndTime: t.Timestamp, Source: first.Observation.Clone()}
	return &t
}

func bindSpeedTestLocalReport(t *model.ThrowEvent) {
	source := &model.ObservationContext{Source: "echoreplay", SourceID: "private-source", Authority: "client_reported", TimeBasis: "recording", SessionID: "throw-test", SourcePlayerID: t.ThrowerID, FrameIndex: t.FrameIndex, Timestamp: t.Timestamp, Freshness: "value_change"}
	t.GameLastThrowProvenance = source.Clone()
	if t.ReleaseWindow == nil {
		t.ReleaseWindow = &model.ReleaseObservation{FirstFreeFrame: t.FrameIndex, StartFrame: t.FrameIndex - 1, EndFrame: t.FrameIndex}
	}
	t.ReleaseWindow.Source = source.Clone()
}

func runSpeedTestWindow(t *testing.T, d *Throw001, speeds []float64, change func(*model.ThrowEvent)) []model.DetectionEvent {
	t.Helper()
	first := speedTestState(100, speeds[0])
	if got := d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": first}, 100); len(got) != 0 {
		t.Fatalf("first-free tick is not a published throw: %+v", got)
	}
	second := speedTestState(101, speeds[1])
	second.LastThrow = speedTestRelease(first, 101)
	if change != nil {
		change(second.LastThrow)
	}
	events := d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": second}, 101)
	for i := 2; i < len(speeds); i++ {
		frame := 100 + i
		events = append(events, d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": speedTestState(frame, speeds[i])}, frame)...)
	}
	return events
}

func TestThrow001ThreeSamplesDistinguishTransientAndSustained(t *testing.T) {
	for _, tt := range []struct {
		name   string
		speeds []float64
		status string
		above  int
		median float64
	}{
		{"transient", []float64{30, 18, 18}, "uncorroborated", 1, 18},
		{"sustained", []float64{20, 20, 20}, "corroborated", 3, 20},
		{"two above do not hide one below", []float64{20, 18, 20}, "uncorroborated", 2, 20},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := NewThrow001(nil)
			d.SetAutoEnforce(true)
			events := runSpeedTestWindow(t, d, tt.speeds, nil)
			if len(events) != 1 {
				t.Fatalf("want one retained review: %+v", events)
			}
			ev := events[0]
			e := ev.Evidence.(model.ThrowEvidence)
			r := e.SpeedReview
			if r.Status != tt.status || r.AboveCapSamples != tt.above || len(r.Samples) != 3 || r.MedianSampledSpeed == nil || *r.MedianSampledSpeed != tt.median {
				t.Fatalf("review %+v", r)
			}
			if ev.FrameIndex != 100 || ev.Timestamp != 5 || ev.FrameRangeStart != 100 || ev.FrameRangeEnd != 102 || r.ReleaseFrame != 100 || r.ResolvedFrame != 102 {
				t.Fatalf("causal identity shifted: %+v", ev)
			}
			if e.ReleaseSpeed != tt.speeds[0] || e.SampledDiscSpeed != tt.speeds[0] || e.ReleaseVelocity != (model.Vec3{tt.speeds[0], 0, 0}) || e.EffectiveCap != 18.9 {
				t.Fatalf("first sample or configured cap changed: %+v", e)
			}
			for i, s := range r.Samples {
				if s.FrameIndex != 100+i || s.Speed != tt.speeds[i] || s.Velocity != (model.Vec3{tt.speeds[i], 0, 0}) || s.Source.SourceID != "" {
					t.Fatalf("raw evidence %+v", s)
				}
			}
			if ev.AutoEnforce {
				t.Fatal("sampled corroboration cannot authorize punishment")
			}
			if tt.status == "uncorroborated" && (ev.Severity != artifactSeverity || ev.EnforcementWeight != 0) {
				t.Fatal("uncertainty was promoted")
			}
			if tt.status == "corroborated" && (ev.Severity <= artifactSeverity || ev.EnforcementWeight != d.Weight) {
				t.Fatal("corroborated sampled review path was not exercised")
			}
			payload, err := json.Marshal(e)
			if err != nil || strings.Contains(string(payload), "do-not-persist") {
				t.Fatalf("private source persisted: %s %v", payload, err)
			}
			if got := d.FlushTracks(testCtx(), 103); len(got) != 0 {
				t.Fatal("completed release was emitted twice")
			}
		})
	}
}

func TestThrow001WindowAbortsWithExplicitEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, reason string
		change       func(*model.PlayerState)
	}{
		{"gap", "release_speed_sample_gap", func(p *model.PlayerState) { p.LastFrameIdx = 104; p.Observation.FrameIndex = 104 }},
		{"clock gap", "release_speed_sample_gap", func(p *model.PlayerState) { p.LastTimestamp += 1; p.Observation.Timestamp = p.LastTimestamp }},
		{"source", "release_speed_source_changed", func(p *model.PlayerState) { p.Observation.SourceID = "new source" }},
		{"bounce", "release_speed_contact_observed", func(p *model.PlayerState) { *p.CurrentDisc.BounceCount = 1 }},
		{"missing bounce", "release_speed_contact_unavailable", func(p *model.PlayerState) { p.CurrentDisc.BounceCount = nil }},
		{"head contact", "release_speed_contact_possible", func(p *model.PlayerState) { p.LegalContext.PossibleHeadContact = true }},
		{"contact direction change", "release_speed_contact_possible", func(p *model.PlayerState) { p.CurrentDisc.Velocity = model.Vec3{-20, 0, 0} }},
		{"held", "release_speed_disc_held", func(p *model.PlayerState) {
			p.CurrentDisc.Attachment = &model.DiscAttachment{State: "held", HolderID: "p1", HandCandidates: []string{"left"}}
			p.DiscAttachment = p.CurrentDisc.Attachment.Clone()
		}},
		{"incomplete roster", "release_speed_roster_unavailable", func(p *model.PlayerState) { p.CurrentDisc.SampledPlayerCount = 2 }},
		{"missing disc", "release_speed_attachment_unknown", func(p *model.PlayerState) { p.CurrentDisc = nil; p.DiscAttachment = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := NewThrow001(nil)
			d.SetAutoEnforce(true)
			if ev := runSpeedTestWindow(t, d, []float64{20, 20}, nil); len(ev) != 0 {
				t.Fatal("two samples resolved early")
			}
			third := speedTestState(102, 20)
			tt.change(third)
			events := d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": third}, third.LastFrameIdx)
			if len(events) != 1 {
				t.Fatalf("lost pending observation: %+v", events)
			}
			ev := events[0]
			r := ev.Evidence.(model.ThrowEvidence).SpeedReview
			if r.Status != "uncorroborated" || r.Reason != tt.reason || ev.AutoEnforce || ev.EnforcementWeight != 0 || ev.Severity != artifactSeverity {
				t.Fatalf("uncertainty wrong: %+v %+v", ev, r)
			}
			if got := d.FlushTracks(testCtx(), 110); len(got) != 0 {
				t.Fatal("abort emitted twice")
			}
		})
	}
}

func TestThrow001EOFPhaseAndSourceResetCloseExactlyOnce(t *testing.T) {
	for _, kind := range []string{"eof", "phase", "source"} {
		t.Run(kind, func(t *testing.T) {
			d := NewThrow001(nil)
			runSpeedTestWindow(t, d, []float64{20, 20}, nil)
			var events []model.DetectionEvent
			reason := ""
			switch kind {
			case "eof":
				events = d.FlushTracks(testCtx(), 101)
				reason = "release_speed_end_of_stream"
			case "phase":
				events = d.FlushPhase(testCtx(), 102)
				reason = "release_speed_inactive_phase"
			case "source":
				d.ResetSource()
				d.ResetSource()
				mc := testCtx()
				mc.MatchID = "new-match"
				mc.Physics.DiscSpeedCap = 100
				events = d.FlushTracks(mc, 102)
				reason = "release_speed_source_changed"
			}
			if len(events) != 1 {
				t.Fatalf("terminal observation %+v", events)
			}
			e := events[0].Evidence.(model.ThrowEvidence)
			if e.SpeedReview.Reason != reason || e.SpeedReview.Status != "uncorroborated" || len(e.SpeedReview.Samples) != 2 || e.EffectiveCap != 18.9 || events[0].MatchID != "throw-test" {
				t.Fatalf("terminal state moved or lost: %+v", events[0])
			}
			if got := d.FlushTracks(testCtx(), 103); len(got) != 0 {
				t.Fatal("terminal observation repeated")
			}
		})
	}
	d := NewThrow001(nil)
	runSpeedTestWindow(t, d, []float64{20, 20}, nil)
	d.Reset()
	if got := d.FlushTracks(testCtx(), 102); len(got) != 0 {
		t.Fatal("plain match reset leaked prior match evidence")
	}
}

func TestThrow001DuplicateSamplesAndMutableInputs(t *testing.T) {
	d := NewThrow001(nil)
	first := speedTestState(100, 30)
	before := first.Observation.Clone()
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": first}, 100)
	second := speedTestState(101, 18)
	second.LastThrow = speedTestRelease(first, 101)
	d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": second}, 101)
	if got := d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": second}, 101); len(got) != 0 {
		t.Fatal("duplicate counted as third sample")
	}
	if !reflect.DeepEqual(first.Observation, before) {
		t.Fatal("source redaction mutated telemetry")
	}
	first.CurrentDisc.Velocity = model.Vec3{1, 0, 0}
	*first.CurrentDisc.BounceCount = 9
	second.LastThrow.ReleaseVelocity = model.Vec3{2, 0, 0}
	got := d.Evaluate(testCtx(), map[string]*model.PlayerState{"p1": speedTestState(102, 18)}, 102)
	if len(got) != 1 {
		t.Fatal("pending event lost")
	}
	r := got[0].Evidence.(model.ThrowEvidence).SpeedReview
	if r.Samples[0].Speed != 30 || r.Samples[0].Velocity != (model.Vec3{30, 0, 0}) || *r.Samples[0].BounceCount != 0 || r.Status != "uncorroborated" {
		t.Fatalf("retained evidence aliases inputs: %+v", r)
	}
}

func TestThrow001UnboundLocalReportCannotCreateOrCorroborateSpeed(t *testing.T) {
	for _, mutation := range []func(*model.ThrowEvent){
		func(t *model.ThrowEvent) { t.GameLastThrowProvenance = nil },
		func(t *model.ThrowEvent) { t.GameLastThrowProvenance.SourcePlayerID = "p2" },
		func(t *model.ThrowEvent) { t.GameLastThrowProvenance.FrameIndex-- },
		func(t *model.ThrowEvent) { t.GameLastThrowProvenance.Freshness = "stale" },
	} {
		ps := withThrow("p1", 100, 18, 0)
		th := ps["p1"].LastThrow
		th.GameLastThrow = &model.GameThrowDetails{TotalSpeed: 50}
		bindSpeedTestLocalReport(th)
		mutation(th)
		if got := NewThrow001(nil).Evaluate(testCtx(), ps, 100); len(got) != 0 {
			t.Fatalf("unbound report created a finding: %+v", got)
		}
	}
}

func TestThrow001RatioConfigurationRemainsContextOnly(t *testing.T) {
	var baseline float64
	for _, limit := range []float64{1, 100} {
		d := NewThrow001(nil)
		if err := d.Configure(map[string]any{"max_speed_ratio": limit}); err != nil {
			t.Fatal(err)
		}
		above := 0
		d.SetDecisionObserver(func(_, _ string, _ int, reason string) {
			if reason == "hand_ratio_above_context_limit" {
				above++
			}
		})
		events := runSpeedTestWindow(t, d, []float64{20, 20, 20}, nil)
		if len(events) != 1 {
			t.Fatalf("limit %.0f lost sampled review", limit)
		}
		want := 0
		if limit == 1 {
			want = 1
			baseline = events[0].Severity
		}
		if above != want || events[0].Severity != baseline {
			t.Fatalf("ratio limit %.0f: traces=%d severity=%v baseline=%v", limit, above, events[0].Severity, baseline)
		}
	}
}
