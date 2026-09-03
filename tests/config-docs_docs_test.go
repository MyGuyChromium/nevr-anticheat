package tests

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/ingest"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

var detectorRow = regexp.MustCompile(`(?m)^\|\s*([A-Z]+_\d{3})\s*\|\s*([^|]+?)\s*\|\s*([a-z]+)\s*\|\s*([0-9.]+)\s*\|`)

// TestConfigDocs_ReadmeDetectorTable: the README catalog must list exactly the
// detectors the config layer (and the detect catalog) know, with the config
// weights scoring actually uses and the constructors' names/categories.
func TestConfigDocs_ReadmeDetectorTable(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	rows := detectorRow.FindAllStringSubmatch(readme, -1)
	if len(rows) == 0 {
		t.Fatal("README has no detector table rows")
	}
	cfg := config.DefaultConfig()
	seen := map[string]bool{}
	for _, r := range rows {
		id, name, category, weightStr := r[1], strings.TrimSpace(r[2]), r[3], r[4]
		if seen[id] {
			t.Errorf("README lists %s twice", id)
		}
		seen[id] = true
		spec, ok := config.DetectorSpecFor(id)
		if !ok {
			t.Errorf("README lists unknown detector %s", id)
			continue
		}
		if name != spec.Name {
			t.Errorf("README %s name %q != code name %q", id, name, spec.Name)
		}
		if category != spec.Category {
			t.Errorf("README %s category %q != code category %q", id, category, spec.Category)
		}
		weight, err := strconv.ParseFloat(weightStr, 64)
		if err != nil || weight != cfg.Detectors[id].EnforcementWeight {
			t.Errorf("README %s weight %s != config enforcement_weight %v", id, weightStr, cfg.Detectors[id].EnforcementWeight)
		}
	}
	want := config.KnownDetectorIDs()
	got := make([]string, 0, len(seen))
	for id := range seen {
		got = append(got, id)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("README detector IDs = %v\nwant %v", got, want)
	}
	for _, desc := range detect.Catalog() {
		if !seen[desc.ID] {
			t.Errorf("catalog detector %s missing from README", desc.ID)
		}
	}
}

// TestConfigDocs_Throw002StatusIsAligned: THROW_002's label is "Unverified:
// v2.0.0 single-delta approach, needs real-data calibration" (the owner's
// choice in 3ab978b, where the BROKEN mechanism was removed). Every artifact
// must carry that label, nothing may still call the detector BROKEN as its
// current status, and nothing may upgrade it to a validated status.
func TestConfigDocs_Throw002StatusIsAligned(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	if !strings.Contains(readme, "| THROW_002 | Impossible Disc Acceleration | throw | 0.7 | Unverified — v2.0.0 single-delta approach, needs real-data calibration |") {
		t.Error("README THROW_002 row must read: Unverified — v2.0.0 single-delta approach, needs real-data calibration")
	}
	readiness := readRepoFile(t, "docs/production_readiness.md")
	if !strings.Contains(readiness, "| THROW_002 | **UNVERIFIED** |") {
		t.Error("production_readiness.md must list THROW_002 as **UNVERIFIED**")
	}
	if strings.Contains(readiness, "| THROW_002 | **BROKEN** |") || strings.Contains(readiness, "1 broken") {
		t.Error("production_readiness.md still carries the retired BROKEN status for THROW_002")
	}
	for _, must := range []string{"v2.0.0", "removed", "unvalidated"} {
		if !strings.Contains(readiness, must) {
			t.Errorf("production_readiness.md must say the BROKEN mechanism was %s", must)
		}
	}
	for _, rel := range []string{"configs/default.toml", "configs/shadow_deploy.toml", "internal/config/defaults.go", "docs/shadow_deployment_guide.md"} {
		src := readRepoFile(t, rel)
		idx := strings.Index(src, "THROW_002")
		if idx < 0 {
			t.Errorf("%s does not mention THROW_002", rel)
			continue
		}
		// The status comment sits within a few lines of the first mention
		// (before the key in defaults.go, after the header in the TOML files).
		window := src[max(0, idx-400):min(len(src), idx+600)]
		if !strings.Contains(window, "UNVERIFIED") || !strings.Contains(window, "v2.0.0") || !strings.Contains(window, "real-data calibration") {
			t.Errorf("%s: THROW_002 must be labelled UNVERIFIED (v2.0.0 single-delta approach, needs real-data calibration); got:\n%s", rel, window)
		}
	}
	// The label is not an upgrade: THROW_002 ships disabled in shadow.
	dc := config.DefaultConfig().Detectors["THROW_002"]
	if dc.Enabled || dc.Mode != "shadow" {
		t.Errorf("THROW_002 default config must stay disabled/shadow, got %+v", dc)
	}
	for _, forbidden := range []string{"THROW_002 | Impossible Disc Acceleration | throw | 0.7 | Physics-grounded", "THROW_002 | Impossible Disc Acceleration | throw | 0.7 | Validated"} {
		if strings.Contains(readme, forbidden) {
			t.Errorf("README upgrades THROW_002: %s", forbidden)
		}
	}
}

// TestConfigDocs_StartupTableClaims: the docs must not claim both binaries
// print the effective table unconditionally; nevr-ac shows it only with
// --verbose / -v / NEVR_AC_VERBOSE=1 (cmd/anticheat/main.go), nevr-server
// always (cmd/server/main.go).
func TestConfigDocs_StartupTableClaims(t *testing.T) {
	mainSrc := readRepoFile(t, "cmd/anticheat/main.go")
	if !strings.Contains(mainSrc, `"verbose"`) || !strings.Contains(mainSrc, "NEVR_AC_VERBOSE") || !strings.Contains(mainSrc, "if startupVerbose") {
		t.Fatalf("cmd/anticheat/main.go no longer gates the table on --verbose; update this test and the docs")
	}
	if !strings.Contains(readRepoFile(t, "cmd/server/main.go"), "config.LogStartup(logger, cfg, os.Stderr)") {
		t.Fatalf("cmd/server/main.go no longer reports the table unconditionally; update this test and the docs")
	}
	for _, rel := range []string{"README.md", "configs/default.toml", "configs/shadow_deploy.toml", "docs/shadow_deployment_guide.md", "docs/operator_checklist.md", "docs/deployment_plan.md"} {
		src := readRepoFile(t, rel)
		for _, stale := range []string{"Both binaries print the effective", "Both binaries log the effective", "table both binaries log"} {
			if strings.Contains(src, stale) {
				t.Errorf("%s still claims %q; nevr-ac prints the table only with --verbose", rel, stale)
			}
		}
		if !strings.Contains(src, "--verbose") {
			t.Errorf("%s mentions the effective table without saying nevr-ac needs --verbose", rel)
		}
	}
	guide := readRepoFile(t, "docs/shadow_deployment_guide.md")
	if !strings.Contains(guide, "./nevr-ac --verbose --config configs/shadow_deploy.toml analyze match.echoreplay") {
		t.Error("shadow guide Step 2 must run analyze with --verbose so the table it tells the operator to check is printed")
	}
	checklist := readRepoFile(t, "docs/operator_checklist.md")
	if strings.Contains(checklist, "shadow_deploy.toml version` loads") {
		t.Error("operator_checklist.md must not claim `nevr-ac version` loads the config (it returns before any config is read)")
	}
	if !strings.Contains(checklist, "./nevr-ac --verbose --config configs/shadow_deploy.toml flagged") {
		t.Error("operator_checklist.md config check must use a command that loads the config (flagged with --verbose)")
	}
}

// TestConfigDocs_ProvenanceLivesOnMatchContext: docs must describe
// server_id as recorded on the match context, not on every event row.
func TestConfigDocs_ProvenanceLivesOnMatchContext(t *testing.T) {
	for _, rel := range []string{"README.md", "docs/telemetry_contract.md", "docs/deployment_plan.md", "docs/production_readiness.md", "docs/shadow_deployment_guide.md"} {
		src := readRepoFile(t, rel)
		if strings.Contains(src, "`server_id` on every event tells") {
			t.Errorf("%s claims server_id is on every event", rel)
		}
		if !strings.Contains(src, "match context") {
			t.Errorf("%s must say server_id provenance lives on the match context", rel)
		}
	}
}

// TestConfigDocs_MaxFrameDtSemantics: pipeline.max_frame_dt is the feature
// extractor's gap threshold (default 0.5 = pipeline.MaxFrameDt); the
// validator's rejection bound is pipeline.MaxProducerDt (60 s). The docs
// must say exactly that and never the old "dt > max*2.5 is rejected".
func TestConfigDocs_MaxFrameDtSemantics(t *testing.T) {
	cfg := config.DefaultConfig()
	if cfg.Pipeline.MaxFrameDt != pipeline.MaxFrameDt {
		t.Errorf("default max_frame_dt %v != pipeline.MaxFrameDt %v", cfg.Pipeline.MaxFrameDt, pipeline.MaxFrameDt)
	}
	if config.MaxProducerDtSeconds != pipeline.MaxProducerDt {
		t.Errorf("config.MaxProducerDtSeconds %v != pipeline.MaxProducerDt %v", config.MaxProducerDtSeconds, pipeline.MaxProducerDt)
	}
	for _, rel := range []string{"configs/default.toml", "docs/telemetry_contract.md", "internal/config/config.go"} {
		src := readRepoFile(t, rel)
		for _, stale := range []string{"max*2.5", "max_frame_dt × 2.5", "dt > 0.5` → frame rejected"} {
			if strings.Contains(src, stale) {
				t.Errorf("%s still documents the old rejection bound %q", rel, stale)
			}
		}
		if !strings.Contains(src, "60") || !strings.Contains(src, "gap") {
			t.Errorf("%s must document max_frame_dt as the extractor gap threshold and 60 s as the rejection bound", rel)
		}
	}
}

var (
	readmeCommand   = regexp.MustCompile(`nevr-ac(?:\s+(?:--verbose|-v|--config\s+\S+))*\s+([a-z][a-z-]*)`)
	dispatchCommand = regexp.MustCompile(`(?m)^\s*case "([a-z][a-z-]*)":`)
)

// TestConfigDocs_ReadmeCommandsExist: every `nevr-ac <command>` in the README
// and the operator docs is dispatched by cmd/anticheat/main.go, and every
// dispatched command is documented in the README.
func TestConfigDocs_ReadmeCommandsExist(t *testing.T) {
	mainSrc := readRepoFile(t, "cmd/anticheat/main.go")
	dispatch := map[string]bool{}
	for _, m := range dispatchCommand.FindAllStringSubmatch(mainSrc, -1) {
		dispatch[m[1]] = true
	}
	if len(dispatch) < 10 {
		t.Fatalf("parsed only %d dispatch cases from cmd/anticheat/main.go: %v", len(dispatch), dispatch)
	}
	documented := map[string]bool{}
	for _, rel := range []string{"README.md", "docs/shadow_deployment_guide.md", "docs/operator_checklist.md", "docs/deployment_plan.md", "docs/production_readiness.md"} {
		src := readRepoFile(t, rel)
		for _, m := range readmeCommand.FindAllStringSubmatch(src, -1) {
			cmd := m[1]
			if rel == "README.md" {
				documented[cmd] = true
			}
			if !dispatch[cmd] {
				t.Errorf("%s references `nevr-ac %s`, which cmd/anticheat/main.go does not dispatch", rel, cmd)
			}
		}
	}
	for cmd := range dispatch {
		if !documented[cmd] {
			t.Errorf("cmd/anticheat/main.go dispatches %q but README.md never shows `nevr-ac %s`", cmd, cmd)
		}
	}
}

var metricName = regexp.MustCompile(`nevr_ac_[a-z_]+`)

// TestConfigDocs_MetricNamesExist: operator docs may only name metrics the
// Prometheus exporter really renders (F93 / live-ingest crossCutting).
func TestConfigDocs_MetricNamesExist(t *testing.T) {
	exporter := readRepoFile(t, "internal/metrics/prometheus.go")
	for _, rel := range []string{"README.md", "docs/shadow_deployment_guide.md", "docs/operator_checklist.md", "docs/deployment_plan.md", "docs/telemetry_contract.md", "docs/production_readiness.md"} {
		src := readRepoFile(t, rel)
		for _, name := range metricName.FindAllString(src, -1) {
			if !strings.Contains(exporter, `"`+name+`"`) {
				t.Errorf("%s names metric %s, which internal/metrics/prometheus.go does not export", rel, name)
			}
		}
	}
}

var contractField = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|")

// TestConfigDocs_ContractFieldsExist: every field named in the telemetry
// contract's tables must be a JSON key the decoder actually reads.
func TestConfigDocs_ContractFieldsExist(t *testing.T) {
	known := map[string]bool{"holder_id": true} // accepted alias of possessor_id/is_held
	for _, typ := range []reflect.Type{
		reflect.TypeOf(model.PlayerTelemetryFrame{}), reflect.TypeOf(model.DiscState{}),
		reflect.TypeOf(model.ControlMessage{}), reflect.TypeOf(ingest.FrameBatch{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if tag != "" && tag != "-" {
				known[tag] = true
			}
		}
	}
	src := readRepoFile(t, "docs/telemetry_contract.md")
	for _, m := range contractField.FindAllStringSubmatch(src, -1) {
		if !known[m[1]] {
			t.Errorf("telemetry_contract.md documents field `%s`, which no wire struct decodes", m[1])
		}
	}
	for _, must := range []string{"`possessor_id`", "`is_held`", "`holder_id`", "`team`", "21", "82"} {
		if !strings.Contains(src, must) {
			t.Errorf("telemetry_contract.md must mention %s", must)
		}
	}
}

// TestConfigDocs_DocsDoNotReferenceDeadConfigKeys: renamed/removed keys may
// appear in the docs only inside the migration table of default.toml.
func TestConfigDocs_DocsDoNotReferenceDeadConfigKeys(t *testing.T) {
	dead := []string{"speed_threshold", "max_frame_gap_ms", "playspace_abuse_samples", "min_incidents_to_surface",
		"max_stddev_frames", "boost_margin", "max_release_acceleration", "min_bhattacharyya", "speed_distance_tolerance",
		"active_only", "in_match_decay_points_per_min", "clean_match_reset_count", "auto_enforce_min_confidence"}
	for _, rel := range []string{"README.md", "docs/shadow_deployment_guide.md", "docs/operator_checklist.md", "docs/deployment_plan.md", "docs/production_readiness.md", "configs/shadow_deploy.toml"} {
		src := readRepoFile(t, rel)
		for _, k := range dead {
			if strings.Contains(src, k) {
				t.Errorf("%s still references the dead config key %s", rel, k)
			}
		}
	}
}
