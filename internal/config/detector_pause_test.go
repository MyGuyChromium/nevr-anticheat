package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlayspacePauseOverridesSavedAndProgrammaticConfig(t *testing.T) {
	for _, id := range []string{"MOV_006", "PAT_005"} {
		t.Run(id, func(t *testing.T) {
			if DefaultConfig().Detectors[id].Enabled {
				t.Fatal("paused detector enabled in defaults")
			}
			cfg := mustLoad(t, "[detector."+id+"]\nenabled=true\nmode=\"review\"\nauto_enforce=true\nenforcement_weight=1\n")
			if !hasWarning(cfg, "detector."+id+".enabled=true is ignored") {
				t.Fatalf("older override was not explained: %v", cfg.Warnings)
			}
			before := cfg.Detectors[id]
			check := func() {
				t.Helper()
				dc := cfg.GetDetectorConfig(id)
				if dc.Enabled || dc.AutoEnforce || dc.EnforcementWeight != 0 || dc.Mode != "shadow" || cfg.IsDetectorEnabled(id) || !cfg.IsDetectorShadow(id) {
					t.Fatalf("saved config bypassed pause: %+v", dc)
				}
				for _, row := range cfg.EffectiveTable() {
					if row.ID == id && (row.Enabled || row.Weight != 0 || row.AutoEnforce || !row.Shadow || row.PauseReason == "") {
						t.Fatalf("effective config misrepresents pause: %+v", row)
					}
				}
			}
			check()
			cfg.Detectors[id] = before // models an older JSON profile applied after loading
			check()
			if !reflect.DeepEqual(before, cfg.Detectors[id]) {
				t.Fatal("resolving policy rewrote stored parameters/settings")
			}
			if !strings.Contains(FormatEffectiveTable(cfg.EffectiveTable()), "PAUSED:") {
				t.Fatal("startup table does not explain the pause")
			}
		})
	}
}

func TestPlayspacePausePreservesFocusChecksAndShippedConfigs(t *testing.T) {
	for _, path := range []string{"", "../../configs/default.toml", "../../configs/shadow_deploy.toml"} {
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"MOV_006", "PAT_005"} {
			if cfg.IsDetectorEnabled(id) {
				t.Fatalf("%s enables %s", path, id)
			}
		}
		for _, id := range []string{"THROW_003", "BIO_001", "STATE_001", "STATE_008"} {
			if !cfg.IsDetectorEnabled(id) || !cfg.IsDetectorShadow(id) || DetectorPauseReason(id) != "" {
				t.Fatalf("%s changed focus detector %s", path, id)
			}
		}
	}
}
