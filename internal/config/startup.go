package config

import (
	"fmt"
	"io"
	"log/slog"
)

// LogStartup reports what a loaded config resolved to, once per process.
//
// Every entry of cfg.Warnings (deprecated keys, ignored values) is logged at
// Warn so an operator running a stale file sees it at every start. When
// table is non-nil the effective per-detector table (EffectiveTable) is
// announced at Info and reported, so moderators can verify their edits took
// effect. cmd/server always passes os.Stderr; the CLI only does so under
// --verbose (or NEVR_AC_VERBOSE=1) so report output stays clean.
//
// How the table is reported depends on general.log_format: with "json" the
// log stream must stay one JSON record per line, so every detector becomes
// one Info record ("effective detector" with id, enabled, mode, shadow,
// weight, auto_enforce, params) and nothing is written to table; with "text"
// the fixed-width FormatEffectiveTable block is written to table.
func LogStartup(logger *slog.Logger, cfg *Config, table io.Writer) {
	if cfg == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	for _, w := range cfg.Warnings {
		logger.Warn("config warning", "warning", w)
	}
	if table == nil {
		return
	}
	rows := cfg.EffectiveTable()
	logger.Info("effective detector configuration", "detectors", len(rows), "format", cfg.General.LogFormat)
	if cfg.General.LogFormat == "json" {
		for _, r := range rows {
			logger.Info("effective detector", effectiveDetectorAttrs(r)...)
		}
		return
	}
	fmt.Fprint(table, FormatEffectiveTable(rows))
}

// effectiveDetectorAttrs renders one EffectiveDetector as slog attributes.
// Params are a nested group keyed by param name; a value that falls back to
// the constructor default is listed under params_from_constructor.
func effectiveDetectorAttrs(r EffectiveDetector) []any {
	params := make([]any, 0, len(r.Params))
	var fromCtor []string
	for _, p := range r.Params {
		params = append(params, slog.Any(p.Key, p.Value))
		if p.Source != "config" {
			fromCtor = append(fromCtor, p.Key)
		}
	}
	attrs := []any{
		"id", r.ID, "name", r.Name, "enabled", r.Enabled, "mode", r.Mode, "shadow", r.Shadow,
		"weight", r.Weight, "auto_enforce", r.AutoEnforce, slog.Group("params", params...),
	}
	if len(fromCtor) > 0 {
		attrs = append(attrs, "params_from_constructor", fromCtor)
	}
	if len(r.Unknown) > 0 {
		attrs = append(attrs, "unknown_keys", r.Unknown)
	}
	return attrs
}
