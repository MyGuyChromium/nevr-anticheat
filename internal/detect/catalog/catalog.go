// Package catalog builds the production detector set from configuration.
//
// It is the single place cmd/anticheat, cmd/server and the test harness get
// their detectors from: importing it registers every detector sub-package
// (throw, bio, movement, state, pattern) in detect.Catalog, and Build applies
// each detector's [detector.<ID>] config block (enabled, enforcement_weight,
// auto_enforce, params) through detect.BuildAll so the three binaries can
// never drift apart or hand-roll separate detector lists again.
package catalog

import (
	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/pattern"

	// Register every detector sub-package in the process-wide catalog.
	_ "github.com/nevr-anticheat/nevr-anticheat/internal/detect/bio"
	_ "github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	_ "github.com/nevr-anticheat/nevr-anticheat/internal/detect/state"
	_ "github.com/nevr-anticheat/nevr-anticheat/internal/detect/throw"
)

// HistoryProviderParam is the params key through which PAT_003 receives its
// cross-match history provider.
const HistoryProviderParam = "history_provider"

// Options returns the detect.BuildOptions mapping for cfg: a detector is
// built when its config block says enabled, its configured
// enforcement_weight and auto_enforce are applied (HasWeight/HasAuto), and
// its params are copied so injecting the history provider never mutates the
// shared config. historyProvider (may be nil) is passed to PAT_003 only.
func Options(cfg *config.Config, historyProvider pattern.HistoryProvider) func(id string) detect.BuildOptions {
	return func(id string) detect.BuildOptions {
		dc := cfg.GetDetectorConfig(id)
		params := make(map[string]any, len(dc.Params)+1)
		for k, v := range dc.Params {
			params[k] = v
		}
		if id == "PAT_003" && historyProvider != nil {
			params[HistoryProviderParam] = historyProvider
		}
		return detect.BuildOptions{
			Enabled:     dc.Enabled,
			Weight:      dc.EnforcementWeight,
			HasWeight:   true,
			AutoEnforce: dc.AutoEnforce,
			HasAuto:     true,
			Params:      params,
		}
	}
}

// Build constructs every enabled detector from cfg in ID order. Call it once
// per match (detectors are stateful) or per pipeline.
func Build(cfg *config.Config, historyProvider pattern.HistoryProvider) []detect.Detector {
	return detect.BuildAll(Options(cfg, historyProvider))
}

// Names maps every catalogued detector ID to its human-readable name (for
// review case evidence).
func Names() map[string]string {
	cat := detect.Catalog()
	names := make(map[string]string, len(cat))
	for _, d := range cat {
		names[d.ID] = d.Name
	}
	return names
}
