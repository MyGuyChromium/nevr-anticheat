package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// EventReview is a direct human label on one detector observation. Event
// metadata is copied into the row so the label remains useful if a forced
// re-analysis replaces the derived detection_events rows with new event IDs.
type EventReview struct {
	EventID         string    `json:"event_id"`
	MatchID         string    `json:"match_id"`
	PlayerID        string    `json:"player_id"`
	DetectorID      string    `json:"detector_id"`
	DetectorVersion string    `json:"detector_version"`
	FrameIndex      int       `json:"frame_index"`
	Timestamp       float64   `json:"timestamp"`
	Severity        float64   `json:"severity"`
	Confidence      float64   `json:"confidence"`
	ObservedValue   string    `json:"observed_value"`
	ExpectedRange   string    `json:"expected_range"`
	EvidenceType    string    `json:"evidence_type"`
	EvidenceJSON    string    `json:"evidence_json"`
	Verdict         string    `json:"verdict"`
	Comment         string    `json:"comment"`
	ReviewerID      string    `json:"reviewer_id"`
	BlindReview     bool      `json:"blind_review"`
	ReviewedAt      time.Time `json:"reviewed_at"`
}

func validEventVerdict(verdict string) bool {
	switch verdict {
	case "yes", "no", "uncertain":
		return true
	default:
		return false
	}
}

// StoreEventReview creates or replaces the direct label for eventID.
func (s *Store) StoreEventReview(ctx context.Context, eventID, verdict, comment, reviewerID string) (EventReview, error) {
	return s.StoreEventReviewWithBlind(ctx, eventID, verdict, comment, reviewerID, false)
}

// StoreEventReviewWithBlind records whether the reviewer made the decision
// before the detector identity and confidence were revealed.
func (s *Store) StoreEventReviewWithBlind(ctx context.Context, eventID, verdict, comment, reviewerID string, blindReview bool) (EventReview, error) {
	eventID = strings.TrimSpace(eventID)
	verdict = strings.ToLower(strings.TrimSpace(verdict))
	comment = strings.TrimSpace(comment)
	reviewerID = strings.TrimSpace(reviewerID)
	if eventID == "" {
		return EventReview{}, fmt.Errorf("event id is required")
	}
	if !validEventVerdict(verdict) {
		return EventReview{}, fmt.Errorf("invalid event verdict %q (want yes, no, or uncertain)", verdict)
	}
	if reviewerID == "" {
		reviewerID = "local-owner"
	}
	if len(comment) > 2000 {
		return EventReview{}, fmt.Errorf("comment exceeds 2000 characters")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EventReview{}, fmt.Errorf("begin event review: %w", err)
	}
	defer tx.Rollback()
	var review EventReview
	var reviewedAt string
	err = tx.QueryRowContext(ctx,
		`SELECT event_id, match_id, player_id, detector_id, detector_version, frame_index,
		 timestamp, severity, confidence, COALESCE(observed_value,''), COALESCE(expected_range,''),
		 COALESCE(evidence_type,''), COALESCE(evidence_json,'')
		 FROM detection_events WHERE event_id = ?`, eventID).Scan(
		&review.EventID, &review.MatchID, &review.PlayerID, &review.DetectorID,
		&review.DetectorVersion, &review.FrameIndex, &review.Timestamp, &review.Severity,
		&review.Confidence, &review.ObservedValue, &review.ExpectedRange,
		&review.EvidenceType, &review.EvidenceJSON)
	if err == sql.ErrNoRows {
		return EventReview{}, fmt.Errorf("event %s: %w", eventID, ErrNotFound)
	}
	if err != nil {
		return EventReview{}, fmt.Errorf("loading event %s: %w", eventID, err)
	}
	review.Verdict, review.Comment, review.ReviewerID, review.BlindReview = verdict, comment, reviewerID, blindReview
	review.ReviewedAt = nowUTC()
	reviewedAt = fmtDBTime(review.ReviewedAt)
	blind := 0
	if blindReview {
		blind = 1
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO event_reviews
		 (event_id, match_id, player_id, detector_id, detector_version, frame_index,
		  timestamp, severity, confidence, observed_value, expected_range, evidence_type,
		  evidence_json, verdict, comment, reviewer_id, reviewed_at, blind_review)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(event_id) DO UPDATE SET
		  verdict=excluded.verdict, comment=excluded.comment,
		  reviewer_id=excluded.reviewer_id, reviewed_at=excluded.reviewed_at,
		  blind_review=excluded.blind_review`,
		review.EventID, review.MatchID, review.PlayerID, review.DetectorID,
		review.DetectorVersion, review.FrameIndex, review.Timestamp, review.Severity,
		review.Confidence, review.ObservedValue, review.ExpectedRange, review.EvidenceType,
		review.EvidenceJSON, review.Verdict, review.Comment, review.ReviewerID, reviewedAt, blind)
	if err != nil {
		return EventReview{}, fmt.Errorf("storing event review: %w", err)
	}
	if verdict == "no" {
		if err := invalidatePendingCrossMatchTx(ctx, tx, review.PlayerID, []string{review.MatchID}, review.ReviewedAt); err != nil {
			return EventReview{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return EventReview{}, fmt.Errorf("commit event review: %w", err)
	}
	return review, nil
}

const eventReviewColumns = `event_id, match_id, player_id, detector_id,
	detector_version, frame_index, timestamp, severity, confidence, observed_value,
	expected_range, evidence_type, evidence_json, verdict, comment, reviewer_id, reviewed_at, blind_review`

func scanEventReview(r rowScanner) (EventReview, error) {
	var out EventReview
	var reviewedAt string
	var blind int
	err := r.Scan(&out.EventID, &out.MatchID, &out.PlayerID, &out.DetectorID,
		&out.DetectorVersion, &out.FrameIndex, &out.Timestamp, &out.Severity,
		&out.Confidence, &out.ObservedValue, &out.ExpectedRange, &out.EvidenceType,
		&out.EvidenceJSON, &out.Verdict, &out.Comment,
		&out.ReviewerID, &reviewedAt, &blind)
	out.BlindReview = blind != 0
	out.ReviewedAt = parseDBTimeLenient(reviewedAt)
	return out, err
}

// GetEventReviewsByMatch returns direct labels keyed by event ID.
func (s *Store) GetEventReviewsByMatch(ctx context.Context, matchID string) (map[string]EventReview, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+eventReviewColumns+` FROM event_reviews WHERE match_id = ?`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]EventReview)
	for rows.Next() {
		review, err := scanEventReview(rows)
		if err != nil {
			return nil, err
		}
		out[review.EventID] = review
	}
	return out, rows.Err()
}

// ListEventReviews returns direct labels at or after since (zero means all).
func (s *Store) ListEventReviews(ctx context.Context, since time.Time) ([]EventReview, error) {
	query := `SELECT ` + eventReviewColumns + ` FROM event_reviews`
	var args []any
	if !since.IsZero() {
		query += ` WHERE reviewed_at >= ?`
		args = append(args, fmtDBTime(since))
	}
	query += ` ORDER BY reviewed_at DESC, event_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventReview
	for rows.Next() {
		review, err := scanEventReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, review)
	}
	return out, rows.Err()
}

// GetEventReviewCount returns the number of directly labeled events.
func (s *Store) GetEventReviewCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_reviews`).Scan(&count)
	return count, err
}
