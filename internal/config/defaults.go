package config

// DefaultConfig returns a fully populated Config with production-safe defaults.
// All detectors default to shadow mode for safety.
func DefaultConfig() *Config {
	return &Config{
		General: GeneralConfig{
			Mode:       "offline",
			LogLevel:   "info",
			LogFormat:  "json",
			MaxWorkers: 4,
			DBPath:     "./nevr-anticheat.db",
		},
		Physics: PhysicsConfig{
			DiscSpeedCap:   18.7,
			BoostSpeedCap:  5.0,
			MaxPlayerSpeed: 55.0,
			MaxThrowSpeed:  20.0,
			StunDuration:   3.0,
			ShieldCooldown: 5.0,
			ImmunityWindow: 1.5,
			GrabRange:      0.8,
		},
		Pipeline: PipelineConfig{
			HistoryWindow:                 30,
			MinFrameDt:                    0.01,
			MaxFrameDt:                    0.2,
			MaxEventsPerPlayerPerDetector: 100,
			HighPingThresholdMs:           150,
			CooldownFrames:                300,
		},
		Scoring: ScoringConfig{
			DecayHalfLifeHours:            168.0,
			ReviewThreshold:               15.0,
			AutoEnforceThreshold:          50.0,
			AutoEnforceMinConfidence:      0.95,
			MinMatchesForCrossMatch:       3,
			MaxSingleContribution:         15.0,
			MaxContribPerDetectorPerMatch: 2,
			InMatchDecayPointsPerMin:      2.0,
			CrossMatchDecayFactor:         0.95,
			CleanMatchResetCount:          100,
			CorrelationBonusCap:           15.0,
			SameCategoryDiminishing:       0.8,
		},
		Baseline: BaselineConfig{
			MinBaselineMatches:     10,
			BaselineUpdateInterval: 5,
			PlayerBaselineWeight:   0.3,
		},
		Shadow: ShadowConfig{
			ShadowDetectors: []string{},
		},
		Detectors: defaultDetectors(),
	}
}

func defaultDetectors() map[string]DetectorConfig {
	return map[string]DetectorConfig{
		"THROW_001": {Enabled: true, EnforcementWeight: 0.8, AutoEnforce: true, Mode: "shadow", Params: map[string]any{
			"speed_threshold": 20.0, "base_tolerance": 1.3, "ping_tolerance_scalar": 5.0,
			"max_speed_ratio": 3.0, "sigmoid_steepness": 2.0,
		}},
		"THROW_002": {Enabled: true, EnforcementWeight: 0.7, Mode: "shadow", Params: map[string]any{
			"max_release_acceleration": 500.0, "release_window_frames": 3, "max_accel_ratio": 5.0,
		}},
		"THROW_003": {Enabled: true, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"max_release_angle_deviation": 175.0, "min_hand_speed": 3.0, "min_throw_speed": 5.0,
			"min_angle_stddev": 1.0, "consistency_min_throws": 5,
		}},
		"THROW_004": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"min_throws": 5, "min_generalized_variance": 0.001, "min_bhattacharyya": 0.3,
		}},
		"THROW_005": {Enabled: true, EnforcementWeight: 0.7, Mode: "shadow", Params: map[string]any{
			"max_mean_deviation": 2.0, "max_stddev_deviation": 1.5, "min_throws_for_pattern": 8,
		}},
		"THROW_006": {Enabled: true, EnforcementWeight: 0.8, Mode: "shadow", Params: map[string]any{
			"min_trajectory_change": 8.0, "post_release_frames": 15, "min_distance_from_thrower": 2.0,
			"max_cumulative_change": 80.0,
		}},
		"THROW_007": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"expected_penalty_speed_loss": 0.5, "penalty_tolerance": 0.1, "min_entry_speed": 5.0,
		}},
		"THROW_008": {Enabled: true, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"speed_increase_tolerance": 5.0, "speed_distance_tolerance": 1.5, "max_tracking_frames": 30,
		}},
		"BIO_001": {Enabled: true, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"max_wrist_angular_velocity": 50.0, "min_violation_frames": 2,
		}},
		"BIO_002": {Enabled: true, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"max_hand_speed": 50.0, "min_violation_frames": 2,
		}},
		"BIO_003": {Enabled: true, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"max_jitter_variance": 0.00001, "jitter_window_frames": 90, "min_active_frames": 60,
		}},
		"BIO_004": {Enabled: true, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"max_wobble_variance": 0.00005, "wobble_window_frames": 90, "active_only": true,
		}},
		"MOV_001": {Enabled: true, EnforcementWeight: 0.7, Mode: "shadow", Params: map[string]any{
			"max_legitimate_speed": 55.0, "sustained_speed_window": 30,
		}},
		"MOV_002": {Enabled: true, EnforcementWeight: 0.8, Mode: "shadow", Params: map[string]any{
			"teleport_threshold": 8.0, "velocity_mismatch_factor": 3.0, "max_frame_gap_ms": 200.0,
		}},
		"MOV_003": {Enabled: false, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"min_reversal_angle": 175.0, "min_speed": 12.0, "collision_lookback_frames": 5,
		}},
		"MOV_004": {Enabled: false, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"boost_speed_cap": 5.0, "boost_margin": 1.5,
		}},
		"MOV_005": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"max_boosts_per_10s": 25, "max_consecutive_boosts": 8, "recharge_pause_threshold": 2.0,
			"min_sequences_to_surface": 2,
		}},
		"STATE_001": {Enabled: true, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"grab_distance_threshold": 3.0, "closing_velocity_scale": 0.25,
		}},
		"STATE_002": {Enabled: true, EnforcementWeight: 0.7, Mode: "shadow", Params: map[string]any{
			"min_stun_frames": 20, "min_incidents_to_surface": 2,
		}},
		"STATE_003": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"suspicious_frames": 300, "high_frames": 375, "impossible_frames": 600,
		}},
		"STATE_004": {Enabled: false, EnforcementWeight: 0.8, Mode: "shadow", Params: map[string]any{
			"max_immune_frames": 200,
		}},
		"STATE_005": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"min_cooldown_frames": 60, "min_violations": 15,
		}},
		"STATE_006": {Enabled: true, EnforcementWeight: 1.0, AutoEnforce: true, Mode: "shadow", Params: map[string]any{}},
		"STATE_007": {Enabled: true, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"punch_range_threshold": 10.0, "min_incidents": 3,
		}},
		"PAT_001": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"max_cov": 0.05, "max_stddev_frames": 3.0, "min_throw_count": 12,
		}},
		"PAT_002": {Enabled: false, EnforcementWeight: 0.6, Mode: "shadow", Params: map[string]any{
			"min_release_spread": 0.01, "min_throw_count": 12,
		}},
		"PAT_003": {Enabled: true, EnforcementWeight: 0.8, Mode: "shadow", Params: map[string]any{
			"min_matches_soft": 3, "min_matches_hard": 2, "min_avg_confidence": 0.6, "match_history_depth": 20,
		}},
		"PAT_004": {Enabled: true, EnforcementWeight: 0.9, Mode: "shadow", Params: map[string]any{
			"min_categories": 3,
		}},
		"PAT_005": {Enabled: true, EnforcementWeight: 0.5, Mode: "shadow", Params: map[string]any{
			"max_hand_to_head_distance": 1.6, "playspace_abuse_samples": 45,
		}},
	}
}
