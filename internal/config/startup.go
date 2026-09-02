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
// announced at Info and written to table, so moderators can verify their
// edits took effect. cmd/server always passes os.Stderr; the CLI only does
// so under --verbose (or NEVR_AC_VERBOSE=1) so report output stays clean.
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
	logger.Info("effective detector configuration", "detectors", len(rows))
	fmt.Fprint(table, FormatEffectiveTable(rows))
}
