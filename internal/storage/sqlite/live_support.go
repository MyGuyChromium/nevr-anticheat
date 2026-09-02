package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// GetMaxFrameIndex returns the highest frame_index stored for a match, or -1
// when the match has no telemetry rows. The live ingest server uses it to seed
// its per-match monotonic frame counter when a match is first seen, so a
// producer that restarts (and restarts its indices at 0) is re-based instead
// of colliding with rows that already exist.
func (s *Store) GetMaxFrameIndex(ctx context.Context, matchID string) (int, error) {
	var maxIdx sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(frame_index) FROM telemetry_frames WHERE match_id = ?`, matchID,
	).Scan(&maxIdx)
	if err != nil {
		return -1, fmt.Errorf("max frame index for %s: %w", matchID, err)
	}
	if !maxIdx.Valid {
		return -1, nil
	}
	return int(maxIdx.Int64), nil
}

// GetMatchFrameCount returns the number of telemetry rows stored for a match.
func (s *Store) GetMatchFrameCount(ctx context.Context, matchID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM telemetry_frames WHERE match_id = ?`, matchID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("frame count for %s: %w", matchID, err)
	}
	return n, nil
}
