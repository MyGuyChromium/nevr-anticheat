package state

import (
	"math"
	"reflect"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Tolerates binary timestamp rounding, not a shorter observation window.
const catchTimeEpsilon = 1e-9

// catchFitReference estimates a constant free-flight reference from the
// trapezoidal time integral of reported velocity. Every position sample must
// agree with that reference; endpoint agreement alone can hide an excursion.
// This is an empirical sampling filter, not an assertion of game physics.
func catchFitReference(samples []catchSample, maxError float64) (model.Vec3, float64, string) {
	if len(samples) < 2 {
		return model.Vec3{}, 0, "catch_baseline_low_information"
	}
	first, last := samples[0], samples[len(samples)-1]
	duration := last.timestamp - first.timestamp
	if duration <= 0 {
		return model.Vec3{}, 0, "catch_baseline_low_information"
	}
	var integral model.Vec3
	for i := 1; i < len(samples); i++ {
		dt := samples[i].timestamp - samples[i-1].timestamp
		if dt <= 0 {
			return model.Vec3{}, 0, "catch_baseline_low_information"
		}
		integral = integral.Add(samples[i-1].velocity.Add(samples[i].velocity).Scale(dt / 2))
	}
	velocity := integral.Scale(1 / duration)
	// At least two fit-tolerance widths of displacement are needed before a
	// direction reference is informative. More high-rate samples cannot turn a
	// nearly stationary/noisy interval into reliable directional evidence.
	if velocity.Magnitude() < 2 || integral.Magnitude() < 2*maxError {
		return model.Vec3{}, 0, "catch_baseline_low_information"
	}
	maxResidual := 0.0
	for _, s := range samples {
		residual := s.position.Sub(first.position).Sub(velocity.Scale(s.timestamp - first.timestamp)).Magnitude()
		maxResidual = math.Max(maxResidual, residual)
		if residual > maxError || velocity.AngleBetweenDeg(s.velocity) > 3 {
			return velocity, maxResidual, "catch_baseline_unstable"
		}
	}
	return velocity, maxResidual, ""
}

// catchReadPossession is intentionally smaller than the event input contract.
// Missing motion/head tracking may still leave an explicit free-to-held
// transition observable. No disc kinematics from this path reaches detection.
func catchReadPossession(players map[string]*model.PlayerState, frame int) (catchSample, string) {
	s := catchSample{frame: frame}
	var reference *model.DiscState
	holderPresent := false
	seen := make(map[string]bool)
	for _, ps := range detect.SortedPlayers(players) {
		if ps.LastFrameIdx != frame {
			continue
		}
		dc := ps.CurrentDisc
		if ps.PlayerID == "" || seen[ps.PlayerID] || dc == nil ||
			dc.SampledPlayerCount < 1 || dc.SampledPlayerCount > catchPlayerLimit || len(s.poses) == catchPlayerLimit ||
			math.IsNaN(ps.LastTimestamp) || math.IsInf(ps.LastTimestamp, 0) || ps.LastTimestamp < 0 {
			return s, "catch_input_unavailable"
		}
		seen[ps.PlayerID] = true
		// The legacy possession booleans cannot upgrade unknown attachments.
		// HasDisc is deliberately not used: it may retain sticky ownership.
		if !dc.Attachment.Known() || !ps.DiscAttachment.Known() {
			return s, "catch_attachment_unknown"
		}
		if dc.PossessionConflict || !reflect.DeepEqual(dc.Attachment, ps.DiscAttachment) ||
			dc.Attachment.Free() != !dc.IsHeld || dc.Attachment.HolderID != dc.PossessorID {
			return s, "catch_possession_conflict"
		}
		if !ps.Observation.Valid() || ps.Observation.FrameIndex != frame || ps.Observation.Timestamp != ps.LastTimestamp {
			return s, "catch_source_unavailable"
		}
		if reference == nil {
			reference, s.timestamp = dc, ps.LastTimestamp
			s.holder, s.source = dc.Attachment.HolderID, ps.Observation.Clone()
		} else if ps.LastTimestamp != s.timestamp || dc.IsHeld != reference.IsHeld ||
			dc.PossessorID != reference.PossessorID || dc.SampledPlayerCount != reference.SampledPlayerCount ||
			!reflect.DeepEqual(dc.Attachment, reference.Attachment) || !s.source.SameSource(ps.Observation) {
			return s, "catch_inconsistent_snapshot"
		}
		if ps.PlayerID == s.holder {
			holderPresent = true
		}
		s.poses = append(s.poses, catchPose{id: ps.PlayerID})
	}
	if reference == nil || len(s.poses) != reference.SampledPlayerCount || (s.holder != "" && !holderPresent) {
		return s, "catch_roster_incomplete"
	}
	return s, ""
}

// observeCatchPossession finalizes exactly once, only after explicit sampled
// identity continuity or a documented interruption. Its separate state lets
// insufficient-data catches remain visible without weakening any event guard.
func (d *State008) observeCatchPossession(players map[string]*model.PlayerState, frame int, unavailable string) {
	now, identityReason := catchReadPossession(players, frame)
	if identityReason != "" {
		d.finishCatchReview(model.CatchReviewInsufficientData, identityReason, false)
		d.diagnosticPrevious = nil
		return
	}
	previous := d.diagnosticPrevious
	d.diagnosticPrevious = &now
	if previous == nil {
		return
	}
	if !previous.source.SameSource(now.source) {
		d.finishCatchReview(model.CatchReviewInsufficientData, "catch_source_changed", false)
		return
	}
	dt := now.timestamp - previous.timestamp
	if now.frame != previous.frame+1 || dt <= 0 || dt > d.maxSampleGap || !catchSameRoster(*previous, now) {
		d.finishCatchReview(model.CatchReviewInsufficientData, "catch_sample_gap", false)
		return
	}
	if d.pendingReview != nil {
		if now.holder == d.pendingReviewHolder && previous.holder == d.pendingReviewHolder {
			if unavailable != "" {
				d.finishCatchReview(model.CatchReviewInsufficientData, unavailable, true)
			} else {
				d.finishCatchReview("", "", true)
			}
		} else {
			d.finishCatchReview(model.CatchReviewUnconfirmed, "catch_possession_unconfirmed", false)
		}
	}
	if previous.holder == "" && now.holder != "" {
		reason := unavailable
		if reason == "" {
			reason = "catch_baseline_pending"
		}
		d.pendingReview = &model.CatchReviewRecord{FrameIndex: now.frame, Timestamp: now.timestamp,
			StartFrame: previous.frame, LastFreeFrame: previous.frame,
			Outcome: model.CatchReviewInsufficientData, Reason: reason, Metrics: map[string]float64{}}
		d.pendingReviewHolder = now.holder
	}
}

func catchSecantTurnRate(previousStep, step model.Vec3, previousDT, dt float64) float64 {
	if previousDT <= 0 || dt <= 0 || previousStep.IsZero() || step.IsZero() {
		return 0
	}
	return previousStep.AngleBetweenDeg(step) / ((previousDT + dt) / 2)
}
