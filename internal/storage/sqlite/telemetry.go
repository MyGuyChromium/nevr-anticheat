package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// TelemetryFrameRow pairs a normalized frame with optional raw profiler JSON.
//
// Storage contract for telemetry_frames:
//
//   frame_json: Always populated. Contains the normalized PlayerTelemetryFrame
//               as JSON. This is what the detection pipeline consumes during
//               reprocessing. All fields needed for current detectors are here.
//
//   raw_json:   The original, unmodified profiler/API session payload.
//               Populated when the source provides richer data than the normalized
//               frame captures (e.g., per-player stats, goal events, disc bounce
//               count, player level). Specifically:
//
//               - .echoreplay ingestion: populated with EchoVRSessionResponse JSON.
//                 Contains assists, saves, steals, passes, catches, blocks,
//                 interceptions, possession_time, shots_taken, last_score details,
//                 player level/name, and any future API fields.
//
//               - Legacy JSON ingestion: NULL. The legacy format IS the normalized
//                 schema (RawFrame). No additional profiler fields exist to preserve.
//
//               - Live WebSocket ingestion: NULL. The FrameBatch protocol sends
//                 PlayerTelemetryFrame directly. No extra profiler fields in transit.
//
//               NULL means "not available from this source", never "bug / not wired."
//               Future ingestion paths that have raw profiler payloads should populate
//               this column to preserve the profiler truth.
type TelemetryFrameRow struct {
	Frame   model.PlayerTelemetryFrame
	RawJSON string // original profiler/API JSON, empty if unavailable
}

// StoreTelemetryFrames persists normalized telemetry frames for a match in bulk.
// Uses a transaction with prepared statement for efficiency.
func (s *Store) StoreTelemetryFrames(ctx context.Context, matchID string, frames []model.PlayerTelemetryFrame) (int, error) {
	rows := make([]TelemetryFrameRow, len(frames))
	for i, f := range frames {
		rows[i] = TelemetryFrameRow{Frame: f}
	}
	return s.StoreTelemetryFrameRows(ctx, matchID, rows)
}

// StoreTelemetryFrameRows persists telemetry frame rows that may include raw profiler JSON.
// The raw_json column preserves the original API/profiler payload (stats, goal events, etc.)
// that the normalized frame_json does not capture.
func (s *Store) StoreTelemetryFrameRows(ctx context.Context, matchID string, rows []TelemetryFrameRow) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO telemetry_frames (match_id, player_id, frame_index, timestamp, frame_json, raw_json)
		 VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	stored := 0
	for _, row := range rows {
		frameJSON, err := json.Marshal(row.Frame)
		if err != nil {
			continue
		}
		var rawPtr *string
		if row.RawJSON != "" {
			rawPtr = &row.RawJSON
		}
		_, err = stmt.ExecContext(ctx, matchID, row.Frame.PlayerID, row.Frame.FrameIndex,
			row.Frame.Timestamp, string(frameJSON), rawPtr)
		if err != nil {
			continue
		}
		stored++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return stored, nil
}

// StoreMatchContext persists match metadata so it can be reconstructed for reprocessing.
func (s *Store) StoreMatchContext(ctx context.Context, matchCtx *model.MatchContext, frameCount int) error {
	ctxJSON, err := json.Marshal(matchCtx)
	if err != nil {
		return fmt.Errorf("marshal match context: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO match_contexts (match_id, context_json, frame_count)
		 VALUES (?, ?, ?)`,
		matchCtx.MatchID, string(ctxJSON), frameCount,
	)
	return err
}

// GetMatchContext loads match metadata from the database.
func (s *Store) GetMatchContext(ctx context.Context, matchID string) (*model.MatchContext, error) {
	var ctxJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT context_json FROM match_contexts WHERE match_id = ?`, matchID,
	).Scan(&ctxJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("match %s not found in database", matchID)
		}
		return nil, err
	}
	var mc model.MatchContext
	if err := json.Unmarshal([]byte(ctxJSON), &mc); err != nil {
		return nil, fmt.Errorf("unmarshal match context: %w", err)
	}
	return &mc, nil
}

// GetMatchFrames loads all telemetry frames for a match from the database,
// ordered by frame_index and player_id.
func (s *Store) GetMatchFrames(ctx context.Context, matchID string) ([]model.PlayerTelemetryFrame, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT frame_json FROM telemetry_frames
		 WHERE match_id = ? ORDER BY frame_index, player_id`,
		matchID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var frames []model.PlayerTelemetryFrame
	for rows.Next() {
		var fJSON string
		if err := rows.Scan(&fJSON); err != nil {
			return nil, err
		}
		var f model.PlayerTelemetryFrame
		if err := json.Unmarshal([]byte(fJSON), &f); err != nil {
			continue
		}
		frames = append(frames, f)
	}
	return frames, rows.Err()
}

// GetPlayerFrames loads telemetry frames for a specific player across matches.
func (s *Store) GetPlayerFrames(ctx context.Context, playerID string, matchLimit int) (map[string][]model.PlayerTelemetryFrame, error) {
	// First get distinct match IDs for this player, most recent first
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT match_id FROM telemetry_frames
		 WHERE player_id = ? ORDER BY ingested_at DESC LIMIT ?`,
		playerID, matchLimit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matchIDs []string
	for rows.Next() {
		var mid string
		if err := rows.Scan(&mid); err != nil {
			return nil, err
		}
		matchIDs = append(matchIDs, mid)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make(map[string][]model.PlayerTelemetryFrame, len(matchIDs))
	for _, mid := range matchIDs {
		frows, err := s.db.QueryContext(ctx,
			`SELECT frame_json FROM telemetry_frames
			 WHERE match_id = ? AND player_id = ? ORDER BY frame_index`,
			mid, playerID,
		)
		if err != nil {
			return nil, err
		}
		var frames []model.PlayerTelemetryFrame
		for frows.Next() {
			var fJSON string
			if err := frows.Scan(&fJSON); err != nil {
				frows.Close()
				return nil, err
			}
			var f model.PlayerTelemetryFrame
			if err := json.Unmarshal([]byte(fJSON), &f); err != nil {
				continue
			}
			frames = append(frames, f)
		}
		frows.Close()
		if err := frows.Err(); err != nil {
			return nil, err
		}
		result[mid] = frames
	}
	return result, nil
}

// GetMatchIDsByTimeRange returns match IDs with telemetry ingested in the given time range.
func (s *Store) GetMatchIDsByTimeRange(ctx context.Context, since, until time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT match_id FROM telemetry_frames
		 WHERE ingested_at >= ? AND ingested_at <= ?
		 ORDER BY ingested_at`,
		since.Format(time.RFC3339), until.Format(time.RFC3339),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetStoredMatchCount returns how many matches have stored telemetry.
func (s *Store) GetStoredMatchCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT match_id) FROM match_contexts`,
	).Scan(&count)
	return count, err
}

// PruneOldTelemetry removes telemetry frames older than the given duration.
//
// WARNING: Telemetry frames are IMMUTABLE SOURCE DATA — the profiler truth.
// Pruning telemetry permanently destroys the ability to reprocess those matches.
// This method exists for explicit manual maintenance only (e.g., disk space emergency).
// It MUST NOT be called automatically or on a timer. The default retention policy
// is indefinite: "all telemetry ever collected."
func (s *Store) PruneOldTelemetry(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan).Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM telemetry_frames WHERE ingested_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
