package model

// BehaviorDescriptor names the observation a detector can actually describe.
// It is presentation metadata, not a verdict, an enforcement policy, or a
// promise to identify the software or input setting responsible for a signal.
type BehaviorDescriptor struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	Observes         string `json:"observes"`
	DoesNotEstablish string `json:"does_not_establish"`
}

const (
	BehaviorReleaseDirection = "release_direction_observation"
	BehaviorShotTargeting    = "shot_targeting_pattern_observation"
	BehaviorCatchPath        = "receiver_associated_catch_path_observation"
	BehaviorFreeFlight       = "free_flight_path_observation"
)

// DetectorBehavior keeps release-direction/targeting reviews separate from
// receiver-associated catch-path reviews. "Autopocket" is not a measurement:
// it can mean different alleged behaviors and must not merge those checks.
// Unknown IDs deliberately have no default classification.
func DetectorBehavior(detectorID string) (BehaviorDescriptor, bool) {
	switch detectorID {
	case "THROW_003":
		return BehaviorDescriptor{
			ID: BehaviorReleaseDirection, Label: "Release direction review",
			Observes:         "Angle between sampled world-space hand motion and first-free disc velocity, with movement and attribution context.",
			DoesNotEstablish: "Does not measure WristAngleOffset, identify a macro, reconstruct the exact release tick, or establish an illegal setting.",
		}, true
	case "THROW_005":
		return BehaviorDescriptor{
			ID: BehaviorShotTargeting, Label: "Shot targeting review",
			Observes:         "Descriptive direction-to-reported-goal and speed patterns across sampled releases.",
			DoesNotEstablish: "Does not establish targeting assistance, verified pocket geometry, made-goal accuracy, or a calibrated human-variance limit.",
		}, true
	case "STATE_008":
		return BehaviorDescriptor{
			ID: BehaviorCatchPath, Label: "Receiver-associated catch-path review",
			Observes:         "Sampled free-disc path changes near an eventual receiver, subject to continuity, contact, and possession-confirmation checks.",
			DoesNotEstablish: "Does not identify the cause, attribute control to the receiver, prove a catch-range violation, or identify release-side assistance.",
		}, true
	case "THROW_006":
		return BehaviorDescriptor{
			ID: BehaviorFreeFlight, Label: "Free-flight path review",
			Observes:         "Changes in sampled free-flight position and velocity after a release.",
			DoesNotEstablish: "Does not establish contact-free motion, authoritative physics, the controlling actor, or a targeting mechanism.",
		}, true
	default:
		return BehaviorDescriptor{}, false
	}
}
