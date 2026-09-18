package throw

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Throw003 reviews a sampled release direction (THROW_003): the first-free
// disc velocity nearly opposite to the throwing hand's world-space motion.
//
// ReleaseAngle is measured in the world frame (hand velocity vs. game-reported
// disc velocity). Body translation contributes to both vectors, and whether
// the disc inherits the carrier's body velocity is not verified on Echo data,
// so a player translating fast with a body-stationary hand can legitimately
// read a large angle. The detector therefore requires the hand to move faster
// than the body and scales confidence by how much of the hand motion cannot
// be explained by body motion. A first free-disc sample that is closer to the
// head than either controller is also skipped: it may contain a headbutt
// between replay samples rather than the original hand-release vector. The
// 177 degree threshold itself is UNVERIFIED. Neither the sampled angle nor a
// controller pose reveals a player's WristAngleOffset setting. Native aim
// assistance is enabled in reference defaults; its applicability and precise
// contribution in a recording are unobserved, not subtracted as a made-up law.
type Throw003 struct {
	detect.BaseDetector
	maxAngleDev   float64
	minHandSpeed  float64
	minThrowSpeed float64
}

func NewThrow003(params map[string]any) *Throw003 {
	return &Throw003{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_003", DetectorVersion: "1.4.1",
			TraceBranches: true,
			DetectorName:  "Release Direction Review", DetectorCategory: "throw",
			Inputs: []string{"throw_event"}, Warmup: 5, Weight: 0.5,
		},
		maxAngleDev:   detect.GetFloat(params, "max_release_angle_deviation", 177.0),
		minHandSpeed:  detect.GetFloat(params, "min_hand_speed", 3.0),
		minThrowSpeed: detect.GetFloat(params, "min_throw_speed", 5.0),
	}
}

func (d *Throw003) Reset() {}
func (d *Throw003) Configure(params map[string]any) error {
	d.maxAngleDev = detect.GetFloat(params, "max_release_angle_deviation", d.maxAngleDev)
	d.minHandSpeed = detect.GetFloat(params, "min_hand_speed", d.minHandSpeed)
	d.minThrowSpeed = detect.GetFloat(params, "min_throw_speed", d.minThrowSpeed)
	return nil
}

func (d *Throw003) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	for _, pid := range sortedPlayerIDs(players) {
		ps := players[pid]
		t := throwAt(ps, frameIdx)
		if t == nil {
			if ps != nil && ps.LastFrameIdx == frameIdx {
				d.TraceDecision(pid, frameIdx, "no_current_release")
			} else {
				d.TraceDecision(pid, frameIdx, "stale_player_context")
			}
			continue
		}
		if ps.LastFrameIdx != frameIdx {
			d.TraceDecision(pid, frameIdx, "stale_player_context")
			continue
		}
		window, sourceStatus, contextReason := releaseAngleContext(ps, t, pid, frameIdx)
		if contextReason != "" {
			d.TraceDecision(pid, frameIdx, contextReason)
			continue
		}
		if (t.ThrowingHand != "left" && t.ThrowingHand != "right") || !t.HandKinematicsValid || (t.HandTracked && t.HandAttributionConfidence == 0) {
			d.TraceDecision(pid, frameIdx, "release_hand_unavailable")
			continue
		}
		if t.HandVelocity.HasNaN() || t.HandVelocity.HasInf() || t.ReleaseVelocity.HasNaN() || t.ReleaseVelocity.HasInf() ||
			t.PlayerVelocity.HasNaN() || t.PlayerVelocity.HasInf() || t.HandRelativeVelocity.HasNaN() || t.HandRelativeVelocity.HasInf() ||
			math.IsNaN(t.HandSpeed) || math.IsInf(t.HandSpeed, 0) || math.IsNaN(t.ReleaseSpeed) || math.IsInf(t.ReleaseSpeed, 0) ||
			math.IsNaN(t.HandRelativeSpeed) || math.IsInf(t.HandRelativeSpeed, 0) || math.IsInf(t.ReleaseAngle, 0) {
			d.TraceDecision(pid, frameIdx, "release_hand_unavailable")
			continue
		}
		if t.PossibleHeadContact {
			d.TraceDecision(pid, frameIdx, "possible_head_contact")
			continue
		}
		if t.HandSpeed < d.minHandSpeed || t.ReleaseSpeed < d.minThrowSpeed {
			d.TraceDecision(pid, frameIdx, "release_motion_below_gate")
			continue
		}
		// These fields are cached measurements, not independent evidence.
		// Reject contradictions instead of allowing a stale scalar angle or
		// speed to manufacture an otherwise unsupported candidate. The small
		// tolerance only covers floating-point round trips, not game physics.
		if !releaseAngleMeasurementsConsistent(t) {
			d.TraceDecision(pid, frameIdx, "release_measurement_inconsistent")
			continue
		}
		if math.IsNaN(t.ReleaseAngle) || t.ReleaseAngle <= d.maxAngleDev {
			d.TraceDecision(pid, frameIdx, "release_angle_not_exceeded")
			continue
		}
		// Frame-of-reference guard: if the body moved as fast as the hand,
		// the world-frame hand velocity may be pure body translation and the
		// angle carries no information about the wrist motion.
		bodySpeed := t.PlayerVelocity.Magnitude()
		if t.HandSpeed <= 0 || bodySpeed >= t.HandSpeed {
			d.TraceDecision(pid, frameIdx, "body_translation_dominates")
			continue
		}
		bodyFactor := 1.0 - bodySpeed/t.HandSpeed
		d.TraceDecision(pid, frameIdx, "release_angle_candidate")

		severity := model.SigmoidConfidence(t.ReleaseAngle, d.maxAngleDev, 0.1)
		confidence := severity * bodyFactor
		if t.Attribution.Confidence > 0 {
			confidence *= t.Attribution.Confidence
		}
		if t.HandAttributionConfidence > 0 {
			confidence *= t.HandAttributionConfidence
		}
		// This comparison uses sampled world-frame velocities, not controller
		// forward or a WristAngleOffset setting. Missing orientation therefore
		// does not invalidate the velocity angle, but must remain explicit in
		// the optional pose evidence (including legacy identity fallbacks).
		wristOrientation, wristOrientationValid := model.ObservedHandRotation(t.WristOrientation, &t.WristOrientationValid)
		ev := d.MakeEvent(matchCtx, pid, t.FrameIndex, t.Timestamp, severity, confidence,
			model.ReleaseAngleEvidence{
				Behavior:     model.BehaviorReleaseDirection,
				Measurement:  "world_hand_velocity_vs_first_free_disc_velocity",
				ReleaseFrame: t.FrameIndex, ObservedFrame: frameIdx,
				ReleaseWindow: window, SourceStatus: sourceStatus,
				PlayerVelocity: t.PlayerVelocity, BodyTranslationFactor: bodyFactor,
				HandTracked:               t.HandTracked,
				ContactStatus:             "head_contact_not_indicated_other_contact_unresolved",
				NativeAssistanceReference: model.DefaultGameRuleReference(),
				NativeAssistanceStatus:    "enabled_in_reference_defaults_recording_setting_unobserved",
				Limitations: []string{
					"This angle compares sampled world-space velocities; it does not measure WristAngleOffset or identify a macro or illegal setting.",
					"Native aim assistance is enabled in the reference defaults. Its activation, exact transformation and match configuration are not established by these samples.",
					"A first-free disc sample can follow an unobserved slap, headbutt, bounce or other contact; passing conservative contact guards does not establish a contact-free release.",
					"Body translation and hand selection are retained as context; their adjustments do not reconstruct the exact release velocity or engine inheritance rules.",
					"The configured angle threshold and confidence are engineering heuristics, not independently calibrated probabilities of cheating.",
				},
				ReleaseAngle: t.ReleaseAngle, HandVelocity: t.HandVelocity,
				HandRelativeVelocity: t.HandRelativeVelocity,
				DiscVelocity:         t.ReleaseVelocity, HandSpeed: t.HandSpeed,
				HandRelativeSpeed: t.HandRelativeSpeed,
				DiscSpeed:         t.ReleaseSpeed, WristOrientation: wristOrientation,
				WristOrientationValid:     wristOrientationValid,
				HandKinematicsValid:       t.HandKinematicsValid,
				ThrowingHand:              t.ThrowingHand,
				HandAttributionConfidence: t.HandAttributionConfidence,
				HandAttributionAnchor:     t.HandAttributionAnchor,
			},
			fmt.Sprintf("sampled hand/disc angle: %.1f deg (body %.1f m/s, hand %.1f m/s)", t.ReleaseAngle, bodySpeed, t.HandSpeed),
			fmt.Sprintf("configured review threshold: > %.1f deg; not a wrist-setting limit", d.maxAngleDev),
			model.CausalKey{PlayerID: pid, FrameStart: t.FrameIndex - 3, FrameEnd: frameIdx, AnomalyType: "release_angle"},
		)
		attribution := t.Attribution
		ev.Attribution = &attribution
		events = append(events, ev)
	}
	return events
}

// releaseAngleContext refuses contradictory supplied identity/timing/source
// metadata. Legacy events without a window remain reviewable with an explicit
// unavailable source label; absence is never upgraded into source continuity.
// SourceID may contain local paths or URL credentials and is not persisted.
func releaseAngleContext(ps *model.PlayerState, t *model.ThrowEvent, pid string, frame int) (*model.ReleaseObservation, string, string) {
	if t.ThrowerID != pid || ps.PlayerID != pid || t.Attribution.PlayerID != pid ||
		t.FrameIndex < 0 || t.FrameIndex > frame || frame-t.FrameIndex > 1 ||
		!mechanicsFinite(t.Timestamp) || t.Timestamp < 0 {
		return nil, "", "release_angle_context_invalid"
	}
	// Validate supplied confirmation metadata even when the release's source
	// is absent. Missing one side cannot make a contradictory other side valid.
	if ps.Observation != nil && (!ps.Observation.Valid() || ps.Observation.FrameIndex != frame || ps.Observation.Timestamp != ps.LastTimestamp ||
		ps.Observation.Timestamp < t.Timestamp || (frame == t.FrameIndex && ps.Observation.Timestamp != t.Timestamp) ||
		(frame > t.FrameIndex && ps.Observation.Timestamp == t.Timestamp)) {
		return nil, "", "release_angle_context_invalid"
	}
	w := t.ReleaseWindow
	if w == nil {
		return nil, "release_window_unavailable", ""
	}
	if w.PlayerID != pid || w.FirstFreeFrame != t.FrameIndex || w.StartFrame < 0 ||
		w.EndFrame != t.FrameIndex || w.StartFrame+1 != w.EndFrame ||
		w.EndTime != t.Timestamp || !mechanicsFinite(w.StartTime) || !mechanicsFinite(w.EndTime) ||
		w.StartTime < 0 || w.StartTime >= w.EndTime || len(w.PlayerMovement) > model.MaxMechanicsRawSamples || len(w.HandCandidates) > 2 {
		return nil, "", "release_angle_context_invalid"
	}
	for _, m := range w.PlayerMovement {
		if m.FrameIndex < w.StartFrame || m.FrameIndex > w.EndFrame || !mechanicsFinite(m.Timestamp) || m.Timestamp < w.StartTime || m.Timestamp > w.EndTime ||
			m.Position.HasNaN() || m.Position.HasInf() || (m.ReportedVelocity != nil && (m.ReportedVelocity.HasNaN() || m.ReportedVelocity.HasInf())) {
			return nil, "", "release_angle_context_invalid"
		}
	}
	for i, hand := range w.HandCandidates {
		if (hand != "left" && hand != "right") || (i > 0 && hand == w.HandCandidates[0]) {
			return nil, "", "release_angle_context_invalid"
		}
	}
	status := "release_source_unavailable"
	if w.Source != nil {
		if !w.Source.Valid() || w.Source.FrameIndex != t.FrameIndex || w.Source.Timestamp != t.Timestamp {
			return nil, "", "release_angle_context_invalid"
		}
		status = "release_source_recorded_confirmation_source_unavailable"
		if ps.Observation != nil {
			if !w.Source.SameSource(ps.Observation) {
				return nil, "", "release_angle_source_changed"
			}
			status = "same_source_sampled_transition_not_authoritative"
		}
	}
	out := w.Clone()
	if out.Source != nil {
		out.Source.SourceID = ""
	}
	return out, status, ""
}

func releaseAngleMeasurementsConsistent(t *model.ThrowEvent) bool {
	if !mechanicsFinite(t.Attribution.Confidence) || t.Attribution.Confidence <= 0 || t.Attribution.Confidence > 1 ||
		!mechanicsFinite(t.HandAttributionConfidence) || t.HandAttributionConfidence < 0 || t.HandAttributionConfidence > 1 ||
		!mechanicsFinite(t.ReleaseAngle) || t.ReleaseAngle < 0 || t.ReleaseAngle > 180 {
		return false
	}
	handSpeed, discSpeed := t.HandVelocity.Magnitude(), t.ReleaseVelocity.Magnitude()
	if !mechanicsFinite(handSpeed) || !mechanicsFinite(discSpeed) || handSpeed <= 0 || discSpeed <= 0 ||
		!measurementClose(t.HandSpeed, handSpeed) || !measurementClose(t.HandRelativeSpeed, t.HandRelativeVelocity.Magnitude()) {
		return false
	}
	return measurementClose(t.ReleaseAngle, t.HandVelocity.AngleBetweenDeg(t.ReleaseVelocity))
}

func measurementClose(reported, derived float64) bool {
	return mechanicsFinite(reported) && mechanicsFinite(derived) &&
		math.Abs(reported-derived) <= 1e-8*math.Max(1, math.Max(math.Abs(reported), math.Abs(derived)))
}
