// Package mechanics evaluates project rules separately from detector scores.
package mechanics

import (
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// GrabKnowledge is a capability held by an audited rule implementation, not a
// telemetry assertion. Its zero value is unknown. There is deliberately no
// production constructor granting verification: no supported engine build has
// a verified mapping of the project rule to tracking origins/disc centers,
// authoritative acquisition timing and bounded measurement error. Package tests
// construct hypothetical verified knowledge to test the mathematical boundary.
type GrabKnowledge struct {
	ruleVersion, engineBuild, validationReference string
	geometry                                      string
	sourceValidated, boundsValidated              bool
	linearMotionValidated                         bool
}

// GrabSample is simultaneous hand/disc evidence. Only free-disc samples can
// participate in interpolation; a held position may include attachment snap.
type GrabSample struct {
	Timestamp    float64
	HandPosition model.Vec3
	DiscPosition model.Vec3
	Attachment   string
}

// GrabCandidate covers one plausible grabbing hand over the entire acquisition
// interval. UncertaintyM is an explicit combined distance-error bound; it never
// expands the legal rule. Without audited bound/source knowledge it is inert.
type GrabCandidate struct {
	Hand         string
	Samples      []GrabSample
	UncertaintyM float64
}

type GrabInput struct {
	Rules      model.ProjectRules
	Assessment model.MechanicsAssessment
	Candidates []GrabCandidate
	Knowledge  GrabKnowledge
}

// EvaluateGrab keeps the exact inclusive project boundary. "Consistent" means
// a plausible legal acquisition exists under the stated model, not proof that
// the action occurred at that point. A violation requires every plausible hand
// and instant to have a trustworthy lower distance bound strictly above it.
func EvaluateGrab(in GrabInput) model.MechanicsAssessment {
	out := in.Assessment.Clone()
	out.VerifiedEngineBuild, out.ValidationReference, out.GeometryDefinition = "", "", ""
	out.Kind, out.Result, out.Reason = model.MechanicsGrabGeometry, model.MechanicsInconclusive, "grab_geometry_unverified"
	out.RuleVersion, out.Object = in.Rules.Version, "disc"
	if out.Metrics == nil {
		out.Metrics = make(map[string]float64)
	}
	if !finite(in.Rules.DiscGrabLimitM) || in.Rules.DiscGrabLimitM <= 0 || in.Rules.Version == "" {
		out.Reason = "grab_rule_unavailable"
		return out
	}
	out.Metrics["legal_limit_m"] = in.Rules.DiscGrabLimitM
	knowledge := in.Knowledge
	if knowledge.geometry != "hand_origin_to_disc_center" || knowledge.ruleVersion != in.Rules.Version || knowledge.engineBuild == "" || knowledge.validationReference == "" {
		return out
	}
	if !knowledge.sourceValidated {
		out.Reason = "grab_source_unverified"
		return out
	}
	out.VerifiedEngineBuild, out.ValidationReference, out.GeometryDefinition = knowledge.engineBuild, knowledge.validationReference, knowledge.geometry
	if !knowledge.boundsValidated {
		out.Reason = "grab_uncertainty_unbounded"
		return out
	}
	if !finite(out.IntervalStart) || !finite(out.IntervalEnd) || out.IntervalStart < 0 || out.IntervalEnd < out.IntervalStart || len(in.Candidates) == 0 || len(in.Candidates) > 2 {
		out.Reason = "grab_acquisition_unavailable"
		return out
	}
	minimumLower, minimumUpper := math.Inf(1), math.Inf(1)
	seen := map[string]bool{}
	for _, candidate := range in.Candidates {
		if (candidate.Hand != "left" && candidate.Hand != "right") || seen[candidate.Hand] || !finite(candidate.UncertaintyM) || candidate.UncertaintyM < 0 {
			out.Reason = "grab_candidate_unavailable"
			return out
		}
		seen[candidate.Hand] = true
		distance, reason := minimumDistance(candidate.Samples, out.IntervalStart, out.IntervalEnd, knowledge.linearMotionValidated)
		if reason != "" {
			out.Reason = reason
			return out
		}
		lower, upper := math.Max(0, distance-candidate.UncertaintyM), distance+candidate.UncertaintyM
		if !finite(lower) || !finite(upper) {
			out.Reason = "grab_candidate_unavailable"
			return out
		}
		minimumLower = math.Min(minimumLower, lower)
		minimumUpper = math.Min(minimumUpper, upper)
	}
	out.Metrics["minimum_distance_lower_bound_m"] = minimumLower
	out.Metrics["minimum_distance_upper_bound_m"] = minimumUpper
	out.Metrics["candidate_hands"] = float64(len(in.Candidates))
	switch {
	case minimumUpper <= in.Rules.DiscGrabLimitM:
		out.Result, out.Reason = model.MechanicsConsistent, "grab_legal_candidate_supported"
	case minimumLower > in.Rules.DiscGrabLimitM:
		out.Result, out.Reason = model.MechanicsValidatedViolation, "grab_all_candidates_outside_rule"
	default:
		out.Reason = "grab_boundary_uncertain"
	}
	return out
}

func minimumDistance(samples []GrabSample, start, end float64, linearVerified bool) (float64, string) {
	if len(samples) == 0 || len(samples) > model.MaxMechanicsRawSamples {
		return 0, "grab_acquisition_unavailable"
	}
	for i, sample := range samples {
		if sample.Attachment != "free" {
			return 0, "grab_attachment_snap_excluded"
		}
		if !finite(sample.Timestamp) || sample.Timestamp < 0 || sample.HandPosition.HasNaN() || sample.HandPosition.HasInf() || sample.DiscPosition.HasNaN() || sample.DiscPosition.HasInf() || (i > 0 && sample.Timestamp <= samples[i-1].Timestamp) {
			return 0, "grab_candidate_unavailable"
		}
	}
	if samples[0].Timestamp > start || samples[len(samples)-1].Timestamp < end {
		return 0, "grab_acquisition_unavailable"
	}
	if len(samples) == 1 {
		if start != end || start != samples[0].Timestamp {
			return 0, "grab_acquisition_unavailable"
		}
		distance := samples[0].HandPosition.Distance(samples[0].DiscPosition)
		if !finite(distance) {
			return 0, "grab_candidate_unavailable"
		}
		return distance, ""
	}
	if !linearVerified {
		return 0, "grab_interpolation_unverified"
	}
	minimum := math.Inf(1)
	for i := 1; i < len(samples); i++ {
		a, b := samples[i-1], samples[i]
		lo, hi := math.Max(start, a.Timestamp), math.Min(end, b.Timestamp)
		if hi < lo {
			continue
		}
		r0, r1 := a.DiscPosition.Sub(a.HandPosition), b.DiscPosition.Sub(b.HandPosition)
		scale := b.Timestamp - a.Timestamp
		u0, u1 := (lo-a.Timestamp)/scale, (hi-a.Timestamp)/scale
		from, to := r0.Lerp(r1, u0), r0.Lerp(r1, u1)
		delta := to.Sub(from)
		u := 0.0
		if length := delta.MagnitudeSq(); length > 0 {
			u = math.Max(0, math.Min(1, -from.Dot(delta)/length))
		}
		distance := from.Add(delta.Scale(u)).Magnitude()
		if !finite(distance) {
			return 0, "grab_candidate_unavailable"
		}
		minimum = math.Min(minimum, distance)
	}
	if !finite(minimum) {
		return 0, "grab_acquisition_unavailable"
	}
	return minimum, ""
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
