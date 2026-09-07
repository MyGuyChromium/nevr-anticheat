package tests

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/ingest"
)

const (
	defaultTomlPath      = "../configs/default.toml"
	shadowDeployTomlPath = "../configs/shadow_deploy.toml"
)

// TestConfigDocs_DefaultTomlEqualsDefaultConfig: the three copies of the
// defaults (defaults.go, default.toml, constructor fallbacks) used to drift
// (F188). defaults.go and default.toml must now be identical, and the
// constructor fallbacks are reported through EffectiveTable.
func TestConfigDocs_DefaultTomlEqualsDefaultConfig(t *testing.T) {
	loaded, err := config.LoadConfig(defaultTomlPath)
	if err != nil {
		t.Fatalf("configs/default.toml does not load: %v", err)
	}
	if len(loaded.Warnings) != 0 {
		t.Fatalf("configs/default.toml produced warnings: %v", loaded.Warnings)
	}
	def := config.DefaultConfig()
	if err := config.Validate(def); err != nil {
		t.Fatalf("DefaultConfig does not validate: %v", err)
	}

	// Section by section so a mismatch names the field.
	for _, sec := range []struct {
		name     string
		got, def any
	}{
		{"general", loaded.General, def.General},
		{"physics", loaded.Physics, def.Physics},
		{"pipeline", loaded.Pipeline, def.Pipeline},
		{"scoring", loaded.Scoring, def.Scoring},
		{"baseline", loaded.Baseline, def.Baseline},
		{"shadow", loaded.Shadow, def.Shadow},
		{"server", loaded.Server, def.Server},
	} {
		if !reflect.DeepEqual(sec.got, sec.def) {
			t.Errorf("[%s] default.toml = %+v\n           defaults.go = %+v", sec.name, sec.got, sec.def)
		}
	}
	for _, id := range def.DetectorIDs() {
		got, ok := loaded.Detectors[id]
		if !ok {
			t.Errorf("detector %s missing from default.toml", id)
			continue
		}
		want := def.Detectors[id]
		if got.Enabled != want.Enabled || got.Mode != want.Mode || got.EnforcementWeight != want.EnforcementWeight || got.AutoEnforce != want.AutoEnforce {
			t.Errorf("detector %s header: toml=%+v go=%+v", id, headerOf(got), headerOf(want))
		}
		for k, wv := range want.Params {
			gv, ok := got.Params[k]
			if !ok {
				t.Errorf("detector %s: param %s in defaults.go but not in default.toml", id, k)
				continue
			}
			if !reflect.DeepEqual(gv, wv) {
				t.Errorf("detector %s: param %s toml=%v (%T) go=%v (%T)", id, k, gv, gv, wv, wv)
			}
		}
		for k := range got.Params {
			if _, ok := want.Params[k]; !ok {
				t.Errorf("detector %s: param %s in default.toml but not in defaults.go", id, k)
			}
		}
	}
	for id := range loaded.Detectors {
		if _, ok := def.Detectors[id]; !ok {
			t.Errorf("detector %s in default.toml but not in defaults.go", id)
		}
	}
	if !reflect.DeepEqual(loaded, def) {
		t.Errorf("LoadConfig(default.toml) != DefaultConfig() (see field diffs above)")
	}
}

func headerOf(dc config.DetectorConfig) string {
	dc.Params = nil
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(strings.TrimSuffix(
		reflect.ValueOf(dc).String(), ">"), "<"), "\n", " "), "  ", " ")) + " " +
		strings.Join([]string{boolStr(dc.Enabled), dc.Mode}, "/")
}

func boolStr(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

// TestConfigDocs_DefaultTomlDefinesEveryKey: DeepEqual cannot see a key that
// is simply absent from the file (the default fills it in), so every
// toml-tagged field of every section must be explicitly defined in
// default.toml (the deprecated general.mode is the one exception).
func TestConfigDocs_DefaultTomlDefinesEveryKey(t *testing.T) {
	data, err := os.ReadFile(defaultTomlPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw config.Config
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		t.Fatal(err)
	}
	if undecoded := md.Undecoded(); len(undecoded) != 0 {
		t.Fatalf("default.toml carries keys the Config struct does not know: %v", undecoded)
	}
	sections := map[string]any{
		"general": config.GeneralConfig{}, "physics": config.PhysicsConfig{}, "pipeline": config.PipelineConfig{},
		"scoring": config.ScoringConfig{}, "baseline": config.BaselineConfig{}, "shadow": config.ShadowConfig{},
		"server": config.ServerConfig{},
	}
	for section, zero := range sections {
		st := reflect.TypeOf(zero)
		for i := 0; i < st.NumField(); i++ {
			tag := strings.Split(st.Field(i).Tag.Get("toml"), ",")[0]
			if tag == "" || tag == "-" {
				continue
			}
			if section == "general" && tag == "mode" {
				continue // deprecated; must NOT be in the file
			}
			if !md.IsDefined(section, tag) {
				t.Errorf("default.toml: [%s] is missing key %s", section, tag)
			}
		}
	}
	if md.IsDefined("general", "mode") {
		t.Error("default.toml still sets the deprecated general.mode")
	}
	for _, id := range config.KnownDetectorIDs() {
		for _, key := range []string{"enabled", "enforcement_weight", "mode"} {
			if !md.IsDefined("detector", id, key) {
				t.Errorf("default.toml: [detector.%s] is missing %s", id, key)
			}
		}
	}
}

// TestConfigDocs_ConfigsUseOnlyCanonicalKeys: the shipped TOML files must
// not rely on deprecated aliases or removed keys (F15/F26/F90/F221).
func TestConfigDocs_ConfigsUseOnlyCanonicalKeys(t *testing.T) {
	for _, path := range []string{defaultTomlPath, shadowDeployTomlPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var raw struct {
			Detector map[string]struct {
				Params map[string]any `toml:"params"`
			} `toml:"detector"`
		}
		if _, err := toml.Decode(string(data), &raw); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for id, dc := range raw.Detector {
			spec, ok := config.DetectorSpecFor(id)
			if !ok {
				t.Errorf("%s: unknown detector %s", path, id)
				continue
			}
			canonical := map[string]bool{}
			for _, k := range spec.AcceptedKeys() {
				canonical[k] = true
			}
			for k := range dc.Params {
				if !canonical[k] {
					t.Errorf("%s: detector.%s.params.%s is not a canonical key (accepted: %v)", path, id, k, spec.AcceptedKeys())
				}
			}
		}
	}
}

// TestConfigDocs_ShadowDeployLoadsAndIsShadowOnly: the first-deployment
// config must load cleanly, keep every detector in shadow mode and never
// grant auto-enforce.
func TestConfigDocs_ShadowDeployLoadsAndIsShadowOnly(t *testing.T) {
	cfg, err := config.LoadConfig(shadowDeployTomlPath)
	if err != nil {
		t.Fatalf("configs/shadow_deploy.toml does not load: %v", err)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("shadow_deploy.toml produced warnings: %v", cfg.Warnings)
	}
	if len(cfg.Detectors) != len(config.KnownDetectorIDs()) {
		t.Fatalf("shadow_deploy.toml configures %d detectors, want %d", len(cfg.Detectors), len(config.KnownDetectorIDs()))
	}
	var enabled []string
	for _, id := range cfg.DetectorIDs() {
		dc := cfg.Detectors[id]
		if dc.Mode != "shadow" || !cfg.IsDetectorShadow(id) {
			t.Errorf("%s is not in shadow mode (mode=%q)", id, dc.Mode)
		}
		if dc.AutoEnforce {
			t.Errorf("%s has auto_enforce=true in the shadow config", id)
		}
		if dc.Enabled {
			enabled = append(enabled, id)
		}
		// The overlay must have kept the calibrated params from default.toml.
		if len(config.DefaultConfig().Detectors[id].Params) != len(dc.Params) {
			t.Errorf("%s: params were not inherited from the defaults (%d vs %d keys)", id, len(dc.Params), len(config.DefaultConfig().Detectors[id].Params))
		}
	}
	sort.Strings(enabled)
	want := []string{"BIO_001", "BIO_002", "BIO_003", "BIO_004", "MOV_001", "MOV_002", "MOV_006", "PAT_004", "PAT_005",
		"STATE_001", "STATE_002", "STATE_007", "STATE_008", "THROW_001", "THROW_003", "THROW_005", "THROW_006", "THROW_008"}
	if !reflect.DeepEqual(enabled, want) {
		t.Errorf("enabled detectors = %v\nwant %v", enabled, want)
	}
	if cfg.General.DBPath != "./nevr-ac-shadow.db" || cfg.Pipeline.MaxEventsPerPlayerPerDetector != 50 {
		t.Errorf("shadow overrides not applied: %+v %+v", cfg.General, cfg.Pipeline)
	}
	// Inherited from the defaults.
	if cfg.Scoring.ReviewThreshold != 60 || cfg.Physics.DiscSpeedCap != 18.9 || cfg.Pipeline.HistoryWindow != 30 {
		t.Errorf("defaults not inherited: %+v %+v", cfg.Scoring, cfg.Pipeline)
	}
}

// TestConfigDocs_ServerDurationsRejectBareNumbers: the shipped files write
// durations as strings; a copy that drops the quotes must fail at load with
// an error that names the key, not silently run with nanosecond deadlines.
func TestConfigDocs_ServerDurationsRejectBareNumbers(t *testing.T) {
	data, err := os.ReadFile(defaultTomlPath)
	if err != nil {
		t.Fatal(err)
	}
	src := strings.Replace(string(data), `idle_timeout              = "5m"`, `idle_timeout              = 300`, 1)
	if src == string(data) {
		t.Fatal("default.toml no longer carries idle_timeout = \"5m\"; update this test")
	}
	_, err = config.LoadConfigFromReader(strings.NewReader(src))
	if err == nil || !strings.Contains(err.Error(), "server.idle_timeout") {
		t.Fatalf("idle_timeout = 300 (300 ns) must be a load error naming the key, got %v", err)
	}
}

// TestConfigDocs_ServerDefaultsMatchIngest: the [server] section documents
// the cmd/server flag defaults; they must agree with ingest's own defaults.
func TestConfigDocs_ServerDefaultsMatchIngest(t *testing.T) {
	sv := config.DefaultConfig().Server
	in := ingest.DefaultServerConfig()
	if sv.Listen != in.ListenAddr {
		t.Errorf("listen: %q vs %q", sv.Listen, in.ListenAddr)
	}
	if sv.MaxConnections != in.MaxConnectionsPerServer {
		t.Errorf("max_connections: %d vs %d", sv.MaxConnections, in.MaxConnectionsPerServer)
	}
	if sv.MaxMessageBytes != in.MaxFrameSize {
		t.Errorf("max_message_bytes: %d vs %d", sv.MaxMessageBytes, in.MaxFrameSize)
	}
	if sv.MaxFrameRatePerPlayer != in.MaxFrameRatePerPlayer {
		t.Errorf("max_frame_rate_per_player: %d vs %d", sv.MaxFrameRatePerPlayer, in.MaxFrameRatePerPlayer)
	}
	if sv.MaxPlayersPerMatch != in.MaxPlayersPerMatch {
		t.Errorf("max_players_per_match: %d vs %d", sv.MaxPlayersPerMatch, in.MaxPlayersPerMatch)
	}
	if sv.IdleTimeout != in.ReadTimeout {
		t.Errorf("idle_timeout: %v vs %v", sv.IdleTimeout, in.ReadTimeout)
	}
	if sv.MaxMatches != ingest.DefaultMaxMatches {
		t.Errorf("max_matches: %d vs %d", sv.MaxMatches, ingest.DefaultMaxMatches)
	}
	if sv.PersistInterval != ingest.DefaultPersistInterval {
		t.Errorf("persist_interval: %v vs %v", sv.PersistInterval, ingest.DefaultPersistInterval)
	}
}

// TestConfigDocs_ScoringContract: review_threshold is the high_risk boundary
// and the tier table equals the README bands.
func TestConfigDocs_ScoringContract(t *testing.T) {
	cfg := config.DefaultConfig()
	lt := cfg.Scoring.LevelTable()
	if lt.Informational != 20 || lt.Suspicious != 40 || lt.HighRisk != 60 || lt.Critical != 80 || lt.ActionWorthy != 95 {
		t.Fatalf("default tiers = %+v", lt)
	}
	if cfg.Scoring.ReviewThreshold != lt.HighRisk {
		t.Fatalf("review_threshold %v != high_risk %v", cfg.Scoring.ReviewThreshold, lt.HighRisk)
	}
	if cfg.Scoring.AutoEnforceThreshold != lt.ActionWorthy {
		t.Fatalf("auto_enforce_threshold %v != action_worthy %v", cfg.Scoring.AutoEnforceThreshold, lt.ActionWorthy)
	}
}
