package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// ErrNotFound is returned when a case, decision or match cannot be located.
var ErrNotFound = errors.New("not found")

// Case lifecycle statuses. "pending" is the only status aggregation may
// overwrite; every other status was set by a moderator and is preserved.
const (
	CaseStatusPending  = "pending"
	CaseStatusAssigned = "assigned"
	CaseStatusInReview = "in_review"
	CaseStatusDecided  = "decided"
	CaseStatusAppealed = "appealed"
	CaseStatusClosed   = "closed"
)

var validCaseStatuses = map[string]bool{
	CaseStatusPending: true, CaseStatusAssigned: true, CaseStatusInReview: true,
	CaseStatusDecided: true, CaseStatusAppealed: true, CaseStatusClosed: true,
}

// Moderator verdicts (model.ModeratorDecision.Verdict).
const (
	VerdictConfirmedCheat = "confirmed_cheat"
	VerdictFalsePositive  = "false_positive"
	VerdictInconclusive   = "inconclusive"
	VerdictNeedsMoreData  = "needs_more_data"
)

var validVerdicts = map[string]bool{
	VerdictConfirmedCheat: true, VerdictFalsePositive: true,
	VerdictInconclusive: true, VerdictNeedsMoreData: true,
}

// ValidVerdicts returns the accepted moderator verdict strings, sorted.
func ValidVerdicts() []string {
	return []string{VerdictConfirmedCheat, VerdictFalsePositive, VerdictInconclusive, VerdictNeedsMoreData}
}

// Score snapshot scopes (suspicion_scores.scope).
const (
	ScoreScopeMatch      = "match"
	ScoreScopeCrossMatch = "cross_match"
)

// pruneChunkSize bounds each DELETE so the single connection is released
// between chunks and ingestion is never blocked behind one long scan.
const pruneChunkSize = 5000

// Store provides SQLite-backed persistence.
type Store struct {
	db *sql.DB
}

// NewStore opens or creates a SQLite database with optimized settings and
// applies every schema migration. A migration or schema-verification failure
// is fatal: the store is not returned.
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

// ---------------------------------------------------------------------------
// Detection events
// ---------------------------------------------------------------------------

// eventColumns is the single column list used by every event reader so that
// evidence, causal key and provenance are never silently dropped.
const eventColumns = `event_id, detector_id, detector_version, match_id, player_id,
	frame_index, frame_range_start, frame_range_end, timestamp,
	severity, confidence, COALESCE(observed_value,''), COALESCE(expected_range,''),
	enforcement_weight, auto_enforce, is_shadow,
	COALESCE(evidence_json,''), COALESCE(evidence_type,''), COALESCE(causal_key,''), created_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEvent(r rowScanner) (model.DetectionEvent, error) {
	var ev model.DetectionEvent
	var autoEnforce, shadow int
	var evidenceJSON, evidenceType, causalJSON, createdAt string
	if err := r.Scan(
		&ev.EventID, &ev.DetectorID, &ev.DetectorVersion, &ev.MatchID, &ev.PlayerID,
		&ev.FrameIndex, &ev.FrameRangeStart, &ev.FrameRangeEnd, &ev.Timestamp,
		&ev.Severity, &ev.Confidence, &ev.ObservedValue, &ev.ExpectedRange,
		&ev.EnforcementWeight, &autoEnforce, &shadow,
		&evidenceJSON, &evidenceType, &causalJSON, &createdAt,
	); err != nil {
		return ev, err
	}
	ev.AutoEnforce = autoEnforce == 1
	ev.IsShadow = shadow == 1
	ev.StoredAt = parseDBTimeLenient(createdAt)
	if causalJSON != "" && causalJSON != "null" {
		_ = json.Unmarshal([]byte(causalJSON), &ev.CausalKey)
	}
	ev.Evidence = DecodeEvidence(evidenceType, evidenceJSON)
	return ev, nil
}

func (s *Store) queryEvents(ctx context.Context, query string, args ...any) ([]model.DetectionEvent, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []model.DetectionEvent
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// encodeEvidence serializes the typed evidence. A value that cannot be
// serialized (NaN/Inf) is recorded as a visible marker instead of being
// silently dropped, so the moderator report shows why evidence is missing.
func encodeEvidence(ev model.Evidence) (evidenceJSON, evidenceType string) {
	if ev == nil {
		return "", ""
	}
	b, err := json.Marshal(ev)
	if err != nil {
		msg, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(msg), EvidenceTypeMarshalError
	}
	return string(b), ev.EvidenceType()
}

// StoreDetectionEvent persists a detection event with analysis_source "initial".
func (s *Store) StoreDetectionEvent(ctx context.Context, event model.DetectionEvent) error {
	return s.StoreDetectionEventWithSource(ctx, event, "initial")
}

// StoreDetectionEventWithSource persists a detection event with an explicit analysis_source tag.
// source should be "initial" for first-time analysis or "reprocess" for reprocessed results.
func (s *Store) StoreDetectionEventWithSource(ctx context.Context, event model.DetectionEvent, source string) error {
	evidenceJSON, evidenceType := encodeEvidence(event.Evidence)
	causalJSON, err := json.Marshal(event.CausalKey)
	if err != nil {
		return fmt.Errorf("marshal causal key: %w", err)
	}
	autoEnforce := 0
	if event.AutoEnforce {
		autoEnforce = 1
	}
	shadow := 0
	if event.IsShadow {
		shadow = 1
	}
	createdAt := event.StoredAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO detection_events (event_id, detector_id, detector_version, match_id, player_id,
			frame_index, frame_range_start, frame_range_end, timestamp, severity, confidence,
			observed_value, expected_range, enforcement_weight, auto_enforce, is_shadow,
			evidence_json, evidence_type, causal_key, analysis_source, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.DetectorID, event.DetectorVersion, event.MatchID, event.PlayerID,
		event.FrameIndex, event.FrameRangeStart, event.FrameRangeEnd, event.Timestamp,
		event.Severity, event.Confidence, event.ObservedValue, event.ExpectedRange,
		event.EnforcementWeight, autoEnforce, shadow,
		evidenceJSON, evidenceType, string(causalJSON), source, fmtDBTime(createdAt),
	)
	return err
}

// StoreDetectionEvents persists a batch of events in one transaction.
// Returns the number of rows inserted.
func (s *Store) StoreDetectionEvents(ctx context.Context, events []model.DetectionEvent, source string) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO detection_events (event_id, detector_id, detector_version, match_id, player_id,
			frame_index, frame_range_start, frame_range_end, timestamp, severity, confidence,
			observed_value, expected_range, enforcement_weight, auto_enforce, is_shadow,
			evidence_json, evidence_type, causal_key, analysis_source, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()
	now := time.Now()
	inserted := 0
	for _, event := range events {
		evidenceJSON, evidenceType := encodeEvidence(event.Evidence)
		causalJSON, err := json.Marshal(event.CausalKey)
		if err != nil {
			return inserted, fmt.Errorf("marshal causal key for %s: %w", event.EventID, err)
		}
		autoEnforce, shadow := 0, 0
		if event.AutoEnforce {
			autoEnforce = 1
		}
		if event.IsShadow {
			shadow = 1
		}
		createdAt := event.StoredAt
		if createdAt.IsZero() {
			createdAt = now
		}
		res, err := stmt.ExecContext(ctx,
			event.EventID, event.DetectorID, event.DetectorVersion, event.MatchID, event.PlayerID,
			event.FrameIndex, event.FrameRangeStart, event.FrameRangeEnd, event.Timestamp,
			event.Severity, event.Confidence, event.ObservedValue, event.ExpectedRange,
			event.EnforcementWeight, autoEnforce, shadow,
			evidenceJSON, evidenceType, string(causalJSON), source, fmtDBTime(createdAt))
		if err != nil {
			return inserted, fmt.Errorf("insert event %s: %w", event.EventID, err)
		}
		n, _ := res.RowsAffected()
		inserted += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return inserted, nil
}

// GetPlayerEvents returns detection events for a player, newest first
// (created_at, then match/frame for a deterministic order within a second).
func (s *Store) GetPlayerEvents(ctx context.Context, playerID string, limit, offset int) ([]model.DetectionEvent, error) {
	return s.queryEvents(ctx,
		`SELECT `+eventColumns+` FROM detection_events
		 WHERE player_id = ?
		 ORDER BY created_at DESC, match_id, frame_index DESC LIMIT ? OFFSET ?`,
		playerID, limit, offset)
}

// GetMatchEvents returns all detection events for a match ordered by frame.
func (s *Store) GetMatchEvents(ctx context.Context, matchID string) ([]model.DetectionEvent, error) {
	return s.queryEvents(ctx,
		`SELECT `+eventColumns+` FROM detection_events
		 WHERE match_id = ? ORDER BY frame_index, player_id, detector_id`, matchID)
}

// GetMatchPlayerEvents returns a player's events in one match ordered by frame.
func (s *Store) GetMatchPlayerEvents(ctx context.Context, matchID, playerID string) ([]model.DetectionEvent, error) {
	return s.queryEvents(ctx,
		`SELECT `+eventColumns+` FROM detection_events
		 WHERE match_id = ? AND player_id = ? ORDER BY frame_index, detector_id`, matchID, playerID)
}

// GetPlayerHistoryEvents returns a player's non-shadow events from the most
// recent matchLimit distinct matches. Matches are ordered by their wall-clock
// start (match_contexts.match_start_time), falling back to the newest
// created_at in the match. Meta-detectors PAT_003 and PAT_004 are excluded:
// their output is derived from other detectors and must not feed itself.
// Every event of a selected match is returned (no row cap), so a match is never
// cut in half.
func (s *Store) GetPlayerHistoryEvents(ctx context.Context, playerID string, matchLimit int) ([]model.DetectionEvent, error) {
	if matchLimit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT de.match_id
		 FROM detection_events de
		 LEFT JOIN match_contexts mc ON mc.match_id = de.match_id
		 WHERE de.player_id = ? AND de.is_shadow = 0 AND de.match_id != ''
		   AND de.detector_id NOT IN ('PAT_003', 'PAT_004')
		 GROUP BY de.match_id
		 ORDER BY COALESCE(mc.match_start_time, MAX(de.created_at)) DESC, de.match_id
		 LIMIT ?`, playerID, matchLimit)
	if err != nil {
		return nil, err
	}
	var matchIDs []string
	for rows.Next() {
		var mid string
		if err := rows.Scan(&mid); err != nil {
			rows.Close()
			return nil, err
		}
		matchIDs = append(matchIDs, mid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(matchIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(matchIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(matchIDs)+1)
	args = append(args, playerID)
	for _, mid := range matchIDs {
		args = append(args, mid)
	}
	return s.queryEvents(ctx,
		`SELECT `+eventColumns+` FROM detection_events
		 WHERE player_id = ? AND is_shadow = 0
		   AND detector_id NOT IN ('PAT_003', 'PAT_004')
		   AND match_id IN (`+placeholders+`)
		 ORDER BY match_id, frame_index, detector_id`, args...)
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

// DeleteMatchScores removes the per-match score snapshots recorded for a match
// (scope = 'match', match_id = matchID). Cross-match snapshots and other
// matches' snapshots are untouched. Safe to call before or after DeleteMatchEvents.
func (s *Store) DeleteMatchScores(ctx context.Context, matchID string) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM suspicion_scores WHERE scope = ? AND match_id = ?`,
		ScoreScopeMatch, matchID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteMatchAnalysis removes every derived output for a match (events and
// per-match score snapshots) so the match can be re-analyzed idempotently.
// Returns (events deleted, scores deleted).
func (s *Store) DeleteMatchAnalysis(ctx context.Context, matchID string) (int64, int64, error) {
	events, err := s.DeleteMatchEvents(ctx, matchID)
	if err != nil {
		return 0, 0, fmt.Errorf("deleting events: %w", err)
	}
	scores, err := s.DeleteMatchScores(ctx, matchID)
	if err != nil {
		return events, 0, fmt.Errorf("deleting scores: %w", err)
	}
	return events, scores, nil
}

// PruneOldEvents removes detection events older than the given duration.
// Detection events are DERIVED DATA and can be regenerated by reprocessing
// stored telemetry. Safe to prune as database maintenance.
// Deletes run in chunks so ingestion is never blocked behind one long delete.
func (s *Store) PruneOldEvents(ctx context.Context, olderThan time.Duration) (int64, error) {
	return s.pruneBefore(ctx, "detection_events", "created_at", time.Now().Add(-olderThan))
}

// PruneOldScores removes score snapshots older than the given duration.
// Scores are DERIVED DATA (append-only snapshots) and can be regenerated
// from detection_events. Safe to prune as database maintenance.
func (s *Store) PruneOldScores(ctx context.Context, olderThan time.Duration) (int64, error) {
	return s.pruneBefore(ctx, "suspicion_scores", "snapshot_time", time.Now().Add(-olderThan))
}

// pruneBefore deletes rows of table whose column is strictly before cutoff,
// in chunks of pruneChunkSize. Table and column names are internal constants.
func (s *Store) pruneBefore(ctx context.Context, table, column string, cutoff time.Time) (int64, error) {
	cutoffStr := fmtDBTime(cutoff)
	query := fmt.Sprintf(
		"DELETE FROM %s WHERE rowid IN (SELECT rowid FROM %s WHERE %s < ? LIMIT %d)",
		table, table, column, pruneChunkSize)
	var total int64
	for {
		result, err := s.db.ExecContext(ctx, query, cutoffStr)
		if err != nil {
			return total, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < pruneChunkSize {
			return total, nil
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
	}
}

// ---------------------------------------------------------------------------
// Suspicion score snapshots
// ---------------------------------------------------------------------------

// StoreSuspicionScore appends a per-match suspicion score snapshot (scope 'match').
// Scores are DERIVED DATA — append-only convenience snapshots, not source truth.
// The match_id column is filled when the score belongs to exactly one match
// (score.MatchIDs has one entry); use StoreMatchSuspicionScore to set it explicitly.
func (s *Store) StoreSuspicionScore(ctx context.Context, score model.SuspicionScore) error {
	matchID := ""
	if len(score.MatchIDs) == 1 {
		for mid := range score.MatchIDs {
			matchID = mid
		}
	}
	return s.storeScore(ctx, ScoreScopeMatch, matchID, score)
}

// StoreMatchSuspicionScore appends a per-match snapshot tagged with matchID so
// DeleteMatchScores can remove it on reprocessing.
func (s *Store) StoreMatchSuspicionScore(ctx context.Context, matchID string, score model.SuspicionScore) error {
	return s.storeScore(ctx, ScoreScopeMatch, matchID, score)
}

func (s *Store) storeScore(ctx context.Context, scope, matchID string, score model.SuspicionScore) error {
	detJSON, err := json.Marshal(score.ScoreByDetector)
	if err != nil {
		return fmt.Errorf("marshal score_by_detector: %w", err)
	}
	catJSON, err := json.Marshal(score.ScoreByCategory)
	if err != nil {
		return fmt.Errorf("marshal score_by_category: %w", err)
	}
	var matchPtr *string
	if matchID != "" {
		matchPtr = &matchID
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO suspicion_scores (player_id, total_score, score_by_detector, score_by_category,
			event_count, match_count, snapshot_time, scope, match_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		score.PlayerID, score.TotalScore, string(detJSON), string(catJSON),
		score.EventCount, score.MatchCount, fmtDBTime(score.SnapshotTime), scope, matchPtr,
	)
	return err
}

const scoreColumns = `player_id, total_score, COALESCE(score_by_detector,''), COALESCE(score_by_category,''),
	event_count, match_count, snapshot_time, COALESCE(match_id,'')`

func scanScore(r rowScanner) (model.SuspicionScore, string, error) {
	var sc model.SuspicionScore
	var detJSON, catJSON, snapStr, matchID string
	if err := r.Scan(&sc.PlayerID, &sc.TotalScore, &detJSON, &catJSON,
		&sc.EventCount, &sc.MatchCount, &snapStr, &matchID); err != nil {
		return sc, "", err
	}
	if detJSON != "" {
		_ = json.Unmarshal([]byte(detJSON), &sc.ScoreByDetector)
	}
	if catJSON != "" {
		_ = json.Unmarshal([]byte(catJSON), &sc.ScoreByCategory)
	}
	sc.SnapshotTime = parseDBTimeLenient(snapStr)
	if matchID != "" {
		sc.MatchIDs = map[string]bool{matchID: true}
	}
	return sc, matchID, nil
}

// GetPlayerScore returns the most recent per-match suspicion score snapshot for
// a player. Ties within one second are broken by insertion order (id), so the
// result is always the last snapshot written. Cross-match snapshots are never
// returned here; see GetPlayerCrossMatchScore.
func (s *Store) GetPlayerScore(ctx context.Context, playerID string) (model.SuspicionScore, error) {
	return s.latestScore(ctx, playerID, ScoreScopeMatch)
}

// GetPlayerCrossMatchScore returns the most recent cross-match aggregation snapshot.
func (s *Store) GetPlayerCrossMatchScore(ctx context.Context, playerID string) (model.SuspicionScore, error) {
	return s.latestScore(ctx, playerID, ScoreScopeCrossMatch)
}

func (s *Store) latestScore(ctx context.Context, playerID, scope string) (model.SuspicionScore, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+scoreColumns+` FROM suspicion_scores
		 WHERE player_id = ? AND scope = ?
		 ORDER BY snapshot_time DESC, id DESC LIMIT 1`, playerID, scope)
	sc, _, err := scanScore(row)
	return sc, err
}

// GetPlayerHistory returns per-match score snapshots at or after `since`, oldest first.
func (s *Store) GetPlayerHistory(ctx context.Context, playerID string, since time.Time) ([]model.SuspicionScore, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+scoreColumns+` FROM suspicion_scores
		 WHERE player_id = ? AND scope = ? AND snapshot_time >= ?
		 ORDER BY snapshot_time, id`,
		playerID, ScoreScopeMatch, fmtDBTime(since),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var scores []model.SuspicionScore
	for rows.Next() {
		sc, _, err := scanScore(rows)
		if err != nil {
			return nil, err
		}
		scores = append(scores, sc)
	}
	return scores, rows.Err()
}

// ---------------------------------------------------------------------------
// Review cases
// ---------------------------------------------------------------------------

// StoreReviewCase inserts or updates a review case. On conflict the analytical
// columns are refreshed but a status set by a moderator (anything other than
// 'pending') and an existing assignment are preserved, so re-running analysis
// never reopens a handled case.
func (s *Store) StoreReviewCase(ctx context.Context, rc model.ReviewCase) error {
	detectorsJSON, err := json.Marshal(rc.DetectorsTriggered)
	if err != nil {
		return fmt.Errorf("marshal detectors: %w", err)
	}
	status := rc.Status
	if status == "" {
		status = CaseStatusPending
	}
	now := time.Now()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO review_cases (case_id, player_id, match_id, severity, suspicion_score,
			recommended_action, explanation, status, assigned_to, detectors_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(case_id) DO UPDATE SET
			player_id = excluded.player_id,
			match_id = excluded.match_id,
			severity = excluded.severity,
			suspicion_score = excluded.suspicion_score,
			recommended_action = excluded.recommended_action,
			explanation = excluded.explanation,
			detectors_json = excluded.detectors_json,
			status = CASE WHEN review_cases.status = 'pending' THEN excluded.status ELSE review_cases.status END,
			assigned_to = COALESCE(NULLIF(review_cases.assigned_to, ''), excluded.assigned_to),
			updated_at = excluded.updated_at`,
		rc.CaseID, rc.PlayerID, rc.MatchID, rc.Severity, rc.SuspicionScore,
		rc.RecommendedAction, rc.Explanation, status, rc.AssignedTo,
		string(detectorsJSON), fmtDBTime(rc.CreatedAt), fmtDBTime(now),
	)
	return err
}

const reviewCaseColumns = `case_id, player_id, match_id, COALESCE(severity,''), COALESCE(suspicion_score,0),
	COALESCE(recommended_action,''), COALESCE(explanation,''), status, COALESCE(assigned_to,''),
	COALESCE(detectors_json,''), created_at, COALESCE(updated_at,'')`

func scanReviewCase(r rowScanner) (model.ReviewCase, error) {
	var rc model.ReviewCase
	var detectorsJSON, createdStr, updatedStr string
	if err := r.Scan(&rc.CaseID, &rc.PlayerID, &rc.MatchID, &rc.Severity, &rc.SuspicionScore,
		&rc.RecommendedAction, &rc.Explanation, &rc.Status, &rc.AssignedTo,
		&detectorsJSON, &createdStr, &updatedStr); err != nil {
		return rc, err
	}
	if detectorsJSON != "" {
		_ = json.Unmarshal([]byte(detectorsJSON), &rc.DetectorsTriggered)
	}
	rc.CreatedAt = parseDBTimeLenient(createdStr)
	if updatedStr != "" {
		rc.UpdatedAt = parseDBTimeLenient(updatedStr)
	}
	return rc, nil
}

// GetReviewCase retrieves a review case by ID.
func (s *Store) GetReviewCase(ctx context.Context, caseID string) (model.ReviewCase, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+reviewCaseColumns+` FROM review_cases WHERE case_id = ?`, caseID)
	return scanReviewCase(row)
}

// GetPendingReviewCases returns pending review cases, highest score first.
func (s *Store) GetPendingReviewCases(ctx context.Context, limit int) ([]model.ReviewCase, error) {
	return s.queryReviewCases(ctx,
		`SELECT `+reviewCaseColumns+` FROM review_cases
		 WHERE status = 'pending' ORDER BY suspicion_score DESC, case_id LIMIT ?`, limit)
}

// GetReviewCasesByPlayer returns every review case for a player, newest first.
func (s *Store) GetReviewCasesByPlayer(ctx context.Context, playerID string) ([]model.ReviewCase, error) {
	return s.queryReviewCases(ctx,
		`SELECT `+reviewCaseColumns+` FROM review_cases
		 WHERE player_id = ? ORDER BY created_at DESC, case_id`, playerID)
}

func (s *Store) queryReviewCases(ctx context.Context, query string, args ...any) ([]model.ReviewCase, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cases []model.ReviewCase
	for rows.Next() {
		rc, err := scanReviewCase(rows)
		if err != nil {
			return nil, err
		}
		cases = append(cases, rc)
	}
	return cases, rows.Err()
}

// UpdateReviewCaseStatus changes the status (and optionally the assignee) of a
// single-match or cross-match review case. assignedTo == "" leaves the current
// assignment untouched. Returns ErrNotFound when no case has that ID.
func (s *Store) UpdateReviewCaseStatus(ctx context.Context, caseID, status, assignedTo string, at time.Time) error {
	if !validCaseStatuses[status] {
		return fmt.Errorf("invalid case status %q", status)
	}
	return s.updateCaseStatusTx(ctx, s.db, caseID, status, assignedTo, at)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) updateCaseStatusTx(ctx context.Context, ex execer, caseID, status, assignedTo string, at time.Time) error {
	res, err := ex.ExecContext(ctx,
		`UPDATE review_cases SET status = ?,
			assigned_to = CASE WHEN ? = '' THEN assigned_to ELSE ? END,
			updated_at = ?
		 WHERE case_id = ?`, status, assignedTo, assignedTo, fmtDBTime(at), caseID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	res, err = ex.ExecContext(ctx,
		`UPDATE cross_match_review_cases SET status = ?, updated_at = ? WHERE case_id = ?`,
		status, fmtDBTime(at), caseID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	return fmt.Errorf("case %s: %w", caseID, ErrNotFound)
}

// ---------------------------------------------------------------------------
// Moderator decisions
// ---------------------------------------------------------------------------

// StoreModeratorDecision records a moderator verdict and marks the referenced
// case (single-match or cross-match) as 'decided' in the same transaction.
// The verdict must be one of ValidVerdicts. A missing DecisionID or DecidedAt
// is filled in.
func (s *Store) StoreModeratorDecision(ctx context.Context, d model.ModeratorDecision) error {
	if !validVerdicts[d.Verdict] {
		return fmt.Errorf("invalid verdict %q (want one of %s)", d.Verdict, strings.Join(ValidVerdicts(), ", "))
	}
	if d.CaseID == "" || d.ModeratorID == "" {
		return fmt.Errorf("case_id and moderator_id are required")
	}
	for _, fb := range d.DetectorFeedback {
		switch fb.Correct {
		case "yes", "no", "uncertain":
		default:
			return fmt.Errorf("invalid detector feedback %q for %s (want yes|no|uncertain)", fb.Correct, fb.DetectorID)
		}
	}
	if d.DecisionID == "" {
		d.DecisionID = uuid.New().String()
	}
	if d.DecidedAt.IsZero() {
		d.DecidedAt = time.Now()
	}
	feedbackJSON, err := json.Marshal(d.DetectorFeedback)
	if err != nil {
		return fmt.Errorf("marshal detector feedback: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := s.updateCaseStatusTx(ctx, tx, d.CaseID, CaseStatusDecided, d.ModeratorID, d.DecidedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO moderator_decisions (decision_id, case_id, moderator_id, verdict, action_taken, notes,
			decided_at, detector_feedback, confidence_override, review_duration_sec)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.DecisionID, d.CaseID, d.ModeratorID, d.Verdict, d.ActionTaken, d.Notes,
		fmtDBTime(d.DecidedAt), string(feedbackJSON), d.ConfidenceOverride, d.ReviewDurationSec,
	); err != nil {
		return fmt.Errorf("insert decision: %w", err)
	}
	return tx.Commit()
}

const decisionColumns = `decision_id, case_id, moderator_id, verdict, COALESCE(action_taken,''), COALESCE(notes,''),
	decided_at, COALESCE(detector_feedback,''), confidence_override, review_duration_sec`

func scanDecision(r rowScanner) (model.ModeratorDecision, error) {
	var d model.ModeratorDecision
	var decidedStr, feedbackJSON string
	var confOverride sql.NullFloat64
	if err := r.Scan(&d.DecisionID, &d.CaseID, &d.ModeratorID, &d.Verdict, &d.ActionTaken, &d.Notes,
		&decidedStr, &feedbackJSON, &confOverride, &d.ReviewDurationSec); err != nil {
		return d, err
	}
	d.DecidedAt = parseDBTimeLenient(decidedStr)
	if feedbackJSON != "" && feedbackJSON != "null" {
		_ = json.Unmarshal([]byte(feedbackJSON), &d.DetectorFeedback)
	}
	if confOverride.Valid {
		v := confOverride.Float64
		d.ConfidenceOverride = &v
	}
	return d, nil
}

// ListModeratorDecisions returns decisions made at or after `since`, newest
// first. A zero `since` returns all; limit <= 0 means no limit.
func (s *Store) ListModeratorDecisions(ctx context.Context, since time.Time, limit int) ([]model.ModeratorDecision, error) {
	sinceStr := "0000-00-00T00:00:00Z"
	if !since.IsZero() {
		sinceStr = fmtDBTime(since)
	}
	if limit <= 0 {
		limit = -1 // SQLite: no limit
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+decisionColumns+` FROM moderator_decisions
		 WHERE decided_at >= ? ORDER BY decided_at DESC, decision_id LIMIT ?`, sinceStr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ModeratorDecision
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetCaseDecisions returns every decision recorded for one case, oldest first.
func (s *Store) GetCaseDecisions(ctx context.Context, caseID string) ([]model.ModeratorDecision, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+decisionColumns+` FROM moderator_decisions
		 WHERE case_id = ? ORDER BY decided_at, decision_id`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ModeratorDecision
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Match summaries and enforcement
// ---------------------------------------------------------------------------

// StoreMatchSummary persists a match summary.
func (s *Store) StoreMatchSummary(ctx context.Context, summary model.MatchSummary) error {
	flaggedJSON, err := json.Marshal(summary.FlaggedPlayers)
	if err != nil {
		return fmt.Errorf("marshal flagged players: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO match_summaries (match_id, map_name, game_mode, is_ranked, start_time,
			duration_seconds, frame_count, total_detections, flagged_players, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		summary.MatchID, summary.Map, summary.GameMode, summary.IsRanked,
		fmtDBTimeOrEmpty(summary.StartTime), summary.Duration.Seconds(),
		summary.FrameCount, summary.TotalDetectionEvents, string(flaggedJSON), fmtDBTime(time.Now()),
	)
	return err
}

// StoreEnforcementAction persists an enforcement action including its duration
// and the matches that justified it.
func (s *Store) StoreEnforcementAction(ctx context.Context, action model.EnforcementAction) error {
	evidenceJSON, err := json.Marshal(action.EvidenceIDs)
	if err != nil {
		return fmt.Errorf("marshal evidence ids: %w", err)
	}
	matchJSON, err := json.Marshal(action.MatchIDs)
	if err != nil {
		return fmt.Errorf("marshal match ids: %w", err)
	}
	var duration *float64
	if action.Duration > 0 {
		d := action.Duration.Seconds()
		duration = &d
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO enforcement_actions (action_id, player_id, action_type, reason, duration_seconds,
			issued_by, issued_at, evidence_ids, match_ids, score_at_time, notes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		action.ActionID, action.PlayerID, action.ActionType, action.Reason, duration,
		action.IssuedBy, fmtDBTime(action.IssuedAt),
		string(evidenceJSON), string(matchJSON), action.ScoreAtTime, action.Notes, fmtDBTime(time.Now()),
	)
	return err
}

// GetEnforcementActions returns a player's enforcement history, newest first.
func (s *Store) GetEnforcementActions(ctx context.Context, playerID string, limit int) ([]model.EnforcementAction, error) {
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT action_id, player_id, action_type, COALESCE(reason,''), duration_seconds,
			issued_by, issued_at, COALESCE(evidence_ids,''), COALESCE(match_ids,''),
			COALESCE(score_at_time,0), COALESCE(notes,'')
		 FROM enforcement_actions WHERE player_id = ?
		 ORDER BY issued_at DESC, action_id LIMIT ?`, playerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EnforcementAction
	for rows.Next() {
		var a model.EnforcementAction
		var duration sql.NullFloat64
		var issuedStr, evidenceJSON, matchJSON string
		if err := rows.Scan(&a.ActionID, &a.PlayerID, &a.ActionType, &a.Reason, &duration,
			&a.IssuedBy, &issuedStr, &evidenceJSON, &matchJSON, &a.ScoreAtTime, &a.Notes); err != nil {
			return nil, err
		}
		if duration.Valid {
			a.Duration = time.Duration(duration.Float64 * float64(time.Second))
		}
		a.IssuedAt = parseDBTimeLenient(issuedStr)
		if evidenceJSON != "" {
			_ = json.Unmarshal([]byte(evidenceJSON), &a.EvidenceIDs)
		}
		if matchJSON != "" {
			_ = json.Unmarshal([]byte(matchJSON), &a.MatchIDs)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
