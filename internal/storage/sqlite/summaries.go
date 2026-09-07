package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// MatchSummaryMeta is what the offline analysis writes into the legacy
// columns of match_summaries next to its JSON document, so readers of those
// columns (the live pipeline's prior-summary lookup, ad-hoc SQL) see the
// same facts whichever path produced the row.
type MatchSummaryMeta struct {
	MatchID         string
	Map             string
	GameMode        string
	IsRanked        bool
	StartTime       time.Time
	Duration        time.Duration
	FrameCount      int
	TotalDetections int
	FlaggedPlayers  []string // sorted
}

// StoreMatchSummaryJSON upserts a match's summary row: the legacy columns
// from meta and the summary document (schema owned by internal/replay) in
// summary_json. Replaces any previous document of the match.
func (s *Store) StoreMatchSummaryJSON(ctx context.Context, meta MatchSummaryMeta, doc []byte) error {
	if len(doc) == 0 || !json.Valid(doc) {
		return fmt.Errorf("match %s: summary document is not valid JSON", meta.MatchID)
	}
	flagged := meta.FlaggedPlayers
	if flagged == nil {
		flagged = []string{}
	}
	flaggedJSON, err := json.Marshal(flagged)
	if err != nil {
		return fmt.Errorf("marshal flagged players: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO match_summaries (match_id, map_name, game_mode, is_ranked, start_time,
			duration_seconds, frame_count, total_detections, flagged_players, created_at, summary_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(match_id) DO UPDATE SET
			map_name = excluded.map_name, game_mode = excluded.game_mode, is_ranked = excluded.is_ranked,
			start_time = excluded.start_time, duration_seconds = excluded.duration_seconds,
			frame_count = excluded.frame_count, total_detections = excluded.total_detections,
			flagged_players = excluded.flagged_players, created_at = excluded.created_at,
			summary_json = excluded.summary_json`,
		meta.MatchID, meta.Map, meta.GameMode, meta.IsRanked,
		fmtDBTimeOrEmpty(meta.StartTime), meta.Duration.Seconds(),
		meta.FrameCount, meta.TotalDetections, string(flaggedJSON), fmtDBTime(nowUTC()), string(doc))
	return err
}

// GetMatchSummaryJSON returns the stored summary document of a match, or
// ErrNotFound when the match has no summary row or its row predates the
// document (rows written by older versions or by the live pipeline).
func (s *Store) GetMatchSummaryJSON(ctx context.Context, matchID string) ([]byte, error) {
	var doc sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT summary_json FROM match_summaries WHERE match_id = ?`, matchID).Scan(&doc)
	if err == sql.ErrNoRows || (err == nil && (!doc.Valid || doc.String == "")) {
		return nil, fmt.Errorf("summary of match %s: %w", matchID, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return []byte(doc.String), nil
}

// DeleteMatchSummary removes a match's summary row (tests and maintenance).
func (s *Store) DeleteMatchSummary(ctx context.Context, matchID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM match_summaries WHERE match_id = ?`, matchID)
	return err
}

// ForEachMatchTick streams a match's raw session payloads in frame order to
// fn, one at a time (a full match holds hundreds of megabytes of JSON, so
// nothing is accumulated). Each tick absent from match_ticks falls back to
// telemetry_frames.raw_json, including partially upgraded matches. Returns fn's
// error as it is; n is the number of ticks delivered.
func (s *Store) ForEachMatchTick(ctx context.Context, matchID string, fn func(frameIndex int, raw string) error) (n int, err error) {
	rows, err := s.db.QueryContext(ctx, matchRawTicksSelect+` ORDER BY frame_index`, matchID, matchID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n = 0
	for rows.Next() {
		var idx int
		var raw string
		if err := rows.Scan(&idx, &raw); err != nil {
			return n, err
		}
		n++
		if err := fn(idx, raw); err != nil {
			return n, err
		}
	}
	return n, rows.Err()
}

// GetMatchTickTimestamps returns the match-relative time (seconds since the
// first sample, as the mapper stamped it on the telemetry frames) of every
// stored frame index of a match: the time base a raw tick lost when only
// its payload was kept.
func (s *Store) GetMatchTickTimestamps(ctx context.Context, matchID string) (map[int]float64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT frame_index, MIN(timestamp) FROM telemetry_frames WHERE match_id = ? GROUP BY frame_index`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int]float64)
	for rows.Next() {
		var idx int
		var ts float64
		if err := rows.Scan(&idx, &ts); err != nil {
			return nil, err
		}
		out[idx] = ts
	}
	return out, rows.Err()
}
