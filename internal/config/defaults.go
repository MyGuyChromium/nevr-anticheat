package config

import (
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"time"
)

// DefaultConfig returns a fully populated Config with conservative defaults.
// It is byte-for-byte the same configuration as configs/default.toml
// (tests/config-docs_config_test.go asserts the two never drift). All
// detectors default to shadow mode. No detector has been validated against
// labelled real Echo VR telemetry; observations must be calibrated before any
// detector is promoted to scored review mode.
func DefaultConfig() *Config {
	return &Config{
		ProjectRules: model.DefaultProjectRules(),
		General: GeneralConfig{
			LogLevel:   "info",
			LogFormat:  "json",
			MaxWorkers: 4,
			DBPath:     "./nevr-anticheat.db",
		},
		Physics: PhysicsConfig{
			DiscSpeedCap:   18.9,
			BoostSpeedCap:  5.0,
			MaxPlayerSpeed: 55.0,
			MaxThrowSpeed:  18.9,
			StunDuration:   3.0,
			ShieldCooldown: 5.0,
			ImmunityWindow: 1.5,
			GrabRange:      0.8,
			GoalZ:          36.078,
		},
		Pipeline: PipelineConfig{
			HistoryWindow:                 30,
			MinFrameDt:                    0.01,
			MaxFrameDt:                    0.5,
			MaxEventsPerPlayerPerDetector: 100,
			HighPingThresholdMs:           150,
			CooldownFrames:                300,
		},
		Scoring: ScoringConfig{
			DecayHalfLifeHours:            168.0,
			ReviewThreshold:               60.0,
			AutoEnforceThreshold:          95.0,
			MinMatchesForCrossMatch:       3,
			MaxSingleContribution:         15.0,
			MaxContribPerDetectorPerMatch: 2,
			CorrelationBonusCap:           15.0,
			SameCategoryDiminishing:       0.8,
			Informational:                 20,
			Suspicious:                    40,
			HighRisk:                      60,
			Critical:                      80,
			ActionWorthy:                  95,
		},
		Baseline: BaselineConfig{
			MinBaselineMatches:     10,
			BaselineUpdateInterval: 5,
			PlayerBaselineWeight:   0.3,
		},
		Shadow: ShadowConfig{
			ShadowDetectors: []string{},
		},
		Server: ServerConfig{
			SourceGrants:          []SourceGrantConfig{},
			Listen:                ":8080",
			Metrics:               ":9090",
			AllowUnauthenticated:  false,
			MaxMatches:            64,
			MaxPlayersPerMatch:    16,
			MaxConnections:        100,
			MaxMessageBytes:       1024 * 1024,
			MaxFrameRatePerPlayer: 30,
			IdleTimeout:           5 * time.Minute,
			StaleMatchAfter:       30 * time.Minute,
			PersistInterval:       2 * time.Minute,
		},
		Detectors: defaultDetectors(),
	}
}

// defaultDetectors holds the calibrated per-detector settings. Every params
// key here is one the constructor reads (params.go is the contract; the
// test in tests/config-docs_params_test.go proves consumption). Values are
// the calibrated settings, which may differ from the constructor fallbacks
// (e.g. MOV_001 55 m/s here vs 12 m/s in code); the config value is what
// runs when a binary is started with any config, including the defaults.
func defaultDetectors() map[string]DetectorConfig {
	return map[string]DetectorConfig{
		// PHYSICS_GROUNDED: Echo VR caps throws at 18.9 m/s. Release speed is
		// read directly from disc.velocity, so network ping does not enlarge it.
		// Repeatable at-or-below-cap throws stay legal. Keep actual over-cap
		// observations shadow-only until the replay telemetry is validated.
		"THROW_001": {Enabled: true, EnforcementWeight: 0.8, AutoEnforce: false, Mode: "shadow", Params: map[string]any{
			"base_tolerance": 0.0, "ping_tolerance_scalar": 0.0, "max_speed_ratio": 3.0,
			"sigmoid_steepness": 2.0,
		}},
		// UNVERIFIED: v2.0.0 single-delta approach, needs real-data calibration
		// (the BROKEN multi-frame mechanism was removed in 3ab978b).
		"THROW_002": {Enabled: false, EnforcementWeight: 0.7, Mode: "shadow", Params: map[string]any{
			"max_speed_delta": 22.0,
		}},
		"THROW_003": {Enabled: true, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"max_release_angle_deviation": 177.0, "min_hand_speed": 3.0, "min_throw_speed": 5.0,
		}},
		// UNSAFE: regrab playstyle produces low variance naturally.
		"THROW_004": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"min_throws": 12, "min_generalized_variance": 1e-8,
		}},
		"THROW_005": {Enabled: true, EnforcementWeight: 0.0, Mode: "shadow", Params: map[string]any{}},
		"THROW_006": {Enabled: true, EnforcementWeight: 0.0, Mode: "shadow", Params: map[string]any{
			"min_trajectory_change": 8.0, "post_release_frames": 15, "min_distance_from_thrower": 2.0,
			"max_cumulative_change": 130.0,
		}},
		// STUB: Evaluate() returns nil; no penalty-field telemetry exists.
		"THROW_007": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"expected_penalty_speed_loss": 0.5, "penalty_tolerance": 0.1, "min_entry_speed": 5.0,
		}},
		"THROW_008": {Enabled: true, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"speed_increase_tolerance": 5.0, "max_tracking_frames": 30,
		}},
		// UNVERIFIED: rotation convention unconfirmed; unreachable at 15 Hz (pi/dt = 46.9 rad/s).
		"BIO_001": {Enabled: true, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"max_wrist_angular_velocity": 50.0, "min_violation_frames": 3, "sigmoid_steepness": 8.5,
		}},
		"BIO_002": {Enabled: true, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"max_hand_speed": 50.0, "min_violation_frames": 2, "sigmoid_steepness": 8.5,
		}},
		"BIO_003": {Enabled: true, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"max_jitter_variance": 0.00001, "jitter_window_frames": 90, "min_active_frames": 60,
			"severity_decades": 2.0, "min_consecutive_windows": 2,
		}},
		"BIO_004": {Enabled: true, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"max_wobble_variance": 0.00005, "wobble_window_frames": 90, "min_active_frames": 60,
			"severity_decades": 2.0, "min_consecutive_windows": 2,
		}},
		"MOV_001": {Enabled: true, EnforcementWeight: 0.7, Mode: "shadow", Params: map[string]any{
			"max_legitimate_speed": 55.0, "sustained_speed_window": 30, "sigmoid_steepness": 0.5,
			"min_burst_frames": 5,
		}},
		// UNVALIDATED: defensive design, network desync behaviour unverified.
		"MOV_002": {Enabled: true, EnforcementWeight: 0.8, AutoEnforce: false, Mode: "shadow", Params: map[string]any{
			"teleport_threshold": 8.0, "velocity_mismatch_factor": 3.0, "max_frame_gap": 5,
			"sigmoid_steepness": 0.5, "min_solo_teleporters": 2, "cluster_window": 60,
			"goal_cooldown_frames": 150, "max_displacement": 12.0, "min_incidents": 5,
		}},
		// UNSAFE: wall bounces and collisions produce legitimate reversals.
		"MOV_003": {Enabled: false, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"min_angle_deg": 175.0, "min_speed": 12.0, "stun_cooldown_frames": 30, "sigmoid_steepness": 0.5,
			"confirm_frames": 3, "heading_tolerance_deg": 20.0, "collision_radius": 2.0,
		}},
		// TELEMETRY_DEPENDENT: needs is_boosting.
		"MOV_004": {Enabled: false, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"boost_cap_margin": 1.5, "sigmoid_steepness": 0.8,
		}},
		// TELEMETRY_DEPENDENT: needs is_boosting.
		"MOV_005": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"window_seconds": 10.0, "max_boosts_per_window": 25, "max_consecutive": 5,
			"recharge_pause_frames": 10, "min_sequences": 2, "sigmoid_steepness": 0.5,
		}},
		// EchoTools-grounded feature, unvalidated classification: game velocity
		// is removed from arena pose motion, then coherent physical rig
		// translation is surfaced as an observation. Feet and guardian origin
		// are unavailable, so legal leaning cannot be ruled out automatically.
		// Temporarily paused; DetectorPauseReason also covers saved overrides.
		"MOV_006": {Enabled: false, EnforcementWeight: 0.75, AutoEnforce: false, Mode: "shadow", Params: map[string]any{
			"min_playspace_speed": 1.0, "min_playspace_distance": 0.55, "min_rig_coherence": 0.65,
			"min_observed_pose_speed": 0.35, "min_sustained_frames": 5, "min_sustained_seconds": 0.3, "max_ping_ms": 150.0,
		}},
		"STATE_001": {Enabled: true, EnforcementWeight: 0.0, Mode: "shadow", Params: map[string]any{}},
		"STATE_002": {Enabled: true, EnforcementWeight: 0.7, Mode: "shadow", Params: map[string]any{
			"min_stun_frames": 20, "min_stun_seconds": 0.0, "min_incidents": 2, "sigmoid_steepness": 0.2,
		}},
		// TELEMETRY_DEPENDENT: needs shield_active.
		"STATE_003": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"suspicious_frames": 300, "high_frames": 375, "impossible_frames": 600, "sigmoid_steepness": 0.02,
		}},
		// TELEMETRY_DEPENDENT: needs is_immune.
		"STATE_004": {Enabled: false, EnforcementWeight: 0.8, Mode: "shadow", Params: map[string]any{
			"max_immune_frames": 225, "escalation_interval_frames": 0, "sigmoid_steepness": 8.0,
		}},
		// TELEMETRY_DEPENDENT: needs shield_active.
		"STATE_005": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"min_cooldown_frames": 60, "min_cooldown_seconds": 0.0, "min_violations": 15, "sigmoid_steepness": 0.05,
		}},
		// SUSPENDED: no confirmed impossible score invariant (delta=1 proved legitimate).
		"STATE_006": {Enabled: false, EnforcementWeight: 1.0, AutoEnforce: false, Mode: "shadow", Params: map[string]any{}},
		// TELEMETRY_DEPENDENT: per-frame stun count granularity unconfirmed.
		"STATE_007": {Enabled: false, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"punch_range_threshold": 10.0, "velocity_adjust_scale": 0.15, "min_incidents": 3,
			"sigmoid_steepness": 1.0, "attribution_window_frames": 2, "max_range": 25.0,
		}},
		// OBSERVATION_ONLY: pre-catch trajectory review has no validated cheat signature.
		"STATE_008": {Enabled: true, EnforcementWeight: 0.0, AutoEnforce: false, Mode: "shadow", Params: map[string]any{
			"baseline_samples": 4, "min_correction_samples": 2, "max_sample_gap_s": 0.12,
			"baseline_duration_s": 0.20, "min_correction_duration_s": 0.12,
			"min_turn_rate_deg_s": 60.0, "max_turn_rate_deg_s": 300.0,
			"max_window_s": 1.5, "max_step_error_m": 0.20, "min_lateral_deviation_m": 0.30,
			"contact_margin_m": 0.65, "contact_accel_allowance_mps2": 30.0,
			"min_miss_improvement_m": 0.50, "max_catch_approach_m": 1.5, "regrab_grace_s": 0.35,
		}},
		// UNSAFE: regrab rhythm produces low CoV naturally.
		"PAT_001": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"max_cov": 0.05, "max_stddev": 3.0, "min_throw_count": 12, "sigmoid_steepness": 20.0,
		}},
		// UNSAFE: consistent throwing form produces tight spreads naturally.
		"PAT_002": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"min_release_spread": 0.01, "min_throw_count": 12, "min_release_speed": 0.0, "sigmoid_steepness": 50.0,
		}},
		// CROSS_MATCH_DEPENDENT: needs accumulated DB history.
		"PAT_003": {Enabled: false, EnforcementWeight: 0.8, Mode: "shadow", Params: map[string]any{
			"min_matches": 3, "min_avg_confidence": 0.6, "match_limit": 20, "sigmoid_steepness": 2.0,
			"trusted_detectors": []string{},
		}},
		"PAT_004": {Enabled: true, EnforcementWeight: 0.9, Mode: "shadow", Params: map[string]any{
			"min_categories": 3, "sigmoid_steepness": 1.0,
		}},
		"PAT_005": {Enabled: false, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"hand_to_head_threshold": 1.6, "min_sustained_frames": 30, "sigmoid_steepness": 2.0,
		}},
	}
}
