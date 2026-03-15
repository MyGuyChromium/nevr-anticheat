package config

import (
	"fmt"
	"strings"
)

// Validate checks all config values for consistency and valid ranges.
func Validate(cfg *Config) error {
	var errs []string

	if cfg.General.Mode != "offline" && cfg.General.Mode != "online" {
		errs = append(errs, fmt.Sprintf("general.mode must be 'offline' or 'online', got %q", cfg.General.Mode))
	}
	if cfg.General.MaxWorkers < 1 {
		errs = append(errs, "general.max_workers must be >= 1")
	}
	if cfg.Physics.DiscSpeedCap <= 0 {
		errs = append(errs, "physics.disc_speed_cap must be > 0")
	}
	if cfg.Physics.MaxPlayerSpeed <= 0 {
		errs = append(errs, "physics.max_player_speed must be > 0")
	}
	if cfg.Pipeline.HistoryWindow < 5 {
		errs = append(errs, "pipeline.history_window must be >= 5")
	}
	if cfg.Pipeline.MinFrameDt <= 0 {
		errs = append(errs, "pipeline.min_frame_dt must be > 0")
	}
	if cfg.Pipeline.MaxFrameDt <= cfg.Pipeline.MinFrameDt {
		errs = append(errs, "pipeline.max_frame_dt must be > min_frame_dt")
	}
	if cfg.Scoring.ReviewThreshold <= 0 {
		errs = append(errs, "scoring.review_threshold must be > 0")
	}
	if cfg.Scoring.AutoEnforceThreshold <= cfg.Scoring.ReviewThreshold {
		errs = append(errs, "scoring.auto_enforce_threshold must be > review_threshold")
	}
	if cfg.Scoring.MaxSingleContribution <= 0 {
		errs = append(errs, "scoring.max_single_contribution must be > 0")
	}
	if cfg.Scoring.DecayHalfLifeHours <= 0 {
		errs = append(errs, "scoring.decay_half_life_hours must be > 0")
	}
	if cfg.Scoring.SameCategoryDiminishing <= 0 || cfg.Scoring.SameCategoryDiminishing > 1 {
		errs = append(errs, "scoring.same_category_diminishing must be in (0, 1]")
	}

	for id, dc := range cfg.Detectors {
		if dc.EnforcementWeight < 0 || dc.EnforcementWeight > 1 {
			errs = append(errs, fmt.Sprintf("detector.%s.enforcement_weight must be in [0, 1]", id))
		}
		if dc.Mode != "" && dc.Mode != "shadow" && dc.Mode != "review" && dc.Mode != "enforce" {
			errs = append(errs, fmt.Sprintf("detector.%s.mode must be shadow/review/enforce", id))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation errors:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}
