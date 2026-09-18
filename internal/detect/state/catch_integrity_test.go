package state

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Retries are not independent samples and must neither confirm a catch nor
// erase valid evidence. These are synthetic integrity regressions, not
// independent validation of an exploit or a legal physics threshold.
func TestState008ExactRetriesPreserveEvidenceWithoutConfirming(t *testing.T) {
	baseline := catchRun(NewState008(nil), catchTestFlight(1.0/15, true))
	if len(baseline) != 1 {
		t.Fatal("synthetic positive control did not produce a review observation")
	}
	for _, duplicateAt := range []int{0, 3, 5, 9, 10, 11} {
		t.Run(fmt.Sprintf("frame_%d", duplicateAt+1), func(t *testing.T) {
			d := NewState008(nil)
			var records []model.CatchReviewRecord
			d.SetCatchObserver(func(_, _ string, r model.CatchReviewRecord) { records = append(records, r.Clone()) })
			ticks := catchTestFlight(1.0/15, true)
			var events []model.DetectionEvent
			for i, tick := range ticks {
				frame := tick["receiver"].LastFrameIdx
				events = append(events, d.Evaluate(ctx(), tick, frame)...)
				if i == duplicateAt {
					for retry := 0; retry < 3; retry++ {
						if extra := d.Evaluate(ctx(), tick, frame); len(extra) != 0 {
							t.Fatal("a repeated snapshot confirmed or repeated an event")
						}
					}
				}
			}
			d.FlushTracks(ctx(), 12)
			if len(events) != 1 || !reflect.DeepEqual(events[0].Evidence, baseline[0].Evidence) {
				t.Fatalf("retry changed uninterrupted evidence: events=%d", len(events))
			}
			if len(records) != 1 || !records[0].Confirmed || records[0].Outcome != model.CatchReviewObservation {
				t.Fatalf("retry changed review confirmation: %+v", records)
			}
		})
	}
}

func TestState008ConflictingRetryCannotConfirm(t *testing.T) {
	for name, mutate := range map[string]func(*model.PlayerState){
		"disc position": func(p *model.PlayerState) { p.CurrentDisc.Position[0] += .01 },
		"source":        func(p *model.PlayerState) { p.Observation.SourceID = "changed" },
		"attachment": func(p *model.PlayerState) {
			p.DiscAttachment.HandCandidates = []string{"right"}
			p.CurrentDisc.Attachment = p.DiscAttachment.Clone()
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := NewState008(nil)
			ticks := catchTestFlight(1.0/15, true)
			var records []model.CatchReviewRecord
			d.SetCatchObserver(func(_, _ string, r model.CatchReviewRecord) { records = append(records, r.Clone()) })
			catchRun(d, ticks[:11])
			for _, p := range ticks[10] {
				mutate(p) // mutate caller-owned inputs after the original snapshot
			}
			if events := catchRun(d, ticks[10:]); len(events) != 0 {
				t.Fatal("contradictory retry survived as a confirmed trajectory")
			}
			d.FlushTracks(ctx(), 12)
			if len(records) != 1 || records[0].Confirmed || records[0].Outcome != model.CatchReviewInsufficientData {
				t.Fatalf("contradictory retry confirmation: %+v", records)
			}
		})
	}
}

func TestState008PhaseBoundaryFinalizesPendingAndClearsFlight(t *testing.T) {
	for _, phaseAt := range []int{8, 10} {
		t.Run(fmt.Sprintf("after_frame_%d", phaseAt+1), func(t *testing.T) {
			d := NewState008(nil)
			ticks := catchTestFlight(1.0/15, true)
			var records []model.CatchReviewRecord
			d.SetCatchObserver(func(_, _ string, r model.CatchReviewRecord) { records = append(records, r.Clone()) })
			catchRun(d, ticks[:phaseAt+1])
			// Match the pipeline's optional phase hook without an import cycle.
			if flusher, ok := any(d).(interface {
				FlushPhase(*model.MatchContext, int) []model.DetectionEvent
			}); ok {
				for repeat := 0; repeat < 2; repeat++ {
					if events := flusher.FlushPhase(ctx(), phaseAt+2); len(events) != 0 {
						t.Fatal("phase close manufactured a detection")
					}
				}
			}
			if d.pending != nil || d.pendingReview != nil || d.previous != nil || d.diagnosticPrevious != nil || len(d.history) != 0 {
				t.Fatal("phase boundary retained an active-play flight or pending catch")
			}
			if phaseAt == 10 {
				if len(records) != 1 || records[0].Reason != "catch_phase_changed" || records[0].Outcome != model.CatchReviewUnconfirmed || records[0].Confirmed {
					t.Fatalf("first-held observation not preserved as unconfirmed: %+v", records)
				}
				if err := records[0].Validate(); err != nil {
					t.Fatal(err)
				}
			} else if len(records) != 0 {
				t.Fatal("free flight without a catch manufactured a diagnostic")
			}
			if events := catchRun(d, ticks[phaseAt+1:]); len(events) != 0 {
				t.Fatal("phase boundary combined old flight with new possession")
			}
		})
	}
}

func TestState001ExplicitOwnershipConflictCannotConfirm(t *testing.T) {
	d, records := grabReviewRecorder()
	d.Evaluate(ctx(), players(grabReviewFrame(0, "free")), 0)
	d.Evaluate(ctx(), players(grabReviewFrame(1, "held")), 1)
	p := grabReviewFrame(2, "held")
	p.CurrentDisc.PossessionConflict = true
	assertGrabUnscored(t, d, d.Evaluate(ctx(), players(p), 2))
	d.FlushTracks(ctx(), 2)
	if len(*records) != 1 {
		t.Fatalf("pending observation lost: %+v", records)
	}
	assertGrabRecord(t, (*records)[0], "grab_possession_unconfirmed_attachment_unknown", false)
}

func TestState001OwnershipConflictCannotSeedAcquisition(t *testing.T) {
	for _, conflictAt := range []int{0, 1} {
		t.Run(fmt.Sprintf("frame_%d", conflictAt), func(t *testing.T) {
			d, records := grabReviewRecorder()
			for frame, state := range []string{"free", "held", "held"} {
				p := grabReviewFrame(frame, state)
				p.CurrentDisc.PossessionConflict = frame == conflictAt
				assertGrabUnscored(t, d, d.Evaluate(ctx(), players(p), frame))
			}
			d.FlushTracks(ctx(), 2)
			if len(*records) != 0 {
				t.Fatal("conflicting ownership seeded an acquisition")
			}
			for offset, state := range []string{"free", "held", "held"} {
				frame := offset + 3
				assertGrabUnscored(t, d, d.Evaluate(ctx(), players(grabReviewFrame(frame, state)), frame))
			}
			if len(*records) != 1 {
				t.Fatal("subsequent coherent acquisition did not recover")
			}
			assertGrabRecord(t, (*records)[0], "grab_geometry_unverified", true)
		})
	}
}

func TestState001DuplicateIdentityCannotConfirm(t *testing.T) {
	d, records := grabReviewRecorder()
	d.Evaluate(ctx(), players(grabReviewFrame(0, "free")), 0)
	d.Evaluate(ctx(), players(grabReviewFrame(1, "held")), 1)
	p, duplicate := grabReviewFrame(2, "held"), grabReviewFrame(2, "held")
	assertGrabUnscored(t, d, d.Evaluate(ctx(), map[string]*model.PlayerState{"p1": p, "duplicate": duplicate}, 2))
	d.FlushTracks(ctx(), 2)
	if len(*records) != 1 {
		t.Fatalf("pending observation lost: %+v", records)
	}
	assertGrabRecord(t, (*records)[0], "grab_possession_unconfirmed_roster_unavailable", false)
}

func TestState001MalformedRosterClearsPendingBeforeAnyRow(t *testing.T) {
	for _, emptyID := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty_%t", emptyID), func(t *testing.T) {
			for trial := 0; trial < 12; trial++ {
				d, records := grabReviewRecorder()
				d.Evaluate(ctx(), players(grabReviewFrame(0, "free")), 0)
				d.Evaluate(ctx(), players(grabReviewFrame(1, "held")), 1)
				p, bad := grabReviewFrame(2, "held"), grabReviewFrame(2, "free")
				if emptyID {
					bad.PlayerID = ""
				}
				assertGrabUnscored(t, d, d.Evaluate(ctx(), map[string]*model.PlayerState{"p1": p, "bad": bad}, 2))
				d.FlushTracks(ctx(), 2)
				if len(*records) != 1 || len(d.pending) != 0 || len(d.previous) != 0 || len(d.history) != 0 {
					t.Fatal("malformed roster lost or retained pending state")
				}
				assertGrabRecord(t, (*records)[0], "grab_possession_unconfirmed_roster_unavailable", false)
			}
		})
	}
}
