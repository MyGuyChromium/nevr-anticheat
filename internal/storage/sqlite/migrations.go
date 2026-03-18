// Package sqlite provides SQLite-backed storage for the NEVR anticheat system.
//
// # Data classification
//
// The database stores two categories of data:
//
// IMMUTABLE SOURCE DATA — the profiler truth. Never modified after ingestion.
// These tables are the canonical long-term telemetry source.
//
//   - telemetry_frames: Raw and normalized telemetry frames from profiler collection.
//     frame_json is the normalized PlayerTelemetryFrame. raw_json (when available)
//     is the original profiler/API session payload with additional fields.
//     MUST NOT be pruned by default. Pruning telemetry destroys the source of truth.
//
//   - match_contexts: Match metadata (players, teams, physics, source).
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
//     Each row is a point-in-time snapshot. GetPlayerScore reads the latest.
//     Recomputable from detection_events via cross-match aggregation.
//     Safe to prune old snapshots as maintenance.
//
//   - cross_match_review_cases: Aggregated review cases. Replace semantics
//     per player (case_id = "XM-{playerID}", INSERT OR REPLACE).
//     Recomputable from detection_events via cross-match aggregation.
//
//   - review_cases: Single-match review cases. Created during analysis.
//     Recomputable from detection_events.
package sqlite

import "database/sql"

// RunMigrations creates all required tables.
func RunMigrations(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS detection_events (
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
			created_at       TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_player ON detection_events(player_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_events_match ON detection_events(match_id)`,
		`CREATE INDEX IF NOT EXISTS idx_events_detector ON detection_events(detector_id, created_at)`,

		`CREATE TABLE IF NOT EXISTS suspicion_scores (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			player_id        TEXT NOT NULL,
			total_score      REAL NOT NULL,
			score_by_detector TEXT,
			score_by_category TEXT,
			event_count      INTEGER NOT NULL DEFAULT 0,
			match_count      INTEGER NOT NULL DEFAULT 0,
			snapshot_time    TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_scores_player ON suspicion_scores(player_id, snapshot_time)`,

		`CREATE TABLE IF NOT EXISTS match_summaries (
			match_id         TEXT PRIMARY KEY,
			map_name         TEXT,
			game_mode        TEXT,
			is_ranked        INTEGER,
			start_time       TEXT,
			duration_seconds REAL,
			frame_count      INTEGER,
			total_detections INTEGER,
			flagged_players  TEXT,
			created_at       TEXT NOT NULL DEFAULT (datetime('now'))
		)`,

		`CREATE TABLE IF NOT EXISTS enforcement_actions (
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
			created_at       TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_actions_player ON enforcement_actions(player_id, issued_at)`,

		`CREATE TABLE IF NOT EXISTS review_cases (
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
			created_at       TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at       TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cases_status ON review_cases(status)`,
		`CREATE INDEX IF NOT EXISTS idx_cases_player ON review_cases(player_id)`,

		`CREATE TABLE IF NOT EXISTS moderator_decisions (
			decision_id      TEXT PRIMARY KEY,
			case_id          TEXT NOT NULL,
			moderator_id     TEXT NOT NULL,
			verdict          TEXT NOT NULL,
			action_taken     TEXT,
			notes            TEXT,
			decided_at       TEXT NOT NULL DEFAULT (datetime('now'))
		)`,

		`CREATE TABLE IF NOT EXISTS player_profiles (
			player_id        TEXT PRIMARY KEY,
			profile_json     TEXT NOT NULL,
			match_count      INTEGER NOT NULL DEFAULT 0,
			last_updated     TEXT NOT NULL DEFAULT (datetime('now'))
		)`,

		`CREATE TABLE IF NOT EXISTS population_baselines (
			metric           TEXT PRIMARY KEY,
			baseline_json    TEXT NOT NULL,
			sample_count     INTEGER NOT NULL DEFAULT 0,
			last_updated     TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}
