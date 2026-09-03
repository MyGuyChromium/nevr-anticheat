package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// StoredMatch is one match_contexts row: the persisted context plus what
// the store knows about the ingest (used by the desktop app's history).
type StoredMatch struct {
	Context    *model.MatchContext
	FrameCount int
	IngestedAt time.Time
}

const storedMatchColumns = `match_id, context_json, frame_count, ingested_at`

func scanStoredMatch(r rowScanner) (StoredMatch, error) {
	var sm StoredMatch
	var matchID, ctxJSON, ingested string
	if err := r.Scan(&matchID, &ctxJSON, &sm.FrameCount, &ingested); err != nil {
		return sm, err
	}
	var mc model.MatchContext
	if err := json.Unmarshal([]byte(ctxJSON), &mc); err != nil {
		return sm, fmt.Errorf("unmarshal match context %s: %w", matchID, err)
	}
	if mc.MatchID == "" {
		mc.MatchID = matchID
	}
	sm.Context = &mc
	sm.IngestedAt = parseDBTimeLenient(ingested)
	return sm, nil
}

// GetStoredMatch returns the explicit match_contexts row for a match, or
// ErrNotFound when none was stored (a live match whose context was never
// finalized; see GetMatchContext for the synthesized fallback).
func (s *Store) GetStoredMatch(ctx context.Context, matchID string) (StoredMatch, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+storedMatchColumns+` FROM match_contexts WHERE match_id = ?`, matchID)
	sm, err := scanStoredMatch(row)
	if err == sql.ErrNoRows {
		return sm, fmt.Errorf("match %s: %w", matchID, ErrNotFound)
	}
	return sm, err
}

// ListMatches returns the most recently ingested matches (newest first, at
// most limit), one per match_contexts row.
func (s *Store) ListMatches(ctx context.Context, limit int) ([]StoredMatch, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+storedMatchColumns+` FROM match_contexts
		 ORDER BY ingested_at DESC, match_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredMatch
	for rows.Next() {
		sm, err := scanStoredMatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// GetMatchScores returns the latest per-match score snapshot of every player
// scored in the match (scope 'match'). Players without a snapshot (no scored
// events) are absent.
func (s *Store) GetMatchScores(ctx context.Context, matchID string) (map[string]model.SuspicionScore, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+scoreColumns+` FROM suspicion_scores
		 WHERE scope = ? AND match_id = ?
		 ORDER BY snapshot_time, id`, ScoreScopeMatch, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]model.SuspicionScore)
	for rows.Next() {
		sc, _, err := scanScore(rows)
		if err != nil {
			return nil, err
		}
		out[sc.PlayerID] = sc // later snapshots overwrite earlier ones
	}
	return out, rows.Err()
}

// GetReviewCasesByMatch returns every single-match review case of a match,
// highest score first.
func (s *Store) GetReviewCasesByMatch(ctx context.Context, matchID string) ([]model.ReviewCase, error) {
	return s.queryReviewCases(ctx,
		`SELECT `+reviewCaseColumns+` FROM review_cases
		 WHERE match_id = ? ORDER BY suspicion_score DESC, case_id`, matchID)
}

// GetMatchPlayerFrameCounts returns how many telemetry frames are stored per
// player for a match.
func (s *Store) GetMatchPlayerFrameCounts(ctx context.Context, matchID string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT player_id, COUNT(*) FROM telemetry_frames WHERE match_id = ? GROUP BY player_id`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var pid string
		var n int
		if err := rows.Scan(&pid, &n); err != nil {
			return nil, err
		}
		out[pid] = n
	}
	return out, rows.Err()
}

// GetMatchFinalScore returns the team scores carried by the last stored
// telemetry frame of a match. ok is false when the match has no frames or
// no frame ever carried a score.
func (s *Store) GetMatchFinalScore(ctx context.Context, matchID string) (blue, orange int, ok bool, err error) {
	var frameJSON string
	err = s.db.QueryRowContext(ctx,
		`SELECT frame_json FROM telemetry_frames WHERE match_id = ?
		 ORDER BY frame_index DESC, player_id LIMIT 1`, matchID).Scan(&frameJSON)
	if err == sql.ErrNoRows {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	var f model.PlayerTelemetryFrame
	if err := json.Unmarshal([]byte(frameJSON), &f); err != nil {
		return 0, 0, false, fmt.Errorf("unmarshal last frame of %s: %w", matchID, err)
	}
	if f.BlueScore == 0 && f.OrangeScore == 0 {
		var n int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM telemetry_frames WHERE match_id = ?
			 AND (frame_json LIKE '%"blue_score":%' OR frame_json LIKE '%"orange_score":%')`, matchID).Scan(&n); err != nil {
			return 0, 0, false, err
		}
		return 0, 0, n > 0, nil
	}
	return f.BlueScore, f.OrangeScore, true, nil
}
