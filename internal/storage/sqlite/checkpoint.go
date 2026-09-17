package sqlite

import (
	"context"
	"fmt"
)

// CheckpointTruncate folds the write-ahead log back into the database file
// and truncates it, without the integrity scan CheckAndCheckpoint runs first.
// One large import otherwise leaves a WAL of hundreds of megabytes beside the
// database until the next manual maintenance. It changes no data. A busy
// result is reported as an error so callers can log it; nothing is lost and
// the next checkpoint will catch up.
func (s *Store) CheckpointTruncate(ctx context.Context) (MaintenanceResult, error) {
	var out MaintenanceResult
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(
		&out.BusyConnections, &out.WALFrames, &out.Checkpointed,
	); err != nil {
		return out, fmt.Errorf("sqlite: checkpointing: %w", err)
	}
	if out.BusyConnections != 0 {
		return out, fmt.Errorf("sqlite: checkpoint could not finish because the database is busy")
	}
	return out, nil
}
