package config

import "sort"

// ParamType is the value type a detector parameter accepts in TOML.
type ParamType string

const (
	ParamInt        ParamType = "int"
	ParamFloat      ParamType = "float"
	ParamBool       ParamType = "bool"
	ParamStringList ParamType = "string_list"
)

// ParamSpec describes one key a detector constructor / Configure() reads.
//
// Default is the constructor's own fallback (what the detector uses when the
// key is absent from the params map). It is deliberately NOT always equal to
// the value shipped in configs/default.toml: the config value is the
// calibrated setting, the constructor value is the code fallback that applies
// when a detector is built without config (tests, ad-hoc tooling).
// EffectiveTable reports which one is in force.
type ParamSpec struct {
	Key     string
	Type    ParamType
	Default any
	Unit    string
	Doc     string
	// Aliases are older spellings the constructor still honours through the
	// detect.Get*Alias helpers. The loader rewrites them to Key and warns.
	Aliases []string
}

// DetectorSpec is the accepted-parameter contract for one detector, derived
// by reading each constructor in internal/detect/** (see
// tests/config-docs_params_test.go, which proves every key is consumed).
type DetectorSpec struct {
	ID       string
	Name     string
	Category string
	Params   []ParamSpec
	// Removed maps keys that older configs carried but no code reads to a
	// one-line explanation. The loader drops them with a warning instead of
	// failing, so a config written for an earlier build still loads.
	Removed map[string]string
}

// reservedParamKeys are params entries the binaries inject at build time and
// that must never appear in a TOML file.
var reservedParamKeys = map[string]string{
	"history_provider": "injected by cmd/anticheat and cmd/server for PAT_003; it is not a config value",
}

// deprecatedTopLevelKeys are keys that earlier configs carried at the section
// level and that no code path reads any more. They are accepted with a warning.
var deprecatedTopLevelKeys = map[string]string{
	"general.mode":                          "ignored: the binary (nevr-ac vs nevr-server) determines the mode; remove the key",
	"scoring.auto_enforce_min_confidence":   "removed: no code path reads it; THROW_001 gates auto-enforce per event on severity > 0.95, attribution confidence >= 0.9 and a 5 m/s over-cap margin, every other detector stamps the flag whenever its auto_enforce is true",
	"scoring.in_match_decay_points_per_min": "removed: the per-match scorer is never decayed; decay is applied by cross-match aggregation (decay_half_life_hours)",
	"scoring.cross_match_decay_factor":      "removed: cross-match aggregation uses decay_half_life_hours",
	"scoring.clean_match_reset_count":       "removed: no code path reads it",
}

func f(key string, def float64, unit, doc string, aliases ...string) ParamSpec {
	return ParamSpec{Key: key, Type: ParamFloat, Default: def, Unit: unit, Doc: doc, Aliases: aliases}
}

func i(key string, def int, unit, doc string, aliases ...string) ParamSpec {
	return ParamSpec{Key: key, Type: ParamInt, Default: def, Unit: unit, Doc: doc, Aliases: aliases}
}

const sigmoidDoc = "steepness of the severity/confidence sigmoid"

// detectorSpecs is the accepted-key table. Order within Params is the order
// the effective table prints. Defaults are the constructor fallbacks copied
// from internal/detect/**.
var detectorSpecs = map[string]DetectorSpec{
	"THROW_001": {ID: "THROW_001", Name: "Impossible Release Velocity", Category: "throw",
		Params: []ParamSpec{
			f("base_tolerance", 0.0, "m/s", "optional tolerance added to physics.disc_speed_cap before a release is over-cap"),
			f("ping_tolerance_scalar", 0.0, "m/s per s of ping", "optional legacy tolerance per second of estimated ping; zero by default because release speed comes from disc.velocity"),
			f("max_speed_ratio", 3.0, "ratio", "release/cap ratio at which severity saturates"),
			f("sigmoid_steepness", 2.0, "", sigmoidDoc),
			i("cap_riding_cooldown_frames", 900, "frames", "at most one cap-riding event per player per window"),
		},
		Removed: map[string]string{
			"speed_threshold": "the cap is physics.disc_speed_cap (via MatchContext.Physics), not a detector param",
		}},
	"THROW_002": {ID: "THROW_002", Name: "Impossible Disc Acceleration", Category: "throw",
		Params: []ParamSpec{
			f("max_speed_delta", 22.0, "m/s", "max jump from the last pre-release disc speed to the release speed"),
		},
		Removed: map[string]string{
			"max_release_acceleration": "detector v2.0.0 compares speeds (max_speed_delta); acceleration keys are gone",
			"release_window_frames":    "detector v2.0.0 uses the last pre-release snapshot only",
			"max_accel_ratio":          "detector v2.0.0 has no ratio tier",
		}},
	"THROW_003": {ID: "THROW_003", Name: "Unnatural Release Angle", Category: "throw",
		Params: []ParamSpec{
			f("max_release_angle_deviation", 177.0, "deg", "angle between hand velocity and disc velocity above which a release is unnatural"),
			f("min_hand_speed", 3.0, "m/s", "minimum hand speed for a release to be evaluated"),
			f("min_throw_speed", 5.0, "m/s", "minimum release speed for a release to be evaluated"),
		},
		Removed: map[string]string{
			"min_angle_stddev":       "consistency check removed from the detector",
			"consistency_min_throws": "consistency check removed from the detector",
		}},
	"THROW_004": {ID: "THROW_004", Name: "Repeated Release Signatures", Category: "throw",
		Params: []ParamSpec{
			i("min_throws", 12, "count", "throws needed before the 6-dim signature variance is evaluated"),
			f("min_generalized_variance", 1e-8, "", "generalized variance below which signatures are 'identical' (rescaled to the informative dimensions)"),
		},
		Removed: map[string]string{
			"min_bhattacharyya": "population comparison was never implemented",
		}},
	"THROW_005": {ID: "THROW_005", Name: "Superhuman Target Precision", Category: "throw",
		Params: []ParamSpec{
			f("max_mean_deviation", 2.0, "m", "mean goal-line deviation below which precision is superhuman"),
			f("max_stddev_deviation", 1.5, "m", "deviation stddev below which precision is superhuman"),
			i("min_throws_for_pattern", 8, "count", "goal-directed throws needed before evaluation"),
		}},
	"THROW_006": {ID: "THROW_006", Name: "Trajectory Correction (Mags)", Category: "throw",
		Params: []ParamSpec{
			f("min_trajectory_change", 8.0, "deg", "per-frame heading change counted as a bend"),
			f("max_cumulative_change", 130.0, "deg", "cumulative bend that saturates severity"),
			i("post_release_frames", 15, "frames", "frames tracked after release"),
			f("min_distance_from_thrower", 2.0, "m", "disc must be this far from the thrower before bends count"),
		}},
	"THROW_007": {ID: "THROW_007", Name: "Penalty Field Tampering", Category: "throw",
		Params: []ParamSpec{
			f("expected_penalty_speed_loss", 0.5, "ratio", "STUB: expected fractional speed loss in the penalty field"),
			f("penalty_tolerance", 0.1, "ratio", "STUB: tolerance around the expected loss"),
			f("min_entry_speed", 5.0, "m/s", "STUB: minimum entry speed to evaluate"),
		}},
	"THROW_008": {ID: "THROW_008", Name: "Speed-Distance Anomaly", Category: "throw",
		Params: []ParamSpec{
			f("speed_increase_tolerance", 5.0, "m/s", "allowed disc speed increase in free flight"),
			i("max_tracking_frames", 30, "frames", "frames tracked after release"),
		},
		Removed: map[string]string{
			"speed_distance_tolerance": "distance-corrected tolerance was never implemented",
		}},
	"BIO_001": {ID: "BIO_001", Name: "Impossible Wrist Rotation", Category: "bio",
		Params: []ParamSpec{
			f("max_wrist_angular_velocity", 50.0, "rad/s", "wrist rate above which a frame violates (unreachable at 15 Hz: pi/dt = 46.9 rad/s)"),
			i("min_violation_frames", 3, "frames", "consecutive violating frames before an event (floored at 3 in code)"),
			f("sigmoid_steepness", 8.5, "", sigmoidDoc),
		}},
	"BIO_002": {ID: "BIO_002", Name: "Impossible Hand Speed", Category: "bio",
		Params: []ParamSpec{
			f("max_hand_speed", 50.0, "m/s", "player-relative hand speed above which a frame violates"),
			i("min_violation_frames", 2, "frames", "consecutive violating frames before an event (floored at 3 in code)"),
			f("sigmoid_steepness", 8.5, "", sigmoidDoc),
		}},
	"BIO_003": {ID: "BIO_003", Name: "Zero Hand Jitter", Category: "bio",
		Params: []ParamSpec{
			i("jitter_window_frames", 90, "frames", "window over which hand position variance is measured"),
			f("max_jitter_variance", 0.00001, "m^2", "variance below which the hand is 'too still'"),
			i("min_active_frames", 60, "frames", "active (moving) frames required inside the window"),
			f("severity_decades", 2.0, "log10", "decades below the threshold that saturate severity"),
			i("min_consecutive_windows", 2, "count", "consecutive zero-jitter windows before an event"),
		}},
	"BIO_004": {ID: "BIO_004", Name: "Zero Aim Wobble", Category: "bio",
		Params: []ParamSpec{
			i("wobble_window_frames", 90, "frames", "window over which hand rotation variance is measured"),
			f("max_wobble_variance", 0.00005, "", "rotation variance below which aim is 'too steady'"),
			i("min_active_frames", 60, "frames", "active frames required inside the window"),
			f("severity_decades", 2.0, "log10", "decades below the threshold that saturate severity"),
			i("min_consecutive_windows", 2, "count", "consecutive zero-wobble windows before an event"),
		},
		Removed: map[string]string{
			"active_only": "the activity gate is always on (min_active_frames)",
		}},
	"MOV_001": {ID: "MOV_001", Name: "Impossible Player Speed", Category: "movement",
		Params: []ParamSpec{
			f("max_legitimate_speed", 55.0, "m/s", "sustained (median) speed above which movement is impossible; burst cap is max(this, physics.max_player_speed)"),
			i("sustained_speed_window", 30, "frames", "median window length"),
			f("sigmoid_steepness", 0.5, "", sigmoidDoc),
			i("min_burst_frames", 5, "frames", "frames above the burst cap before a burst event"),
		}},
	"MOV_002": {ID: "MOV_002", Name: "Teleportation", Category: "movement",
		Params: []ParamSpec{
			f("teleport_threshold", 8.0, "m", "position jump between consecutive frames that counts as a teleport"),
			f("velocity_mismatch_factor", 3.0, "ratio", "jump must exceed reported velocity * dt by this factor"),
			i("max_frame_gap", 5, "frames", "gaps larger than this are not compared (respawn, tracking loss)"),
			f("sigmoid_steepness", 0.5, "", sigmoidDoc),
			i("min_solo_teleporters", 2, "count", "this many simultaneous teleporters means a game event, not a cheat"),
			i("cluster_window", 60, "frames", "window for the distinct-player cluster suppression"),
			i("goal_cooldown_frames", 150, "frames", "frames after a score change during which jumps are ignored"),
			f("max_displacement", 12.0, "m", "jumps above this are treated as resets, not teleports"),
			i("min_incidents", 5, "count", "teleport incidents per player before an event"),
		},
		Removed: map[string]string{
			"max_frame_gap_ms": "replaced by max_frame_gap (frames, not milliseconds)",
		}},
	"MOV_003": {ID: "MOV_003", Name: "Zero-Inertia Direction Change", Category: "movement",
		Params: []ParamSpec{
			f("min_angle_deg", 175.0, "deg", "velocity reversal angle that counts as zero-inertia", "min_reversal_angle"),
			f("min_speed", 12.0, "m/s", "minimum speed before and after the reversal"),
			i("stun_cooldown_frames", 30, "frames", "frames after a stun during which reversals are ignored"),
			f("sigmoid_steepness", 0.5, "", sigmoidDoc),
			i("confirm_frames", 3, "frames", "frames the new heading must persist"),
			f("heading_tolerance_deg", 20.0, "deg", "heading drift tolerated during confirmation"),
			f("collision_radius", 2.0, "m", "another player within this radius explains the reversal"),
		},
		Removed: map[string]string{
			"collision_lookback_frames": "replaced by collision_radius (metres) and confirm_frames",
		}},
	"MOV_004": {ID: "MOV_004", Name: "Boost Speed Cap Violation", Category: "movement",
		Params: []ParamSpec{
			f("boost_cap_margin", 1.5, "m/s", "margin above physics.boost_speed_cap before a boost gain violates", "boost_margin"),
			f("sigmoid_steepness", 0.8, "", sigmoidDoc),
		},
		Removed: map[string]string{
			"boost_speed_cap": "the cap is physics.boost_speed_cap (via MatchContext.Physics)",
		}},
	"MOV_005": {ID: "MOV_005", Name: "Boost Spam", Category: "movement",
		Params: []ParamSpec{
			f("window_seconds", 10.0, "s", "sliding window for the activation count"),
			i("max_boosts_per_window", 25, "count", "boost activations allowed per window", "max_boosts_per_10s"),
			i("max_consecutive", 5, "count", "consecutive activations without a recharge pause"),
			i("recharge_pause_frames", 10, "frames", "pause that closes a boost sequence"),
			i("min_sequences", 2, "count", "suspicious sequences before an event", "min_sequences_to_surface"),
			f("sigmoid_steepness", 0.5, "", sigmoidDoc),
		},
		Removed: map[string]string{
			"max_consecutive_boosts":   "documented in frames; the detector counts activations (max_consecutive)",
			"recharge_pause_threshold": "documented in seconds; replaced by recharge_pause_frames",
		}},
	"MOV_006": {ID: "MOV_006", Name: "Physical Playspace Walking", Category: "movement",
		Params: []ParamSpec{
			f("min_playspace_speed", 1.0, "m/s", "minimum game-velocity-subtracted tracked-rig speed"),
			f("min_playspace_distance", 0.55, "m", "minimum accumulated physical playspace displacement"),
			f("min_observed_pose_speed", 0.35, "m/s", "minimum observed tracked-rig speed; rejects frozen remote poses with stale game velocity"),
			f("min_rig_coherence", 0.65, "ratio", "minimum head/hand translation agreement"),
			i("min_sustained_frames", 5, "frames", "minimum consecutive qualifying samples before an event"),
			f("min_sustained_seconds", 0.3, "s", "minimum real elapsed time across qualifying samples"),
			f("max_ping_ms", 150.0, "ms", "skip samples above this latency; 0 disables the hard cap"),
		}},
	"STATE_001": {ID: "STATE_001", Name: "Impossible Grab Distance", Category: "state",
		Params: []ParamSpec{
			f("grab_distance_threshold", 3.0, "m", "hand-to-disc distance at possession gain above which a grab is impossible"),
			f("closing_velocity_scale", 0.25, "s", "latency credited to closing speed; 0 = use the frame dt"),
			f("desync_margin", 3.0, "m", "extra distance tolerated as network desync"),
			f("sigmoid_steepness", 2.0, "", sigmoidDoc),
		}},
	"STATE_002": {ID: "STATE_002", Name: "Stun Recovery Exploit", Category: "state",
		Params: []ParamSpec{
			i("min_stun_frames", 20, "frames", "shortest legitimate stun, converted to seconds with the match tick rate"),
			f("min_stun_seconds", 0, "s", "shortest legitimate stun in seconds; overrides min_stun_frames when > 0"),
			i("min_incidents", 2, "count", "short stuns per player before an event", "min_incidents_to_surface"),
			f("sigmoid_steepness", 0.2, "", sigmoidDoc),
		}},
	"STATE_003": {ID: "STATE_003", Name: "Shield Duration Abuse", Category: "state",
		Params: []ParamSpec{
			i("suspicious_frames", 300, "frames", "continuous shield frames for the suspicious tier"),
			i("high_frames", 375, "frames", "continuous shield frames for the high tier"),
			i("impossible_frames", 600, "frames", "continuous shield frames for the impossible tier"),
			f("sigmoid_steepness", 0.02, "", sigmoidDoc),
		}},
	"STATE_004": {ID: "STATE_004", Name: "Damage Immunity Exploit", Category: "state",
		Params: []ParamSpec{
			i("max_immune_frames", 225, "frames", "active immune frames before an event", "immunity_threshold_frames"),
			i("escalation_interval_frames", 0, "frames", "re-fire interval while immunity persists; 0 = max_immune_frames/2"),
			f("sigmoid_steepness", 8.0, "", sigmoidDoc),
		}},
	"STATE_005": {ID: "STATE_005", Name: "Cooldown Bypass", Category: "state",
		Params: []ParamSpec{
			i("min_cooldown_frames", 60, "frames", "shortest legitimate shield cooldown, converted with the tick rate", "cooldown_bypass_threshold_frames"),
			f("min_cooldown_seconds", 0, "s", "shortest legitimate cooldown in seconds; overrides min_cooldown_frames when > 0"),
			i("min_violations", 15, "count", "violations per player before an event", "min_violations_to_surface"),
			f("sigmoid_steepness", 0.05, "", sigmoidDoc),
		}},
	"STATE_006": {ID: "STATE_006", Name: "Score Manipulation", Category: "state"},
	"STATE_007": {ID: "STATE_007", Name: "Impossible Punch Range", Category: "state",
		Params: []ParamSpec{
			f("punch_range_threshold", 10.0, "m", "puncher-to-victim distance above which a punch is impossible"),
			f("velocity_adjust_scale", 0.15, "s", "closing-velocity credit applied to the distance"),
			i("min_incidents", 3, "count", "incidents per player before an event", "min_incidents_to_surface"),
			f("sigmoid_steepness", 1.0, "", sigmoidDoc),
			i("attribution_window_frames", 2, "frames", "frames a stun start may lag the punch"),
			f("max_range", 25.0, "m", "distances above this are attribution failures, not punches"),
		}},
	"PAT_001": {ID: "PAT_001", Name: "Frame-Perfect Timing", Category: "pattern",
		Params: []ParamSpec{
			i("min_throw_count", 12, "count", "throws needed before inter-throw timing is evaluated (clamped to [3, 50])"),
			f("max_cov", 0.05, "ratio", "coefficient of variation below which timing is frame-perfect"),
			f("max_stddev", 3.0, "frames", "inter-throw stddev below which timing is frame-perfect", "max_stddev_frames"),
			f("sigmoid_steepness", 20.0, "", sigmoidDoc),
		}},
	"PAT_002": {ID: "PAT_002", Name: "Identical Release Points", Category: "pattern",
		Params: []ParamSpec{
			i("min_throw_count", 12, "count", "throws needed before release spread is evaluated"),
			f("min_release_spread", 0.01, "m", "body-frame release spread below which points are identical"),
			f("min_release_speed", 0, "m/s", "releases slower than this are not sampled; 0 = sample every release"),
			f("sigmoid_steepness", 50.0, "", sigmoidDoc),
		}},
	"PAT_003": {ID: "PAT_003", Name: "Cross-Match Consistency", Category: "pattern",
		Params: []ParamSpec{
			i("min_matches", 3, "count", "prior matches with eligible events before an event", "min_matches_soft"),
			f("min_avg_confidence", 0.6, "ratio", "mean confidence of prior events required"),
			i("match_limit", 20, "count", "most recent matches consulted", "match_history_depth"),
			f("sigmoid_steepness", 2.0, "", sigmoidDoc),
			{Key: "trusted_detectors", Type: ParamStringList, Default: []string(nil), Unit: "",
				Doc: "detector IDs whose history counts; empty = every non-shadow, non-meta detector"},
		},
		Removed: map[string]string{
			"min_matches_hard": "there is no high-confidence tier; use min_avg_confidence",
		}},
	"PAT_004": {ID: "PAT_004", Name: "Composite Multi-Cheat", Category: "pattern",
		Params: []ParamSpec{
			i("min_categories", 3, "count", "distinct detector categories firing in one match"),
			f("sigmoid_steepness", 1.0, "", sigmoidDoc),
		}},
	"PAT_005": {ID: "PAT_005", Name: "Playspace Abuse", Category: "pattern",
		Params: []ParamSpec{
			f("hand_to_head_threshold", 1.6, "m", "hand-to-head distance above which reach is abusive", "max_hand_to_head_distance"),
			i("min_sustained_frames", 30, "frames", "consecutive frames above the threshold before an event"),
			f("sigmoid_steepness", 2.0, "", sigmoidDoc),
		},
		Removed: map[string]string{
			"playspace_abuse_samples": "replaced by min_sustained_frames",
		}},
}

// KnownDetectorIDs returns every detector ID the config layer accepts, sorted.
func KnownDetectorIDs() []string {
	ids := make([]string, 0, len(detectorSpecs))
	for id := range detectorSpecs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// DetectorSpecFor returns the accepted-parameter contract for a detector.
func DetectorSpecFor(id string) (DetectorSpec, bool) {
	s, ok := detectorSpecs[id]
	return s, ok
}

// DetectorSpecs returns every detector spec sorted by ID.
func DetectorSpecs() []DetectorSpec {
	out := make([]DetectorSpec, 0, len(detectorSpecs))
	for _, id := range KnownDetectorIDs() {
		out = append(out, detectorSpecs[id])
	}
	return out
}

// param looks up a canonical key or one of its aliases. The returned bool is
// false when the key is neither.
func (s DetectorSpec) param(key string) (spec ParamSpec, viaAlias bool, ok bool) {
	for _, p := range s.Params {
		if p.Key == key {
			return p, false, true
		}
		for _, a := range p.Aliases {
			if a == key {
				return p, true, true
			}
		}
	}
	return ParamSpec{}, false, false
}

// AcceptedKeys returns the canonical parameter keys for a detector.
func (s DetectorSpec) AcceptedKeys() []string {
	keys := make([]string, 0, len(s.Params))
	for _, p := range s.Params {
		keys = append(keys, p.Key)
	}
	return keys
}
