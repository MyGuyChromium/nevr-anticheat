package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Validate checks all config values for consistency and valid ranges. It is
// ValidateWithWarnings without the warnings.
func Validate(cfg *Config) error {
	_, err := ValidateWithWarnings(cfg)
	return err
}

// ValidateWithWarnings checks the config and returns non-fatal warnings
// (ignored values, deprecated aliases, physics that differ from the engine
// defaults) alongside any hard error. A Config produced by DefaultConfig()
// yields no warnings and no error.
func ValidateWithWarnings(cfg *Config) ([]string, error) {
	var errs, warnings []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	warn := func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }

	// --- general ---
	g := cfg.General
	switch g.LogLevel {
	case "debug", "info", "warn", "warning", "error":
	default:
		add("general.log_level must be debug/info/warn/error, got %q", g.LogLevel)
	}
	if g.LogFormat != "json" && g.LogFormat != "text" {
		add("general.log_format must be json or text, got %q", g.LogFormat)
	}
	if g.MaxWorkers < 1 || g.MaxWorkers > 256 {
		add("general.max_workers must be in [1, 256], got %d", g.MaxWorkers)
	}
	if strings.TrimSpace(g.DBPath) == "" {
		add("general.db_path must not be empty")
	}

	// --- physics ---
	ph := cfg.Physics
	for _, f := range []struct {
		name string
		v    float64
	}{
		{"disc_speed_cap", ph.DiscSpeedCap}, {"boost_speed_cap", ph.BoostSpeedCap},
		{"max_player_speed", ph.MaxPlayerSpeed}, {"max_throw_speed", ph.MaxThrowSpeed},
		{"stun_duration", ph.StunDuration}, {"shield_cooldown", ph.ShieldCooldown},
		{"immunity_window", ph.ImmunityWindow}, {"grab_range", ph.GrabRange}, {"goal_z", ph.GoalZ},
	} {
		if !(f.v > 0) {
			add("physics.%s must be > 0, got %v", f.name, f.v)
		}
	}
	if ph.MaxThrowSpeed < ph.DiscSpeedCap {
		add("physics.max_throw_speed (%v) must be >= disc_speed_cap (%v)", ph.MaxThrowSpeed, ph.DiscSpeedCap)
	}
	def := model.DefaultPhysics()
	if got := ph.Constants(); got != def {
		warn("[physics] differs from the engine defaults (disc_speed_cap %v, boost_speed_cap %v, max_player_speed %v, goal_z %v); every MatchContext produced from this config uses the configured values",
			def.DiscSpeedCap, def.BoostSpeedCap, def.MaxPlayerSpeed, def.GoalZ)
	}

	// --- pipeline ---
	p := cfg.Pipeline
	if p.HistoryWindow < 5 {
		add("pipeline.history_window must be >= 5, got %d", p.HistoryWindow)
	}
	if !(p.MinFrameDt > 0) {
		add("pipeline.min_frame_dt must be > 0, got %v", p.MinFrameDt)
	}
	if !(p.MaxFrameDt > p.MinFrameDt) {
		add("pipeline.max_frame_dt (%v) must be > min_frame_dt (%v)", p.MaxFrameDt, p.MinFrameDt)
	}
	if p.MaxEventsPerPlayerPerDetector < 1 {
		add("pipeline.max_events_per_player_per_detector must be >= 1, got %d", p.MaxEventsPerPlayerPerDetector)
	}
	if p.HighPingThresholdMs < 0 {
		add("pipeline.high_ping_threshold_ms must be >= 0, got %v", p.HighPingThresholdMs)
	}
	if p.CooldownFrames < 0 {
		add("pipeline.cooldown_frames must be >= 0, got %d", p.CooldownFrames)
	}

	// --- scoring ---
	s := cfg.Scoring
	if !(s.DecayHalfLifeHours > 0) {
		add("scoring.decay_half_life_hours must be > 0, got %v", s.DecayHalfLifeHours)
	}
	if !(s.ReviewThreshold > 0) || s.ReviewThreshold > 100 {
		add("scoring.review_threshold must be in (0, 100], got %v", s.ReviewThreshold)
	}
	if s.AutoEnforceThreshold < s.ReviewThreshold || s.AutoEnforceThreshold > 100 {
		add("scoring.auto_enforce_threshold (%v) must be in [review_threshold, 100]", s.AutoEnforceThreshold)
	}
	if !(s.MaxSingleContribution > 0) {
		add("scoring.max_single_contribution must be > 0, got %v", s.MaxSingleContribution)
	}
	if s.MaxContribPerDetectorPerMatch < 1 {
		add("scoring.max_contrib_per_detector_per_match must be >= 1 (0 would mean no event ever scores), got %d", s.MaxContribPerDetectorPerMatch)
	}
	if s.CorrelationBonusCap < 0 {
		add("scoring.correlation_bonus_cap must be >= 0, got %v", s.CorrelationBonusCap)
	}
	if s.SameCategoryDiminishing <= 0 || s.SameCategoryDiminishing > 1 {
		add("scoring.same_category_diminishing must be in (0, 1], got %v", s.SameCategoryDiminishing)
	}
	if s.MinMatchesForCrossMatch < 2 {
		add("scoring.min_matches_for_cross_match must be >= 2 (the aggregator floors it at 2), got %d", s.MinMatchesForCrossMatch)
	}
	tiers := s.Tiers()
	if tiers.IsZero() {
		add("scoring tiers (informational/suspicious/high_risk/critical/action_worthy) must be set")
	} else {
		if err := tiers.Validate(); err != nil {
			add("scoring tiers: %v", err)
		}
		if tiers.ActionWorthy > 100 {
			add("scoring.action_worthy must be <= 100, got %v", tiers.ActionWorthy)
		}
		if s.ReviewThreshold > 0 && tiers.HighRisk != s.ReviewThreshold {
			warn("scoring.high_risk (%v) differs from scoring.review_threshold (%v); review_threshold wins (it is the high_risk boundary)",
				tiers.HighRisk, s.ReviewThreshold)
		}
	}

	// --- shadow ---
	for _, id := range cfg.Shadow.ShadowDetectors {
		if _, ok := detectorSpecs[id]; !ok {
			add("shadow.shadow_detectors: unknown detector ID %q", id)
		}
	}

	// --- server ---
	sv := cfg.Server
	if strings.TrimSpace(sv.Listen) == "" {
		add("server.listen must not be empty")
	}
	if strings.TrimSpace(sv.Metrics) == "" {
		add("server.metrics must not be empty")
	}
	for _, f := range []struct {
		name string
		v    int
	}{
		{"max_matches", sv.MaxMatches}, {"max_players_per_match", sv.MaxPlayersPerMatch},
		{"max_connections", sv.MaxConnections}, {"max_message_bytes", sv.MaxMessageBytes},
		{"max_frame_rate_per_player", sv.MaxFrameRatePerPlayer},
	} {
		if f.v < 1 {
			add("server.%s must be >= 1, got %d", f.name, f.v)
		}
	}
	if sv.IdleTimeout <= 0 || sv.StaleMatchAfter <= 0 || sv.PersistInterval <= 0 {
		add("server.idle_timeout, stale_match_after and persist_interval must be > 0")
	}
	if sv.AllowUnauthenticated {
		warn("server.allow_unauthenticated=true: the ingest endpoint will accept telemetry from any host")
	}

	// --- detectors ---
	for _, id := range cfg.DetectorIDs() {
		dc := cfg.Detectors[id]
		spec, known := detectorSpecs[id]
		if !known {
			add("detector.%s: unknown detector ID", id)
			continue
		}
		if dc.EnforcementWeight < 0 || dc.EnforcementWeight > 1 {
			add("detector.%s.enforcement_weight must be in [0, 1], got %v", id, dc.EnforcementWeight)
		}
		switch dc.Mode {
		case "shadow":
		case "review", "enforce":
			warn("detector.%s.mode=%q: only \"shadow\" changes behaviour; this detector's events will be SCORED", id, dc.Mode)
		case "":
			if dc.Enabled {
				add("detector.%s.mode must be set (shadow/review/enforce) for an enabled detector; an empty mode would score its events", id)
			}
		default:
			add("detector.%s.mode must be shadow/review/enforce, got %q", id, dc.Mode)
		}
		if dc.AutoEnforce && dc.Mode == "shadow" {
			warn("detector.%s.auto_enforce=true has no effect while mode is shadow", id)
		}
		keys := make([]string, 0, len(dc.Params))
		for k := range dc.Params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if msg, ok := reservedParamKeys[key]; ok {
				add("detector.%s.params.%s: %s", id, key, msg)
				continue
			}
			if msg, ok := spec.Removed[key]; ok {
				warn("detector.%s.params.%s is no longer read: %s", id, key, msg)
				continue
			}
			ps, viaAlias, ok := spec.param(key)
			if !ok {
				add("detector.%s.params.%s: unknown parameter (accepted: %s)", id, key, strings.Join(spec.AcceptedKeys(), ", "))
				continue
			}
			if viaAlias {
				warn("detector.%s.params.%s is a deprecated alias of %s", id, key, ps.Key)
			}
			if _, err := normalizeParamValue(ps, dc.Params[key]); err != nil {
				add("detector.%s.params.%s: %v", id, key, err)
			}
		}
		if id == "PAT_003" {
			if list, ok := dc.Params["trusted_detectors"]; ok {
				if norm, err := normalizeParamValue(ParamSpec{Type: ParamStringList}, list); err == nil {
					for _, tid := range norm.([]string) {
						if _, ok := detectorSpecs[tid]; !ok {
							add("detector.PAT_003.params.trusted_detectors: unknown detector ID %q", tid)
						}
					}
				}
			}
		}
	}

	if len(errs) > 0 {
		return warnings, fmt.Errorf("config validation errors:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return warnings, nil
}
