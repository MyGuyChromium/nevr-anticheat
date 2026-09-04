package sqlite

import (
	"context"
	"fmt"
	"sort"
)

// StorageStats describes the evidence-bearing telemetry payload inside SQLite.
// Byte counts are logical payload bytes, not allocated database pages.
type StorageStats struct {
	RawTicks         int   `json:"raw_ticks"`
	RawTickBytes     int64 `json:"raw_tick_bytes"`
	NormalizedFrames int   `json:"normalized_frames"`
	NormalizedBytes  int64 `json:"normalized_bytes"`
}

// GetStorageStats returns global logical telemetry storage use.
func (s *Store) GetStorageStats(ctx context.Context) (StorageStats, error) {
	return s.queryStorageStats(ctx, "", false)
}

// GetMatchStorageStats returns logical telemetry storage use for one match.
func (s *Store) GetMatchStorageStats(ctx context.Context, matchID string) (StorageStats, error) {
	return s.queryStorageStats(ctx, matchID, true)
}

func (s *Store) queryStorageStats(ctx context.Context, matchID string, scoped bool) (StorageStats, error) {
	var out StorageStats
	where, args := "", []any{}
	if scoped {
		where, args = " WHERE match_id = ?", []any{matchID}
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(LENGTH(raw_json)),0) FROM match_ticks`+where, args...).Scan(&out.RawTicks, &out.RawTickBytes); err != nil {
		return out, fmt.Errorf("measuring raw ticks: %w", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(LENGTH(frame_json)),0) FROM telemetry_frames`+where, args...).Scan(&out.NormalizedFrames, &out.NormalizedBytes); err != nil {
		return out, fmt.Errorf("measuring normalized frames: %w", err)
	}
	return out, nil
}

// DeleteMatchRawTicks removes only the original raw payloads for one exact
// match. Callers must first create and verify a restorable archive. Normalized
// frames, labels, events, summaries and cases remain available.
func (s *Store) DeleteMatchRawTicks(ctx context.Context, matchID string) (int64, error) {
	if matchID == "" {
		return 0, fmt.Errorf("empty match id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM match_ticks WHERE match_id = ?`, matchID)
	if err != nil {
		return 0, fmt.Errorf("deleting raw ticks for %s: %w", matchID, err)
	}
	// Clear the pre-v9 fallback copy too. Current writes never populate it.
	if _, err := tx.ExecContext(ctx, `UPDATE telemetry_frames SET raw_json = NULL WHERE match_id = ?`, matchID); err != nil {
		return 0, fmt.Errorf("clearing legacy raw ticks for %s: %w", matchID, err)
	}
	removed, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}

// RestoreMatchRawTicks restores archived raw payloads without overwriting any
// tick already present. It returns inserted and already-present counts.
func (s *Store) RestoreMatchRawTicks(ctx context.Context, matchID string, ticks map[int]string) (inserted, present int, err error) {
	if matchID == "" {
		return 0, 0, fmt.Errorf("empty match id")
	}
	if len(ticks) == 0 {
		return 0, 0, fmt.Errorf("archive contains no raw ticks")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO match_ticks
		(match_id, frame_index, raw_json, ingested_at) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()
	indices := make([]int, 0, len(ticks))
	for idx := range ticks {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	stamp := fmtDBTime(nowUTC())
	for _, idx := range indices {
		if idx < 0 || ticks[idx] == "" {
			return 0, 0, fmt.Errorf("invalid archived raw tick %d", idx)
		}
		res, execErr := stmt.ExecContext(ctx, matchID, idx, ticks[idx], stamp)
		if execErr != nil {
			return 0, 0, execErr
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			inserted++
		} else {
			present++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return inserted, present, nil
}
