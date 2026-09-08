package pipeline

import (
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/mechanics"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func releaseAssessment(r *model.ReleaseObservation, rules model.ProjectRules) model.MechanicsAssessment {
	out := model.MechanicsAssessment{Result: model.MechanicsInconclusive, RuleVersion: rules.Version, Object: "disc", CoordinateSpace: "arena_world"}
	if r == nil {
		return out
	}
	out.FrameIndex, out.Timestamp = r.FirstFreeFrame, r.EndTime
	out.IntervalStart, out.IntervalEnd = r.StartTime, r.EndTime
	out.Hand = strings.Join(r.HandCandidates, ",")
	out.PlayerID = r.PlayerID
	if r.Source != nil {
		out.Source, out.Authority = r.Source.Source, r.Source.Authority
		out.SessionID, out.TimeBasis = r.Source.SessionID, r.Source.TimeBasis
	}
	// All mechanics from this release share one event identity. They must not
	// be counted as separate cheating incidents merely for having different kinds.
	out.EventID = r.EventID()
	for _, m := range r.PlayerMovement {
		if len(out.RawSamples) >= model.MaxMechanicsRawSamples-1 {
			break
		}
		position := m.Position
		sample := model.MechanicsRawSample{FrameIndex: m.FrameIndex, Timestamp: m.Timestamp, PlayerPosition: &position}
		if m.ReportedVelocity != nil {
			v := *m.ReportedVelocity
			sample.ReportedVelocity = &v
		}
		out.RawSamples = append(out.RawSamples, sample)
	}
	return out
}

func (p *Pipeline) reviewRelease(ps *model.PlayerState, frame int) {
	if ps == nil {
		return
	}
	t := ps.LastThrow
	if p.decisionCoverage == nil || t == nil || !t.ObservedAt(frame) || t.ReleaseWindow == nil {
		return
	}
	if t.ThrowerID != ps.PlayerID || t.ReleaseWindow.PlayerID != ps.PlayerID {
		return
	}
	base := releaseAssessment(t.ReleaseWindow, p.cfg.ProjectRules)
	base.Metrics = map[string]float64{"sampled_disc_speed_mps": t.SampledDiscSpeed, "pose_interval_movement_mps": t.PlayerVelocity.Magnitude()}
	position, velocity := t.ReleasePosition, t.ReleaseVelocity
	base.RawSamples = append(base.RawSamples, model.MechanicsRawSample{FrameIndex: t.FrameIndex, Timestamp: t.Timestamp, DiscPosition: &position, DiscVelocity: &velocity, Attachment: "free"})
	base.Limitations = []string{
		"The 19 m/s–4.7 m/s implication is an owner rule, not a verified engine/build law.",
		"First-free disc speed and interval player motion do not bound simultaneous speeds at the actual release tick.",
		"Capture time, engine build, authoritative controller inputs and measurement-error bounds are unavailable.",
	}
	physics := mechanics.EvaluateRelease(mechanics.ReleaseInput{Rules: p.cfg.ProjectRules, Assessment: base})
	p.decisionCoverage.mechanicsRecord("THROW_001", ps.PlayerID, physics)
	settings := base.Clone()
	settings.Metrics = nil
	settings.Limitations = []string{"Physical hand orientation is not the WristAngleOffset setting.", "No event-aligned player setting, verified permitted range or build is available. Nonzero settings alone are not cheating."}
	settings = mechanics.EvaluateWristSetting(settings, nil, mechanics.SettingsKnowledge{})
	p.decisionCoverage.mechanicsRecord("THROW_003", ps.PlayerID, settings)
}

func (p *Pipeline) reviewCancelledRelease(playerID string, r model.ReleaseObservation, reason string) {
	if p.decisionCoverage == nil {
		return
	}
	if playerID == "" || r.PlayerID != playerID {
		return
	}
	record := releaseAssessment(&r, p.cfg.ProjectRules)
	record.Kind, record.Reason = model.MechanicsThrowPhysics, reason
	record.Limitations = []string{"The sampled held-to-free transition was not confirmed; no throw event or physics violation was inferred."}
	p.decisionCoverage.mechanicsRecord("THROW_001", playerID, record)
}
