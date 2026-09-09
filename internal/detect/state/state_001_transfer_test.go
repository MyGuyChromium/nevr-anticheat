package state

import (
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func otherHolderFrame(frame int) *model.PlayerState {
	p := grabReviewFrame(frame, "held")
	p.DiscAttachment.HolderID = "other-player"
	p.CurrentDisc.Attachment = p.DiscAttachment.Clone()
	p.HasDisc = false
	return p
}

func TestState001DifferentHolderTransferIsInconclusiveNeverScored(t *testing.T) {
	// These synthetic distances describe both nearby handoffs and remote
	// sampled ownership changes. Neither establishes what happened in between.
	for _, position := range []model.Vec3{{1, 2, 3}, {1000, 2, 3}} {
		d, records := grabReviewRecorder()
		previous, now := otherHolderFrame(0), grabReviewFrame(1, "held")
		previous.CurrentDisc.Position = position
		for _, p := range []*model.PlayerState{previous, now, now, grabReviewFrame(2, "held")} {
			if events := d.Evaluate(ctx(), players(p), p.LastFrameIdx); len(events) != 0 {
				t.Fatal("handoff/steal/unsampled release became a scored incident")
			}
		}
		if len(*records) != 1 || d.AutoEnforce() || d.DefaultEnforcementWeight() != 0 {
			t.Fatalf("transfer duplicate or enforcement policy mismatch: %+v", records)
		}
		r := (*records)[0]
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
		if r.Result != model.MechanicsInconclusive || r.Reason != "grab_transfer_without_free_sample" || !strings.Contains(r.ReasonDescription, "legal handoff or steal") {
			t.Fatalf("transfer misrepresented: %+v", r)
		}
		if r.FrameIndex != 1 || len(r.RawSamples) != 2 || r.RawSamples[0].Attachment != "held" || r.RawSamples[1].Attachment != "held" {
			t.Fatalf("invented a free-flight state: %+v", r)
		}
		if _, ok := r.Metrics["previous_held_left_origin_to_disc_center_m"]; !ok {
			t.Fatal("previous held geometry was not preserved descriptively")
		}
		for key := range r.Metrics {
			if strings.HasPrefix(key, "last_free_") || strings.Contains(key, "lower_bound") || strings.Contains(key, "upper_bound") {
				t.Fatalf("invented free-state/verified distance bound %q", key)
			}
		}
		if !strings.Contains(strings.Join(r.Limitations, " "), "grab range cannot be reconstructed") {
			t.Fatal("missing transfer sampling limitation")
		}
	}
}

func TestState001TransferStillRequiresKnownContinuousOwnership(t *testing.T) {
	for name, mutate := range map[string]func(*model.PlayerState, *model.PlayerState){
		"missing previous": func(a, b *model.PlayerState) { a.DiscAttachment, a.CurrentDisc.Attachment = nil, nil },
		"unknown previous": func(a, b *model.PlayerState) {
			a.DiscAttachment.State = "unknown"
			a.CurrentDisc.Attachment = a.DiscAttachment.Clone()
		},
		"missing holder": func(a, b *model.PlayerState) {
			a.DiscAttachment.HolderID = ""
			a.CurrentDisc.Attachment = a.DiscAttachment.Clone()
		},
		"conflicting previous": func(a, b *model.PlayerState) { a.CurrentDisc.Attachment.HolderID = "conflicting-player" },
		"conflicting current":  func(a, b *model.PlayerState) { b.CurrentDisc.Attachment.HolderID = "conflicting-player" },
		"duplicate hands": func(a, b *model.PlayerState) {
			a.DiscAttachment.HandCandidates = []string{"left", "left"}
			a.CurrentDisc.Attachment = a.DiscAttachment.Clone()
		},
		"same holder hand switch": func(a, b *model.PlayerState) {
			a.DiscAttachment.HolderID = b.PlayerID
			a.DiscAttachment.HandCandidates = []string{"left"}
			a.CurrentDisc.Attachment = a.DiscAttachment.Clone()
			b.DiscAttachment.HandCandidates = []string{"right"}
			b.CurrentDisc.Attachment = b.DiscAttachment.Clone()
		},
		"source change":  func(a, b *model.PlayerState) { b.Observation.SourceID = "different-source" },
		"session change": func(a, b *model.PlayerState) { b.Observation.SessionID = "different-session" },
		"frame gap":      func(a, b *model.PlayerState) { b.LastFrameIdx = 3; b.Observation.FrameIndex = 3 },
		"time gap":       func(a, b *model.PlayerState) { b.LastTimestamp = .201; b.Observation.Timestamp = .201 },
	} {
		t.Run(name, func(t *testing.T) {
			d, records := grabReviewRecorder()
			a, b := otherHolderFrame(0), grabReviewFrame(1, "held")
			mutate(a, b)
			d.Evaluate(ctx(), players(a), a.LastFrameIdx)
			d.Evaluate(ctx(), players(b), b.LastFrameIdx)
			if len(*records) != 0 {
				t.Fatalf("uncertain or self transfer became an acquisition: %+v", records)
			}
		})
	}
}
