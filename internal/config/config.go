// Package config handles TOML configuration loading and validation.
//
// # Overlay semantics
//
// LoadConfig starts from DefaultConfig() and overlays only the keys that are
// actually present in the file (toml.MetaData.IsDefined). A partial
//
//	[detector.MOV_001]
//	enabled = true
//
// therefore keeps the default mode ("shadow"), enforcement_weight and every
// default param, and a params-only section
//
//	[detector.MOV_001.params]
//	sustained_speed_window = 45
//
// changes exactly that one key. Parameter keys are merged per key, not per
// section, so overriding one threshold never discards the others.
//
// # Strictness
//
// Unknown top-level keys, unknown detector IDs, unknown parameter keys and
// values of the wrong type are errors. Keys that earlier releases documented
// but that no code reads are dropped with a warning (see params.go:
// deprecatedTopLevelKeys and DetectorSpec.Removed); deprecated aliases are
// rewritten to their canonical key with a warning. Warnings are collected in
// Config.Warnings so binaries can log them at startup.
package config

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Config is the top-level configuration structure.
type Config struct {
	General  GeneralConfig  `toml:"general"`
	Physics  PhysicsConfig  `toml:"physics"`
	Pipeline PipelineConfig `toml:"pipeline"`
	Scoring  ScoringConfig  `toml:"scoring"`
	// Baseline is EXPERIMENTAL: no code path consumes it yet (there is no
	// BaselineProvider implementation). It is kept so the section can be
	// present in a file without being rejected as unknown.
	Baseline  BaselineConfig            `toml:"baseline"`
	Shadow    ShadowConfig              `toml:"shadow"`
	Server    ServerConfig              `toml:"server"`
	Detectors map[string]DetectorConfig `toml:"detector"`

	// Warnings collected while loading (deprecated keys, ignored values).
	// Never written to TOML.
	Warnings []string `toml:"-"`
}

// GeneralConfig holds general system settings.
type GeneralConfig struct {
	// Mode is DEPRECATED and ignored: the binary determines the mode
	// (nevr-ac = offline analysis, nevr-server = live ingestion). The field
	// remains only so older files still decode; the loader warns when it is set.
	Mode       string `toml:"mode"`
	LogLevel   string `toml:"log_level"`  // "debug","info","warn","error"
	LogFormat  string `toml:"log_format"` // "json" or "text"
	MaxWorkers int    `toml:"max_workers"`
	DBPath     string `toml:"db_path"`
}

// PhysicsConfig holds game physics constants. Constants() converts it into
// the model.PhysicsConstants copied onto every MatchContext (adapter.Mapper
// SetPhysics, ingest.MatchManager, replay reader), which is what the
// detectors and the arena-bounds validator actually read.
type PhysicsConfig struct {
	DiscSpeedCap   float64 `toml:"disc_speed_cap"`
	BoostSpeedCap  float64 `toml:"boost_speed_cap"`
	MaxPlayerSpeed float64 `toml:"max_player_speed"`
	MaxThrowSpeed  float64 `toml:"max_throw_speed"`
	StunDuration   float64 `toml:"stun_duration"`
	ShieldCooldown float64 `toml:"shield_cooldown"`
	ImmunityWindow float64 `toml:"immunity_window"`
	GrabRange      float64 `toml:"grab_range"`
	// GoalZ is the |Z| coordinate of both goal centres (goals sit at
	// (0, 0, +-goal_z)). Used by the throw feature extractor for goal-directed
	// throws (THROW_005 / THROW_006). Confirmed from a real replay: 36.078.
	GoalZ float64 `toml:"goal_z"`
}

// Constants converts the config block into model.PhysicsConstants. Fields
// left at zero fall back to model.DefaultPhysics(); arena extents are not
// configurable and always come from DefaultPhysics.
func (p PhysicsConfig) Constants() model.PhysicsConstants {
	ph := model.DefaultPhysics()
	if p.DiscSpeedCap > 0 {
		ph.DiscSpeedCap = p.DiscSpeedCap
	}
	if p.BoostSpeedCap > 0 {
		ph.BoostSpeedCap = p.BoostSpeedCap
	}
	if p.MaxPlayerSpeed > 0 {
		ph.MaxPlayerSpeed = p.MaxPlayerSpeed
	}
	if p.MaxThrowSpeed > 0 {
		ph.MaxThrowSpeed = p.MaxThrowSpeed
	}
	if p.StunDuration > 0 {
		ph.StunDuration = p.StunDuration
	}
	if p.ShieldCooldown > 0 {
		ph.ShieldCooldown = p.ShieldCooldown
	}
	if p.ImmunityWindow > 0 {
		ph.ImmunityWindow = p.ImmunityWindow
	}
	if p.GrabRange > 0 {
		ph.GrabRange = p.GrabRange
	}
	if p.GoalZ > 0 {
		ph.GoalZ = p.GoalZ
	}
	return ph
}

// PipelineConfig holds pipeline processing settings.
type PipelineConfig struct {
	HistoryWindow                 int     `toml:"history_window"`
	MinFrameDt                    float64 `toml:"min_frame_dt"`
	MaxFrameDt                    float64 `toml:"max_frame_dt"`
	MaxEventsPerPlayerPerDetector int     `toml:"max_events_per_player_per_detector"`
	// HighPingThresholdMs feeds pipeline.FeatureExtractor.SetHighPingThreshold:
	// players above it get PlayerState.IsHighPing (confidence reduction).
	HighPingThresholdMs float64 `toml:"high_ping_threshold_ms"`
	CooldownFrames      int     `toml:"cooldown_frames"`
}

// ScoringConfig holds suspicion scoring settings.
//
// review_threshold is the score at which a player enters the moderator
// review queue. It maps onto the high_risk tier boundary
// (model.LevelTable.WithReviewThreshold), so review_threshold and high_risk
// are the same knob; when they differ review_threshold wins and the loader
// warns. The five tier keys populate model.LevelTable and must be
// non-decreasing.
type ScoringConfig struct {
	DecayHalfLifeHours            float64 `toml:"decay_half_life_hours"`
	ReviewThreshold               float64 `toml:"review_threshold"`
	AutoEnforceThreshold          float64 `toml:"auto_enforce_threshold"`
	MinMatchesForCrossMatch       int     `toml:"min_matches_for_cross_match"`
	MaxSingleContribution         float64 `toml:"max_single_contribution"`
	MaxContribPerDetectorPerMatch int     `toml:"max_contrib_per_detector_per_match"`
	CorrelationBonusCap           float64 `toml:"correlation_bonus_cap"`
	SameCategoryDiminishing       float64 `toml:"same_category_diminishing"`

	// Tier boundaries (inclusive lower bounds, 0-100).
	Informational float64 `toml:"informational"`
	Suspicious    float64 `toml:"suspicious"`
	HighRisk      float64 `toml:"high_risk"`
	Critical      float64 `toml:"critical"`
	ActionWorthy  float64 `toml:"action_worthy"`
}

// Tiers returns the raw tier table as configured (before review_threshold is
// applied). A zero table means "use model.DefaultLevelTable()".
func (s ScoringConfig) Tiers() model.LevelTable {
	return model.LevelTable{
		Informational: s.Informational,
		Suspicious:    s.Suspicious,
		HighRisk:      s.HighRisk,
		Critical:      s.Critical,
		ActionWorthy:  s.ActionWorthy,
	}
}

// LevelTable returns the tier table every score consumer should classify
// with: the configured tiers with review_threshold mapped onto high_risk.
// Pass it as scoring.ScorerConfig.Levels, review.Queue.SetLevels,
// enforce.EngineConfig.Levels and evidence.NewBuilderWithLevels.
func (s ScoringConfig) LevelTable() model.LevelTable {
	t := s.Tiers()
	if t.IsZero() {
		t = model.DefaultLevelTable()
	}
	return t.WithReviewThreshold(s.ReviewThreshold)
}

// BaselineConfig holds baseline modeling settings. EXPERIMENTAL: unused.
type BaselineConfig struct {
	MinBaselineMatches     int     `toml:"min_baseline_matches"`
	BaselineUpdateInterval int     `toml:"baseline_update_interval"`
	PlayerBaselineWeight   float64 `toml:"player_baseline_weight"`
}

// ShadowConfig holds shadow mode settings. ShadowDetectors is an additional
// list of detector IDs forced into shadow mode; it does NOT override a
// per-detector mode that is already "shadow" (that is the normal case) and an
// empty list overrides nothing.
type ShadowConfig struct {
	ShadowDetectors []string `toml:"shadow_detectors"`
}

// ServerConfig holds the live ingestion server (cmd/server) settings. Each
// key mirrors a cmd/server flag; the flag, when given, wins. Defaults equal
// ingest.DefaultServerConfig() and the flag defaults.
type ServerConfig struct {
	Listen                string `toml:"listen"`
	Metrics               string `toml:"metrics"`
	AllowUnauthenticated  bool   `toml:"allow_unauthenticated"`
	MaxMatches            int    `toml:"max_matches"`
	MaxPlayersPerMatch    int    `toml:"max_players_per_match"`
	MaxConnections        int    `toml:"max_connections"`
	MaxMessageBytes       int    `toml:"max_message_bytes"`
	MaxFrameRatePerPlayer int    `toml:"max_frame_rate_per_player"`
	// Durations accept TOML strings such as "5m" or "30s".
	IdleTimeout     time.Duration `toml:"idle_timeout"`
	StaleMatchAfter time.Duration `toml:"stale_match_after"`
	PersistInterval time.Duration `toml:"persist_interval"`
}

// DetectorConfig holds per-detector configuration.
//
// Mode: only "shadow" changes behaviour (events are stored with is_shadow=1
// and never scored). "review" and "enforce" are accepted and both mean "not
// shadow" (events are scored); there is no separate enforcement mode wired
// to enforce.Engine yet.
type DetectorConfig struct {
	Enabled           bool           `toml:"enabled"`
	EnforcementWeight float64        `toml:"enforcement_weight"`
	AutoEnforce       bool           `toml:"auto_enforce"`
	Mode              string         `toml:"mode"` // "shadow", "review", "enforce"
	Params            map[string]any `toml:"params"`
}

// clone returns a copy whose Params map is independent of the receiver.
func (dc DetectorConfig) clone() DetectorConfig {
	out := dc
	out.Params = make(map[string]any, len(dc.Params))
	for k, v := range dc.Params {
		out.Params[k] = v
	}
	return out
}

// GetDetectorConfig returns config for a detector, or a sensible default
// (disabled, shadow, weight 0.5, no params) for an unknown ID.
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

// IsDetectorShadow returns whether a detector is in shadow mode: its own mode
// is "shadow", or it is listed in shadow.shadow_detectors, or it has no
// config entry at all (unknown detectors default to shadow for safety).
func (c *Config) IsDetectorShadow(id string) bool {
	for _, sid := range c.Shadow.ShadowDetectors {
		if sid == id {
			return true
		}
	}
	if dc, ok := c.Detectors[id]; ok {
		return dc.Mode == "shadow"
	}
	return true
}

// DetectorIDs returns the configured detector IDs in sorted order.
func (c *Config) DetectorIDs() []string {
	ids := make([]string, 0, len(c.Detectors))
	for id := range c.Detectors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// LoadConfig loads configuration from a TOML file, overlaying it onto
// DefaultConfig() (see the package doc for the overlay semantics) and
// validating the result. An empty path returns the validated defaults.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		cfg := DefaultConfig()
		if err := Validate(cfg); err != nil {
			return nil, fmt.Errorf("validating config: %w", err)
		}
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return loadFromString(string(data))
}

// LoadConfigFromReader loads configuration from a reader with the same
// semantics as LoadConfig.
func LoadConfigFromReader(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return loadFromString(string(data))
}

func loadFromString(data string) (*Config, error) {
	cfg := DefaultConfig()
	var overlay Config
	md, err := toml.Decode(data, &overlay)
	if err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	var errs []string
	warnings := applyOverlay(cfg, &overlay, md, &errs)
	if len(errs) > 0 {
		return nil, fmt.Errorf("config errors:\n  - %s", strings.Join(errs, "\n  - "))
	}
	vwarn, err := ValidateWithWarnings(cfg)
	warnings = append(warnings, vwarn...)
	if err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}
	if len(warnings) > 0 {
		cfg.Warnings = warnings
	}
	return cfg, nil
}

// applyOverlay merges the decoded file onto cfg field by field. It returns
// the warnings it produced and appends hard errors to errs.
func applyOverlay(cfg, overlay *Config, md toml.MetaData, errs *[]string) []string {
	var warnings []string

	// Unknown or deprecated keys outside the detector map. Params entries are
	// decoded into map[string]any so they never appear here; they are checked
	// per detector below.
	for _, k := range md.Undecoded() {
		path := k.String()
		if msg, ok := deprecatedTopLevelKeys[path]; ok {
			warnings = append(warnings, fmt.Sprintf("%s is deprecated: %s", path, msg))
			continue
		}
		*errs = append(*errs, fmt.Sprintf("unknown key %q", path))
	}

	overlayStruct(reflect.ValueOf(&cfg.General).Elem(), reflect.ValueOf(&overlay.General).Elem(), md, "general")
	overlayStruct(reflect.ValueOf(&cfg.Physics).Elem(), reflect.ValueOf(&overlay.Physics).Elem(), md, "physics")
	overlayStruct(reflect.ValueOf(&cfg.Pipeline).Elem(), reflect.ValueOf(&overlay.Pipeline).Elem(), md, "pipeline")
	overlayStruct(reflect.ValueOf(&cfg.Scoring).Elem(), reflect.ValueOf(&overlay.Scoring).Elem(), md, "scoring")
	overlayStruct(reflect.ValueOf(&cfg.Baseline).Elem(), reflect.ValueOf(&overlay.Baseline).Elem(), md, "baseline")
	overlayStruct(reflect.ValueOf(&cfg.Shadow).Elem(), reflect.ValueOf(&overlay.Shadow).Elem(), md, "shadow")
	overlayStruct(reflect.ValueOf(&cfg.Server).Elem(), reflect.ValueOf(&overlay.Server).Elem(), md, "server")

	if md.IsDefined("general", "mode") {
		warnings = append(warnings, "general.mode is deprecated: "+deprecatedTopLevelKeys["general.mode"])
	}
	if md.IsDefined("scoring", "review_threshold") && md.IsDefined("scoring", "high_risk") &&
		cfg.Scoring.ReviewThreshold != cfg.Scoring.HighRisk {
		warnings = append(warnings, fmt.Sprintf("scoring.high_risk (%g) is overridden by scoring.review_threshold (%g); they are the same boundary",
			cfg.Scoring.HighRisk, cfg.Scoring.ReviewThreshold))
	}

	ids := make([]string, 0, len(overlay.Detectors))
	for id := range overlay.Detectors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		src := overlay.Detectors[id]
		spec, known := detectorSpecs[id]
		if !known {
			*errs = append(*errs, fmt.Sprintf("detector.%s: unknown detector ID (known: %s)", id, strings.Join(KnownDetectorIDs(), ", ")))
			continue
		}
		dst, ok := cfg.Detectors[id]
		if !ok {
			dst = cfg.GetDetectorConfig(id)
		}
		dst = dst.clone()
		if md.IsDefined("detector", id, "enabled") {
			dst.Enabled = src.Enabled
		}
		if md.IsDefined("detector", id, "enforcement_weight") {
			dst.EnforcementWeight = src.EnforcementWeight
		}
		if md.IsDefined("detector", id, "auto_enforce") {
			dst.AutoEnforce = src.AutoEnforce
		}
		if md.IsDefined("detector", id, "mode") {
			dst.Mode = src.Mode
		}
		if md.IsDefined("detector", id, "params") {
			w := mergeParams(spec, dst.Params, src.Params, errs)
			warnings = append(warnings, w...)
		}
		cfg.Detectors[id] = dst
	}
	return warnings
}

// overlayStruct copies every field of src whose toml key is defined in the
// file onto dst. Both values must be addressable structs of the same type.
func overlayStruct(dst, src reflect.Value, md toml.MetaData, section string) {
	t := dst.Type()
	for idx := 0; idx < t.NumField(); idx++ {
		tag := t.Field(idx).Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if md.IsDefined(section, name) {
			dst.Field(idx).Set(src.Field(idx))
		}
	}
}

// mergeParams overlays src onto dst per key, rewriting aliases to their
// canonical key, dropping removed keys with a warning, rejecting unknown or
// reserved keys and normalising value types to the spec. Keys are processed
// in sorted order so warnings and errors are deterministic.
func mergeParams(spec DetectorSpec, dst, src map[string]any, errs *[]string) []string {
	var warnings []string
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		val := src[key]
		if msg, ok := reservedParamKeys[key]; ok {
			*errs = append(*errs, fmt.Sprintf("detector.%s.params.%s: %s", spec.ID, key, msg))
			continue
		}
		if msg, ok := spec.Removed[key]; ok {
			warnings = append(warnings, fmt.Sprintf("detector.%s.params.%s is no longer read and was ignored: %s", spec.ID, key, msg))
			continue
		}
		p, viaAlias, ok := spec.param(key)
		if !ok {
			*errs = append(*errs, fmt.Sprintf("detector.%s.params.%s: unknown parameter (accepted: %s)",
				spec.ID, key, strings.Join(spec.AcceptedKeys(), ", ")))
			continue
		}
		norm, err := normalizeParamValue(p, val)
		if err != nil {
			*errs = append(*errs, fmt.Sprintf("detector.%s.params.%s: %v", spec.ID, key, err))
			continue
		}
		if viaAlias {
			warnings = append(warnings, fmt.Sprintf("detector.%s.params.%s is a deprecated alias of %s; the value was applied to %s",
				spec.ID, key, p.Key, p.Key))
		}
		dst[p.Key] = norm
	}
	return warnings
}

// normalizeParamValue converts a decoded TOML (or Go literal) value into the
// canonical Go type for the spec: int, float64, bool or []string.
func normalizeParamValue(p ParamSpec, val any) (any, error) {
	switch p.Type {
	case ParamInt:
		switch v := val.(type) {
		case int:
			return v, nil
		case int64:
			return int(v), nil
		case float64:
			if v != float64(int64(v)) {
				return nil, fmt.Errorf("expected an integer, got %v", v)
			}
			return int(v), nil
		case float32:
			if float64(v) != float64(int64(v)) {
				return nil, fmt.Errorf("expected an integer, got %v", v)
			}
			return int(v), nil
		}
	case ParamFloat:
		switch v := val.(type) {
		case float64:
			return v, nil
		case float32:
			return float64(v), nil
		case int:
			return float64(v), nil
		case int64:
			return float64(v), nil
		}
	case ParamBool:
		if v, ok := val.(bool); ok {
			return v, nil
		}
	case ParamStringList:
		switch v := val.(type) {
		case []string:
			return append([]string(nil), v...), nil
		case []any:
			out := make([]string, 0, len(v))
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("expected a list of strings, found %T", item)
				}
				out = append(out, s)
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("expected %s, got %T (%v)", p.Type, val, val)
}
