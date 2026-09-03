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
	if !strings.Contains(readme, "THROW_002 | Impossible Disc Acceleration | throw | 0.7 | Unverified") {
		t.Error("README must keep THROW_002 at Unverified (no status upgrades without real data)")
	}
}

var (
	readmeCommand   = regexp.MustCompile(`nevr-ac(?:\s+--config\s+\S+)?\s+([a-z][a-z-]*)`)
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
