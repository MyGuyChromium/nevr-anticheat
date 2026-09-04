package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// AnalysisSnapshot is the derived detector output immediately before a
// reprocess replaces it. Source telemetry remains canonical; snapshots are
// intentionally compact comparison aids rather than evidence sources.
type AnalysisSnapshot struct {
	SnapshotID int64                           `json:"snapshot_id"`
	MatchID    string                          `json:"match_id"`
	CreatedAt  time.Time                       `json:"created_at"`
	Events     []model.DetectionEvent          `json:"events"`
	Scores     map[string]model.SuspicionScore `json:"scores"`
}

// captureAnalysisSnapshotTx records the current derived output inside the
// same transaction that will remove it. Empty analyses are also snapshotted
// so a previously clean match can be compared with newly added signals.
func captureAnalysisSnapshotTx(ctx context.Context, tx *sql.Tx, matchID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT `+eventColumns+` FROM detection_events WHERE match_id = ? ORDER BY frame_index, detector_id`, matchID)
	if err != nil {
		return err
	}
	events, err := scanEventRows(rows)
	rows.Close()
	if err != nil {
		return err
	}
	eventsJSON, err := json.Marshal(events)
	if err != nil {
		return err
	}
	// Scores are optional context for the comparison UI. Event changes are
	// the regression contract because all shipped detectors remain shadow.
	scores := make(map[string]model.SuspicionScore)
	scoreRows, err := tx.QueryContext(ctx, `SELECT `+scoreColumns+` FROM suspicion_scores
		WHERE scope = ? AND match_id = ? ORDER BY id`, ScoreScopeMatch, matchID)
	if err == nil {
		for scoreRows.Next() {
			sc, _, scanErr := scanScore(scoreRows)
			if scanErr != nil {
				err = scanErr
				break
			}
			scores[sc.PlayerID] = sc
		}
		scoreRows.Close()
	}
	if err != nil {
		return err
	}
	scoresJSON, err := json.Marshal(scores)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO analysis_snapshots
		(match_id, created_at, events_json, scores_json) VALUES (?, ?, ?, ?)`,
		matchID, fmtDBTime(nowUTC()), string(eventsJSON), string(scoresJSON))
	if err != nil {
		return err
	}
	// Always-on desktop reanalysis can run frequently. Five comparisons per
	// match are enough for rollback/debugging without unbounded DB growth.
	_, err = tx.ExecContext(ctx, `DELETE FROM analysis_snapshots WHERE match_id = ? AND snapshot_id NOT IN
		(SELECT snapshot_id FROM analysis_snapshots WHERE match_id = ? ORDER BY snapshot_id DESC LIMIT 5)`, matchID, matchID)
	return err
}

func scanEventRows(rows *sql.Rows) ([]model.DetectionEvent, error) {
	var out []model.DetectionEvent
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

// GetLatestAnalysisSnapshot returns the newest pre-reprocess snapshot.
func (s *Store) GetLatestAnalysisSnapshot(ctx context.Context, matchID string) (AnalysisSnapshot, error) {
	var out AnalysisSnapshot
	var created, eventsJSON, scoresJSON string
	err := s.db.QueryRowContext(ctx, `SELECT snapshot_id, match_id, created_at, events_json, scores_json
		FROM analysis_snapshots WHERE match_id = ? ORDER BY snapshot_id DESC LIMIT 1`, matchID).
		Scan(&out.SnapshotID, &out.MatchID, &created, &eventsJSON, &scoresJSON)
	if err == sql.ErrNoRows {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	out.CreatedAt = parseDBTimeLenient(created)
	if err := json.Unmarshal([]byte(eventsJSON), &out.Events); err != nil {
		return out, fmt.Errorf("decoding analysis snapshot events: %w", err)
	}
	out.Scores = make(map[string]model.SuspicionScore)
	if err := json.Unmarshal([]byte(scoresJSON), &out.Scores); err != nil {
		return out, fmt.Errorf("decoding analysis snapshot scores: %w", err)
	}
	return out, nil
}
