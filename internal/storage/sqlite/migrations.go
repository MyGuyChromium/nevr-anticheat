// Package sqlite provides SQLite-backed storage for the NEVR anticheat system.
//
// # Data classification
//
// The database stores two categories of data:
//
// IMMUTABLE SOURCE DATA — the profiler truth. Never modified after ingestion.
// These tables are the canonical long-term telemetry source.
//
//   - telemetry_frames: Normalized telemetry frames from profiler collection.
//     frame_json is the normalized PlayerTelemetryFrame, one row per player per tick.
//     MUST NOT be pruned by default. Pruning telemetry destroys the source of truth.
//
//   - match_ticks: The original profiler/API session payload, stored ONCE per
//     (match_id, frame_index) rather than once per player row. Only populated by
//     sources that carry a raw payload (.echoreplay); NULL/absent otherwise.
//
//   - match_contexts: Match metadata (players, teams, physics, source, start time).
//     Needed to reconstruct a pipeline run during reprocessing.
//     MUST NOT be pruned by default.
//
// DERIVED ANALYSIS OUTPUTS — recomputable from source data at any time.
//
//   - detection_events: Detector outputs. Replace semantics on reprocessing
//     (old events deleted, new events inserted with fresh UUIDs).
//     analysis_source column distinguishes "initial" from "reprocess".
//     Safe to prune old events as maintenance — they can be regenerated.
//
//   - suspicion_scores: Append-only scoring snapshots. NOT canonical truth.
//     scope = 'match' rows are per-match pipeline snapshots (match_id set);
//     scope = 'cross_match' rows are aggregation snapshots. Readers never mix
//     the two. GetPlayerScore reads the latest 'match' snapshot.
//
//   - cross_match_review_cases: Aggregated review cases. Upsert semantics per
//     player (case_id = "XM-{playerID}"); a moderator-handled status is preserved.
//
//   - review_cases / moderator_decisions / enforcement_actions: moderator
//     workflow. Decisions are the calibration ground truth for detectors.
//     A review case records the level, threshold_version and match time
//     window it was built under so calibration can group by threshold set.
//     Re-analysis upserts cases under their deterministic id; a pending case
//     whose player no longer reaches the review tier is closed with a
//     close_reason (never deleted), and reopened if a later run flags the
//     player again. Moderator-set statuses are always preserved.
//
//   - event_reviews: direct human labels on individual detector observations.
//     Event metadata is snapshotted so labels remain calibration evidence when
//     re-analysis replaces derived detection_events rows.
//
//   - match_labels: human ground truth for the replay as a whole. This is the
//     calibration-library index (known clean, suspected, or confirmed cheat),
//     not an enforcement decision.
//
// # Timestamps
//
// Every timestamp column is TEXT in UTC RFC3339 with second precision and a
// literal 'Z' ("2006-01-02T15:04:05Z", see dbtime.go). Inserts always supply the
// value from Go; the SQL DEFAULT exists only for ad-hoc inserts. Migration 10
// rewrote rows written by older versions in datetime('now') or offset layouts
// (migration 11 did the same for schema_migrations.applied_at).
package sqlite

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// MigrationVersion is one versioned schema step. Every step must be idempotent
// with respect to CREATE (IF NOT EXISTS) because version 1 is also applied to
// databases bootstrapped by hand from migrations/001_initial.sql.
type MigrationVersion struct {
	Version     int
	Description string
	SQL         string
}

// timestampColumnsV10 lists every (table, column) holding a dbTimeLayout value
// as of schema version 10. Migration 10 normalizes exactly this list; it is
// frozen because a migration's SQL must not change after it has been applied.
// schema_migrations.applied_at is in the list for coverage but is normalized
// by migration 11: the pre-v10 runner inserted rows with the datetime('now')
// DEFAULT, so versions 2..8 of an old file are in the legacy layout.
var timestampColumnsV10 = [][2]string{
	{"detection_events", "created_at"},
	{"suspicion_scores", "snapshot_time"},
	{"match_summaries", "start_time"},
	{"match_summaries", "created_at"},
	{"enforcement_actions", "issued_at"},
	{"enforcement_actions", "created_at"},
	{"review_cases", "created_at"},
	{"review_cases", "updated_at"},
	{"moderator_decisions", "decided_at"},
	{"player_profiles", "last_updated"},
	{"population_baselines", "last_updated"},
	{"replay_bundles", "created_at"},
	{"anomaly_clusters", "recorded_at"},
	{"enforcement_log", "executed_at"},
	{"telemetry_frames", "ingested_at"},
	{"match_ticks", "ingested_at"},
	{"match_contexts", "ingested_at"},
	{"match_contexts", "match_start_time"},
	{"cross_match_review_cases", "created_at"},
	{"cross_match_review_cases", "updated_at"},
	{"schema_migrations", "applied_at"},
}

// timestampColumns lists every (table, column) that holds a dbTimeLayout value
// in the current schema (tests assert coverage). Columns added after version
// 10 are created already normalized and appended here.
var timestampColumns = append(append([][2]string{}, timestampColumnsV10...),
	[2]string{"review_cases", "timestamp_start"},
	[2]string{"review_cases", "timestamp_end"},
	[2]string{"event_reviews", "reviewed_at"},
	[2]string{"match_labels", "reviewed_at"},
	[2]string{"analysis_snapshots", "created_at"},
	[2]string{"investigation_notes", "created_at"},
	[2]string{"investigation_notes", "updated_at"},
	[2]string{"analysis_runs", "created_at"},
	[2]string{"config_profiles", "created_at"},
	[2]string{"config_profiles", "updated_at"},
	[2]string{"saved_filters", "created_at"},
	[2]string{"saved_filters", "updated_at"},
	[2]string{"calibration_opportunities", "reviewed_at"},
	[2]string{"detector_promotions", "created_at"},
	[2]string{"detector_promotions", "updated_at"},
)

// requiredTables is the schema surface the Store depends on. NewStore verifies
// each exists after migrations so a partially upgraded file fails at startup
// instead of at the first write.
var requiredTables = []string{
	"detection_events", "suspicion_scores", "match_summaries", "enforcement_actions",
	"review_cases", "moderator_decisions", "player_profiles", "population_baselines",
	"replay_bundles", "anomaly_clusters", "enforcement_log", "telemetry_frames",
	"match_contexts", "cross_match_review_cases", "match_ticks", "schema_migrations",
	"event_reviews", "match_labels", "analysis_snapshots", "investigation_notes",
	"analysis_runs", "config_profiles", "saved_filters",
	"calibration_opportunities", "detector_promotions",
	"calibration_split_assignments",
	"match_analysis_coverage",
	"blind_evidence_artifacts", "blind_review_sessions", "blind_review_ballots",
}

var migrations = []MigrationVersion{
	{
		Version: 1, Description: "initial schema",
		SQL: `CREATE TABLE IF NOT EXISTS detection_events (
			event_id         TEXT PRIMARY KEY,
			detector_id      TEXT NOT NULL,
			detector_version TEXT NOT NULL DEFAULT '',
			match_id         TEXT NOT NULL,
			player_id        TEXT NOT NULL,
			frame_index      INTEGER NOT NULL,
			frame_range_start INTEGER NOT NULL DEFAULT 0,
			frame_range_end  INTEGER NOT NULL DEFAULT 0,
			timestamp        REAL NOT NULL DEFAULT 0,
			severity         REAL NOT NULL,
			confidence       REAL NOT NULL,
			observed_value   TEXT,
			expected_range   TEXT,
			enforcement_weight REAL NOT NULL DEFAULT 0,
			auto_enforce     INTEGER NOT NULL DEFAULT 0,
			is_shadow        INTEGER NOT NULL DEFAULT 0,
			evidence_json    TEXT,
			causal_key       TEXT,
			created_at       TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE INDEX IF NOT EXISTS idx_events_player ON detection_events(player_id, created_at);
		CREATE INDEX IF NOT EXISTS idx_events_match ON detection_events(match_id);
		CREATE INDEX IF NOT EXISTS idx_events_detector ON detection_events(detector_id, created_at);

		CREATE TABLE IF NOT EXISTS suspicion_scores (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			player_id        TEXT NOT NULL,
			total_score      REAL NOT NULL,
			score_by_detector TEXT,
			score_by_category TEXT,
			event_count      INTEGER NOT NULL DEFAULT 0,
			match_count      INTEGER NOT NULL DEFAULT 0,
			snapshot_time    TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE INDEX IF NOT EXISTS idx_scores_player ON suspicion_scores(player_id, snapshot_time);

		CREATE TABLE IF NOT EXISTS match_summaries (
			match_id         TEXT PRIMARY KEY,
			map_name         TEXT,
			game_mode        TEXT,
			is_ranked        INTEGER,
			start_time       TEXT,
			duration_seconds REAL,
			frame_count      INTEGER,
			total_detections INTEGER,
			flagged_players  TEXT,
			created_at       TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);

		CREATE TABLE IF NOT EXISTS enforcement_actions (
			action_id        TEXT PRIMARY KEY,
			player_id        TEXT NOT NULL,
			action_type      TEXT NOT NULL,
			reason           TEXT,
			duration_seconds REAL,
			issued_by        TEXT NOT NULL,
			issued_at        TEXT NOT NULL,
			evidence_ids     TEXT,
			score_at_time    REAL,
			notes            TEXT,
			created_at       TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE INDEX IF NOT EXISTS idx_actions_player ON enforcement_actions(player_id, issued_at);

		CREATE TABLE IF NOT EXISTS review_cases (
			case_id          TEXT PRIMARY KEY,
			player_id        TEXT NOT NULL,
			match_id         TEXT NOT NULL,
			severity         TEXT,
			suspicion_score  REAL,
			recommended_action TEXT,
			explanation      TEXT,
			status           TEXT NOT NULL DEFAULT 'pending',
			assigned_to      TEXT,
			detectors_json   TEXT,
			created_at       TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `,
			updated_at       TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_cases_status ON review_cases(status);
		CREATE INDEX IF NOT EXISTS idx_cases_player ON review_cases(player_id);

		CREATE TABLE IF NOT EXISTS moderator_decisions (
			decision_id      TEXT PRIMARY KEY,
			case_id          TEXT NOT NULL,
			moderator_id     TEXT NOT NULL,
			verdict          TEXT NOT NULL,
			action_taken     TEXT,
			notes            TEXT,
			decided_at       TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);

		CREATE TABLE IF NOT EXISTS player_profiles (
			player_id        TEXT PRIMARY KEY,
			profile_json     TEXT NOT NULL,
			match_count      INTEGER NOT NULL DEFAULT 0,
			last_updated     TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);

		CREATE TABLE IF NOT EXISTS population_baselines (
			metric           TEXT PRIMARY KEY,
			baseline_json    TEXT NOT NULL,
			sample_count     INTEGER NOT NULL DEFAULT 0,
			last_updated     TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);`,
	},
	{
		Version: 2, Description: "add replay_bundles table",
		SQL: `CREATE TABLE IF NOT EXISTS replay_bundles (
			bundle_id    TEXT PRIMARY KEY,
			case_id      TEXT,
			match_id     TEXT NOT NULL,
			player_id    TEXT NOT NULL,
			bundle_json  TEXT NOT NULL,
			created_at   TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE INDEX IF NOT EXISTS idx_bundles_case ON replay_bundles(case_id);
		CREATE INDEX IF NOT EXISTS idx_bundles_player ON replay_bundles(player_id);`,
	},
	{
		Version: 3, Description: "add anomaly_clusters table",
		SQL: `CREATE TABLE IF NOT EXISTS anomaly_clusters (
			player_id    TEXT NOT NULL,
			detector_id  TEXT NOT NULL,
			match_id     TEXT NOT NULL,
			severity     REAL NOT NULL,
			confidence   REAL NOT NULL,
			recorded_at  TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `,
			PRIMARY KEY (player_id, detector_id, match_id)
		);
		CREATE INDEX IF NOT EXISTS idx_clusters_player ON anomaly_clusters(player_id);`,
	},
	{
		Version: 4, Description: "add enforcement_log table",
		SQL: `CREATE TABLE IF NOT EXISTS enforcement_log (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			action_id    TEXT NOT NULL,
			player_id    TEXT NOT NULL,
			action_type  TEXT NOT NULL,
			mode         TEXT NOT NULL,
			score        REAL NOT NULL,
			reason       TEXT,
			executed_at  TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE INDEX IF NOT EXISTS idx_enforce_log_player ON enforcement_log(player_id, executed_at);`,
	},
	{
		Version: 5, Description: "add telemetry_frames table for raw frame persistence",
		SQL: `CREATE TABLE IF NOT EXISTS telemetry_frames (
			match_id     TEXT NOT NULL,
			player_id    TEXT NOT NULL,
			frame_index  INTEGER NOT NULL,
			timestamp    REAL NOT NULL,
			frame_json   TEXT NOT NULL,
			ingested_at  TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `,
			PRIMARY KEY (match_id, player_id, frame_index)
		);
		CREATE INDEX IF NOT EXISTS idx_frames_match ON telemetry_frames(match_id);
		CREATE INDEX IF NOT EXISTS idx_frames_player ON telemetry_frames(player_id, ingested_at);
		CREATE INDEX IF NOT EXISTS idx_frames_time ON telemetry_frames(ingested_at);`,
	},
	{
		Version: 6, Description: "add match_contexts table for match metadata persistence",
		SQL: `CREATE TABLE IF NOT EXISTS match_contexts (
			match_id     TEXT PRIMARY KEY,
			context_json TEXT NOT NULL,
			frame_count  INTEGER NOT NULL DEFAULT 0,
			ingested_at  TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);`,
	},
	{
		Version: 7, Description: "add cross_match_review_cases table",
		SQL: `CREATE TABLE IF NOT EXISTS cross_match_review_cases (
			case_id          TEXT PRIMARY KEY,
			player_id        TEXT NOT NULL,
			match_ids        TEXT NOT NULL,
			match_count      INTEGER NOT NULL,
			severity         TEXT NOT NULL,
			cumulative_score REAL NOT NULL,
			decayed_score    REAL NOT NULL,
			detectors_json   TEXT,
			explanation      TEXT,
			status           TEXT NOT NULL DEFAULT 'pending',
			created_at       TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE INDEX IF NOT EXISTS idx_xmatch_cases_player ON cross_match_review_cases(player_id);
		CREATE INDEX IF NOT EXISTS idx_xmatch_cases_status ON cross_match_review_cases(status);`,
	},
	{
		Version: 8, Description: "add analysis_source to detection_events, raw_json to telemetry_frames",
		SQL: `ALTER TABLE detection_events ADD COLUMN analysis_source TEXT NOT NULL DEFAULT 'initial';
		ALTER TABLE telemetry_frames ADD COLUMN raw_json TEXT;`,
	},
	{
		Version: 9, Description: "match_ticks, score scope/match_id, evidence_type, moderator feedback, enforcement match_ids, maintenance indexes",
		SQL: `CREATE TABLE IF NOT EXISTS match_ticks (
			match_id     TEXT NOT NULL,
			frame_index  INTEGER NOT NULL,
			raw_json     TEXT NOT NULL,
			ingested_at  TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `,
			PRIMARY KEY (match_id, frame_index)
		);
		ALTER TABLE suspicion_scores ADD COLUMN scope TEXT NOT NULL DEFAULT 'match';
		ALTER TABLE suspicion_scores ADD COLUMN match_id TEXT;
		ALTER TABLE detection_events ADD COLUMN evidence_type TEXT NOT NULL DEFAULT '';
		ALTER TABLE match_contexts ADD COLUMN match_start_time TEXT;
		ALTER TABLE enforcement_actions ADD COLUMN match_ids TEXT;
		ALTER TABLE moderator_decisions ADD COLUMN detector_feedback TEXT;
		ALTER TABLE moderator_decisions ADD COLUMN confidence_override REAL;
		ALTER TABLE moderator_decisions ADD COLUMN review_duration_sec INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE cross_match_review_cases ADD COLUMN updated_at TEXT;
		CREATE INDEX IF NOT EXISTS idx_events_created ON detection_events(created_at);
		CREATE INDEX IF NOT EXISTS idx_events_player_match ON detection_events(player_id, match_id);
		CREATE INDEX IF NOT EXISTS idx_scores_time ON suspicion_scores(snapshot_time);
		CREATE INDEX IF NOT EXISTS idx_scores_player_scope ON suspicion_scores(player_id, scope, snapshot_time, id);
		CREATE INDEX IF NOT EXISTS idx_scores_match ON suspicion_scores(match_id);
		CREATE INDEX IF NOT EXISTS idx_contexts_start ON match_contexts(match_start_time);
		CREATE INDEX IF NOT EXISTS idx_decisions_case ON moderator_decisions(case_id);
		CREATE INDEX IF NOT EXISTS idx_decisions_time ON moderator_decisions(decided_at);`,
	},
	{
		Version: 10, Description: "normalize all timestamp columns to UTC RFC3339 'Z'; backfill match_contexts.match_start_time",
		SQL: normalizeTimestampsSQL(),
	},
	{
		Version: 11, Description: "review_cases level/threshold_version/timestamps/close_reason, detection_events merged_count, normalize schema_migrations.applied_at",
		SQL: `ALTER TABLE review_cases ADD COLUMN level TEXT NOT NULL DEFAULT '';
		ALTER TABLE review_cases ADD COLUMN threshold_version TEXT NOT NULL DEFAULT '';
		ALTER TABLE review_cases ADD COLUMN timestamp_start TEXT;
		ALTER TABLE review_cases ADD COLUMN timestamp_end TEXT;
		ALTER TABLE review_cases ADD COLUMN close_reason TEXT NOT NULL DEFAULT '';
		ALTER TABLE detection_events ADD COLUMN merged_count INTEGER NOT NULL DEFAULT 0;
		CREATE INDEX IF NOT EXISTS idx_cases_match_status ON review_cases(match_id, status);
		CREATE INDEX IF NOT EXISTS idx_cases_threshold ON review_cases(threshold_version);
		` + normalizeTimestampSQL("schema_migrations", "applied_at"),
	},
	{
		Version: 12, Description: "match_summaries.summary_json: the offline match summary document (scoreboard, goals, throws)",
		SQL: `ALTER TABLE match_summaries ADD COLUMN summary_json TEXT;`,
	},
	{
		Version: 13, Description: "durable event-level detector reviews for desktop calibration",
		SQL: `CREATE TABLE IF NOT EXISTS event_reviews (
			event_id         TEXT PRIMARY KEY,
			match_id         TEXT NOT NULL,
			player_id        TEXT NOT NULL,
			detector_id      TEXT NOT NULL,
			detector_version TEXT NOT NULL DEFAULT '',
			frame_index      INTEGER NOT NULL,
			timestamp        REAL NOT NULL DEFAULT 0,
			severity         REAL NOT NULL DEFAULT 0,
			confidence       REAL NOT NULL DEFAULT 0,
			observed_value   TEXT NOT NULL DEFAULT '',
			expected_range   TEXT NOT NULL DEFAULT '',
			evidence_type    TEXT NOT NULL DEFAULT '',
			evidence_json    TEXT NOT NULL DEFAULT '',
			verdict          TEXT NOT NULL CHECK (verdict IN ('yes','no','uncertain')),
			comment          TEXT NOT NULL DEFAULT '',
			reviewer_id      TEXT NOT NULL,
			reviewed_at      TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE INDEX IF NOT EXISTS idx_event_reviews_detector ON event_reviews(detector_id, reviewed_at);
		CREATE INDEX IF NOT EXISTS idx_event_reviews_match ON event_reviews(match_id);`,
	},
	{
		Version: 14, Description: "match-level labels for the replay calibration library",
		SQL: `CREATE TABLE IF NOT EXISTS match_labels (
			match_id     TEXT PRIMARY KEY,
			label        TEXT NOT NULL CHECK (label IN ('known_clean','suspected','confirmed_cheat')),
			comment      TEXT NOT NULL DEFAULT '',
			reviewer_id  TEXT NOT NULL,
			app_version  TEXT NOT NULL DEFAULT '',
			config_fingerprint TEXT NOT NULL DEFAULT '',
			reviewed_at  TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE INDEX IF NOT EXISTS idx_match_labels_label ON match_labels(label, reviewed_at);`,
	},
	{
		Version: 15, Description: "pre-reprocess analysis snapshots for detector regression comparisons",
		SQL: `CREATE TABLE IF NOT EXISTS analysis_snapshots (
			snapshot_id INTEGER PRIMARY KEY AUTOINCREMENT,
			match_id    TEXT NOT NULL,
			created_at  TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `,
			events_json TEXT NOT NULL,
			scores_json TEXT NOT NULL DEFAULT '{}'
		);
		CREATE INDEX IF NOT EXISTS idx_analysis_snapshots_match
			ON analysis_snapshots(match_id, snapshot_id DESC);`,
	},
	{
		Version: 16, Description: "investigation notes, analysis provenance, configuration profiles, and saved filters",
		SQL: `CREATE TABLE IF NOT EXISTS investigation_notes (
			note_id     TEXT PRIMARY KEY,
			match_id    TEXT NOT NULL,
			player_id   TEXT NOT NULL DEFAULT '',
			frame_index INTEGER NOT NULL DEFAULT -1,
			kind        TEXT NOT NULL CHECK (kind IN ('note','bookmark')),
			body        TEXT NOT NULL DEFAULT '',
			created_at  TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `,
			updated_at  TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE INDEX IF NOT EXISTS idx_investigation_notes_match ON investigation_notes(match_id, frame_index);

		CREATE TABLE IF NOT EXISTS analysis_runs (
			run_id             INTEGER PRIMARY KEY AUTOINCREMENT,
			match_id           TEXT NOT NULL,
			source             TEXT NOT NULL,
			app_version        TEXT NOT NULL,
			build_commit       TEXT NOT NULL,
			config_fingerprint TEXT NOT NULL,
			profile_name       TEXT NOT NULL DEFAULT '',
			telemetry_quality  REAL NOT NULL DEFAULT 0,
			quality_grade      TEXT NOT NULL DEFAULT '',
			quality_gated      INTEGER NOT NULL DEFAULT 0,
			wall_milliseconds  INTEGER NOT NULL DEFAULT 0,
			pipeline_milliseconds INTEGER NOT NULL DEFAULT 0,
			frames_processed   INTEGER NOT NULL DEFAULT 0,
			events_produced    INTEGER NOT NULL DEFAULT 0,
			created_at         TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE INDEX IF NOT EXISTS idx_analysis_runs_match ON analysis_runs(match_id, run_id DESC);

		CREATE TABLE IF NOT EXISTS config_profiles (
			name           TEXT PRIMARY KEY COLLATE NOCASE,
			detectors_json TEXT NOT NULL,
			description    TEXT NOT NULL DEFAULT '',
			active         INTEGER NOT NULL DEFAULT 0,
			created_at     TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `,
			updated_at     TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);

		CREATE TABLE IF NOT EXISTS saved_filters (
			name        TEXT PRIMARY KEY COLLATE NOCASE,
			filter_json TEXT NOT NULL,
			created_at  TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `,
			updated_at  TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);`,
	},
	{
		Version: 17, Description: "calibration opportunities, blinded reviews, and gated detector promotions",
		SQL: `ALTER TABLE event_reviews ADD COLUMN blind_review INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE analysis_runs ADD COLUMN calibration_fingerprint TEXT NOT NULL DEFAULT '';

		CREATE TABLE IF NOT EXISTS calibration_opportunities (
			opportunity_id  TEXT PRIMARY KEY,
			match_id        TEXT NOT NULL,
			player_id       TEXT NOT NULL,
			detector_id     TEXT NOT NULL,
			behavior_type   TEXT NOT NULL DEFAULT '',
			opportunity_kind TEXT NOT NULL CHECK (opportunity_kind IN ('throw','movement_window','state_transition','match','player_history','custom')),
			frame_start     INTEGER NOT NULL,
			frame_end       INTEGER NOT NULL,
			timestamp_start REAL NOT NULL DEFAULT 0,
			timestamp_end   REAL NOT NULL DEFAULT 0,
			ground_truth    TEXT NOT NULL CHECK (ground_truth IN ('positive','negative','uncertain')),
			comment         TEXT NOT NULL DEFAULT '',
			reviewer_id     TEXT NOT NULL,
			blind_review    INTEGER NOT NULL DEFAULT 0,
			reviewed_at     TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE INDEX IF NOT EXISTS idx_calibration_opportunities_detector
			ON calibration_opportunities(detector_id, ground_truth, reviewed_at);
		CREATE INDEX IF NOT EXISTS idx_calibration_opportunities_match
			ON calibration_opportunities(match_id, player_id, frame_start, frame_end);

		CREATE TABLE IF NOT EXISTS detector_promotions (
			detector_id       TEXT PRIMARY KEY,
			profile_name      TEXT NOT NULL,
			status            TEXT NOT NULL CHECK (status IN ('candidate','active','rolled_back')),
			metrics_json      TEXT NOT NULL DEFAULT '{}',
			config_fingerprint TEXT NOT NULL DEFAULT '',
			created_at        TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `,
			updated_at        TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE INDEX IF NOT EXISTS idx_detector_promotions_status
			ON detector_promotions(status, updated_at);`,
	},
	{
		Version: 18, Description: "independent calibration evidence and durable dataset isolation",
		SQL: `ALTER TABLE calibration_opportunities ADD COLUMN verifier_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE calibration_opportunities ADD COLUMN verified_ground_truth TEXT NOT NULL DEFAULT '';
		ALTER TABLE calibration_opportunities ADD COLUMN evidence_method TEXT NOT NULL DEFAULT '';
		ALTER TABLE calibration_opportunities ADD COLUMN evidence_reference TEXT NOT NULL DEFAULT '';
		CREATE TABLE IF NOT EXISTS match_analysis_coverage (match_id TEXT PRIMARY KEY, coverage_json TEXT NOT NULL);
		CREATE TABLE IF NOT EXISTS calibration_split_assignments (
			match_id TEXT PRIMARY KEY,
			policy_version INTEGER NOT NULL,
			group_key TEXT NOT NULL,
			split TEXT NOT NULL CHECK (split IN ('training','validation','holdout')),
			players_json TEXT NOT NULL,
			exposure_fingerprint TEXT NOT NULL DEFAULT '',
			quarantined INTEGER NOT NULL DEFAULT 0 CHECK (quarantined IN (0,1))
		);
		-- Previously inspected data cannot retrospectively become an unseen holdout.
		-- No FK: deleting a replay must not erase its exposure/player history.
		INSERT OR IGNORE INTO calibration_split_assignments
			(match_id, policy_version, group_key, split, players_json)
			SELECT match_id, 1, 'legacy-exposure', 'training',
			COALESCE(json_extract(context_json, '$.player_ids'), '[]') FROM match_contexts;
		INSERT OR IGNORE INTO calibration_split_assignments
			(match_id, policy_version, group_key, split, players_json)
			SELECT match_id, 1, 'legacy-review-exposure', 'training', json_group_array(DISTINCT player_id)
			FROM event_reviews GROUP BY match_id;
		INSERT OR IGNORE INTO calibration_split_assignments
			(match_id, policy_version, group_key, split, players_json)
			SELECT match_id, 1, 'legacy-window-exposure', 'training', json_group_array(DISTINCT player_id)
			FROM calibration_opportunities GROUP BY match_id;
		INSERT OR IGNORE INTO calibration_split_assignments
			(match_id, policy_version, group_key, split, players_json)
			SELECT match_id, 1, 'legacy-label-exposure', 'training', '[]' FROM match_labels;
		-- A shortened current roster must not erase players still identified by
		-- earlier reviews/windows, nor may one evidence source hide another.
		UPDATE calibration_split_assignments SET players_json=(
			SELECT json_group_array(player_id) FROM (
				SELECT DISTINCT player_id FROM (
					SELECT trim(value) AS player_id FROM json_each(calibration_split_assignments.players_json)
					UNION SELECT trim(player_id) FROM event_reviews WHERE match_id=calibration_split_assignments.match_id
					UNION SELECT trim(player_id) FROM calibration_opportunities WHERE match_id=calibration_split_assignments.match_id
				) WHERE player_id <> '' ORDER BY player_id
			)
		);`,
	},
	{
		Version: 19, Description: "hash-bound local evidence and immutable blind ballots",
		SQL: `ALTER TABLE calibration_opportunities ADD COLUMN review_session_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE analysis_runs ADD COLUMN executable_sha256 TEXT NOT NULL DEFAULT '';
		CREATE TABLE blind_evidence_artifacts (
			sha256 TEXT PRIMARY KEY, filename TEXT NOT NULL, payload BLOB NOT NULL,
			created_at TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE TABLE blind_review_sessions (
			session_id TEXT PRIMARY KEY, binding_json TEXT NOT NULL,
			artifact_sha256 TEXT NOT NULL, candidate_fingerprint TEXT NOT NULL,
			window_sha256 TEXT NOT NULL, revealed_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
		);
		CREATE TABLE blind_review_ballots (
			session_id TEXT NOT NULL, reviewer_key TEXT NOT NULL, ballot_json TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `,
			PRIMARY KEY(session_id, reviewer_key)
		);
		CREATE TRIGGER blind_ballots_no_update BEFORE UPDATE ON blind_review_ballots BEGIN SELECT RAISE(ABORT,'ballots are immutable'); END;
		CREATE TRIGGER blind_ballots_no_delete BEFORE DELETE ON blind_review_ballots BEGIN SELECT RAISE(ABORT,'ballots are immutable'); END;
		CREATE TRIGGER blind_artifacts_no_update BEFORE UPDATE ON blind_evidence_artifacts BEGIN SELECT RAISE(ABORT,'evidence is immutable'); END;
		CREATE TRIGGER blind_bindings_no_update BEFORE UPDATE OF binding_json,artifact_sha256,candidate_fingerprint,window_sha256 ON blind_review_sessions BEGIN SELECT RAISE(ABORT,'review bindings are immutable'); END;`,
	},
}

// normalizeTimestampSQL rewrites every legacy value of one timestamp column
// (datetime('now') 'YYYY-MM-DD HH:MM:SS' or an RFC3339 value with a numeric
// offset) into dbTimeLayout. SQLite's strftime understands both legacy shapes
// and converts offsets to UTC; values it cannot parse are left untouched
// (COALESCE). The statement is idempotent.
func normalizeTimestampSQL(table, column string) string {
	return fmt.Sprintf(
		"UPDATE %s SET %s = COALESCE(strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ', %s), %s) "+
			"WHERE %s IS NOT NULL AND %s != '' AND %s NOT LIKE '____-__-__T__:__:__Z';\n",
		table, column, column, column, column, column, column)
}

// normalizeTimestampsSQL is migration 10: every timestamp column that existed
// at version 10 except schema_migrations.applied_at, which migration 11
// normalizes (the skip was an oversight: the old runner's DEFAULT wrote the
// legacy layout for versions 2..8).
func normalizeTimestampsSQL() string {
	var b strings.Builder
	for _, tc := range timestampColumnsV10 {
		if tc[0] == "schema_migrations" {
			continue
		}
		b.WriteString(normalizeTimestampSQL(tc[0], tc[1]))
	}
	// Backfill the indexed match start time from the JSON context written by
	// earlier versions. Zero Go times ('0001-01-01...') mean "unknown" and stay NULL.
	b.WriteString(`UPDATE match_contexts SET match_start_time =
		strftime('%Y-%m-%dT%H:%M:%SZ', json_extract(context_json, '$.start_time'))
		WHERE match_start_time IS NULL
		  AND json_extract(context_json, '$.start_time') IS NOT NULL
		  AND json_extract(context_json, '$.start_time') NOT LIKE '0001-%';
`)
	return b.String()
}

// RunMigrations applies every versioned migration (1..N) that has not yet been
// recorded in schema_migrations, then verifies the schema surface. It is
// idempotent and safe to call on every startup. NewStore calls it and fails
// hard on error; there is no non-fatal migration path.
func RunMigrations(db *sql.DB) error {
	return RunMigrationsV2(db, nil)
}

// RunMigrationsV2 is RunMigrations with an optional logger. It is retained for
// callers that ran the v2+ migrations separately; the call is now a no-op on a
// database NewStore already opened.
func RunMigrationsV2(db *sql.DB, logger *slog.Logger) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version     INTEGER PRIMARY KEY,
		description TEXT,
		applied_at  TEXT NOT NULL DEFAULT ` + dbTimeSQLDefault + `
	)`); err != nil {
		return fmt.Errorf("creating migration table: %w", err)
	}

	for _, m := range migrations {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", m.Version).Scan(&count); err != nil {
			return fmt.Errorf("checking migration %d: %w", m.Version, err)
		}
		if count > 0 {
			continue
		}
		if logger != nil {
			logger.Info("applying migration", "version", m.Version, "description", m.Description)
		}
		if err := applyMigration(db, m); err != nil {
			return err
		}
	}

	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion())); err != nil {
		return fmt.Errorf("stamping user_version: %w", err)
	}
	return verifySchema(db)
}

func applyMigration(db *sql.DB, m MigrationVersion) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("starting transaction for migration %d: %w", m.Version, err)
	}
	if _, err := tx.Exec(m.SQL); err != nil {
		tx.Rollback()
		return fmt.Errorf("applying migration %d (%s): %w", m.Version, m.Description, err)
	}
	if _, err := tx.Exec("INSERT INTO schema_migrations (version, description, applied_at) VALUES (?, ?, ?)",
		m.Version, m.Description, fmtDBTime(nowUTC())); err != nil {
		tx.Rollback()
		return fmt.Errorf("recording migration %d: %w", m.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing migration %d: %w", m.Version, err)
	}
	return nil
}

// SchemaVersion returns the highest migration version this binary knows about.
func SchemaVersion() int {
	return migrations[len(migrations)-1].Version
}

// AppliedSchemaVersion returns the highest migration version recorded in the database.
func AppliedSchemaVersion(db *sql.DB) (int, error) {
	var v sql.NullInt64
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&v); err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}

// verifySchema asserts every table the Store uses exists.
func verifySchema(db *sql.DB) error {
	for _, table := range requiredTables {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?", table).Scan(&n); err != nil {
			return fmt.Errorf("verifying schema: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("schema verification failed: table %q is missing", table)
		}
	}
	return nil
}
