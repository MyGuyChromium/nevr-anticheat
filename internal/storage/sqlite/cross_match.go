package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
)

// GetDistinctPlayersWithEvents returns all distinct player IDs that have
// detection events since the given time.
func (s *Store) GetDistinctPlayersWithEvents(ctx context.Context, since time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT player_id FROM detection_events
		 WHERE created_at >= ? AND is_shadow = 0
		 ORDER BY player_id`,
		since.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("querying players: %w", err)
	}
	defer rows.Close()

	var players []string
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			return nil, err
		}
		players = append(players, pid)
	}
	return players, rows.Err()
}

// GetAllPlayerEvents returns all non-shadow detection events for a player.
func (s *Store) GetAllPlayerEvents(ctx context.Context, playerID string) ([]model.DetectionEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id, detector_id, detector_version, match_id, player_id,
			frame_index, frame_range_start, frame_range_end, timestamp,
			severity, confidence, observed_value, expected_range,
			enforcement_weight, auto_enforce, is_shadow, created_at
		FROM detection_events
		WHERE player_id = ? AND is_shadow = 0
		ORDER BY created_at`,
		playerID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []model.DetectionEvent
	for rows.Next() {
		var ev model.DetectionEvent
		var autoEnforce, shadow int
		var createdAt string
		err := rows.Scan(
			&ev.EventID, &ev.DetectorID, &ev.DetectorVersion, &ev.MatchID, &ev.PlayerID,
			&ev.FrameIndex, &ev.FrameRangeStart, &ev.FrameRangeEnd, &ev.Timestamp,
			&ev.Severity, &ev.Confidence, &ev.ObservedValue, &ev.ExpectedRange,
			&ev.EnforcementWeight, &autoEnforce, &shadow, &createdAt,
		)
		if err != nil {
			return nil, err
		}
		ev.AutoEnforce = autoEnforce == 1
		ev.IsShadow = shadow == 1
		// Parse created_at into the dedicated StoredAt field for decay calculations.
		ev.StoredAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		if ev.StoredAt.IsZero() {
			ev.StoredAt, _ = time.Parse(time.RFC3339, createdAt)
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// PlayerCrossMatchSummary holds aggregated analysis across all matches for a player.
type PlayerCrossMatchSummary struct {
	PlayerID          string             `json:"player_id"`
	TotalEvents       int                `json:"total_events"`
	DistinctMatches   int                `json:"distinct_matches"`
	DistinctDetectors int                `json:"distinct_detectors"`
	CumulativeScore   float64            `json:"cumulative_score"`
	DecayedScore      float64            `json:"decayed_score"`
	ByDetector        map[string]int     `json:"by_detector"`
	ByMatch           map[string]int     `json:"by_match"`
	MatchIDs          []string           `json:"match_ids"`
	AvgSeverity       float64            `json:"avg_severity"`
	AvgConfidence     float64            `json:"avg_confidence"`
	Level             model.ScoringLevel `json:"level"`
}

// ComputePlayerCrossMatchSummary aggregates a player's detection events into
// a cross-match summary with time-decayed scoring. Events from long ago contribute
// less than recent events, preventing permanent suspicion from old matches.
func ComputePlayerCrossMatchSummary(events []model.DetectionEvent, decayHalfLifeHours float64) PlayerCrossMatchSummary {
	s := PlayerCrossMatchSummary{
		ByDetector: make(map[string]int),
		ByMatch:    make(map[string]int),
	}
	if len(events) == 0 {
		return s
	}

	s.PlayerID = events[0].PlayerID
	s.TotalEvents = len(events)
	now := time.Now()

	var totalSev, totalConf float64
	for _, ev := range events {
		s.ByDetector[ev.DetectorID]++
		s.ByMatch[ev.MatchID]++
		totalSev += ev.Severity
		totalConf += ev.Confidence

		rawContribution := ev.Severity * ev.Confidence * ev.EnforcementWeight * 100.0
		s.CumulativeScore += rawContribution

		// Apply time-based decay: contributions from older events are reduced.
		// Uses the StoredAt field populated by GetAllPlayerEvents.
		if !ev.StoredAt.IsZero() && decayHalfLifeHours > 0 {
			elapsedHours := now.Sub(ev.StoredAt).Hours()
			s.DecayedScore += scoring.ExponentialDecay(rawContribution, elapsedHours, decayHalfLifeHours)
		} else {
			s.DecayedScore += rawContribution
		}
	}

	s.DistinctMatches = len(s.ByMatch)
	s.DistinctDetectors = len(s.ByDetector)
	s.AvgSeverity = totalSev / float64(len(events))
	s.AvgConfidence = totalConf / float64(len(events))

	// Collect match IDs
	s.MatchIDs = make([]string, 0, len(s.ByMatch))
	for mid := range s.ByMatch {
		s.MatchIDs = append(s.MatchIDs, mid)
	}

	// Level is based on decayed score (capped at 100)
	capped := s.DecayedScore
	if capped > 100 {
		capped = 100
	}
	sc := model.SuspicionScore{TotalScore: capped}
	s.Level = sc.Level()

	return s
}

// StoreCrossMatchScore stores a cumulative cross-match score snapshot.
func (s *Store) StoreCrossMatchScore(ctx context.Context, summary PlayerCrossMatchSummary) error {
	detJSON, _ := json.Marshal(summary.ByDetector)
	matchJSON, _ := json.Marshal(summary.ByMatch)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO suspicion_scores (player_id, total_score, score_by_detector, score_by_category, event_count, match_count)
		VALUES (?, ?, ?, ?, ?, ?)`,
		summary.PlayerID, summary.DecayedScore, string(detJSON), string(matchJSON),
		summary.TotalEvents, summary.DistinctMatches,
	)
	return err
}

// CrossMatchReviewCase represents a review case aggregating multiple matches.
type CrossMatchReviewCase struct {
	CaseID          string   `json:"case_id"`
	PlayerID        string   `json:"player_id"`
	MatchIDs        []string `json:"match_ids"`
	MatchCount      int      `json:"match_count"`
	Severity        string   `json:"severity"`
	CumulativeScore float64  `json:"cumulative_score"`
	DecayedScore    float64  `json:"decayed_score"`
	Detectors       map[string]int `json:"detectors"`
	Explanation     string   `json:"explanation"`
	Status          string   `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

// StoreCrossMatchReviewCase persists an aggregate review case spanning multiple matches.
func (s *Store) StoreCrossMatchReviewCase(ctx context.Context, rc CrossMatchReviewCase) error {
	matchIDsJSON, _ := json.Marshal(rc.MatchIDs)
	detectorsJSON, _ := json.Marshal(rc.Detectors)
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO cross_match_review_cases
		 (case_id, player_id, match_ids, match_count, severity, cumulative_score, decayed_score,
		  detectors_json, explanation, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rc.CaseID, rc.PlayerID, string(matchIDsJSON), rc.MatchCount,
		rc.Severity, rc.CumulativeScore, rc.DecayedScore,
		string(detectorsJSON), rc.Explanation, rc.Status,
		rc.CreatedAt.Format(time.RFC3339),
	)
	return err
}

// GetPendingCrossMatchReviewCases returns pending cross-match review cases.
func (s *Store) GetPendingCrossMatchReviewCases(ctx context.Context, limit int) ([]CrossMatchReviewCase, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT case_id, player_id, match_ids, match_count, severity,
		        cumulative_score, decayed_score, detectors_json, explanation, status, created_at
		 FROM cross_match_review_cases
		 WHERE status = 'pending'
		 ORDER BY decayed_score DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cases []CrossMatchReviewCase
	for rows.Next() {
		var rc CrossMatchReviewCase
		var matchIDsJSON, detectorsJSON, createdStr string
		if err := rows.Scan(&rc.CaseID, &rc.PlayerID, &matchIDsJSON, &rc.MatchCount,
			&rc.Severity, &rc.CumulativeScore, &rc.DecayedScore,
			&detectorsJSON, &rc.Explanation, &rc.Status, &createdStr); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(matchIDsJSON), &rc.MatchIDs)
		json.Unmarshal([]byte(detectorsJSON), &rc.Detectors)
		rc.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		cases = append(cases, rc)
	}
	return cases, rows.Err()
}

// GetCrossMatchReviewCase retrieves a cross-match review case by ID.
func (s *Store) GetCrossMatchReviewCase(ctx context.Context, caseID string) (CrossMatchReviewCase, error) {
	var rc CrossMatchReviewCase
	var matchIDsJSON, detectorsJSON, createdStr string
	err := s.db.QueryRowContext(ctx,
		`SELECT case_id, player_id, match_ids, match_count, severity,
		        cumulative_score, decayed_score, detectors_json, explanation, status, created_at
		 FROM cross_match_review_cases WHERE case_id = ?`, caseID,
	).Scan(&rc.CaseID, &rc.PlayerID, &matchIDsJSON, &rc.MatchCount,
		&rc.Severity, &rc.CumulativeScore, &rc.DecayedScore,
		&detectorsJSON, &rc.Explanation, &rc.Status, &createdStr)
	if err != nil {
		return rc, err
	}
	json.Unmarshal([]byte(matchIDsJSON), &rc.MatchIDs)
	json.Unmarshal([]byte(detectorsJSON), &rc.Detectors)
	rc.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	return rc, nil
}

// BuildCrossMatchReviewCase creates an aggregate review case from a cross-match summary.
// Only creates a case if the decayed score exceeds the review threshold.
func BuildCrossMatchReviewCase(summary PlayerCrossMatchSummary, reviewThreshold float64) *CrossMatchReviewCase {
	if summary.DecayedScore < reviewThreshold {
		return nil
	}

	severity := "low"
	capped := math.Min(summary.DecayedScore, 100)
	switch {
	case capped >= 80:
		severity = "critical"
	case capped >= 60:
		severity = "high"
	case capped >= 40:
		severity = "medium"
	}

	now := time.Now()
	// Case ID is stable per-player: re-running aggregation replaces the existing case
	// rather than creating a new one per day. INSERT OR REPLACE on case_id handles this.
	return &CrossMatchReviewCase{
		CaseID:          fmt.Sprintf("XM-%s", summary.PlayerID),
		PlayerID:        summary.PlayerID,
		MatchIDs:        summary.MatchIDs,
		MatchCount:      summary.DistinctMatches,
		Severity:        severity,
		CumulativeScore: summary.CumulativeScore,
		DecayedScore:    summary.DecayedScore,
		Detectors:       summary.ByDetector,
		Explanation: fmt.Sprintf(
			"Player %s flagged across %d matches (%d events, %d detectors). Decayed score: %.1f (raw: %.1f).",
			summary.PlayerID, summary.DistinctMatches, summary.TotalEvents,
			summary.DistinctDetectors, summary.DecayedScore, summary.CumulativeScore,
		),
		Status:    "pending",
		CreatedAt: now,
	}
}

