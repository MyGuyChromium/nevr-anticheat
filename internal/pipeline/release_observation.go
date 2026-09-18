package pipeline

import (
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

type pendingRelease struct {
	event             *model.ThrowEvent
	observation       *model.ReleaseObservation
	unavailableReason string
}

func (fe *FeatureExtractor) clearSourceHistory(ps *model.PlayerState) {
	delete(fe.discHistory, ps.PlayerID)
	ps.LastThrow = nil
	ps.ThrowHistory = nil
	ps.PositionHistory, ps.VelocityHistory, ps.LeftHandHistory, ps.RightHandHistory, ps.DiscVelocityHistory = nil, nil, nil, nil, nil
	ps.LeftHandRotHistory, ps.RightHandRotHistory = nil, nil
	ps.SpeedHistory, ps.TimestampHistory = nil, nil
}

func (fe *FeatureExtractor) SetReleaseObserver(observer func(string, model.ReleaseObservation, string)) {
	fe.releaseObserver = observer
}

func (fe *FeatureExtractor) rejectRelease(player, reason string) {
	if pending := fe.pendingReleases[player]; pending != nil {
		delete(fe.pendingReleases, player)
		if fe.releaseObserver != nil && pending.observation != nil {
			fe.releaseObserver(player, *pending.observation.Clone(), reason)
		}
	}
}

// DrainPendingReleases reports incomplete sampled releases without making a
// ThrowEvent. It is idempotent and must run on offline EOF/live finalization.
func (fe *FeatureExtractor) DrainPendingReleases(reason string) {
	ids := make([]string, 0, len(fe.pendingReleases))
	for id := range fe.pendingReleases {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fe.rejectRelease(id, reason)
	}
}

func sameObservationSource(a, b *model.ObservationContext) bool {
	if a == nil && b == nil {
		return true
	}
	return a.SameSource(b)
}

func observedAttachment(frame *model.PlayerTelemetryFrame) *model.DiscAttachment {
	if frame.Disc == nil || !frame.Disc.Attachment.Known() {
		return &model.DiscAttachment{State: "unknown", Reason: "attachment_unknown"}
	}
	a := frame.Disc.Attachment
	if frame.Disc.PossessionConflict || a.Free() != !frame.Disc.IsHeld ||
		(a.State == "held" && a.HolderID != frame.Disc.PossessorID) {
		return &model.DiscAttachment{State: "unknown", Reason: "attachment_conflict"}
	}
	return a.Clone()
}

func movementObservation(frame int, timestamp float64, position model.Vec3, velocity *model.Vec3) model.MovementObservation {
	s := model.MovementObservation{FrameIndex: frame, Timestamp: timestamp, Position: position}
	if velocity != nil {
		v := *velocity
		s.ReportedVelocity = &v
	}
	return s
}

func (fe *FeatureExtractor) confirmRelease(ps *model.PlayerState, frame *model.PlayerTelemetryFrame, continuous bool, phaseActive bool) {
	pending := fe.pendingReleases[ps.PlayerID]
	if pending == nil {
		return
	}
	reason := ""
	switch {
	case !continuous:
		reason = "release_observation_gap"
	case !phaseActive:
		reason = "release_inactive_phase"
	case !ps.DiscAttachment.Known():
		reason = "release_attachment_unknown"
	case ps.DiscAttachment.HeldBy(ps.PlayerID):
		// Held again by the same player one sample after going free: a regrab,
		// hand swap or attachment flicker with no sampled flight, not a throw.
		reason = "release_same_holder_regrab"
	case !ps.DiscAttachment.Free():
		// Another player holds the disc one sample after it left this hand. A
		// point-blank pass, a steal and a knock-out look identical here, so the
		// first-free velocity is not attributed to this player as a throw.
		reason = "release_transfer_within_one_sample"
	case frame.IsStunned:
		// The stun flag can lag the knock-out by one sample; a holder who is
		// stunned by the confirming sample did not author this release.
		reason = releaseForcedDropReason
	}
	if reason != "" {
		fe.rejectRelease(ps.PlayerID, reason)
		return
	}
	if pending.event == nil {
		fe.rejectRelease(ps.PlayerID, pending.unavailableReason)
		return
	}
	delete(fe.pendingReleases, ps.PlayerID)
	// Capture was completed at the original first-free frame. Only publication
	// identity changes here; subsequent movement cannot replace release data.
	event := *pending.event
	observed := frame.FrameIndex
	event.ObservedFrameIndex = &observed
	event.ReleaseWindow = pending.event.ReleaseWindow.Clone()
	ps.LastThrow = &event
	ps.ThrowHistory = append(ps.ThrowHistory, event)
	// This in-memory history is a recent context window, not the canonical
	// match record. Total count and every LastThrow publication remain intact;
	// detections/evidence are stored independently by the pipeline.
	if len(ps.ThrowHistory) > fe.historyWindow {
		copy(ps.ThrowHistory, ps.ThrowHistory[len(ps.ThrowHistory)-fe.historyWindow:])
		clear(ps.ThrowHistory[fe.historyWindow:])
		ps.ThrowHistory = ps.ThrowHistory[:fe.historyWindow]
	}
	ps.ThrowCount++
}
