package sqlite

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestMatchTelemetryHealthRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	if _, _, err := s.GetMatchTelemetryHealth(ctx, "M1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing document: err=%v, want ErrNotFound", err)
	}
	for _, bad := range []struct{ match, source, doc string }{
		{"", TelemetryHealthSourceAnalysis, `{}`},
		{"M1", "guess", `{}`},
		{"M1", TelemetryHealthSourceAnalysis, `{not json`},
		{"M1", TelemetryHealthSourceAnalysis, ``},
	} {
		if err := s.StoreMatchTelemetryHealth(ctx, bad.match, bad.source, []byte(bad.doc)); err == nil {
			t.Fatalf("accepted invalid document %+v", bad)
		}
	}

	written, err := s.StoreMatchTelemetryHealthIfAbsent(ctx, "M1", TelemetryHealthSourceBackfill, []byte(`{"v":"backfill"}`))
	if err != nil || !written {
		t.Fatalf("first backfill: written=%v err=%v", written, err)
	}
	if err := s.StoreMatchTelemetryHealth(ctx, "M1", TelemetryHealthSourceAnalysis, []byte(`{"v":"analysis"}`)); err != nil {
		t.Fatal(err)
	}
	// A slow backfill that finishes after an analysis must not overwrite it.
	written, err = s.StoreMatchTelemetryHealthIfAbsent(ctx, "M1", TelemetryHealthSourceBackfill, []byte(`{"v":"late backfill"}`))
	if err != nil || written {
		t.Fatalf("late backfill: written=%v err=%v", written, err)
	}
	doc, source, err := s.GetMatchTelemetryHealth(ctx, "M1")
	if err != nil || source != TelemetryHealthSourceAnalysis || string(doc) != `{"v":"analysis"}` {
		t.Fatalf("document = %s source=%q err=%v", doc, source, err)
	}
	if err := s.DeleteMatchTelemetryHealth(ctx, "M1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetMatchTelemetryHealth(ctx, "M1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: err=%v", err)
	}
}

func TestRawTickScansCountsWholeMatchReads(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	if _, _, err := s.RestoreMatchRawTicks(ctx, "M1", map[int]string{0: `{"a":1}`, 1: `{"a":2}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMatchRawTicks(ctx, "M1", 0, 0); err != nil {
		t.Fatal(err)
	}
	if got := s.RawTickScans(); got != 0 {
		t.Fatalf("a bounded frame-range read counted as a whole-match scan: %d", got)
	}
	if _, err := s.GetAllMatchRawTicks(ctx, "M1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForEachMatchTick(ctx, "M1", func(int, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := s.RawTickScans(); got != 2 {
		t.Fatalf("RawTickScans = %d, want 2", got)
	}
}

// bootstrapSchemaBefore builds a database by hand with every migration below
// version, the way an older build would have left it.
func bootstrapSchemaBefore(t *testing.T, dbPath string, version int) *sql.DB {
	t.Helper()
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,description TEXT,applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.Version >= version {
			break
		}
		if err := applyMigration(raw, m); err != nil {
			t.Fatal(err)
		}
	}
	return raw
}

func TestMigration20AddsTelemetryHealthWithoutTouchingStoredMatches(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v19.db")
	raw := bootstrapSchemaBefore(t, dbPath, 20)
	if _, err := raw.Exec(`INSERT INTO match_ticks(match_id, frame_index, raw_json) VALUES('OLD', 0, '{"sessionid":"OLD"}')`); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='match_telemetry_health'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("v19 bootstrap already has the table: %d %v", tables, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s := newTestStoreAt(t, dbPath)
	if v, err := AppliedSchemaVersion(s.DB()); err != nil || v != SchemaVersion() || v < 20 {
		t.Fatalf("applied version %d err=%v", v, err)
	}
	// The migration adds the table only; it never invents a document. Old
	// matches are backfilled lazily by the viewer.
	if _, _, err := s.GetMatchTelemetryHealth(t.Context(), "OLD"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("migration invented a health document: %v", err)
	}
	ticks, err := s.GetAllMatchRawTicks(t.Context(), "OLD")
	if err != nil || len(ticks) != 1 {
		t.Fatalf("stored raw ticks damaged: %v %v", ticks, err)
	}
	if err := s.StoreMatchTelemetryHealth(t.Context(), "OLD", TelemetryHealthSourceBackfill, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(s.DB()); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
}
