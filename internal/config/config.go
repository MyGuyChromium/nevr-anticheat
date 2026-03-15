// Package config handles TOML configuration loading and validation.
package config

import (
	"fmt"
	"io"
	"os"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration structure.
type Config struct {
	General   GeneralConfig              `toml:"general"`
	Physics   PhysicsConfig              `toml:"physics"`
	Pipeline  PipelineConfig             `toml:"pipeline"`
	Scoring   ScoringConfig              `toml:"scoring"`
	Baseline  BaselineConfig             `toml:"baseline"`
	Shadow    ShadowConfig               `toml:"shadow"`
	Detectors map[string]DetectorConfig  `toml:"detector"`
}

// GeneralConfig holds general system settings.
type GeneralConfig struct {
	Mode       string `toml:"mode"`        // "offline" or "online"
	LogLevel   string `toml:"log_level"`   // "debug","info","warn","error"
	LogFormat  string `toml:"log_format"`  // "json" or "text"
	MaxWorkers int    `toml:"max_workers"`
	DBPath     string `toml:"db_path"`
}

// PhysicsConfig holds game physics constants.
type PhysicsConfig struct {
	DiscSpeedCap   float64 `toml:"disc_speed_cap"`
	BoostSpeedCap  float64 `toml:"boost_speed_cap"`
	MaxPlayerSpeed float64 `toml:"max_player_speed"`
	MaxThrowSpeed  float64 `toml:"max_throw_speed"`
	StunDuration   float64 `toml:"stun_duration"`
	ShieldCooldown float64 `toml:"shield_cooldown"`
	ImmunityWindow float64 `toml:"immunity_window"`
	GrabRange      float64 `toml:"grab_range"`
}

// PipelineConfig holds pipeline processing settings.
type PipelineConfig struct {
	HistoryWindow                 int     `toml:"history_window"`
	MinFrameDt                    float64 `toml:"min_frame_dt"`
	MaxFrameDt                    float64 `toml:"max_frame_dt"`
	MaxEventsPerPlayerPerDetector int     `toml:"max_events_per_player_per_detector"`
	HighPingThresholdMs           float64 `toml:"high_ping_threshold_ms"`
	CooldownFrames                int     `toml:"cooldown_frames"`
}

// ScoringConfig holds suspicion scoring settings.
type ScoringConfig struct {
	DecayHalfLifeHours            float64 `toml:"decay_half_life_hours"`
	ReviewThreshold               float64 `toml:"review_threshold"`
	AutoEnforceThreshold          float64 `toml:"auto_enforce_threshold"`
	AutoEnforceMinConfidence      float64 `toml:"auto_enforce_min_confidence"`
	MinMatchesForCrossMatch       int     `toml:"min_matches_for_cross_match"`
	MaxSingleContribution         float64 `toml:"max_single_contribution"`
	MaxContribPerDetectorPerMatch int     `toml:"max_contrib_per_detector_per_match"`
	InMatchDecayPointsPerMin      float64 `toml:"in_match_decay_points_per_min"`
	CrossMatchDecayFactor         float64 `toml:"cross_match_decay_factor"`
	CleanMatchResetCount          int     `toml:"clean_match_reset_count"`
	CorrelationBonusCap           float64 `toml:"correlation_bonus_cap"`
	SameCategoryDiminishing       float64 `toml:"same_category_diminishing"`
}

// BaselineConfig holds baseline modeling settings.
type BaselineConfig struct {
	MinBaselineMatches     int     `toml:"min_baseline_matches"`
	BaselineUpdateInterval int     `toml:"baseline_update_interval"`
	PlayerBaselineWeight   float64 `toml:"player_baseline_weight"`
}

// ShadowConfig holds shadow mode settings.
type ShadowConfig struct {
	ShadowDetectors []string `toml:"shadow_detectors"`
}

// DetectorConfig holds per-detector configuration.
type DetectorConfig struct {
	Enabled           bool           `toml:"enabled"`
	EnforcementWeight float64        `toml:"enforcement_weight"`
	AutoEnforce       bool           `toml:"auto_enforce"`
	Mode              string         `toml:"mode"` // "shadow", "review", "enforce"
	Params            map[string]any `toml:"params"`
}

// GetDetectorConfig returns config for a detector, or a sensible default.
func (c *Config) GetDetectorConfig(id string) DetectorConfig {
	if dc, ok := c.Detectors[id]; ok {
		return dc
	}
	return DetectorConfig{
		Enabled:           false,
		EnforcementWeight: 0.5,
		Mode:              "shadow",
		Params:            make(map[string]any),
	}
}

// IsDetectorEnabled returns whether a detector is enabled.
func (c *Config) IsDetectorEnabled(id string) bool {
	if dc, ok := c.Detectors[id]; ok {
		return dc.Enabled
	}
	return false
}

// IsDetectorShadow returns whether a detector is in shadow mode.
func (c *Config) IsDetectorShadow(id string) bool {
	if dc, ok := c.Detectors[id]; ok {
		return dc.Mode == "shadow"
	}
	// Check global shadow list
	for _, sid := range c.Shadow.ShadowDetectors {
		if sid == id {
			return true
		}
	}
	return true // default to shadow for safety
}

// LoadConfig loads configuration from a TOML file, merging with defaults.
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}
	return cfg, nil
}

// LoadConfigFromReader loads configuration from a reader.
func LoadConfigFromReader(r io.Reader) (*Config, error) {
	cfg := DefaultConfig()
	if _, err := toml.NewDecoder(r).Decode(cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}
	return cfg, nil
}
