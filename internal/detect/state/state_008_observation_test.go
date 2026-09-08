package state

import (
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestState008LegacyFlagsCannotBypassExplicitUnknownIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*model.PlayerState){
		"missing disc attachment":      func(p *model.PlayerState) { p.CurrentDisc.Attachment = nil },
		"unknown extractor attachment": func(p *model.PlayerState) { p.DiscAttachment = &model.DiscAttachment{State: "unknown"} },
		"conflicting holder": func(p *model.PlayerState) {
			p.CurrentDisc.Attachment = &model.DiscAttachment{State: "held", HolderID: "receiver", HandCandidates: []string{"left"}}
		},
		"missing source": func(p *model.PlayerState) { p.Observation = nil },
		"stale source":   func(p *model.PlayerState) { p.Observation.FrameIndex-- },
	} {
		t.Run(name, func(t *testing.T) {
			ticks := catchTestFlight(1.0/15, true)
			for _, players := range ticks {
				for _, p := range players {
					mutate(p)
				}
			}
			if _, reason := catchReadPossession(ticks[0], 1); reason == "" {
				t.Fatal("minimal reader accepted unobserved identity")
			}
			if _, reason := catchReadSample(ticks[0], 1); reason == "" {
				t.Fatal("full reader accepted unobserved identity")
			}
			d := NewState008(nil)
			records := 0
			d.SetCatchObserver(func(_, _ string, _ model.CatchReviewRecord) { records++ })
			if events := catchRun(d, ticks); len(events) != 0 {
				t.Fatal("legacy booleans manufactured event")
			}
			d.FlushTracks(ctx(), 12)
			if records != 0 {
				t.Fatal("unknown identity manufactured catch diagnostic")
			}
		})
	}
}

func TestState008ExplicitAttachmentOverridesStickyLegacyFlags(t *testing.T) {
	ticks := catchTestFlight(1.0/15, true)
	for _, players := range ticks {
		for _, p := range players {
			p.HasDisc = true
			p.CurrentDisc.PossessionKnown = false
		}
	}
	if events := catchRun(NewState008(nil), ticks); len(events) != 1 {
		t.Fatalf("explicit observation lost to sticky legacy flags: %d", len(events))
	}
}

func TestState008StandaloneSourceSwitchBreaksBothHistories(t *testing.T) {
	for _, switchAt := range []int{5, 11} {
		ticks := catchTestFlight(1.0/15, true)
		for _, players := range ticks[switchAt:] {
			for _, p := range players {
				p.Observation.SourceID = "different-capture"
			}
		}
		d := NewState008(nil)
		var records []model.CatchReviewRecord
		d.SetCatchObserver(func(_, _ string, r model.CatchReviewRecord) { records = append(records, r.Clone()) })
		if events := catchRun(d, ticks); len(events) != 0 {
			t.Fatal("source switch combined free-flight evidence")
		}
		d.FlushTracks(ctx(), 12)
		if len(records) != 1 || records[0].Outcome != model.CatchReviewInsufficientData {
			t.Fatalf("source loss diagnostic: %+v", records)
		}
		if switchAt == 11 && (records[0].Reason != "catch_source_changed" || records[0].Confirmed) {
			t.Fatalf("old-source pending confirmation survived: %+v", records[0])
		}
	}
}

func TestState008ResetSourceFinalizesPendingOncePlainResetSilent(t *testing.T) {
	for _, sourceReset := range []bool{false, true} {
		d := NewState008(nil)
		var records []model.CatchReviewRecord
		d.SetCatchObserver(func(_, _ string, r model.CatchReviewRecord) { records = append(records, r.Clone()) })
		catchRun(d, catchTestFlight(1.0/15, true)[:11])
		if sourceReset {
			d.ResetSource()
			d.ResetSource()
		} else {
			d.Reset()
		}
		if d.pending != nil || d.pendingReview != nil || d.diagnosticPrevious != nil || len(d.history) != 0 {
			t.Fatal("reset retained source state")
		}
		if sourceReset {
			if len(records) != 1 || records[0].Outcome != model.CatchReviewInsufficientData || records[0].Reason != "catch_source_changed" || records[0].Confirmed {
				t.Fatalf("source finalization: %+v", records)
			}
		} else if len(records) != 0 {
			t.Fatal("plain new-match reset emitted old callback")
		}
	}
}

func TestState008DoesNotApplyDiscGrabGeometryRule(t *testing.T) {
	run := func(limit float64) []model.DetectionEvent {
		mc := ctx()
		mc.ProjectRules = model.DefaultProjectRules()
		mc.ProjectRules.DiscGrabLimitM = limit
		d := NewState008(nil)
		var events []model.DetectionEvent
		for _, players := range catchTestFlight(1.0/15, true) {
			events = append(events, d.Evaluate(mc, players, players["receiver"].LastFrameIdx)...)
		}
		return events
	}
	ordinary, changed := run(.25), run(100)
	if len(ordinary) != 1 || len(changed) != 1 || !reflect.DeepEqual(ordinary[0].Evidence, changed[0].Evidence) {
		t.Fatal("STATE008 used grab geometry rule as a trajectory threshold")
	}
}
