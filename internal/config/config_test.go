package config

import (
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func load(t *testing.T, src string) (*Config, error) {
	t.Helper()
	return LoadConfigFromReader(strings.NewReader(src))
}

func mustLoad(t *testing.T, src string) *Config {
	t.Helper()
	cfg, err := load(t, src)
	if err != nil {
		t.Fatalf("load failed: %v\n--- config ---\n%s", err, src)
	}
	return cfg
}

func hasWarning(cfg *Config, substr string) bool {
	for _, w := range cfg.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func TestDefaultConfigValidatesWithoutWarnings(t *testing.T) {
	warnings, err := ValidateWithWarnings(DefaultConfig())
	if err != nil {
		t.Fatalf("DefaultConfig does not validate: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("DefaultConfig produced warnings: %v", warnings)
	}
	if len(DefaultConfig().Detectors) != len(KnownDetectorIDs()) {
		t.Fatalf("DefaultConfig has %d detectors, spec table has %d", len(DefaultConfig().Detectors), len(KnownDetectorIDs()))
	}
}

func TestLoadConfigEmptyPathReturnsValidatedDefaults(t *testing.T) {
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scoring.ReviewThreshold != 60 || cfg.Detectors["MOV_001"].Mode != "shadow" {
		t.Fatalf("unexpected defaults: %+v", cfg.Scoring)
	}
}

// F24/F25/F58: a two-line override must not drop the detector out of shadow
// mode or discard its calibrated params.
func TestOverlay_PartialDetectorSectionKeepsDefaults(t *testing.T) {
	cfg := mustLoad(t, "[detector.MOV_001]\nenabled = true\n")
	dc := cfg.Detectors["MOV_001"]
	if !dc.Enabled {
		t.Fatal("enabled not applied")
	}
	if dc.Mode != "shadow" {
		t.Fatalf("mode = %q, want shadow (default must survive a partial section)", dc.Mode)
	}
	if dc.EnforcementWeight != 0.7 {
		t.Fatalf("weight = %v, want 0.7", dc.EnforcementWeight)
	}
	if got := dc.Params["max_legitimate_speed"]; got != 55.0 {
		t.Fatalf("max_legitimate_speed = %v (%T), want 55 (calibrated default)", got, got)
	}
	if got := dc.Params["sustained_speed_window"]; got != 30 {
		t.Fatalf("sustained_speed_window = %v (%T), want 30", got, got)
	}
	if !cfg.IsDetectorShadow("MOV_001") {
		t.Fatal("IsDetectorShadow false after partial override")
	}
}

func TestOverlay_ParamsOnlySectionDoesNotDisable(t *testing.T) {
	cfg := mustLoad(t, "[detector.THROW_001.params]\nbase_tolerance = 2.5\n")
	dc := cfg.Detectors["THROW_001"]
	if !dc.Enabled {
		t.Fatal("params-only section disabled the detector")
	}
	if dc.Mode != "shadow" || dc.EnforcementWeight != 0.8 {
		t.Fatalf("mode/weight lost: %+v", dc)
	}
	if dc.Params["base_tolerance"] != 2.5 {
		t.Fatalf("override not applied: %v", dc.Params["base_tolerance"])
	}
	if dc.Params["ping_tolerance_scalar"] != 5.0 || dc.Params["cap_riding_cooldown_frames"] != 900 {
		t.Fatalf("sibling params lost: %v", dc.Params)
	}
	// The loaded map must be independent of the defaults returned by later calls.
	if DefaultConfig().Detectors["THROW_001"].Params["base_tolerance"] != 1.3 {
		t.Fatal("overlay mutated the shared defaults")
	}
}

func TestOverlay_DisableOnly(t *testing.T) {
	cfg := mustLoad(t, "[detector.THROW_001]\nenabled = false\n")
	dc := cfg.Detectors["THROW_001"]
	if dc.Enabled || dc.Mode != "shadow" || len(dc.Params) == 0 {
		t.Fatalf("disable-only override broke the entry: %+v", dc)
	}
}

func TestOverlay_ScalarSectionsMergePerField(t *testing.T) {
	cfg := mustLoad(t, "[pipeline]\nhistory_window = 45\n[scoring]\nreview_threshold = 70\nhigh_risk = 70\n")
	if cfg.Pipeline.HistoryWindow != 45 {
		t.Fatalf("history_window = %d", cfg.Pipeline.HistoryWindow)
	}
	if cfg.Pipeline.MaxFrameDt != 0.2 || cfg.Pipeline.CooldownFrames != 300 {
		t.Fatalf("sibling pipeline keys lost: %+v", cfg.Pipeline)
	}
	if cfg.Scoring.ReviewThreshold != 70 || cfg.Scoring.DecayHalfLifeHours != 168 {
		t.Fatalf("scoring overlay wrong: %+v", cfg.Scoring)
	}
	if lt := cfg.Scoring.LevelTable(); lt.HighRisk != 70 || lt.Suspicious != 40 || lt.Critical != 80 {
		t.Fatalf("level table = %+v", lt)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", cfg.Warnings)
	}
}

func TestOverlay_ServerDurations(t *testing.T) {
	cfg := mustLoad(t, "[server]\nidle_timeout = \"90s\"\nmax_matches = 8\n")
	if cfg.Server.IdleTimeout != 90*time.Second || cfg.Server.MaxMatches != 8 {
		t.Fatalf("server overlay: %+v", cfg.Server)
	}
	if cfg.Server.StaleMatchAfter != 30*time.Minute || cfg.Server.Listen != ":8080" {
		t.Fatalf("server defaults lost: %+v", cfg.Server)
	}
}

func TestOverlay_AliasIsNormalizedAndWarns(t *testing.T) {
	cfg := mustLoad(t, "[detector.MOV_003.params]\nmin_reversal_angle = 170\n")
	dc := cfg.Detectors["MOV_003"]
	if _, stale := dc.Params["min_reversal_angle"]; stale {
		t.Fatal("alias key left in params; the constructor would prefer the canonical default and ignore the override")
	}
	if got := dc.Params["min_angle_deg"]; got != 170.0 {
		t.Fatalf("min_angle_deg = %v (%T), want 170 (float)", got, got)
	}
	if !hasWarning(cfg, "min_reversal_angle is a deprecated alias of min_angle_deg") {
		t.Fatalf("no alias warning: %v", cfg.Warnings)
	}
}

func TestOverlay_RemovedKeyWarnsAndIsDropped(t *testing.T) {
	cfg := mustLoad(t, "[detector.THROW_001.params]\nspeed_threshold = 20.0\n[detector.MOV_002.params]\nmax_frame_gap_ms = 200.0\n")
	if _, ok := cfg.Detectors["THROW_001"].Params["speed_threshold"]; ok {
		t.Fatal("removed key kept")
	}
	if !hasWarning(cfg, "THROW_001.params.speed_threshold") || !hasWarning(cfg, "MOV_002.params.max_frame_gap_ms") {
		t.Fatalf("missing removed-key warnings: %v", cfg.Warnings)
	}
	if cfg.Detectors["MOV_002"].Params["max_frame_gap"] != 5 {
		t.Fatal("max_frame_gap default lost")
	}
}

func TestOverlay_UnknownParamIsError(t *testing.T) {
	_, err := load(t, "[detector.MOV_001.params]\nmax_legit_speed = 40\n")
	if err == nil || !strings.Contains(err.Error(), "MOV_001.params.max_legit_speed: unknown parameter") {
		t.Fatalf("expected unknown-parameter error, got %v", err)
	}
}

func TestOverlay_UnknownDetectorIsError(t *testing.T) {
	_, err := load(t, "[detector.THROW_01]\nenabled = true\n")
	if err == nil || !strings.Contains(err.Error(), "unknown detector ID") {
		t.Fatalf("expected unknown-detector error, got %v", err)
	}
}

func TestOverlay_UnknownTopLevelKeysAreErrors(t *testing.T) {
	_, err := load(t, "[general]\nenforcment_typo = 1\n")
	if err == nil || !strings.Contains(err.Error(), `unknown key "general.enforcment_typo"`) {
		t.Fatalf("expected unknown-key error, got %v", err)
	}
	_, err = load(t, "[detector.MOV_001]\nenabld = true\n")
	if err == nil || !strings.Contains(err.Error(), "detector.MOV_001.enabld") {
		t.Fatalf("expected unknown detector field error, got %v", err)
	}
	_, err = load(t, "[nonsense]\nx = 1\n")
	if err == nil || !strings.Contains(err.Error(), "nonsense.x") {
		t.Fatalf("expected unknown section error, got %v", err)
	}
}

func TestOverlay_DeprecatedTopLevelKeysWarn(t *testing.T) {
	cfg := mustLoad(t, "[general]\nmode = \"online\"\n[scoring]\nclean_match_reset_count = 100\nin_match_decay_points_per_min = 2.0\n")
	if !hasWarning(cfg, "general.mode is deprecated") {
		t.Fatalf("general.mode not warned: %v", cfg.Warnings)
	}
	if !hasWarning(cfg, "scoring.clean_match_reset_count is deprecated") || !hasWarning(cfg, "scoring.in_match_decay_points_per_min is deprecated") {
		t.Fatalf("removed scoring keys not warned: %v", cfg.Warnings)
	}
}

func TestOverlay_WrongTypesAreErrors(t *testing.T) {
	cases := map[string]string{
		"int gets fraction":      "[detector.MOV_002.params]\nmax_frame_gap = 2.5\n",
		"float gets string":      "[detector.THROW_001.params]\nbase_tolerance = \"1.3\"\n",
		"list gets ints":         "[detector.PAT_003.params]\ntrusted_detectors = [1, 2]\n",
		"reserved key":           "[detector.PAT_003.params]\nhistory_provider = \"x\"\n",
		"trusted unknown id":     "[detector.PAT_003.params]\ntrusted_detectors = [\"NOPE_001\"]\n",
		"shadow list unknown id": "[shadow]\nshadow_detectors = [\"NOPE_001\"]\n",
	}
	for name, src := range cases {
		if _, err := load(t, src); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// Integral floats are accepted for int keys and ints for float keys.
	cfg := mustLoad(t, "[detector.MOV_002.params]\nmax_frame_gap = 7.0\nteleport_threshold = 9\n")
	if v, ok := cfg.Detectors["MOV_002"].Params["max_frame_gap"].(int); !ok || v != 7 {
		t.Fatalf("max_frame_gap = %v", cfg.Detectors["MOV_002"].Params["max_frame_gap"])
	}
	if v, ok := cfg.Detectors["MOV_002"].Params["teleport_threshold"].(float64); !ok || v != 9 {
		t.Fatalf("teleport_threshold = %v", cfg.Detectors["MOV_002"].Params["teleport_threshold"])
	}
}

func TestValidate_EnabledWithEmptyModeIsError(t *testing.T) {
	cfg := DefaultConfig()
	dc := cfg.Detectors["MOV_001"]
	dc.Mode = ""
	cfg.Detectors["MOV_001"] = dc
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "MOV_001.mode must be set") {
		t.Fatalf("expected empty-mode error, got %v", err)
	}
	dc.Enabled = false
	cfg.Detectors["MOV_001"] = dc
	if err := Validate(cfg); err != nil {
		t.Fatalf("disabled detector with empty mode should validate: %v", err)
	}
}

func TestValidate_TiersMustBeMonotonic(t *testing.T) {
	_, err := load(t, "[scoring]\nsuspicious = 80\nhigh_risk = 60\n")
	if err == nil || !strings.Contains(err.Error(), "scoring tiers") {
		t.Fatalf("expected tier error, got %v", err)
	}
	_, err = load(t, "[scoring]\nauto_enforce_threshold = 50\n")
	if err == nil || !strings.Contains(err.Error(), "auto_enforce_threshold") {
		t.Fatalf("expected auto-enforce error, got %v", err)
	}
	cfg := mustLoad(t, "[scoring]\nreview_threshold = 50\n")
	if !hasWarning(cfg, "review_threshold") {
		t.Fatalf("expected high_risk/review_threshold mismatch warning: %v", cfg.Warnings)
	}
	if lt := cfg.Scoring.LevelTable(); lt.HighRisk != 50 || lt.Suspicious != 40 {
		t.Fatalf("review_threshold must win: %+v", lt)
	}
}

func TestValidate_ReviewAndEnforceModesWarn(t *testing.T) {
	cfg := mustLoad(t, "[detector.MOV_001]\nmode = \"review\"\n")
	if !hasWarning(cfg, "MOV_001.mode=\"review\"") {
		t.Fatalf("expected scored-mode warning: %v", cfg.Warnings)
	}
	if cfg.IsDetectorShadow("MOV_001") {
		t.Fatal("review mode must not be shadow")
	}
}

func TestPhysicsConstants(t *testing.T) {
	cfg := DefaultConfig()
	got := cfg.Physics.Constants()
	if got != model.DefaultPhysics() {
		t.Fatalf("default physics config must equal model.DefaultPhysics: %+v vs %+v", got, model.DefaultPhysics())
	}
	if got.GoalZ != 36.078 {
		t.Fatalf("goal_z = %v", got.GoalZ)
	}
	cfg = mustLoad(t, "[physics]\ngoal_z = 40\n")
	if cfg.Physics.Constants().GoalZ != 40 || cfg.Physics.Constants().DiscSpeedCap != 18.7 {
		t.Fatalf("goal_z override wrong: %+v", cfg.Physics.Constants())
	}
	if !hasWarning(cfg, "[physics] differs") {
		t.Fatalf("expected physics-differs warning: %v", cfg.Warnings)
	}
	if (PhysicsConfig{}).Constants() != model.DefaultPhysics() {
		t.Fatal("zero physics config must fall back to defaults")
	}
	// Per-field fallback: a zero field keeps the engine default while the
	// others apply, and arena geometry is never configurable. This is the
	// contract the former pipeline.PhysicsFromConfig / ingest.physicsFromConfig
	// had, plus goal_z.
	cfg = DefaultConfig()
	cfg.Physics.MaxPlayerSpeed = 42
	cfg.Physics.DiscSpeedCap = 21.5
	cfg.Physics.GrabRange = 0 // unset -> default
	cfg.Physics.GoalZ = 0     // unset -> default
	ph := cfg.Physics.Constants()
	def := model.DefaultPhysics()
	if ph.MaxPlayerSpeed != 42 || ph.DiscSpeedCap != 21.5 || ph.GrabRange != def.GrabRange || ph.GoalZ != def.GoalZ ||
		ph.ArenaLength != def.ArenaLength || ph.BoostSpeedCap != def.BoostSpeedCap {
		t.Fatalf("per-field fallback wrong: %+v", ph)
	}
}

func TestLevelTableDefaults(t *testing.T) {
	lt := DefaultConfig().Scoring.LevelTable()
	if lt != model.DefaultLevelTable() {
		t.Fatalf("default level table = %+v, want %+v", lt, model.DefaultLevelTable())
	}
	if (ScoringConfig{ReviewThreshold: 30}).LevelTable().HighRisk != 30 {
		t.Fatal("zero tiers must fall back to the default table with review_threshold applied")
	}
}

func TestEffectiveTable(t *testing.T) {
	cfg := mustLoad(t, "[detector.MOV_004.params]\nboost_margin = 2.0\n")
	delete(cfg.Detectors, "STATE_003") // no entry at all: constructor fallbacks apply
	rows := cfg.EffectiveTable()
	if len(rows) != len(KnownDetectorIDs()) {
		t.Fatalf("rows = %d, want %d", len(rows), len(KnownDetectorIDs()))
	}
	byID := map[string]EffectiveDetector{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	mov1 := byID["MOV_001"]
	if mov1.Name != "Impossible Player Speed" || mov1.Weight != 0.7 || !mov1.Shadow {
		t.Fatalf("MOV_001 row: %+v", mov1)
	}
	for _, p := range mov1.Params {
		if p.Key == "max_legitimate_speed" && (p.Value != 55.0 || p.Source != "config") {
			t.Fatalf("MOV_001 max_legitimate_speed resolved wrong: %+v", p)
		}
	}
	st3 := byID["STATE_003"]
	if st3.Configured || st3.Enabled || st3.Mode != "shadow" {
		t.Fatalf("unconfigured detector row: %+v", st3)
	}
	for _, p := range st3.Params {
		if p.Source != "constructor" {
			t.Fatalf("unconfigured detector must show constructor fallbacks: %+v", p)
		}
		if p.Key == "suspicious_frames" && p.Value != 300 {
			t.Fatalf("constructor fallback wrong: %+v", p)
		}
	}
	mov4 := byID["MOV_004"]
	for _, p := range mov4.Params {
		if p.Key == "boost_cap_margin" && (p.Value != 2.0 || p.Source != "config") {
			t.Fatalf("alias override not resolved onto canonical key: %+v", p)
		}
	}
	text := FormatEffectiveTable(rows)
	if !strings.Contains(text, "STATE_003  off      shadow") || !strings.Contains(text, "suspicious_frames=300*") {
		t.Fatalf("formatted table missing expected rows:\n%s", text)
	}
	if !strings.Contains(text, "MOV_001    on       shadow   0.70") {
		t.Fatalf("formatted MOV_001 row wrong:\n%s", text)
	}
}

func TestEffectiveTable_UnknownEntriesAreListed(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Detectors["NOPE_001"] = DetectorConfig{Enabled: true, Mode: "shadow", Params: map[string]any{"x": 1}}
	dc := cfg.Detectors["MOV_001"]
	dc.Params["bogus"] = 1
	rows := cfg.EffectiveTable()
	var sawNope, sawBogus bool
	for _, r := range rows {
		if r.ID == "NOPE_001" && r.Name == "(unknown detector)" && len(r.Unknown) == 1 {
			sawNope = true
		}
		if r.ID == "MOV_001" && len(r.Unknown) == 1 && r.Unknown[0] == "bogus" {
			sawBogus = true
		}
	}
	if !sawNope || !sawBogus {
		t.Fatalf("unknown entries not surfaced (nope=%v bogus=%v)", sawNope, sawBogus)
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("Validate must reject unknown detector IDs and params")
	}
}

func TestSpecTableIsSelfConsistent(t *testing.T) {
	for _, spec := range DetectorSpecs() {
		seen := map[string]bool{}
		for _, p := range spec.Params {
			if seen[p.Key] {
				t.Errorf("%s: duplicate key %s", spec.ID, p.Key)
			}
			seen[p.Key] = true
			if _, err := normalizeParamValue(p, p.Default); err != nil {
				t.Errorf("%s.%s: default %v does not match type %s: %v", spec.ID, p.Key, p.Default, p.Type, err)
			}
			for _, a := range p.Aliases {
				if seen[a] {
					t.Errorf("%s: alias %s collides", spec.ID, a)
				}
				seen[a] = true
				if _, removed := spec.Removed[a]; removed {
					t.Errorf("%s: %s is both an alias and removed", spec.ID, a)
				}
			}
		}
		for k := range spec.Removed {
			if seen[k] {
				t.Errorf("%s: removed key %s is also accepted", spec.ID, k)
			}
		}
	}
}
