package config

import (
	"math"
	"strings"
	"testing"
)

func TestAutopocketOlderConfigReceivesObservationDefaults(t *testing.T) {
	// Installed configurations written before STATE_008 contain only a database
	// path. The detector must arrive through the normal defaults overlay.
	cfg := mustLoad(t, "[general]\ndb_path = './existing-evidence.db'\n")
	dc, ok := cfg.Detectors["STATE_008"]
	if !ok || !dc.Enabled || dc.Mode != "shadow" || dc.EnforcementWeight != 0 || dc.AutoEnforce || !cfg.IsDetectorShadow("STATE_008") {
		t.Fatalf("older config did not inherit observation-only STATE_008: %+v", dc)
	}
	if cfg.General.DBPath != "./existing-evidence.db" {
		t.Fatal("older installed database path changed")
	}
	if len(dc.Params) != 15 || dc.Params["baseline_samples"] != 4 || dc.Params["max_sample_gap_s"] != 0.12 || dc.Params["min_turn_rate_deg_s"] != 60.0 {
		t.Fatalf("incomplete STATE_008 defaults: %+v", dc.Params)
	}
	for _, row := range cfg.EffectiveTable() {
		if row.ID == "STATE_008" {
			if !row.Enabled || row.Mode != "shadow" || row.Weight != 0 || row.AutoEnforce {
				t.Fatalf("effective table misrepresents STATE_008: %+v", row)
			}
			return
		}
	}
	t.Fatal("STATE_008 absent from effective table")
}

func TestAutopocketPerSampleAngleKeysWarnWithoutChangingTimeDefaults(t *testing.T) {
	cfg := mustLoad(t, "[detector.STATE_008.params]\nmin_correction_angle_deg = 9.0\nmax_turn_angle_deg = 10.0\n")
	params := cfg.Detectors["STATE_008"].Params
	if _, ok := params["min_correction_angle_deg"]; ok {
		t.Fatal("removed per-sample correction key survived loading")
	}
	if _, ok := params["max_turn_angle_deg"]; ok {
		t.Fatal("removed per-sample maximum key survived loading")
	}
	if params["min_turn_rate_deg_s"] != 60.0 || params["max_turn_rate_deg_s"] != 300.0 || params["min_correction_duration_s"] != 0.12 {
		t.Fatalf("per-sample angles were silently reinterpreted as time-based values: %v", params)
	}
	if !hasWarning(cfg, "min_correction_angle_deg") || !hasWarning(cfg, "max_turn_angle_deg") {
		t.Fatalf("old configuration needs explicit migration warnings: %v", cfg.Warnings)
	}
}

func TestAutopocketConfigCannotPromoteObservation(t *testing.T) {
	for name, override := range map[string]string{
		"review":            "mode = 'review'",
		"enforce":           "mode = 'enforce'",
		"empty mode":        "mode = ''",
		"scoring weight":    "enforcement_weight = 0.01",
		"negative weight":   "enforcement_weight = -0.01",
		"automatic action":  "auto_enforce = true",
		"disabled promoted": "enabled = false\nmode = 'review'",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, "[detector.STATE_008]\n"+override+"\n")
			if err == nil || !strings.Contains(err.Error(), "detector.STATE_008 is observation-only") {
				t.Fatalf("unsafe observation configuration accepted: %v", err)
			}
		})
	}
	// Programmatic configuration cannot bypass the zero-weight rule with NaN.
	cfg := DefaultConfig()
	dc := cfg.Detectors["STATE_008"]
	dc.EnforcementWeight = math.NaN()
	cfg.Detectors["STATE_008"] = dc
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "observation-only") {
		t.Fatalf("non-finite observation weight accepted: %v", err)
	}
}

func TestAutopocketCanBeDisabledOrTunedWithinShadow(t *testing.T) {
	cfg := mustLoad(t, "[detector.STATE_008]\nenabled = false\n[detector.STATE_008.params]\nmin_correction_samples = 3\nmax_window_s = 1.0\n")
	dc := cfg.Detectors["STATE_008"]
	if dc.Enabled || dc.Mode != "shadow" || dc.EnforcementWeight != 0 || dc.AutoEnforce {
		t.Fatalf("disabling STATE_008 changed its safety posture: %+v", dc)
	}
	if dc.Params["min_correction_samples"] != 3 || dc.Params["max_window_s"] != 1.0 || dc.Params["baseline_samples"] != 4 {
		t.Fatalf("partial parameter overlay discarded defaults: %+v", dc.Params)
	}
}
