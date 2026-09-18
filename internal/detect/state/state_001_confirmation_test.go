package state

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func assertGrabUnscored(t *testing.T, d *State001, events []model.DetectionEvent) {
	t.Helper()
	if len(events) != 0 || d.AutoEnforce() || d.DefaultEnforcementWeight() != 0 {
		t.Fatalf("mechanics review became scored: events=%+v weight=%v auto=%v", events, d.DefaultEnforcementWeight(), d.AutoEnforce())
	}
}

func assertGrabRecord(t *testing.T, r model.MechanicsAssessment, reason string, confirmed bool) {
	t.Helper()
	if err := r.Validate(); err != nil {
		t.Fatalf("invalid assessment: %v, %+v", err, r)
	}
	want := float64(0)
	if confirmed {
		want = 1
	}
	if r.Result != model.MechanicsInconclusive || r.Reason != reason || r.Metrics["sampled_possession_confirmed"] != want || r.ReasonDescription == "" {
		t.Fatalf("wrong sampled-possession result: %+v", r)
	}
	if !confirmed {
		if _, ok := r.Metrics["confirmation_frame"]; ok {
			t.Fatal("uncertain possession acquired a confirmation frame")
		}
	}
	if len(r.RawSamples) > grabReviewHistoryLimit+2 || len(r.Limitations) > model.MaxMechanicsLimitations-1 {
		t.Fatal("unbounded evidence or no room for centralized rule-reference limitation")
	}
}

func TestState001ConfirmationPreservesFirstHeldGeometry(t *testing.T) {
	d, records := grabReviewRecorder()
	free, first, next := grabReviewFrame(0, "free"), grabReviewFrame(1, "held"), grabReviewFrame(2, "held")
	first.CurrentDisc.Position = model.Vec3{91, 2, 3}
	first.LeftHand = model.Vec3{12, 2, 3}
	next.CurrentDisc.Position = model.Vec3{2, 2, 3}
	next.LeftHand = model.Vec3{2, 2, 3}
	for _, p := range []*model.PlayerState{free, first, first} {
		assertGrabUnscored(t, d, d.Evaluate(ctx(), players(p), p.LastFrameIdx))
	}
	if len(*records) != 0 || len(d.pending) != 1 {
		t.Fatal("one held sample or its duplicate confirmed possession")
	}
	// The detector must own first-held geometry and provenance before callers reuse them.
	first.CurrentDisc.Position[0], first.LeftHand[0] = 999, 999
	first.Observation.SourceID = "caller-mutated-source"
	first.DiscAttachment.HolderID = "changed-holder"
	assertGrabUnscored(t, d, d.Evaluate(ctx(), players(next), 2))
	if len(*records) != 1 {
		t.Fatalf("confirmation count: %d", len(*records))
	}
	r := (*records)[0]
	assertGrabRecord(t, r, "grab_geometry_unverified", true)
	if r.FrameIndex != 1 || r.Timestamp != .067 || r.IntervalStart != 0 || r.IntervalEnd != .067 || r.Metrics["first_held_left_origin_to_disc_center_m"] != 79 {
		t.Fatalf("confirmation replaced original acquisition geometry or timing: %+v", r)
	}
	if len(r.RawSamples) != 3 || r.RawSamples[1].DiscPosition[0] != 91 || r.RawSamples[1].LeftHand[0] != 12 || r.RawSamples[2].DiscPosition[0] != 2 {
		t.Fatal("raw first-held spike was averaged, lost, or aliased")
	}
}

func TestState001HistoryIsBoundedAndStopsAtAttachmentRun(t *testing.T) {
	d, records := grabReviewRecorder()
	for i := 0; i <= 22; i++ {
		state := "free"
		if i >= 21 {
			state = "held"
		}
		p := grabReviewFrame(i, state)
		p.CurrentDisc.Position[0] = float64(i)
		assertGrabUnscored(t, d, d.Evaluate(ctx(), players(p), i))
		if len(d.history["p1"]) > grabReviewHistoryLimit {
			t.Fatal("unbounded live history")
		}
	}
	if len(*records) != 1 {
		t.Fatalf("acquisition count = %d", len(*records))
	}
	r := (*records)[0]
	assertGrabRecord(t, r, "grab_geometry_unverified", true)
	if len(r.RawSamples) != 10 || r.Metrics["pre_acquisition_samples"] != 8 || r.Metrics["pre_acquisition_start_frame"] != 13 {
		t.Fatalf("history bound failed: %+v", r)
	}
	for i, sample := range r.RawSamples {
		if sample.FrameIndex != i+13 || sample.DiscPosition[0] != float64(i+13) {
			t.Fatalf("history ordered or raw value lost: %+v", r.RawSamples)
		}
	}
	for _, p := range []*model.PlayerState{grabReviewFrame(23, "free"), grabReviewFrame(24, "held"), grabReviewFrame(25, "held")} {
		d.Evaluate(ctx(), players(p), p.LastFrameIdx)
	}
	if len(*records) != 2 || (*records)[1].Metrics["pre_acquisition_samples"] != 1 || (*records)[1].RawSamples[0].FrameIndex != 23 {
		t.Fatal("prior held or older free interval leaked into a new acquisition")
	}
}

func TestState001PendingInterruptionsAreExplicitUnconfirmed(t *testing.T) {
	cases := []struct {
		name, reason string
		mutate       func(*model.PlayerState)
		wantRaw      int
	}{
		{"rollback", "holder_changed", func(p *model.PlayerState) {
			p.DiscAttachment = &model.DiscAttachment{State: "free"}
			p.CurrentDisc.Attachment = p.DiscAttachment.Clone()
		}, 3},
		{"other holder", "holder_changed", func(p *model.PlayerState) {
			p.DiscAttachment.HolderID = "other"
			p.CurrentDisc.Attachment = p.DiscAttachment.Clone()
		}, 3},
		{"unknown", "attachment_unknown", func(p *model.PlayerState) {
			p.DiscAttachment.State = "unknown"
			p.CurrentDisc.Attachment = p.DiscAttachment.Clone()
		}, 3},
		{"conflicting attachment", "attachment_unknown", func(p *model.PlayerState) { p.CurrentDisc.Attachment.HolderID = "other" }, 3},
		{"source", "source_changed", func(p *model.PlayerState) { p.Observation.SourceID = "new-source" }, 2},
		{"epoch", "source_changed", func(p *model.PlayerState) { p.Observation.SourceEpoch++ }, 2},
		{"session", "source_changed", func(p *model.PlayerState) { p.Observation.SessionID = "new-session" }, 2},
		{"no source", "source_unavailable", func(p *model.PlayerState) { p.Observation = nil }, 2},
		{"invalid source", "source_unavailable", func(p *model.PlayerState) { p.Observation.TimeBasis = "" }, 2},
		{"frame gap", "sample_gap", func(p *model.PlayerState) { p.LastFrameIdx = 3; p.Observation.FrameIndex = 3 }, 2},
		{"time gap", "sample_gap", func(p *model.PlayerState) { p.LastTimestamp = 1; p.Observation.Timestamp = 1 }, 2},
		{"clock rollback", "sample_gap", func(p *model.PlayerState) { p.LastTimestamp = .05; p.Observation.Timestamp = .05 }, 2},
		{"nonfinite clock", "sample_gap", func(p *model.PlayerState) { p.LastTimestamp = math.NaN() }, 2},
		{"source time mismatch", "sample_gap", func(p *model.PlayerState) { p.Observation.Timestamp += .01 }, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, records := grabReviewRecorder()
			d.Evaluate(ctx(), players(grabReviewFrame(0, "free")), 0)
			d.Evaluate(ctx(), players(grabReviewFrame(1, "held")), 1)
			p := grabReviewFrame(2, "held")
			tc.mutate(p)
			assertGrabUnscored(t, d, d.Evaluate(ctx(), players(p), p.LastFrameIdx))
			assertGrabUnscored(t, d, d.FlushTracks(ctx(), p.LastFrameIdx))
			if len(*records) != 1 {
				t.Fatalf("interrupted acquisition discarded or repeated: %+v", records)
			}
			r := (*records)[0]
			assertGrabRecord(t, r, "grab_possession_unconfirmed_"+tc.reason, false)
			if r.FrameIndex != 1 || r.Timestamp != .067 || len(r.RawSamples) != tc.wantRaw {
				t.Fatalf("uncertain transition lost first-held context: %+v", r)
			}
		})
	}
}

func TestState001TerminalHooksKeepUnconfirmedTransitionsOnce(t *testing.T) {
	for _, hook := range []string{"eof", "source", "reset_source", "phase"} {
		t.Run(hook, func(t *testing.T) {
			d, records := grabReviewRecorder()
			d.Evaluate(ctx(), players(grabReviewFrame(0, "free")), 0)
			d.Evaluate(ctx(), players(grabReviewFrame(1, "held")), 1)
			want := "end_of_stream"
			for i := 0; i < 2; i++ {
				switch hook {
				case "eof":
					assertGrabUnscored(t, d, d.FlushTracks(ctx(), 1))
				case "source":
					want = "source_changed"
					assertGrabUnscored(t, d, d.FlushSource(ctx(), 2))
				case "reset_source":
					want = "source_changed"
					d.ResetSource()
				case "phase":
					want = "phase_changed"
					assertGrabUnscored(t, d, d.FlushPhase(ctx(), 2))
				}
			}
			if len(*records) != 1 {
				t.Fatalf("terminal transition count: %d", len(*records))
			}
			assertGrabRecord(t, (*records)[0], "grab_possession_unconfirmed_"+want, false)
		})
	}
}

func TestState001RollbackRetainedSeparatelyFromSubsequentPossession(t *testing.T) {
	d, records := grabReviewRecorder()
	for i, state := range []string{"free", "held", "free", "held", "held"} {
		assertGrabUnscored(t, d, d.Evaluate(ctx(), players(grabReviewFrame(i, state)), i))
	}
	if len(*records) != 2 {
		t.Fatalf("rollback and subsequent sampled acquisition conflated: %+v", records)
	}
	assertGrabRecord(t, (*records)[0], "grab_possession_unconfirmed_holder_changed", false)
	assertGrabRecord(t, (*records)[1], "grab_geometry_unverified", true)
	if (*records)[0].EventID == (*records)[1].EventID || (*records)[1].FrameIndex != 3 || (*records)[1].RawSamples[0].FrameIndex != 2 {
		t.Fatal("rollback created duplicate confirmation or crossed attachment history")
	}
}

func TestState001NonPlayAndPhaseBoundariesRemainUnadjudicated(t *testing.T) {
	d, records := grabReviewRecorder()
	for i, state := range []string{"free", "held", "held"} {
		assertGrabUnscored(t, d, d.EvaluateReviewPhase(ctx(), players(grabReviewFrame(i, state)), i, "post_match"))
	}
	if len(*records) != 1 {
		t.Fatalf("missing non-play observation: %+v", records)
	}
	assertGrabRecord(t, (*records)[0], "grab_non_play_observation", true)
	if (*records)[0].Metrics["review_active_phase"] != 0 || !strings.Contains(strings.Join((*records)[0].Limitations, " "), "post_match") {
		t.Fatal("non-play observation lost phase/non-adjudication context")
	}
	for _, phases := range [][2]string{{"active", "post_match"}, {"post_match", "pre_match"}, {"post_match", "active"}} {
		t.Run(strings.Join(phases[:], " to "), func(t *testing.T) {
			d, records := grabReviewRecorder()
			for i, state := range []string{"free", "held", "held"} {
				phase := phases[0]
				if i == 2 {
					phase = phases[1]
				}
				if phase == "active" {
					assertGrabUnscored(t, d, d.Evaluate(ctx(), players(grabReviewFrame(i, state)), i))
				} else {
					assertGrabUnscored(t, d, d.EvaluateReviewPhase(ctx(), players(grabReviewFrame(i, state)), i, phase))
				}
			}
			d.FlushTracks(ctx(), 2)
			if len(*records) != 1 {
				t.Fatalf("phase boundary lost or invented transitions: %+v", records)
			}
			assertGrabRecord(t, (*records)[0], "grab_possession_unconfirmed_phase_changed", false)
		})
	}
}

func TestState001SourcePrivateAndIdentityDeterministic(t *testing.T) {
	run := func(epoch uint64) model.MechanicsAssessment {
		d, records := grabReviewRecorder()
		for i, state := range []string{"free", "held", "held"} {
			p := grabReviewFrame(i, state)
			p.Observation.SourceID = "https://synthetic.invalid/?token=do-not-persist"
			p.Observation.SourceEpoch = epoch
			p.Observation.SourcePlayerID = "private-recorder-id"
			d.Evaluate(ctx(), players(p), i)
		}
		if len(*records) != 1 {
			t.Fatalf("missing record: %+v", records)
		}
		return (*records)[0]
	}
	a, b, c := run(0), run(0), run(1)
	if !reflect.DeepEqual(a, b) || a.EventID == c.EventID {
		t.Fatal("event identity is nondeterministic or fails to isolate source epoch")
	}
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"synthetic.invalid", "do-not-persist", "private-recorder-id"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("raw provenance leaked: %s", private)
		}
	}
}

func TestState001UnavailablePlayerAndRosterBoundDeterministic(t *testing.T) {
	for _, overLimit := range []bool{false, true} {
		d, records := grabReviewRecorder()
		for i, state := range []string{"free", "held"} {
			ps := map[string]*model.PlayerState{}
			for _, id := range []string{"z", "a"} {
				p := grabReviewFrame(i, state)
				p.PlayerID = id
				if state == "held" {
					p.DiscAttachment.HolderID = id
					p.CurrentDisc.Attachment = p.DiscAttachment.Clone()
				}
				ps[id] = p
			}
			d.Evaluate(ctx(), ps, i)
		}
		ps := map[string]*model.PlayerState{}
		want := "player_unavailable"
		if overLimit {
			want = "roster_unavailable"
			for i := 0; i <= grabReviewPlayerLimit; i++ {
				p := grabReviewFrame(2, "free")
				p.PlayerID = fmt.Sprintf("player-%02d", i)
				ps[p.PlayerID] = p
			}
		}
		assertGrabUnscored(t, d, d.Evaluate(ctx(), ps, 2))
		if len(*records) != 2 || (*records)[0].PlayerID != "a" || (*records)[1].PlayerID != "z" {
			t.Fatalf("missing or nondeterministic terminal observations: %+v", records)
		}
		for _, r := range *records {
			assertGrabRecord(t, r, "grab_possession_unconfirmed_"+want, false)
		}
		if len(d.pending) != 0 || len(d.previous) != 0 || len(d.history) != 0 {
			t.Fatal("unavailable roster retained live sample history")
		}
	}
}

func TestState001AbsentProvenanceAndDirectTransferRemainExplicit(t *testing.T) {
	d, records := grabReviewRecorder()
	for i, state := range []string{"free", "held", "held"} {
		p := grabReviewFrame(i, state)
		p.Observation = nil
		assertGrabUnscored(t, d, d.Evaluate(ctx(), players(p), i))
	}
	if len(*records) != 1 {
		t.Fatal("known transition with absent provenance silently discarded")
	}
	assertGrabRecord(t, (*records)[0], "grab_possession_unconfirmed_source_unavailable", false)

	d, records = grabReviewRecorder()
	d.EvaluateReviewPhase(ctx(), players(otherHolderFrame(0)), 0, "pre_match")
	d.EvaluateReviewPhase(ctx(), players(grabReviewFrame(1, "held")), 1, "pre_match")
	assertGrabUnscored(t, d, d.FlushTracks(ctx(), 1))
	if len(*records) != 1 {
		t.Fatal("unconfirmed direct transfer discarded")
	}
	r := (*records)[0]
	assertGrabRecord(t, r, "grab_possession_unconfirmed_end_of_stream", false)
	if r.Metrics["direct_holder_transfer"] != 1 || !strings.Contains(r.ReasonDescription, "direct holder transfer") || !strings.Contains(r.ReasonDescription, "outside active play") {
		t.Fatal("uncertain transfer lost its distinct interpretation")
	}
}

func TestState001BaselineHistoryDoesNotCrossSourceOrUnknownState(t *testing.T) {
	for _, mode := range []string{"source", "epoch", "unknown", "clock_gap"} {
		t.Run(mode, func(t *testing.T) {
			d, records := grabReviewRecorder()
			for i := 0; i < 6; i++ {
				state := "free"
				if i >= 4 {
					state = "held"
				}
				p := grabReviewFrame(i, state)
				if mode == "unknown" && i == 2 {
					p.DiscAttachment.State = "unknown"
					p.CurrentDisc.Attachment = p.DiscAttachment.Clone()
				}
				if i >= 3 {
					switch mode {
					case "source":
						p.Observation.SourceID = "new-source"
					case "epoch":
						p.Observation.SourceEpoch = 7
					case "clock_gap":
						p.LastTimestamp += 1
						p.Observation.Timestamp = p.LastTimestamp
					}
				}
				assertGrabUnscored(t, d, d.Evaluate(ctx(), players(p), i))
			}
			if len(*records) != 1 {
				t.Fatalf("missing later continuous transition: %+v", records)
			}
			r := (*records)[0]
			assertGrabRecord(t, r, "grab_geometry_unverified", true)
			if r.Metrics["pre_acquisition_samples"] != 1 || len(r.RawSamples) != 3 || r.RawSamples[0].FrameIndex != 3 {
				t.Fatalf("history crossed a source/time/attachment boundary: %+v", r.RawSamples)
			}
		})
	}
}

func TestState001ObserverCannotMutateLiveHistory(t *testing.T) {
	d := NewState001(nil)
	calls := 0
	d.SetMechanicsObserver(func(_, _ string, r model.MechanicsAssessment) {
		calls++
		r.Metrics["sampled_possession_confirmed"] = 999
		for i := range r.RawSamples {
			if r.RawSamples[i].DiscPosition != nil {
				r.RawSamples[i].DiscPosition[0] = 999
			}
		}
	})
	for i, state := range []string{"free", "held", "held"} {
		d.Evaluate(ctx(), players(grabReviewFrame(i, state)), i)
	}
	if calls != 1 || len(d.history["p1"]) != 2 {
		t.Fatal("missing callback or active held history")
	}
	for _, sample := range d.history["p1"] {
		if sample.raw.DiscPosition[0] != 6 {
			t.Fatal("observer corrupted live geometry history")
		}
	}
}

func TestState001ConfirmedNonPlayTransferAndPhaseInputBounds(t *testing.T) {
	for _, phase := range []string{"pre_match", strings.Repeat("p", 64), strings.Repeat("p", 65), "", "playing"} {
		d, records := grabReviewRecorder()
		for _, p := range []*model.PlayerState{otherHolderFrame(0), grabReviewFrame(1, "held"), grabReviewFrame(2, "held")} {
			assertGrabUnscored(t, d, d.EvaluateReviewPhase(ctx(), players(p), p.LastFrameIdx, phase))
		}
		if len(*records) != 1 {
			t.Fatalf("missing non-play transfer: %+v", records)
		}
		r := (*records)[0]
		assertGrabRecord(t, r, "grab_non_play_transfer_observation", true)
		if r.Metrics["review_active_phase"] != 0 || r.Metrics["direct_holder_transfer"] != 1 || !strings.Contains(r.ReasonDescription, "not adjudicated") {
			t.Fatal("non-play API permitted adjudication or lost transfer context")
		}
	}
}
