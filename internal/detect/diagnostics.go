package detect

// DecisionObserver consumes a branch that was actually visited. It is optional,
// synchronous and per-match; observers must not alter detector/player state.
// The production observer only aggregates fixed reason codes in bounded memory.
type DecisionObserver func(detectorID, playerID string, frame int, reason string)

// DecisionObservable is additive: custom detectors can keep implementing only
// Detector and will still have accurate pipeline-stage diagnostics.
type DecisionObservable interface {
	SetDecisionObserver(DecisionObserver)
	HasDecisionBranches() bool
}

func (b *BaseDetector) SetDecisionObserver(observer DecisionObserver) { b.decisionObserver = observer }
func (b *BaseDetector) HasDecisionBranches() bool                     { return b.TraceBranches }
func (b *BaseDetector) TraceDecision(playerID string, frame int, reason string) {
	if b.decisionObserver != nil {
		b.decisionObserver(b.DetectorID, playerID, frame, reason)
	}
}

// DecisionReasonDescription defines stable, non-verdict language for UI/export.
// Unknown codes remain opaque; no raw telemetry or dynamically formatted values
// belong in a reason code.
func DecisionReasonDescription(code string) string {
	if label, ok := decisionReasonDescriptions[code]; ok {
		return label
	}
	return "Additional detector diagnostic"
}

var decisionReasonDescriptions = map[string]string{
	"frame_rejected":                   "Player sample rejected by input validation",
	"inactive_phase":                   "Detector skipped during a non-active game phase",
	"warming_up":                       "Player has not passed this detector's warmup",
	"detector_evaluated":               "Player included in an actual detector evaluation",
	"no_raw_emission":                  "Evaluation returned no raw emission for this player",
	"raw_emission_returned":            "Detector returned a raw emission; not yet a retained incident",
	"invalid_emission_dropped":         "Raw emission failed event validation and was dropped",
	"quality_confidence_reduced":       "Source quality reduced emission confidence",
	"quality_forced_shadow":            "Source quality forced observation-only handling",
	"context_tracking_limited":         "Tracking uncertainty reduced emission confidence",
	"context_possible_head_contact":    "Possible head contact reduced emission confidence",
	"context_possible_slap_or_push":    "Possible slap or push reduced emission confidence",
	"context_lean_or_step":             "Possible lean or playspace translation reduced emission confidence",
	"context_boost":                    "Boosting context reduced emission confidence",
	"emission_merged":                  "Raw emission merged into an existing incident",
	"incident_rate_limited":            "Closed incident dropped by the per-player detector limit",
	"incident_retained_shadow":         "Final observation-only incident retained in the pipeline result",
	"incident_retained_review":         "Final non-shadow incident retained in the pipeline result",
	"incident_scored":                  "Retained incident accepted by the scorer",
	"incident_not_scored":              "Retained incident did not add score",
	"no_current_release":               "No release event at this sampled frame",
	"stale_player_context":             "Retained player context had no current sample to inspect for a release",
	"release_speed_unusable":           "Release speed was missing, non-finite or non-positive",
	"engine_throw_available":           "Valid attributed engine last_throw measurement was available",
	"hand_ratio_unavailable":           "Hand kinematics were unavailable for speed-ratio evidence",
	"sampled_speed_artifact_band":      "Sampled speed entered the suspected-artifact observation branch",
	"release_at_or_below_cap":          "Observed release speed did not exceed the configured effective cap",
	"release_above_cap":                "Observed release speed exceeded the configured effective cap",
	"player_stunned":                   "Stunned-player guard reset wrist-rate streaks",
	"player_immune":                    "Respawn-immunity guard reset wrist-rate streaks",
	"wrist_interval_too_short":         "Sample interval guard reset wrist-rate streaks",
	"wrist_at_or_below_threshold":      "Sampled hand rate did not sustain a threshold violation",
	"wrist_streak_pending":             "Wrist violation streak was too short or its next emission was deferred",
	"wrist_sustained_candidate":        "Wrist streak reached the detector's emission condition",
	"playspace_unavailable":            "Playspace residual reconstruction was unavailable",
	"tracked_hands_unavailable":        "No tracked hand corroborated rig translation",
	"playspace_speed_below_gate":       "Residual speed did not pass the configured gate",
	"playspace_distance_below_gate":    "Residual displacement did not pass the configured gate",
	"pose_speed_below_gate":            "Observed pose speed did not pass the configured gate",
	"rig_coherence_below_gate":         "Tracked rig coherence did not pass the configured gate",
	"ping_above_gate":                  "Ping exceeded the detector's configured reliability gate",
	"playspace_frames_pending":         "Playspace burst has too few qualifying frames",
	"playspace_duration_pending":       "Playspace burst has insufficient qualifying duration",
	"playspace_burst_already_reported": "This continuous playspace burst was already reported",
	"sustained_playspace_candidate":    "Sustained residual translation reached the review-candidate condition",
	"playspace_observation_gap":        "Missing consecutive observations reset the playspace burst",
	"release_hand_unavailable":         "Throwing-hand attribution or kinematics were unavailable",
	"possible_head_contact":            "Possible intervening head contact prevented wrist-angle evaluation",
	"release_motion_below_gate":        "Hand or disc motion was below the angle detector's minimum",
	"release_angle_not_exceeded":       "Release angle was unavailable or did not exceed its threshold",
	"body_translation_dominates":       "Body translation could explain the observed hand motion",
	"release_angle_candidate":          "Attributed release angle reached the review-candidate condition",
	"pre_release_unavailable":          "No pre-release disc snapshot was available",
	"pre_release_disc_unusable":        "Pre-release disc observation was missing or not before release",
	"release_delta_not_exceeded":       "Sampled disc speed change did not exceed the configured threshold",
	"release_delta_candidate":          "Sampled disc speed change reached the review-candidate condition",
	"speed_window_pending":             "Speed window has too few observations",
	"speed_not_exceeded":               "Speed window did not meet the median or burst condition",
	"speed_burst_candidate":            "Speed window reached the burst review-candidate condition",
	"median_speed_candidate":           "Speed window reached the median review-candidate condition",
}
