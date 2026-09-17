package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Telemetry health sources (match_telemetry_health.source).
const (
	// TelemetryHealthSourceAnalysis rows were written by the analysis that
	// parsed the recording, from the parser's own diagnostic report.
	TelemetryHealthSourceAnalysis = "analysis"
	// TelemetryHealthSourceBackfill rows were rebuilt once from the stored raw
	// ticks of a match analyzed before the table existed.
	TelemetryHealthSourceBackfill = "backfill"
)

// RawTickScans returns how many whole-match raw-tick scans
// (GetAllMatchRawTicks, ForEachMatchTick) this store has started. Viewing a
// stored match must not need one; tests assert that through this counter. It
// is a process-local diagnostic, not persisted state.
func (s *Store) RawTickScans() int64 { return s.rawTickScans.Load() }

// StoreMatchTelemetryHealth upserts the telemetry-health document of a match
// (schema owned by internal/replay). The analysis that parsed the recording is
// the authority, so it replaces whatever was there.
func (s *Store) StoreMatchTelemetryHealth(ctx context.Context, matchID, source string, doc []byte) error {
	if err := validateTelemetryHealth(matchID, source, doc); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO match_telemetry_health (match_id, source, health_json, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(match_id) DO UPDATE SET source = excluded.source,
			health_json = excluded.health_json, created_at = excluded.created_at`,
		matchID, source, string(doc), fmtDBTime(nowUTC()))
	return err
}

// StoreMatchTelemetryHealthIfAbsent writes a backfilled document only when the
// match has none, so a slow backfill can never overwrite the document of an
// analysis that finished meanwhile. It reports whether the row was written.
func (s *Store) StoreMatchTelemetryHealthIfAbsent(ctx context.Context, matchID, source string, doc []byte) (bool, error) {
	if err := validateTelemetryHealth(matchID, source, doc); err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO match_telemetry_health (match_id, source, health_json, created_at) VALUES (?, ?, ?, ?)`,
		matchID, source, string(doc), fmtDBTime(nowUTC()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func validateTelemetryHealth(matchID, source string, doc []byte) error {
	if matchID == "" {
		return fmt.Errorf("telemetry health: match id is required")
	}
	if source != TelemetryHealthSourceAnalysis && source != TelemetryHealthSourceBackfill {
		return fmt.Errorf("match %s: unknown telemetry health source %q", matchID, source)
	}
	if len(doc) == 0 || !json.Valid(doc) {
		return fmt.Errorf("match %s: telemetry health document is not valid JSON", matchID)
	}
	return nil
}

// GetMatchTelemetryHealth returns the stored telemetry-health document of a
// match and where it came from, or ErrNotFound when the match has none yet.
func (s *Store) GetMatchTelemetryHealth(ctx context.Context, matchID string) (doc []byte, source string, err error) {
	var raw string
	err = s.db.QueryRowContext(ctx,
		`SELECT health_json, source FROM match_telemetry_health WHERE match_id = ?`, matchID).Scan(&raw, &source)
	if err == sql.ErrNoRows {
		return nil, "", fmt.Errorf("telemetry health of match %s: %w", matchID, ErrNotFound)
	}
	if err != nil {
		return nil, "", err
	}
	return []byte(raw), source, nil
}

// DeleteMatchTelemetryHealth removes a match's telemetry-health document so
// the next view rebuilds it (used after raw ticks are restored from an
// archive, and by tests).
func (s *Store) DeleteMatchTelemetryHealth(ctx context.Context, matchID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM match_telemetry_health WHERE match_id = ?`, matchID)
	return err
}
