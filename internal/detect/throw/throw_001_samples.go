package throw

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

type releaseSpeedSnapshot struct {
	raw     model.ReleaseSpeedSample
	source  *model.ObservationContext // full source for continuity; never persisted
	reason  string
	hasDisc bool
}

type releaseSpeedTrack struct {
	matchID, player, movementSource string
	releaseFrame                    int
	timestamp, physicsCap           float64
	evidence                        model.ThrowEvidence
	review                          *model.ReleaseSpeedReview
	attribution                     model.ThrowAttribution
	last                            releaseSpeedSnapshot
}

func speedEvidenceSource(source *model.ObservationContext) *model.ObservationContext {
	copy := source.Clone()
	if copy != nil {
		copy.SourceID = ""
	}
	return copy
}

// readFlightReview applies the existing complete-roster, explicit attachment,
// contact-counter and source checks. Retain the available raw sample even when
// those checks fail, but never use that sample to corroborate a speed.
func readReleaseSpeedSnapshot(players []*model.PlayerState, frame int) releaseSpeedSnapshot {
	out := releaseSpeedSnapshot{raw: model.ReleaseSpeedSample{FrameIndex: frame, Attachment: "unknown"}}
	_, reason := readFlightReview(players, frame)
	if reason != "" {
		out.reason = strings.Replace(reason, "trajectory_", "release_speed_", 1)
	}
	for _, ps := range players {
		if ps.CurrentDisc == nil {
			continue
		}
		disc := ps.CurrentDisc
		if disc.Position.HasNaN() || disc.Position.HasInf() || disc.Velocity.HasNaN() || disc.Velocity.HasInf() ||
			!mechanicsFinite(ps.LastTimestamp) || ps.LastTimestamp < 0 {
			continue
		}
		out.hasDisc = true
		out.source = ps.Observation.Clone()
		out.raw.Timestamp, out.raw.Position, out.raw.Velocity = ps.LastTimestamp, disc.Position, disc.Velocity
		out.raw.Speed = disc.Velocity.Magnitude()
		out.raw.Source = speedEvidenceSource(ps.Observation)
		if disc.Attachment.Known() {
			out.raw.Attachment = disc.Attachment.State
		}
		if disc.BounceCount != nil {
			bounce := *disc.BounceCount
			out.raw.BounceCount = &bounce
		}
		break
	}
	if out.reason == "" {
		for _, ps := range players {
			if ps.HasDisc || ps.CurrentDisc.IsHeld || ps.CurrentDisc.PossessionConflict || ps.CurrentDisc.PossessorID != "" {
				out.reason = "release_speed_attachment_conflict"
				break
			}
			if ps.LegalContext.PossibleHeadContact {
				out.reason = "release_speed_contact_possible"
				break
			}
		}
	}
	return out
}

func (tr *releaseSpeedTrack) appendSnapshot(next releaseSpeedSnapshot) string {
	previous := tr.last
	if next.raw.FrameIndex == previous.raw.FrameIndex {
		if reflect.DeepEqual(next, previous) {
			return ""
		}
		return "release_speed_conflicting_duplicate"
	}
	// Keep an available terminal sample too: a bounce, changed source or gap
	// must not disappear behind the cancellation reason. At most three samples.
	if next.hasDisc && len(tr.review.Samples) < releaseSpeedSamples {
		tr.review.Samples = append(tr.review.Samples, next.raw)
	}
	if next.reason != "" {
		return next.reason
	}
	dt := next.raw.Timestamp - previous.raw.Timestamp
	if next.raw.FrameIndex != previous.raw.FrameIndex+1 || !mechanicsFinite(dt) || dt <= 0 || dt > .2 {
		return "release_speed_sample_gap"
	}
	if !previous.source.SameSource(next.source) {
		return "release_speed_source_changed"
	}
	if previous.raw.BounceCount == nil || next.raw.BounceCount == nil {
		return "release_speed_contact_unavailable"
	}
	if *previous.raw.BounceCount != *next.raw.BounceCount {
		return "release_speed_contact_observed"
	}
	// Reuse the existing conservative trajectory contact exclusions. These
	// are sampling filters, not new engine constants or legality thresholds.
	a, b := previous.raw.Speed, next.raw.Speed
	if a > 0 && b > 0 {
		angle := previous.raw.Velocity.AngleBetweenDeg(next.raw.Velocity)
		ratio := b / a
		if !mechanicsFinite(angle) || (ratio < inelasticSpeedRatio && angle > inelasticMinAngle) ||
			angle > elasticBounceAngle || (ratio > deflectionRatio && angle > deflectionMinAngle) {
			return "release_speed_contact_possible"
		}
	}
	tr.last = next
	return ""
}

func (tr *releaseSpeedTrack) event(d *Throw001, reason string, frame int) []model.DetectionEvent {
	r := tr.review
	r.Status = "uncorroborated"
	r.ResolvedFrame = frame
	if r.ResolvedFrame < tr.releaseFrame {
		r.ResolvedFrame = tr.last.raw.FrameIndex
	}
	minimum := math.Inf(1)
	maximum := 0.0
	speeds := make([]float64, 0, len(r.Samples))
	for _, sample := range r.Samples {
		if sample.Speed > tr.evidence.EffectiveCap {
			r.AboveCapSamples++
		}
		minimum = math.Min(minimum, sample.Speed)
		maximum = math.Max(maximum, sample.Speed)
		speeds = append(speeds, sample.Speed)
		if sample.FrameIndex > r.ResolvedFrame {
			r.ResolvedFrame = sample.FrameIndex
		}
	}
	// Median is descriptive only; decisions require every constituent sample
	// above the cap. The original first sample always stays in ReleaseSpeed.
	if len(speeds) == releaseSpeedSamples {
		sort.Float64s(speeds)
		median := speeds[1]
		r.MedianSampledSpeed = &median
	}
	localOver := r.LocalReportStatus == "bound_local_client_report_unverified" && tr.evidence.GameLastThrow.TotalSpeed > tr.evidence.EffectiveCap
	if r.AboveCapSamples == 0 && !localOver {
		d.TraceDecision(tr.player, r.ResolvedFrame, "release_at_or_below_cap")
		return nil
	}
	if reason == "" {
		if len(r.Samples) == releaseSpeedSamples && r.AboveCapSamples == releaseSpeedSamples {
			r.Status, reason = "corroborated", "release_speed_three_samples_above_cap"
		} else if len(r.Samples) == releaseSpeedSamples {
			reason = "release_speed_not_sustained"
		} else {
			reason = "release_speed_insufficient_samples"
		}
	}
	r.Reason = reason
	d.TraceDecision(tr.player, r.ResolvedFrame, reason)
	severity := artifactSeverity
	if r.Status == "corroborated" {
		severity = model.SigmoidConfidence(minimum, tr.evidence.EffectiveCap, d.sigmoidSteepness)
	}
	anomaly := "disc_speed"
	if maximum > tr.physicsCap*artifactCapMultiple {
		d.TraceDecision(tr.player, r.ResolvedFrame, "sampled_speed_artifact_band")
		d.artifactCounts[tr.player]++
		tr.evidence.ArtifactSuspected, tr.evidence.ArtifactCount = true, d.artifactCounts[tr.player]
		severity, anomaly = artifactSeverity, "disc_speed_artifact"
	} else {
		d.TraceDecision(tr.player, r.ResolvedFrame, "release_above_cap")
	}
	confidence := severity
	if tr.attribution.Confidence > 0 {
		confidence *= tr.attribution.Confidence
	}
	movement := fmt.Sprintf("player-relative %.2f; aligned movement %+.2f; %s", tr.evidence.PlayerRelativeSpeed, tr.evidence.AlignedMovementSpeed, tr.movementSource)
	if tr.movementSource == playerVelocityPositionDifference {
		movement = fmt.Sprintf("player-relative unavailable: no engine-reported player velocity; position-difference estimate %.2f, aligned %+.2f", tr.evidence.PlayerRelativeSpeed, tr.evidence.AlignedMovementSpeed)
	}
	observed := fmt.Sprintf("first sampled disc_speed: %.2f m/s; %s (%d/%d sampled speeds above reference; %s); %s",
		tr.evidence.ReleaseSpeed, r.Status, r.AboveCapSamples, len(r.Samples), reason, movement)
	if tr.evidence.GameLastThrow != nil {
		observed += fmt.Sprintf("; separate last_throw total %.2f m/s (%s)", tr.evidence.GameLastThrow.TotalSpeed, r.LocalReportStatus)
	}
	ev := d.MakeEvent(&model.MatchContext{MatchID: tr.matchID}, tr.player, tr.releaseFrame, tr.timestamp, severity, confidence, tr.evidence,
		observed, fmt.Sprintf("configured disc-speed reference %.2f m/s; effective reference %.2f m/s", tr.physicsCap, tr.evidence.EffectiveCap),
		model.CausalKey{PlayerID: tr.player, FrameStart: tr.releaseFrame, FrameEnd: r.ResolvedFrame, AnomalyType: anomaly})
	// Sample corroboration does not grant punishment authority. Uncertainty
	// cannot become a scored finding through an old saved configuration.
	ev.AutoEnforce = false
	if r.Status != "corroborated" || tr.evidence.ArtifactSuspected {
		ev.EnforcementWeight = 0
	}
	attribution := tr.attribution
	ev.Attribution = &attribution
	return []model.DetectionEvent{ev}
}
