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
	"boost_input_unavailable":           "Explicit boost state or compatible sampled timing and speed was unavailable",
	"boost_baseline_unavailable":        "No known pre-boost baseline was observed; an already-active boost is not an activation event",
	"catch_attachment_unknown":          "Explicit disc attachment was missing, ambiguous or inconsistent; no catch was inferred from possession alone",
	"catch_source_unavailable":          "Fresh bound source and timing context was unavailable for this catch sample",
	"catch_source_changed":              "Source context changed before catch confirmation; the pending comparison is inconclusive",
	"catch_inputs_ready":                "Complete sampled roster passed catch-input checks; not a verified catch opportunity",
	"catch_approach_evaluated":          "A free-disc approach was evaluated and possession persisted for a second sample",
	"catch_baseline_pending":            "Collecting a stable free-disc reference path",
	"catch_baseline_unstable":           "Position and velocity did not support a stable reference path",
	"catch_baseline_low_information":    "Baseline duration or displacement did not support a useful direction reference",
	"catch_bounce_changed":              "Bounce-counter change discarded the catch comparison",
	"catch_candidate_confirmed":         "Receiver-associated free-path observation retained; cause and actor unverified",
	"catch_collision_or_discontinuity":  "Abrupt turn, speed change or position/velocity disagreement discarded the path",
	"catch_confirmation_pending":        "Trajectory candidate awaits a second possession sample",
	"catch_correction_not_sustained":    "Free-path correction did not sustain the required interior duration and sample support",
	"catch_correction_duration_pending": "Free-path correction had insufficient duration after uncertain interval edges were excluded",
	"catch_confirmation_unavailable":    "Input ended before a second possession sample; no observation event retained",
	"catch_contact_uncertain":           "Invalid or unresolved contact geometry prevented a trajectory comparison",
	"catch_counterfactual_not_missed":   "Reference path did not miss the moving hand by a sufficient additional margin",
	"catch_disc_too_slow":               "Disc speed was below the trajectory comparison's engineering filter",
	"catch_free_trajectory_tracked":     "Free-disc sample retained for a possible later catch comparison",
	"catch_inconsistent_snapshot":       "Players did not share a consistent disc snapshot and timestamp",
	"catch_input_unavailable":           "Explicit possession, bounce-counter presence or finite disc inputs were unavailable",
	"catch_interval_jitter":             "Uneven sampling discarded the catch comparison",
	"catch_no_free_approach":            "Held disc had no qualifying preceding free-disc reference",
	"catch_no_sustained_correction":     "Approach did not meet sustained free-path review filters",
	"catch_possession_conflict":         "Holder records disagreed; catch attribution was withheld",
	"catch_possession_unavailable":      "Explicit unique holder knowledge was lost before catch confirmation",
	"catch_possession_unconfirmed":      "Candidate possession did not persist for a second sample",
	"catch_possible_contact":            "Disc path entered a conservative hand, head, body or torso motion envelope; comparison discarded",
	"catch_release_grace":               "Recent release or regrab grace excluded this approach",
	"catch_roster_incomplete":           "Fresh player snapshots did not cover the complete sampled roster",
	"catch_sample_gap":                  "Frame, time or roster discontinuity reset catch history",
	"catch_tracking_discontinuity":      "Body or relative head/hand motion exceeded tracking-continuity filters",
	"catch_tracking_unavailable":        "Required head/hand/body tracking or reliable player context was unavailable",
	"catch_window_expired":              "Bounded trajectory window expired; a new reference is required",
	"frame_rejected":                    "Player sample rejected by input validation",
	"inactive_phase":                    "Detector skipped during a non-active game phase",
	"warming_up":                        "Player has not passed this detector's warmup",
	"detector_evaluated":                "Player included in an actual detector evaluation",
	"no_raw_emission":                   "Evaluation returned no raw emission for this player",
	"raw_emission_returned":             "Detector returned a raw emission; not yet a retained incident",
	"invalid_emission_dropped":          "Raw emission failed event validation and was dropped",
	"quality_confidence_reduced":        "Source quality reduced emission confidence",
	"quality_forced_shadow":             "Source quality forced observation-only handling",
	"source_health_abstention":          "Affected source observations are degraded or blind; evidence retained without scoring or automatic enforcement",
	"unsupported_input_abstention":      "Required state/input edges are not observable from this feed; descriptive evidence is not scored",
	"context_tracking_limited":          "Tracking uncertainty reduced emission confidence",
	"context_possible_head_contact":     "Possible head contact reduced emission confidence",
	"context_possible_slap_or_push":     "Possible slap or push reduced emission confidence",
	"context_lean_or_step":              "Possible lean or playspace translation reduced emission confidence",
	"context_boost":                     "Boosting context reduced emission confidence",
	"emission_merged":                   "Raw emission merged into an existing incident",
	"incident_rate_limited":             "Closed incident dropped by the per-player detector limit",
	"incident_retained_shadow":          "Final observation-only incident retained in the pipeline result",
	"incident_retained_review":          "Final non-shadow incident retained in the pipeline result",
	"incident_scored":                   "Retained incident accepted by the scorer",
	"incident_not_scored":               "Retained incident did not add score",
	"no_current_release":                "No release event at this sampled frame",
	"stale_player_context":              "Retained player context had no current sample to inspect for a release",
	"release_speed_unusable":            "Release speed was missing, non-finite or non-positive",
	"engine_throw_available":            "Valid attributed engine last_throw measurement was available",
	"hand_ratio_unavailable":            "Hand kinematics were unavailable for speed-ratio evidence",
	"sampled_speed_artifact_band":       "Sampled speed entered the suspected-artifact observation branch",
	"release_at_or_below_cap":           "Observed release speed did not exceed the configured effective cap",
	"release_above_cap":                 "Observed release speed exceeded the configured effective cap",
	"player_stunned":                    "Stunned-player guard reset wrist-rate streaks",
	"player_immune":                     "Respawn-immunity guard reset wrist-rate streaks",
	"wrist_interval_too_short":          "Sample interval guard reset wrist-rate streaks",
	"wrist_at_or_below_threshold":       "Sampled hand rate did not sustain a threshold violation",
	"wrist_rotation_unknown":            "Wrist orientation provenance, valid rotation pair, or sample continuity was unavailable; no wrist-rate conclusion",
	"wrist_streak_pending":              "Wrist violation streak was too short or its next emission was deferred",
	"wrist_sustained_candidate":         "Wrist streak reached the detector's emission condition",
	"playspace_unavailable":             "Playspace residual reconstruction was unavailable",
	"tracked_hands_unavailable":         "No tracked hand corroborated rig translation",
	"playspace_speed_below_gate":        "Residual speed did not pass the configured gate",
	"playspace_distance_below_gate":     "Residual displacement did not pass the configured gate",
	"pose_speed_below_gate":             "Observed pose speed did not pass the configured gate",
	"rig_coherence_below_gate":          "Tracked rig coherence did not pass the configured gate",
	"ping_above_gate":                   "Ping exceeded the detector's configured reliability gate",
	"playspace_frames_pending":          "Playspace burst has too few qualifying frames",
	"playspace_duration_pending":        "Playspace burst has insufficient qualifying duration",
	"playspace_burst_already_reported":  "This continuous playspace burst was already reported",
	"sustained_playspace_candidate":     "Sustained residual translation reached the review-candidate condition",
	"playspace_observation_gap":         "Missing consecutive observations reset the playspace burst",
	"release_hand_unavailable":          "Throwing-hand attribution or kinematics were unavailable",
	"possible_head_contact":             "Possible intervening head contact prevented wrist-angle evaluation",
	"release_motion_below_gate":         "Hand or disc motion was below the angle detector's minimum",
	"release_angle_not_exceeded":        "Release angle was unavailable or did not exceed its threshold",
	"body_translation_dominates":        "Body translation could explain the observed hand motion",
	"release_angle_candidate":           "Attributed release angle reached the review-candidate condition",
	"pre_release_unavailable":           "No pre-release disc snapshot was available",
	"pre_release_disc_unusable":         "Pre-release disc observation was missing or not before release",
	"release_delta_not_exceeded":        "Sampled disc speed change did not exceed the configured threshold",
	"release_delta_candidate":           "Sampled disc speed change reached the review-candidate condition",
	"speed_window_pending":              "Speed window has too few observations",
	"speed_not_exceeded":                "Speed window did not meet the median or burst condition",
	"speed_burst_candidate":             "Speed window reached the burst review-candidate condition",
	"median_speed_candidate":            "Speed window reached the median review-candidate condition",
}
