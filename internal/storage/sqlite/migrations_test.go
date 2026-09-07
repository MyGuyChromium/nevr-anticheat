package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrations_IdempotentAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m.db")
	s1 := newTestStoreAt(t, dbPath)
	v1, err := AppliedSchemaVersion(s1.DB())
	if err != nil {
		t.Fatal(err)
	}
	if v1 != SchemaVersion() {
		t.Fatalf("applied version %d, want %d", v1, SchemaVersion())
	}
	// Re-running on an open DB is a no-op.
	if err := RunMigrationsV2(s1.DB(), nil); err != nil {
		t.Fatalf("second RunMigrationsV2: %v", err)
	}
	s1.Close()

	s2 := newTestStoreAt(t, dbPath)
	if n := countRows(t, s2, "schema_migrations", ""); n != len(migrations) {
		t.Fatalf("schema_migrations rows = %d, want %d", n, len(migrations))
	}
	var uv int
	if err := s2.DB().QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatal(err)
	}
	if uv != SchemaVersion() {
		t.Fatalf("user_version = %d, want %d", uv, SchemaVersion())
	}
}

func TestMigrations_AllTimestampColumnsExist(t *testing.T) {
	s := newTestStore(t)
	for _, tc := range timestampColumns {
		rows, err := s.DB().Query("PRAGMA table_info(" + tc[0] + ")")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull int
			var dflt sql.NullString
			var pk int
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			if name == tc[1] {
				found = true
			}
		}
		rows.Close()
		if !found {
			t.Errorf("column %s.%s missing", tc[0], tc[1])
		}
	}
}

func TestMigration17UpgradesExistingCalibrationData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v16.db")
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, description TEXT, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version >= 17 {
			break
		}
		if err := applyMigration(raw, migration); err != nil {
			t.Fatalf("applying migration %d: %v", migration.Version, err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO event_reviews
		(event_id, match_id, player_id, detector_id, detector_version, frame_index,
		 timestamp, severity, confidence, observed_value, expected_range, evidence_type,
		 evidence_json, verdict, comment, reviewer_id, reviewed_at)
		VALUES ('E1','M1','P1','THROW_001','1',10,1,1,1,'x','y','','{}','yes','','local','2026-09-04T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO analysis_runs
		(match_id, source, app_version, build_commit, config_fingerprint, profile_name,
		 telemetry_quality, quality_grade, quality_gated, wall_milliseconds,
		 pipeline_milliseconds, frames_processed, events_produced, created_at)
		VALUES ('M1','upload','0.7.0','old','full','default',90,'good',0,1,1,10,1,'2026-09-04T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	store := newTestStoreAt(t, dbPath)
	var blind int
	if err := store.DB().QueryRow(`SELECT blind_review FROM event_reviews WHERE event_id='E1'`).Scan(&blind); err != nil || blind != 0 {
		t.Fatalf("blind_review = %d, %v", blind, err)
	}
	var fingerprint string
	if err := store.DB().QueryRow(`SELECT calibration_fingerprint FROM analysis_runs WHERE match_id='M1'`).Scan(&fingerprint); err != nil || fingerprint != "" {
		t.Fatalf("calibration_fingerprint = %q, %v", fingerprint, err)
	}
	if version, err := AppliedSchemaVersion(store.DB()); err != nil || version != SchemaVersion() {
		t.Fatalf("schema version = %d, %v", version, err)
	}
}

func TestMigration18PreservesLabelsAndReservesLegacyExposure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v17-calibration.db")
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, description TEXT, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version >= 18 {
			break
		}
		if err := applyMigration(raw, migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO match_contexts (match_id,context_json) VALUES ('LEGACY','{"match_id":"LEGACY","player_ids":["P"]}');
		INSERT INTO calibration_opportunities (opportunity_id,match_id,player_id,detector_id,opportunity_kind,frame_start,frame_end,ground_truth,reviewer_id,comment)
		VALUES ('OP','LEGACY','EARLIER','THROW_001','throw',10,20,'positive','Alice','preserve this note'),
		('ARCHIVED','NOT-PRESENT','Q','THROW_001','throw',0,1,'negative','Bob','retained after replay deletion');
		INSERT INTO event_reviews (event_id,match_id,player_id,detector_id,frame_index,verdict,reviewer_id) VALUES
		('E1','LEGACY','REVIEWED','THROW_001',1,'yes','Alice'), ('E2','NOT-PRESENT','ARCHIVED-REVIEW','THROW_001',2,'no','Bob');`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	s := newTestStoreAt(t, dbPath)
	items, err := s.ListCalibrationOpportunities(t.Context(), "LEGACY", "")
	if err != nil || len(items) != 1 || items[0].Comment != "preserve this note" || items[0].ReviewerID != "Alice" || items[0].IndependentEvidenceVerified() {
		t.Fatalf("legacy label lost or auto-verified: %+v %v", items, err)
	}
	matches := []StoredMatch{splitMatch("LEGACY", "P"), splitMatch("NEW", "Q"), splitMatch("NEW-EARLIER", "EARLIER"), splitMatch("NEW-REVIEW", "REVIEWED"), splitMatch("NEW-ARCHIVED-REVIEW", "ARCHIVED-REVIEW")}
	proposed := make(map[string]string)
	for _, match := range matches {
		proposed[match.Context.MatchID] = "holdout"
	}
	got, err := s.ReconcileCalibrationSplits(t.Context(), matches, proposed)
	if err != nil {
		t.Fatal(err)
	}
	for id := range proposed {
		if got[id].Split != "training" {
			t.Fatalf("historical exposure became held out: %+v", got)
		}
	}
	if err := RunMigrations(s.DB()); err != nil {
		t.Fatalf("second migration failed: %v", err)
	}
}

// A database bootstrapped by hand from migrations/001_initial.sql, holding rows
// written in the legacy datetime('now') layout by an older binary, must upgrade
// cleanly and end up with every timestamp normalized.
func TestMigrations_UpgradeFromHandBootstrappedLegacyDB(t *testing.T) {
	sqlPath := filepath.Join("..", "..", "..", "migrations", "001_initial.sql")
	initial, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Skipf("initial migration not available: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(string(initial)); err != nil {
		t.Fatalf("bootstrapping: %v", err)
	}
	// Legacy rows exactly as the old binary + old DEFAULT would have written them.
	stmts := []string{
		`INSERT INTO detection_events (event_id, detector_id, match_id, player_id, frame_index, severity, confidence, created_at)
		 VALUES ('e-legacy', 'THROW_001', 'M1', 'P1', 10, 0.9, 0.8, '2026-09-02 16:10:40')`,
		`INSERT INTO suspicion_scores (player_id, total_score, snapshot_time)
		 VALUES ('P1', 12.5, '2026-09-02 16:10:41')`,
		`INSERT INTO review_cases (case_id, player_id, match_id, created_at)
		 VALUES ('C1', 'P1', 'M1', '2026-09-02T11:10:40-05:00')`,
	}
	for _, st := range stmts {
		if _, err := raw.Exec(st); err != nil {
			t.Fatalf("seeding legacy row: %v", err)
		}
	}
	raw.Close()

	s := newTestStoreAt(t, dbPath)
	ctx := context.Background()

	var created, snap, caseCreated string
	if err := s.DB().QueryRow("SELECT created_at FROM detection_events WHERE event_id='e-legacy'").Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != "2026-09-02T16:10:40Z" {
		t.Errorf("created_at normalized = %q, want 2026-09-02T16:10:40Z", created)
	}
	if err := s.DB().QueryRow("SELECT snapshot_time FROM suspicion_scores WHERE player_id='P1'").Scan(&snap); err != nil {
		t.Fatal(err)
	}
	if snap != "2026-09-02T16:10:41Z" {
		t.Errorf("snapshot_time normalized = %q", snap)
	}
	if err := s.DB().QueryRow("SELECT created_at FROM review_cases WHERE case_id='C1'").Scan(&caseCreated); err != nil {
		t.Fatal(err)
	}
	if caseCreated != "2026-09-02T16:10:40Z" {
		t.Errorf("offset created_at normalized = %q, want UTC", caseCreated)
	}

	// Legacy rows are readable through the normal API with real timestamps.
	evs, err := s.GetPlayerEvents(ctx, "P1", 10, 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("GetPlayerEvents = %v, %v", evs, err)
	}
	if evs[0].StoredAt.IsZero() {
		t.Error("StoredAt zero for legacy row")
	}
	hist, err := s.GetPlayerHistory(ctx, "P1", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(hist) != 1 {
		t.Fatalf("GetPlayerHistory = %v, %v", hist, err)
	}
	if hist[0].SnapshotTime.Year() != 2026 {
		t.Errorf("SnapshotTime not parsed: %v", hist[0].SnapshotTime)
	}
	if hist[0].TotalScore != 12.5 {
		t.Errorf("legacy score = %v", hist[0].TotalScore)
	}
}

func TestMigrations_BackfillsMatchStartTimeFromContextJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bf.db")
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a v9-era database: apply everything except migration 10, then
	// insert a context row the old way (no match_start_time column value).
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, description TEXT, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.Version >= 10 {
			break
		}
		if err := applyMigration(raw, m); err != nil {
			t.Fatalf("applying %d: %v", m.Version, err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO match_contexts (match_id, context_json, frame_count)
		VALUES ('M1', '{"match_id":"M1","start_time":"2026-08-01T10:00:00.5-05:00"}', 5),
		       ('M2', '{"match_id":"M2","start_time":"0001-01-01T00:00:00Z"}', 5)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	s := newTestStoreAt(t, dbPath)
	starts, err := s.GetMatchStartTimes(context.Background(), []string{"M1", "M2"})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC)
	if got, ok := starts["M1"]; !ok || !got.Equal(want) {
		t.Errorf("M1 start = %v (ok=%v), want %v", got, ok, want)
	}
	if _, ok := starts["M2"]; ok {
		t.Error("zero start time must stay NULL")
	}
}
