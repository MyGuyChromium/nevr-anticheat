package sqlite

import (
	"database/sql"
	"fmt"
	"log/slog"
)

// MigrationVersion tracks applied migrations.
type MigrationVersion struct {
	Version     int
	Description string
	SQL         string
}

var migrations = []MigrationVersion{
	{
		Version: 2, Description: "add replay_bundles table",
		SQL: `CREATE TABLE IF NOT EXISTS replay_bundles (
			bundle_id    TEXT PRIMARY KEY,
			case_id      TEXT,
			match_id     TEXT NOT NULL,
			player_id    TEXT NOT NULL,
			bundle_json  TEXT NOT NULL,
			created_at   TEXT NOT NULL DEFAULT (datetime('now'))
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
			recorded_at  TEXT NOT NULL DEFAULT (datetime('now')),
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
			executed_at  TEXT NOT NULL DEFAULT (datetime('now'))
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
			ingested_at  TEXT NOT NULL DEFAULT (datetime('now')),
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
			ingested_at  TEXT NOT NULL DEFAULT (datetime('now'))
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
			created_at       TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_xmatch_cases_player ON cross_match_review_cases(player_id);
		CREATE INDEX IF NOT EXISTS idx_xmatch_cases_status ON cross_match_review_cases(status);`,
	},
}

// RunMigrationsV2 applies incremental migrations beyond the initial schema.
func RunMigrationsV2(db *sql.DB, logger *slog.Logger) error {
	// Create migration tracking table
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version     INTEGER PRIMARY KEY,
		description TEXT,
		applied_at  TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		return fmt.Errorf("creating migration table: %w", err)
	}

	for _, m := range migrations {
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", m.Version).Scan(&count)
		if err != nil {
			return fmt.Errorf("checking migration %d: %w", m.Version, err)
		}
		if count > 0 {
			continue // already applied
		}

		if logger != nil {
			logger.Info("applying migration", "version", m.Version, "description", m.Description)
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("starting transaction for migration %d: %w", m.Version, err)
		}

		if _, err := tx.Exec(m.SQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("applying migration %d: %w", m.Version, err)
		}

		if _, err := tx.Exec("INSERT INTO schema_migrations (version, description) VALUES (?, ?)", m.Version, m.Description); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording migration %d: %w", m.Version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", m.Version, err)
		}
	}
	return nil
}
