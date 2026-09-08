package mechanics

import "github.com/nevr-anticheat/nevr-anticheat/internal/model"

// ReleaseKnowledge has no production verified constructor. An audited engine
// model, source and simultaneous release bounds are required before the owner
// rule can prove a contradiction. A wire field saying "authoritative" is not
// this capability. Same-package tests exercise hypothetical verified inputs.
type ReleaseKnowledge struct {
	ruleVersion, engineBuild, validationReference string
	sourceValidated, simultaneousBoundsValidated  bool
}

type SpeedBounds struct{ Lower, Upper float64 }

type ReleaseInput struct {
	Rules                                  model.ProjectRules
	Assessment                             model.MechanicsAssessment
	InitialDiscSpeed, ReleaseMovementSpeed *SpeedBounds
	Knowledge                              ReleaseKnowledge
}

func validSpeedBounds(b *SpeedBounds) bool {
	return b != nil && finite(b.Lower) && finite(b.Upper) && b.Lower >= 0 && b.Upper >= b.Lower
}

// EvaluateRelease compares simultaneous bounded speeds, never a later player
// sample or goal-entry speed. It is the single implication >=19 => >=4.7,
// not a scalar wrist/arm/player sum or replacement for the separate 18.9 cap.
func EvaluateRelease(in ReleaseInput) model.MechanicsAssessment {
	out := in.Assessment.Clone()
	out.VerifiedEngineBuild, out.ValidationReference, out.GeometryDefinition = "", "", ""
	out.Kind, out.Result, out.Reason = model.MechanicsThrowPhysics, model.MechanicsInconclusive, "release_rule_unverified"
	out.RuleVersion, out.Object = in.Rules.Version, "disc"
	if out.Metrics == nil {
		out.Metrics = make(map[string]float64)
	}
	if !finite(in.Rules.FastThrowSpeedMPS) || !finite(in.Rules.RequiredMovementMPS) || in.Rules.FastThrowSpeedMPS <= 0 || in.Rules.RequiredMovementMPS <= 0 || in.Rules.Version == "" {
		out.Reason = "release_rule_unavailable"
		return out
	}
	out.Metrics["fast_throw_rule_mps"] = in.Rules.FastThrowSpeedMPS
	out.Metrics["required_movement_rule_mps"] = in.Rules.RequiredMovementMPS
	k := in.Knowledge
	if k.ruleVersion != in.Rules.Version || k.engineBuild == "" || k.validationReference == "" {
		return out
	}
	out.VerifiedEngineBuild, out.ValidationReference = k.engineBuild, k.validationReference
	if !k.sourceValidated || !k.simultaneousBoundsValidated || !validSpeedBounds(in.InitialDiscSpeed) || !validSpeedBounds(in.ReleaseMovementSpeed) {
		out.Reason = "release_simultaneous_bounds_unavailable"
		return out
	}
	disc, movement := in.InitialDiscSpeed, in.ReleaseMovementSpeed
	out.Metrics["initial_disc_speed_lower_mps"], out.Metrics["initial_disc_speed_upper_mps"] = disc.Lower, disc.Upper
	out.Metrics["release_movement_lower_mps"], out.Metrics["release_movement_upper_mps"] = movement.Lower, movement.Upper
	switch {
	case disc.Upper < in.Rules.FastThrowSpeedMPS || movement.Lower >= in.Rules.RequiredMovementMPS:
		out.Result, out.Reason = model.MechanicsConsistent, "release_requirement_consistent"
	case disc.Lower >= in.Rules.FastThrowSpeedMPS && movement.Upper < in.Rules.RequiredMovementMPS:
		out.Result, out.Reason = model.MechanicsValidatedViolation, "release_movement_requirement_violated"
	default:
		out.Reason = "release_rule_boundary_uncertain"
	}
	return out
}

type SettingsKnowledge struct {
	ruleVersion, engineBuild, validationReference string
	sourceValidated                               bool
	minimum, maximum                              float64
}

// EvaluateWristSetting is intentionally not derived from physical orientation
// or throwing accuracy. No supported producer supplies an event-aligned offset
// plus a verified allowed range; the production call passes nil/zero knowledge.
func EvaluateWristSetting(base model.MechanicsAssessment, offset *float64, k SettingsKnowledge) model.MechanicsAssessment {
	out := base.Clone()
	out.VerifiedEngineBuild, out.ValidationReference, out.GeometryDefinition = "", "", ""
	out.Kind, out.Result, out.Reason = model.MechanicsSettingsIntegrity, model.MechanicsInconclusive, "wrist_setting_unavailable"
	if offset == nil || !finite(*offset) {
		return out
	}
	if out.Metrics == nil {
		out.Metrics = make(map[string]float64)
	}
	out.Metrics["observed_wrist_offset"] = *offset
	if k.ruleVersion != out.RuleVersion || k.engineBuild == "" || k.validationReference == "" || !k.sourceValidated || !finite(k.minimum) || !finite(k.maximum) || k.minimum > k.maximum {
		out.Reason = "wrist_setting_range_unverified"
		return out
	}
	out.Metrics["allowed_offset_min"], out.Metrics["allowed_offset_max"] = k.minimum, k.maximum
	out.VerifiedEngineBuild, out.ValidationReference = k.engineBuild, k.validationReference
	if *offset >= k.minimum && *offset <= k.maximum {
		out.Result, out.Reason = model.MechanicsConsistent, "wrist_setting_permitted"
	} else {
		out.Result, out.Reason = model.MechanicsValidatedViolation, "wrist_setting_outside_verified_range"
	}
	return out
}
