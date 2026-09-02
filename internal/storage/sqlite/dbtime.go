package sqlite

import (
	"fmt"
	"strings"
	"time"
)

// dbTimeLayout is the single timestamp representation used for every write and
// every range comparison in the database: UTC, RFC3339, second precision,
// literal 'Z' suffix. Because all values share the same shape, lexicographic
// TEXT comparison in SQLite is equivalent to chronological comparison.
//
// Legacy rows written by earlier versions used SQLite's datetime('now')
// ('YYYY-MM-DD HH:MM:SS', UTC). Migration 10 rewrites those in place; parseDBTime
// still accepts the legacy layout so a partially migrated file cannot produce
// zero timestamps.
const dbTimeLayout = "2006-01-02T15:04:05Z"

// legacyDBTimeLayout is the SQLite datetime('now') layout used before migration 10.
const legacyDBTimeLayout = "2006-01-02 15:04:05"

// dbTimeSQLDefault is the SQL expression used as a column DEFAULT for
// freshly created tables. Every insert still supplies an explicit Go-side
// value; the default only exists so ad-hoc inserts stay in the same layout.
const dbTimeSQLDefault = "(strftime('%Y-%m-%dT%H:%M:%SZ','now'))"

// fmtDBTime renders t in dbTimeLayout (UTC). A zero time renders as the
// current time so that DEFAULT-style semantics are preserved by callers that
// pass an unset timestamp.
func fmtDBTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format(dbTimeLayout)
}

// fmtDBTimeOrEmpty renders t in dbTimeLayout, or returns "" for a zero time.
// Used for optional columns (e.g. match_start_time) where "unknown" must stay NULL.
func fmtDBTimeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(dbTimeLayout)
}

// parseDBTime parses a timestamp written by fmtDBTime, an RFC3339 value with an
// offset (older Go-side writes), or the legacy datetime('now') layout.
// The result is always in UTC.
func parseDBTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	if t, err := time.Parse(dbTimeLayout, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(legacyDBTimeLayout, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp %q", s)
}

// parseDBTimeLenient is parseDBTime for read paths where a malformed value must
// not abort the whole result set; it returns the zero time on failure.
func parseDBTimeLenient(s string) time.Time {
	t, err := parseDBTime(s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func nowUTC() time.Time { return time.Now().UTC() }
