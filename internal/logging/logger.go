// Package logging provides structured logging configuration.
package logging

import (
	"io"
	"log/slog"
	"os"
)

// NewLogger creates a configured slog.Logger writing to stderr.
//
// Logs go to stderr so that CLI report output on stdout can be piped, diffed
// or consumed by bots without filtering migration and pipeline log lines.
func NewLogger(level, format string) *slog.Logger {
	return NewLoggerTo(os.Stderr, level, format)
}

// NewLoggerTo creates a configured slog.Logger writing to w.
// level is one of debug/info/warn/error (default info); format is "text" or
// "json" (default json).
func NewLoggerTo(w io.Writer, level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: ParseLevel(level)}
	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}
	return slog.New(handler)
}

// ParseLevel maps a config level string to slog.Level (default Info).
func ParseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
