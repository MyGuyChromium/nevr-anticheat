package throw

import (
	"math"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// releaseMechanicsAssessment describes sampled release evidence without
// granting authority to source metadata or calling the first free sample the
// exact launch tick. Related checks share the same canonical release ID.
func releaseMechanicsAssessment(mc *model.MatchContext, ps *model.PlayerState, t *model.ThrowEvent, kind string) model.MechanicsAssessment {
	rules, matchID, source := model.DefaultProjectRules(), "", "unknown"
	if mc != nil {
		matchID, source = mc.MatchID, mc.Source
		if mc.ProjectRules.Version != "" {
			rules = mc.ProjectRules
		}
	}
	r := model.MechanicsAssessment{Kind: kind, Result: model.MechanicsInconclusive, Reason: "release_observation_unavailable",
		RuleVersion: rules.Version, PlayerID: ps.PlayerID, SessionID: matchID, FrameIndex: t.FrameIndex, Timestamp: t.Timestamp,
		IntervalStart: t.Timestamp, IntervalEnd: t.Timestamp, Source: source, Authority: "unverified", CoordinateSpace: "reported_world", Object: "disc", Hand: t.ThrowingHand,
		Metrics: map[string]float64{},
	}
	observation := ps.Observation
	if window := t.ReleaseWindow; window != nil {
		r.IntervalStart, r.IntervalEnd = window.StartTime, window.EndTime
		r.Hand = strings.Join(window.HandCandidates, "|")
		observation = window.Source
		for _, movement := range window.PlayerMovement {
			if len(r.RawSamples) >= model.MaxMechanicsRawSamples-1 {
				break
			}
			r.RawSamples = append(r.RawSamples, model.MechanicsRawSample{FrameIndex: movement.FrameIndex, Timestamp: movement.Timestamp,
				PlayerPosition: mechanicsVector(movement.Position), ReportedVelocity: mechanicsVectorPointer(movement.ReportedVelocity)})
		}
		if omitted := len(window.PlayerMovement) - len(r.RawSamples); omitted > 0 {
			r.Metrics["context_raw_samples_omitted"] = float64(omitted)
		}
	}
	if observation != nil {
		r.Source, r.Authority, r.SessionID, r.TimeBasis = observation.Source, observation.Authority, observation.SessionID, observation.TimeBasis
	}
	r.RawSamples = append(r.RawSamples, model.MechanicsRawSample{FrameIndex: t.FrameIndex, Timestamp: t.Timestamp,
		DiscPosition: mechanicsVector(t.ReleasePosition), DiscVelocity: mechanicsVector(t.ReleaseVelocity), PlayerPosition: mechanicsVector(t.PlayerPosition),
		PlayerVelocity: mechanicsVector(t.PlayerVelocity), Attachment: "free"})
	r.EventID = t.ReleaseWindow.EventID()
	return r
}

func mechanicsVector(value model.Vec3) *model.Vec3 {
	if value.HasNaN() || value.HasInf() {
		return nil
	}
	copy := value
	return &copy
}
func mechanicsVectorPointer(value *model.Vec3) *model.Vec3 {
	if value == nil {
		return nil
	}
	return mechanicsVector(*value)
}
func mechanicsFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func mechanicsReleaseKnown(ps *model.PlayerState, t *model.ThrowEvent) bool {
	w := t.ReleaseWindow
	return w != nil && w.PlayerID == ps.PlayerID && t.ThrowerID == ps.PlayerID && w.FirstFreeFrame == t.FrameIndex &&
		w.StartFrame < w.EndFrame && w.EndFrame == t.FrameIndex && w.EndTime == t.Timestamp && w.StartTime >= 0 && w.StartTime < w.EndTime &&
		mechanicsFinite(w.StartTime) && mechanicsFinite(w.EndTime) && w.Source.Valid() && w.Source.FrameIndex == t.FrameIndex && w.Source.Timestamp == t.Timestamp
}
