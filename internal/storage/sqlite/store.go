package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Store provides SQLite-backed persistence.
type Store struct {
	db *sql.DB
}

// NewStore opens or creates a SQLite database with optimized settings.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+
		"?_journal_mode=WAL"+
		"&_synchronous=NORMAL"+
		"&_busy_timeout=5000"+
		"&_cache_size=-20000"+
		"&_foreign_keys=ON")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := RunMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return &Store{db: db}, nil
}

// DB returns the underlying *sql.DB for use by migrations and other low-level operations.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// StoreDetectionEvent persists a detection event.
func (s *Store) StoreDetectionEvent(ctx context.Context, event model.DetectionEvent) error {
	evidenceJSON, _ := json.Marshal(event.Evidence)
	causalJSON, _ := json.Marshal(event.CausalKey)
	autoEnforce := 0
	if event.AutoEnforce {
		autoEnforce = 1
	}
	shadow := 0
	if event.IsShadow {
		shadow = 1
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO detection_events (event_id, detector_id, detector_version, match_id, player_id,
			frame_index, frame_range_start, frame_range_end, timestamp, severity, confidence,
			observed_value, expected_range, enforcement_weight, auto_enforce, is_shadow,
			evidence_json, causal_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.DetectorID, event.DetectorVersion, event.MatchID, event.PlayerID,
		event.FrameIndex, event.FrameRangeStart, event.FrameRangeEnd, event.Timestamp,
		event.Severity, event.Confidence, event.ObservedValue, event.ExpectedRange,
		event.EnforcementWeight, autoEnforce, shadow,
		string(evidenceJSON), string(causalJSON),
	)
	return err
}

// GetPlayerEvents returns detection events for a player.
func (s *Store) GetPlayerEvents(ctx context.Context, playerID string, limit, offset int) ([]model.DetectionEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id, detector_id, detector_version, match_id, player_id,
			frame_index, frame_range_start, frame_range_end, timestamp,
			severity, confidence, observed_value, expected_range,
			enforcement_weight, auto_enforce, is_shadow
		FROM detection_events WHERE player_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		playerID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []model.DetectionEvent
	for rows.Next() {
		var ev model.DetectionEvent
		var autoEnforce, shadow int
		err := rows.Scan(
			&ev.EventID, &ev.DetectorID, &ev.DetectorVersion, &ev.MatchID, &ev.PlayerID,
			&ev.FrameIndex, &ev.FrameRangeStart, &ev.FrameRangeEnd, &ev.Timestamp,
			&ev.Severity, &ev.Confidence, &ev.ObservedValue, &ev.ExpectedRange,
			&ev.EnforcementWeight, &autoEnforce, &shadow,
		)
		if err != nil {
			return nil, err
		}
		ev.AutoEnforce = autoEnforce == 1
		ev.IsShadow = shadow == 1
		events = append(events, ev)
	}
	return events, rows.Err()
}

// GetMatchEvents returns all detection events for a match.
func (s *Store) GetMatchEvents(ctx context.Context, matchID string) ([]model.DetectionEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id, detector_id, detector_version, match_id, player_id,
			frame_index, frame_range_start, frame_range_end, timestamp,
			severity, confidence, observed_value, expected_range,
			enforcement_weight, auto_enforce, is_shadow
		FROM detection_events WHERE match_id = ? ORDER BY frame_index`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []model.DetectionEvent
	for rows.Next() {
		var ev model.DetectionEvent
		var autoEnforce, shadow int
		err := rows.Scan(
			&ev.EventID, &ev.DetectorID, &ev.DetectorVersion, &ev.MatchID, &ev.PlayerID,
			&ev.FrameIndex, &ev.FrameRangeStart, &ev.FrameRangeEnd, &ev.Timestamp,
			&ev.Severity, &ev.Confidence, &ev.ObservedValue, &ev.ExpectedRange,
			&ev.EnforcementWeight, &autoEnforce, &shadow,
		)
		if err != nil {
			return nil, err
		}
		ev.AutoEnforce = autoEnforce == 1
		ev.IsShadow = shadow == 1
		events = append(events, ev)
	}
	return events, rows.Err()
}

// StoreSuspicionScore appends a suspicion score snapshot.
// Scores are DERIVED DATA — append-only convenience snapshots, not source truth.
// They are recomputable from detection_events via cross-match aggregation.
// GetPlayerScore reads the latest snapshot by timestamp.
func (s *Store) StoreSuspicionScore(ctx context.Context, score model.SuspicionScore) error {
	detJSON, _ := json.Marshal(score.ScoreByDetector)
	catJSON, _ := json.Marshal(score.ScoreByCategory)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO suspicion_scores (player_id, total_score, score_by_detector, score_by_category, event_count, match_count)
		VALUES (?, ?, ?, ?, ?, ?)`,
		score.PlayerID, score.TotalScore, string(detJSON), string(catJSON), score.EventCount, score.MatchCount,
	)
	return err
}

// GetPlayerScore returns the most recent suspicion score for a player.
func (s *Store) GetPlayerScore(ctx context.Context, playerID string) (model.SuspicionScore, error) {
	var sc model.SuspicionScore
	var detJSON, catJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT player_id, total_score, score_by_detector, score_by_category, event_count, match_count
		FROM suspicion_scores WHERE player_id = ? ORDER BY snapshot_time DESC LIMIT 1`,
		playerID,
	).Scan(&sc.PlayerID, &sc.TotalScore, &detJSON, &catJSON, &sc.EventCount, &sc.MatchCount)
	if err != nil {
		return sc, err
	}
	json.Unmarshal([]byte(detJSON), &sc.ScoreByDetector)
	json.Unmarshal([]byte(catJSON), &sc.ScoreByCategory)
	return sc, nil
}

// GetPlayerHistory returns score history since a given time.
func (s *Store) GetPlayerHistory(ctx context.Context, playerID string, since time.Time) ([]model.SuspicionScore, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT player_id, total_score, event_count, match_count, snapshot_time
		FROM suspicion_scores WHERE player_id = ? AND snapshot_time >= ? ORDER BY snapshot_time`,
		playerID, since.Format(time.RFC3339),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var scores []model.SuspicionScore
	for rows.Next() {
		var sc model.SuspicionScore
		var snapStr string
		if err := rows.Scan(&sc.PlayerID, &sc.TotalScore, &sc.EventCount, &sc.MatchCount, &snapStr); err != nil {
			return nil, err
		}
		sc.SnapshotTime, _ = time.Parse(time.RFC3339, snapStr)
		scores = append(scores, sc)
	}
	return scores, rows.Err()
}

// StoreReviewCase persists a review case.
func (s *Store) StoreReviewCase(ctx context.Context, rc model.ReviewCase) error {
	detectorsJSON, _ := json.Marshal(rc.DetectorsTriggered)
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO review_cases (case_id, player_id, match_id, severity, suspicion_score,
			recommended_action, explanation, status, assigned_to, detectors_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rc.CaseID, rc.PlayerID, rc.MatchID, rc.Severity, rc.SuspicionScore,
		rc.RecommendedAction, rc.Explanation, rc.Status, rc.AssignedTo,
		string(detectorsJSON), rc.CreatedAt.Format(time.RFC3339),
	)
	return err
}

// GetReviewCase retrieves a review case by ID.
func (s *Store) GetReviewCase(ctx context.Context, caseID string) (model.ReviewCase, error) {
	var rc model.ReviewCase
	var detectorsJSON string
	var createdStr string
	err := s.db.QueryRowContext(ctx,
		`SELECT case_id, player_id, match_id, severity, suspicion_score,
			recommended_action, explanation, status, assigned_to, detectors_json, created_at
		FROM review_cases WHERE case_id = ?`, caseID,
	).Scan(&rc.CaseID, &rc.PlayerID, &rc.MatchID, &rc.Severity, &rc.SuspicionScore,
		&rc.RecommendedAction, &rc.Explanation, &rc.Status, &rc.AssignedTo,
		&detectorsJSON, &createdStr,
	)
	if err != nil {
		return rc, err
	}
	json.Unmarshal([]byte(detectorsJSON), &rc.DetectorsTriggered)
	rc.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	return rc, nil
}

// GetPendingReviewCases returns pending review cases.
func (s *Store) GetPendingReviewCases(ctx context.Context, limit int) ([]model.ReviewCase, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT case_id, player_id, match_id, severity, suspicion_score,
			recommended_action, explanation, status, created_at
		FROM review_cases WHERE status = 'pending' ORDER BY suspicion_score DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cases []model.ReviewCase
	for rows.Next() {
		var rc model.ReviewCase
		var createdStr string
		if err := rows.Scan(&rc.CaseID, &rc.PlayerID, &rc.MatchID, &rc.Severity, &rc.SuspicionScore,
			&rc.RecommendedAction, &rc.Explanation, &rc.Status, &createdStr); err != nil {
			return nil, err
		}
		rc.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		cases = append(cases, rc)
	}
	return cases, rows.Err()
}

// StoreMatchSummary persists a match summary.
func (s *Store) StoreMatchSummary(ctx context.Context, summary model.MatchSummary) error {
	flaggedJSON, _ := json.Marshal(summary.FlaggedPlayers)
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO match_summaries (match_id, map_name, game_mode, is_ranked, start_time,
			duration_seconds, frame_count, total_detections, flagged_players)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		summary.MatchID, summary.Map, summary.GameMode, summary.IsRanked,
		summary.StartTime.Format(time.RFC3339), summary.Duration.Seconds(),
		summary.FrameCount, summary.TotalDetectionEvents, string(flaggedJSON),
	)
	return err
}

// DeleteMatchEvents removes all detection events for a match.
// Used before reprocessing to prevent duplicate events.
func (s *Store) DeleteMatchEvents(ctx context.Context, matchID string) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM detection_events WHERE match_id = ?", matchID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteMatchScores removes suspicion score snapshots associated with a match.
// Identifies affected players from the match's detection events, then removes
// their score snapshots so they can be cleanly recomputed.
func (s *Store) DeleteMatchScores(ctx context.Context, matchID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM suspicion_scores WHERE player_id IN
		 (SELECT DISTINCT player_id FROM detection_events WHERE match_id = ?)`,
		matchID)
	return err
}

// StoreDetectionEventWithSource persists a detection event with an explicit analysis_source tag.
// source should be "initial" for first-time analysis or "reprocess" for reprocessed results.
func (s *Store) StoreDetectionEventWithSource(ctx context.Context, event model.DetectionEvent, source string) error {
	evidenceJSON, _ := json.Marshal(event.Evidence)
	causalJSON, _ := json.Marshal(event.CausalKey)
	autoEnforce := 0
	if event.AutoEnforce {
		autoEnforce = 1
	}
	shadow := 0
	if event.IsShadow {
		shadow = 1
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO detection_events (event_id, detector_id, detector_version, match_id, player_id,
			frame_index, frame_range_start, frame_range_end, timestamp, severity, confidence,
			observed_value, expected_range, enforcement_weight, auto_enforce, is_shadow,
			evidence_json, causal_key, analysis_source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.DetectorID, event.DetectorVersion, event.MatchID, event.PlayerID,
		event.FrameIndex, event.FrameRangeStart, event.FrameRangeEnd, event.Timestamp,
		event.Severity, event.Confidence, event.ObservedValue, event.ExpectedRange,
		event.EnforcementWeight, autoEnforce, shadow,
		string(evidenceJSON), string(causalJSON), source,
	)
	return err
}

// PruneOldEvents removes detection events older than the given duration.
// Detection events are DERIVED DATA and can be regenerated by reprocessing
// stored telemetry. Safe to prune as database maintenance.
func (s *Store) PruneOldEvents(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan).Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM detection_events WHERE created_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// PruneOldScores removes score snapshots older than the given duration.
// Scores are DERIVED DATA (append-only snapshots) and can be regenerated
// from detection_events. Safe to prune as database maintenance.
func (s *Store) PruneOldScores(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan).Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM suspicion_scores WHERE snapshot_time < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// StoreEnforcementAction persists an enforcement action.
func (s *Store) StoreEnforcementAction(ctx context.Context, action model.EnforcementAction) error {
	evidenceJSON, _ := json.Marshal(action.EvidenceIDs)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO enforcement_actions (action_id, player_id, action_type, reason, issued_by, issued_at, evidence_ids, score_at_time, notes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		action.ActionID, action.PlayerID, action.ActionType, action.Reason,
		action.IssuedBy, action.IssuedAt.Format(time.RFC3339),
		string(evidenceJSON), action.ScoreAtTime, action.Notes,
	)
	return err
}
