package detect

import (
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"strings"
)

const CapabilityVersion = "evidence-contract-2026-09-08-v1"

// These are missing enforcement observables, not replacement thresholds. All
// current feeds are observational; no game build has been independently attested
// and calibrated for automatic punishment. Config cannot promote this catalog.
var enforcementNeeds = map[string]string{
	"THROW_001": "exact release interval, verified speed cap/reference frame, contact impulses and allowed launch contributions",
	"THROW_002": "attributed release acceleration and authoritative collision/impulse history",
	"THROW_003": "verified hand-to-disc release model, slap/headbutt/contact exclusions; this is not WristAngleOffset",
	"THROW_004": "independent legitimate release distribution; repeated signatures alone do not prove automation",
	"THROW_005": "verified target geometry, aim model and independent labeled shots",
	"THROW_006": "authoritative free-flight forces, contacts, attachment state and integration model",
	"THROW_007": "versioned penalty-field geometry and verified penalty behavior",
	"THROW_008": "verified free-flight model, correct frame of reference and complete contact history",
	"MOV_001":   "state-dependent speed envelope with regrabs, launches, stacking, playspace motion and corrections",
	"MOV_002":   "authoritative teleport/correction/respawn transitions and build-specific movement envelope",
	"MOV_003":   "collision impulses, regrabs, wall slaps and other legal momentum changes",
	"MOV_004":   "explicit boost state, carrying/stacking state and verified state-dependent boost rules",
	"MOV_005":   "trusted boost input edges and actual build cooldown, not inferred speed changes",
	"MOV_006":   "tracked feet/playspace origin and authoritative contacts; sampled poses cannot distinguish all legal leans/lunges",
	"STATE_001": "exact acquisition timing, hand, interaction geometry and verified grab rule",
	"STATE_002": "presence-valid stun edges and verified duration/respawn/immunity transitions",
	"STATE_003": "presence-valid blocking edges and verified shield duration/eligibility rules",
	"STATE_004": "presence-valid immunity edges and verified eligibility at attempted contact",
	"STATE_005": "trusted shield input edges and verified cooldown/transition rules",
	"STATE_006": "authoritative scored-goal events and verified scoring/reset rules",
	"STATE_007": "attributed punch/stun contact, hit geometry, eligibility/blocking/immunity and event-time poses",
	"STATE_008": "trusted grip inputs, complete contact impulses and verified catch/attachment model",
	"BIO_001":   "adequate rotation sampling and independent validation excluding slaps, headbutts and tracking corrections",
	"BIO_002":   "contact/regrab/playspace context, calibrated hand tracking and independently validated movement limits",
	"BIO_003":   "raw unsmoothed tracking and independent legitimate jitter distribution",
	"BIO_004":   "raw unsmoothed orientation and independent legitimate aim distribution",
	"PAT_001":   "trusted grip-input timestamps; sampled release frames cannot establish frame-perfect input automation",
	"PAT_002":   "independent event-level release distribution, hand attribution and actual input events",
	"PAT_003":   "reviewed independent historical incidents and original scoring/config provenance",
	"PAT_004":   "independent underlying event identities; derived pattern evidence must not score twice",
	"PAT_005":   "playspace calibration, actual boundary/feet observations and independently established legal reach",
}

func CapabilityFor(id string) *model.DetectorCapability {
	missing, ok := enforcementNeeds[id]
	if !ok {
		return nil
	}
	var inputs []string
	var rule *model.RuleDefinition
	if d, found := Lookup(id); found {
		inputs = append(inputs, d.New(nil).RequiredInputs()...)
		units := map[string]string{
			"throw":    "positions/distances: m; velocity: m/s; acceleration: m/s²; direction: degrees/radians as named by the parameter; signatures: counts/variance",
			"movement": "position: m; velocity: m/s; acceleration: m/s²; time: seconds; frame/cooldown counts as explicitly named",
			"state":    "distance: m; duration: seconds; sampled frame/event/score counts as explicitly named",
			"bio":      "distance: m; hand speed: m/s; angular rate: rad/s; variance in the squared corresponding quantity",
			"pattern":  "sample/event counts, frame intervals, distances: m; variance/statistical scores are not probabilities",
		}[d.Category]
		rule = &model.RuleDefinition{Version: d.Version + "/" + CapabilityVersion, Meaning: d.Name, Units: units,
			Provenance:    "Current configured project heuristic; no independently verified engine rule is implied by the detector name or numeric precision.",
			Applicability: "Descriptive review only for the recorded config and observed source; unknown game builds/settings are not approved for enforcement.",
			Exceptions:    missing, Tests: []string{"internal/detect/" + d.Category, "internal/pipeline/data_health_test.go", "internal/detect/catalog/capabilities_test.go"}}
		if strings.HasPrefix(id, "MOV_") {
			rule.Tests[0] = "internal/detect/movement"
		}
	}
	return &model.DetectorCapability{
		Rule:    rule,
		Version: CapabilityVersion, DetectorID: id, RequiredInputs: inputs,
		SourceTrust: "Observed or client-reported; authenticated transport alone does not validate gameplay.",
		Timing:      "Strictly advancing per-player frame and source time within one source epoch; no kinematics across tracking gaps; detector-specific minimum window also required.",
		Validity:    "Finite values, explicit field presence where available, normalized tracked rotations, attributed event intervals. Default zero/false is not proof of observation.",
		RuleID:      id, ApplicableBuild: "unverified; no automatic-enforcement build allowlist",
		EnforcementRequires: []string{missing, "independently verified build/ruleset, source integrity, held-out accuracy evaluation and durable reproducible evidence"},
		MissingBehavior:     "inconclusive or unsupported; preserve review diagnostics; automatic enforcement disabled",
		Limitations:         missing,
	}
}

// ReviewInputsAvailable checks only necessary observable inputs. It does not
// establish a valid event opportunity or a satisfied rule. Stateful detectors
// still receive the sample to close tracks and record their own abstentions.
func ReviewInputsAvailable(id string, ps *model.PlayerState, frame int) bool {
	if ps == nil || ps.LastFrameIdx != frame {
		return false
	}
	hands := !ps.LeftHand.IsZero() && !ps.RightHand.IsZero()
	disc := ps.CurrentDisc != nil
	switch id {
	case "THROW_001", "THROW_002", "THROW_003", "THROW_004", "THROW_005", "PAT_001", "PAT_002":
		return ps.LastThrow.ObservedAt(frame)
	case "THROW_006", "THROW_007", "THROW_008":
		return disc
	case "STATE_001", "STATE_008":
		return disc && hands && ps.CurrentDisc.Attachment.Known() && ps.Observation.Valid()
	case "MOV_001", "MOV_002", "MOV_003":
		return ps.FrameDt > 0
	case "MOV_004", "MOV_005":
		return ps.IsBoostingKnown && ps.FrameDt > 0
	case "MOV_006":
		return ps.PlayspaceValid && ps.HasReportedVelocity && ps.PlayspaceTrackedHands > 0
	case "BIO_001":
		return ps.LeftWristAngularRateValid || ps.RightWristAngularRateValid
	case "BIO_002", "BIO_003", "PAT_005":
		return hands && ps.FrameDt > 0
	case "BIO_004":
		return hands && (ps.LeftHandRotationValid || ps.RightHandRotationValid)
	case "STATE_002", "STATE_003", "STATE_004", "STATE_005", "STATE_007":
		// Normalized state booleans currently lack presence-valid event edges.
		return false
	case "STATE_006":
		return false // Current PlayerState does not retain score presence.
	case "PAT_003", "PAT_004":
		// These consume retained historical/events evidence, not frame inputs.
		return false
	}
	return false
}
