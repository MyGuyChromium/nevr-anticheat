package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn,
		"warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "bogus": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNewLoggerTo_FormatAndLevel(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, "warn", "json")
	l.Info("hidden")
	l.Warn("shown", "k", "v")
	out := buf.String()
	if strings.Contains(out, "hidden") || !strings.Contains(out, `"msg":"shown"`) || !strings.Contains(out, `"k":"v"`) {
		t.Errorf("json output = %q", out)
	}

	buf.Reset()
	l = NewLoggerTo(&buf, "debug", "text")
	l.Debug("dbg", "n", 1)
	if out := buf.String(); !strings.Contains(out, "msg=dbg") || !strings.Contains(out, "n=1") {
		t.Errorf("text output = %q", out)
	}

	if NewLogger("info", "json") == nil {
		t.Error("NewLogger returned nil")
	}
}
