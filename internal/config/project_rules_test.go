package config

import (
	"fmt"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"testing"
)

func TestProjectRulesExplicitInclusiveAndSeparateFromCap(t *testing.T) {
	cfg := mustLoad(t, "")
	if cfg.ProjectRules != model.DefaultProjectRules() || cfg.ProjectRules.DiscGrabLimitM != .25 || cfg.ProjectRules.FastThrowSpeedMPS != 19 || cfg.ProjectRules.RequiredMovementMPS != 4.7 || cfg.Physics.DiscSpeedCap != 18.9 {
		t.Fatalf("%+v", cfg)
	}
	cfg = mustLoad(t, "[project_rules]\nversion = 'explicit-other-build-review'\n")
	if cfg.ProjectRules.DiscGrabLimitM != .25 || cfg.Physics.DiscSpeedCap != 18.9 || !hasWarning(cfg, "not engine verification") {
		t.Fatal(cfg)
	}
	for _, bad := range []string{"disc_grab_limit_m = 0", "disc_grab_limit_m = nan", "fast_throw_speed_mps = inf", "required_movement_mps = -1", "verified = true", "version = ''"} {
		if _, err := load(t, "[project_rules]\n"+bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestRemovedMechanicsHeuristicsWarnWithoutReinterpreting(t *testing.T) {
	for id, params := range map[string]string{"STATE_001": "grab_distance_threshold = 3\ndesync_margin = 0.2\nclosing_velocity_scale = 0.5\nsigmoid_steepness = 2", "THROW_005": "max_mean_deviation = 2\nmax_stddev_deviation = 1.5\nmin_throws_for_pattern = 8"} {
		cfg := mustLoad(t, fmt.Sprintf("[detector.%s]\nenforcement_weight = 0.8\nmode = 'review'\n[detector.%s.params]\n%s", id, id, params))
		if len(cfg.Detectors[id].Params) != 0 || cfg.ProjectRules != model.DefaultProjectRules() {
			t.Fatal("legacy threshold reinterpreted")
		}
		if len(cfg.Warnings) == 0 {
			t.Fatal("silent migration")
		}
		for _, row := range cfg.EffectiveTable() {
			if row.ID == id && (row.Weight != 0 || !row.Shadow || row.AutoEnforce) {
				t.Fatal("effective table permits scoring")
			}
		}
	}
}
