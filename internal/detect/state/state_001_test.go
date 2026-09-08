package state

import (
	"math"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func grabReviewFrame(frame int, attachment string) *model.PlayerState {
	p := active("p1", frame)
	a := &model.DiscAttachment{State: attachment}
	if attachment == "held" {
		a.HolderID, a.HandCandidates, p.HasDisc = p.PlayerID, []string{"left", "right"}, true
	}
	p.DiscAttachment = a
	p.CurrentDisc = &model.DiscState{Attachment: a.Clone(), Position: model.Vec3{6, 2, 3}}
	p.Observation = &model.ObservationContext{Source: "replay", Authority: "client_reported", TimeBasis: "capture", SessionID: "s1", FrameIndex: frame, Timestamp: p.LastTimestamp}
	return p
}

func grabReviewRecorder() (*State001, *[]model.MechanicsAssessment) {
	d := NewState001(map[string]any{"grab_distance_threshold": 999.0, "closing_velocity_scale": 999.0})
	d.SetWeight(1)
	d.SetAutoEnforce(true)
	records := []model.MechanicsAssessment{}
	d.SetMechanicsObserver(func(_, _ string, r model.MechanicsAssessment) { records = append(records, r.Clone()) })
	return d, &records
}

func TestState001KnownAcquisitionIsInconclusiveNeverScored(t *testing.T) {
	d, records := grabReviewRecorder()
	for frame, state := range []string{"free", "held", "held"} {
		p := grabReviewFrame(frame, state)
		p.Speed = 100 // no velocity credit changes the project rule
		if events := d.Evaluate(ctx(), players(p), frame); len(events) != 0 {
			t.Fatalf("scored unsupported geometry: %v", events)
		}
	}
	if d.AutoEnforce() || d.DefaultEnforcementWeight() != 0 || len(*records) != 1 {
		t.Fatalf("policy/records: %+v", records)
	}
	r := (*records)[0]
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if r.Result != model.MechanicsInconclusive || r.Reason != "grab_geometry_unverified" || r.Metrics["legal_limit_m"] != .25 || r.Hand != "left|right" || r.FrameIndex != 1 || r.IntervalStart != 0 || r.IntervalEnd != .067 {
		t.Fatalf("assessment: %+v", r)
	}
	if len(r.RawSamples) != 2 || r.RawSamples[0].Attachment != "free" || r.RawSamples[1].Attachment != "held" {
		t.Fatalf("raw interval lost: %+v", r.RawSamples)
	}
	for _, key := range []string{"adjusted_distance", "closing_credit_m", "threshold"} {
		if _, ok := r.Metrics[key]; ok {
			t.Fatalf("legacy tolerance leaked: %s", key)
		}
	}
}

func TestState001DoesNotInventUnknownInitialTransferOrPlayerGripCatch(t *testing.T) {
	for name, states := range map[string][]string{"initially held": {"held", "held"}, "unknown": {"free", "unknown", "held"}, "unknown baseline": {"unknown", "held"}, "transfer": {"held", "held", "held"}} {
		t.Run(name, func(t *testing.T) {
			d, records := grabReviewRecorder()
			for frame, state := range states {
				p := grabReviewFrame(frame, state)
				if name == "transfer" {
					p.DiscAttachment.HandCandidates = []string{[]string{"left", "right", "left"}[frame]}
					p.CurrentDisc.Attachment = p.DiscAttachment.Clone()
				}
				d.Evaluate(ctx(), players(p), frame)
			}
			if len(*records) != 0 {
				t.Fatalf("invented acquisition: %+v", records)
			}
		})
	}
	d, records := grabReviewRecorder()
	for frame := 0; frame < 2; frame++ {
		p := grabReviewFrame(frame, "free")
		label := []string{"none", "player_other"}[frame]
		p.HeldItems = &model.HandAttachments{Left: &label, Right: &label}
		p.HasDisc = true // contradictory legacy boolean must never override explicit free
		d.Evaluate(ctx(), players(p), frame)
	}
	if len(*records) != 0 {
		t.Fatal("player grip became disc acquisition")
	}
}

func TestState001ContinuityAndSourceReset(t *testing.T) {
	for name, mutate := range map[string]func(*model.PlayerState){
		"frame gap":   func(p *model.PlayerState) { p.LastFrameIdx = 5; p.Observation.FrameIndex = 5 },
		"time gap":    func(p *model.PlayerState) { p.LastTimestamp = 1; p.Observation.Timestamp = 1 },
		"new source":  func(p *model.PlayerState) { p.Observation.SourceID = "other" },
		"new session": func(p *model.PlayerState) { p.Observation.SessionID = "s2" },
		"nonfinite":   func(p *model.PlayerState) { p.LastTimestamp = math.NaN() },
	} {
		t.Run(name, func(t *testing.T) {
			d, records := grabReviewRecorder()
			d.Evaluate(ctx(), players(grabReviewFrame(0, "free")), 0)
			p := grabReviewFrame(1, "held")
			mutate(p)
			d.Evaluate(ctx(), players(p), p.LastFrameIdx)
			if len(*records) != 0 {
				t.Fatal("crossed discontinuity")
			}
		})
	}
}

func TestState001DuplicateConflictsAndHookReset(t *testing.T) {
	d, records := grabReviewRecorder()
	free, held := grabReviewFrame(0, "free"), grabReviewFrame(1, "held")
	d.Evaluate(ctx(), players(free), 0)
	d.Evaluate(ctx(), players(free), 0)
	d.Evaluate(ctx(), players(held), 1)
	d.Evaluate(ctx(), players(held), 1)
	if len(*records) != 1 {
		t.Fatalf("duplicate count = %d", len(*records))
	}
	d.Evaluate(ctx(), players(grabReviewFrame(1, "free")), 1) // conflicting duplicate breaks continuity
	d.Evaluate(ctx(), players(grabReviewFrame(2, "held")), 2)
	if len(*records) != 1 {
		t.Fatal("conflicting duplicate invented regrab")
	}
	d.Reset()
	d.Evaluate(ctx(), players(grabReviewFrame(2, "held")), 2)
	if len(*records) != 1 {
		t.Fatal("reset invented initial catch")
	}
	d.SetMechanicsObserver(nil)
	d.Evaluate(ctx(), players(grabReviewFrame(3, "free")), 3)
	d.Evaluate(ctx(), players(grabReviewFrame(4, "held")), 4)
	if len(*records) != 1 {
		t.Fatal("detached hook leaked")
	}
}

func TestState001RawOwnershipAndChunkEquivalent(t *testing.T) {
	run := func(mutate bool) []model.MechanicsAssessment {
		d, records := grabReviewRecorder()
		p := grabReviewFrame(0, "free")
		d.Evaluate(ctx(), players(p), 0)
		if mutate {
			p.CurrentDisc.Position[0] = 900
			p.LeftHand[0] = 900
			p.Observation.Source = "modified"
		}
		d.Evaluate(ctx(), players(grabReviewFrame(1, "held")), 1)
		return *records
	}
	if !reflect.DeepEqual(run(false), run(true)) {
		t.Fatal("retained raw samples alias caller state")
	}
	d, records := grabReviewRecorder()
	p := grabReviewFrame(0, "free")
	p.LeftHand, p.RightHand = model.Vec3{}, model.Vec3{}
	d.Evaluate(ctx(), players(p), 0)
	p = grabReviewFrame(1, "held")
	p.CurrentDisc = nil
	d.Evaluate(ctx(), players(p), 1)
	if len(*records) != 1 || (*records)[0].RawSamples[0].LeftHand != nil || (*records)[0].RawSamples[1].DiscPosition != nil {
		t.Fatal("missing geometry was invented or acquisition discarded")
	}
}
