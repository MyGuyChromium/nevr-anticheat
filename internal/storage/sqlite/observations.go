package sqlite

import (
	"context"
	"fmt"
	"time"
)

// DetectorObservationStats summarizes stored detector output without treating
// it as validated truth. Rows are split by detector version: mixing output
// across algorithm versions would make calibration distributions misleading.
type DetectorObservationStats struct {
	DetectorID      string    `json:"detector_id"`
	DetectorVersion string    `json:"detector_version"`
	Events          int       `json:"events"`
	ShadowEvents    int       `json:"shadow_events"`
	ScoredEvents    int       `json:"scored_events"`
	Matches         int       `json:"matches"`
	Players         int       `json:"players"`
	MaxEventsMatch  int       `json:"max_events_per_match"`
	MeanSeverity    float64   `json:"mean_severity"`
	P95Severity     float64   `json:"p95_severity"`
	MaxSeverity     float64   `json:"max_severity"`
	MeanConfidence  float64   `json:"mean_confidence"`
	P05Confidence   float64   `json:"p05_confidence"`
	MinConfidence   float64   `json:"min_confidence"`
	FirstSeen       time.Time `json:"first_seen"`
	LastSeen        time.Time `json:"last_seen"`
}

// ComputeObservationStats returns version-separated event-rate and evidence-
// quality summaries. If since is non-zero, match start time is the preferred
// filter and event storage time is the fallback for legacy rows. P95/P05 use
// nearest-rank percentiles, which remain deterministic for small samples.
func (s *Store) ComputeObservationStats(ctx context.Context, since time.Time) ([]DetectorObservationStats, error) {
	cutoff := ""
	if !since.IsZero() {
		cutoff = fmtDBTime(since)
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH filtered AS (
			SELECT e.detector_id, e.detector_version, e.match_id, e.player_id,
			       e.severity, e.confidence, e.is_shadow, e.created_at
			FROM detection_events e
			LEFT JOIN match_contexts m ON m.match_id = e.match_id
			WHERE ? = '' OR COALESCE(NULLIF(m.match_start_time, ''), e.created_at) >= ?
		), ranked AS (
			SELECT *,
			       COUNT(*) OVER (PARTITION BY detector_id, detector_version) AS sample_count,
			       ROW_NUMBER() OVER (PARTITION BY detector_id, detector_version ORDER BY severity, match_id, player_id) AS severity_rank,
			       ROW_NUMBER() OVER (PARTITION BY detector_id, detector_version ORDER BY confidence, match_id, player_id) AS confidence_rank
			FROM filtered
		), per_match AS (
			SELECT detector_id, detector_version, match_id, COUNT(*) AS event_count
			FROM filtered GROUP BY detector_id, detector_version, match_id
		), maxima AS (
			SELECT detector_id, detector_version, MAX(event_count) AS max_events_match
			FROM per_match GROUP BY detector_id, detector_version
		)
		SELECT r.detector_id, r.detector_version,
		       COUNT(*), COALESCE(SUM(r.is_shadow), 0), COUNT(*) - COALESCE(SUM(r.is_shadow), 0),
		       COUNT(DISTINCT r.match_id), COUNT(DISTINCT r.player_id), COALESCE(m.max_events_match, 0),
		       AVG(r.severity),
		       MAX(CASE WHEN r.severity_rank = ((95 * r.sample_count + 99) / 100) THEN r.severity END),
		       MAX(r.severity), AVG(r.confidence),
		       MAX(CASE WHEN r.confidence_rank = ((5 * r.sample_count + 99) / 100) THEN r.confidence END),
		       MIN(r.confidence), MIN(r.created_at), MAX(r.created_at)
		FROM ranked r
		JOIN maxima m ON m.detector_id = r.detector_id AND m.detector_version = r.detector_version
		GROUP BY r.detector_id, r.detector_version, m.max_events_match
		ORDER BY r.detector_id, r.detector_version`, cutoff, cutoff)
	if err != nil {
		return nil, fmt.Errorf("computing detector observation stats: %w", err)
	}
	defer rows.Close()
	var out []DetectorObservationStats
	for rows.Next() {
		var stat DetectorObservationStats
		var first, last string
		if err := rows.Scan(
			&stat.DetectorID, &stat.DetectorVersion,
			&stat.Events, &stat.ShadowEvents, &stat.ScoredEvents,
			&stat.Matches, &stat.Players, &stat.MaxEventsMatch,
			&stat.MeanSeverity, &stat.P95Severity, &stat.MaxSeverity,
			&stat.MeanConfidence, &stat.P05Confidence, &stat.MinConfidence,
			&first, &last,
		); err != nil {
			return nil, fmt.Errorf("scanning detector observation stats: %w", err)
		}
		stat.FirstSeen = parseDBTimeLenient(first)
		stat.LastSeen = parseDBTimeLenient(last)
		out = append(out, stat)
	}
	return out, rows.Err()
}
