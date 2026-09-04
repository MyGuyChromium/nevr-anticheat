package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	GroundTruthPositive  = "positive"
	GroundTruthNegative  = "negative"
	GroundTruthUncertain = "uncertain"

	OpportunityThrow           = "throw"
	OpportunityMovementWindow  = "movement_window"
	OpportunityStateTransition = "state_transition"
	OpportunityMatch           = "match"
	OpportunityPlayerHistory   = "player_history"
	OpportunityCustom          = "custom"

	PromotionCandidate  = "candidate"
	PromotionActive     = "active"
	PromotionRolledBack = "rolled_back"
)

// CalibrationOpportunity is a human-verified detector opportunity. Unlike an
// EventReview it may describe a behavior for which the detector emitted
// nothing, allowing calibration to measure false negatives and true
// negatives. It is evidence metadata only and never affects a player score.
type CalibrationOpportunity struct {
	OpportunityID  string    `json:"opportunity_id"`
	MatchID        string    `json:"match_id"`
	PlayerID       string    `json:"player_id"`
	DetectorID     string    `json:"detector_id"`
	BehaviorType   string    `json:"behavior_type"`
	Kind           string    `json:"opportunity_kind"`
	FrameStart     int       `json:"frame_start"`
	FrameEnd       int       `json:"frame_end"`
	TimestampStart float64   `json:"timestamp_start"`
	TimestampEnd   float64   `json:"timestamp_end"`
	GroundTruth    string    `json:"ground_truth"`
	Comment        string    `json:"comment"`
	ReviewerID     string    `json:"reviewer_id"`
	BlindReview    bool      `json:"blind_review"`
	ReviewedAt     time.Time `json:"reviewed_at"`
}

func validOpportunityKind(kind string) bool {
	switch kind {
	case OpportunityThrow, OpportunityMovementWindow, OpportunityStateTransition,
		OpportunityMatch, OpportunityPlayerHistory, OpportunityCustom:
		return true
	default:
		return false
	}
}

func validGroundTruth(truth string) bool {
	switch truth {
	case GroundTruthPositive, GroundTruthNegative, GroundTruthUncertain:
		return true
	default:
		return false
	}
}

func normalizeOpportunity(in CalibrationOpportunity) (CalibrationOpportunity, error) {
	in.OpportunityID = strings.TrimSpace(in.OpportunityID)
	in.MatchID = strings.TrimSpace(in.MatchID)
	in.PlayerID = strings.TrimSpace(in.PlayerID)
	in.DetectorID = strings.ToUpper(strings.TrimSpace(in.DetectorID))
	in.BehaviorType = strings.TrimSpace(in.BehaviorType)
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	in.GroundTruth = strings.ToLower(strings.TrimSpace(in.GroundTruth))
	in.Comment = strings.TrimSpace(in.Comment)
	in.ReviewerID = strings.TrimSpace(in.ReviewerID)
	if in.OpportunityID == "" {
		in.OpportunityID = uuid.NewString()
	}
	if in.MatchID == "" || in.PlayerID == "" || in.DetectorID == "" {
		return in, errors.New("match_id, player_id, and detector_id are required")
	}
	if len(in.DetectorID) > 40 || len(in.BehaviorType) > 120 {
		return in, errors.New("detector_id or behavior_type is too long")
	}
	if !validOpportunityKind(in.Kind) {
		return in, fmt.Errorf("invalid opportunity_kind %q", in.Kind)
	}
	if !validGroundTruth(in.GroundTruth) {
		return in, fmt.Errorf("invalid ground_truth %q", in.GroundTruth)
	}
	if in.FrameStart < 0 || in.FrameEnd < in.FrameStart {
		return in, errors.New("frame range must be non-negative and ordered")
	}
	if in.TimestampStart < 0 || in.TimestampEnd < in.TimestampStart {
		return in, errors.New("timestamp range must be non-negative and ordered")
	}
	if len(in.Comment) > 4000 {
		return in, errors.New("comment exceeds 4000 characters")
	}
	if in.ReviewerID == "" {
		in.ReviewerID = "local-owner"
	}
	if in.ReviewedAt.IsZero() {
		in.ReviewedAt = nowUTC()
	} else {
		in.ReviewedAt = in.ReviewedAt.UTC().Truncate(time.Second)
	}
	return in, nil
}

// StoreCalibrationOpportunity creates or replaces a ground-truth window.
func (s *Store) StoreCalibrationOpportunity(ctx context.Context, in CalibrationOpportunity) (CalibrationOpportunity, error) {
	in, err := normalizeOpportunity(in)
	if err != nil {
		return in, err
	}
	if ok, err := s.HasMatch(ctx, in.MatchID); err != nil {
		return in, fmt.Errorf("checking match: %w", err)
	} else if !ok {
		return in, fmt.Errorf("match %s: %w", in.MatchID, ErrNotFound)
	}
	var playerExists int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM telemetry_frames WHERE match_id = ? AND player_id = ?`,
		in.MatchID, in.PlayerID).Scan(&playerExists); err != nil {
		return in, fmt.Errorf("checking player: %w", err)
	}
	if playerExists == 0 {
		return in, fmt.Errorf("player %s is not stored in match %s", in.PlayerID, in.MatchID)
	}
	return s.upsertCalibrationOpportunity(ctx, in)
}

// ImportCalibrationOpportunity restores portable evidence before or after its
// replay is imported. Like imported match labels, it becomes measurable once
// the referenced match telemetry exists.
func (s *Store) ImportCalibrationOpportunity(ctx context.Context, in CalibrationOpportunity) error {
	in, err := normalizeOpportunity(in)
	if err != nil {
		return err
	}
	_, err = s.upsertCalibrationOpportunity(ctx, in)
	return err
}

func (s *Store) upsertCalibrationOpportunity(ctx context.Context, in CalibrationOpportunity) (CalibrationOpportunity, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return in, fmt.Errorf("starting calibration opportunity transaction: %w", err)
	}
	defer tx.Rollback()
	var overlapping int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM calibration_opportunities
		WHERE match_id = ? AND player_id = ? AND detector_id = ? AND opportunity_id <> ?
		AND frame_end >= ? AND frame_start <= ?`, in.MatchID, in.PlayerID, in.DetectorID,
		in.OpportunityID, in.FrameStart, in.FrameEnd).Scan(&overlapping); err != nil {
		return in, fmt.Errorf("checking opportunity overlap: %w", err)
	}
	if overlapping > 0 {
		return in, errors.New("this detector already has an overlapping ground-truth window for the player")
	}
	blind := 0
	if in.BlindReview {
		blind = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO calibration_opportunities
		(opportunity_id, match_id, player_id, detector_id, behavior_type, opportunity_kind,
		 frame_start, frame_end, timestamp_start, timestamp_end, ground_truth, comment,
		 reviewer_id, blind_review, reviewed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(opportunity_id) DO UPDATE SET
		 match_id=excluded.match_id, player_id=excluded.player_id,
		 detector_id=excluded.detector_id, behavior_type=excluded.behavior_type,
		 opportunity_kind=excluded.opportunity_kind, frame_start=excluded.frame_start,
		 frame_end=excluded.frame_end, timestamp_start=excluded.timestamp_start,
		 timestamp_end=excluded.timestamp_end, ground_truth=excluded.ground_truth,
		 comment=excluded.comment, reviewer_id=excluded.reviewer_id,
		 blind_review=excluded.blind_review, reviewed_at=excluded.reviewed_at`,
		in.OpportunityID, in.MatchID, in.PlayerID, in.DetectorID, in.BehaviorType, in.Kind,
		in.FrameStart, in.FrameEnd, in.TimestampStart, in.TimestampEnd, in.GroundTruth,
		in.Comment, in.ReviewerID, blind, fmtDBTime(in.ReviewedAt))
	if err != nil {
		return in, fmt.Errorf("storing calibration opportunity: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return in, fmt.Errorf("committing calibration opportunity: %w", err)
	}
	return in, nil
}

const calibrationOpportunityColumns = `opportunity_id, match_id, player_id, detector_id,
	behavior_type, opportunity_kind, frame_start, frame_end, timestamp_start, timestamp_end,
	ground_truth, comment, reviewer_id, blind_review, reviewed_at`

func scanCalibrationOpportunity(row rowScanner) (CalibrationOpportunity, error) {
	var out CalibrationOpportunity
	var blind int
	var reviewed string
	err := row.Scan(&out.OpportunityID, &out.MatchID, &out.PlayerID, &out.DetectorID,
		&out.BehaviorType, &out.Kind, &out.FrameStart, &out.FrameEnd,
		&out.TimestampStart, &out.TimestampEnd, &out.GroundTruth, &out.Comment,
		&out.ReviewerID, &blind, &reviewed)
	out.BlindReview = blind != 0
	out.ReviewedAt = parseDBTimeLenient(reviewed)
	return out, err
}

// ListCalibrationOpportunities returns durable ground-truth windows. Empty
// matchID and detectorID filters mean all rows.
func (s *Store) ListCalibrationOpportunities(ctx context.Context, matchID, detectorID string) ([]CalibrationOpportunity, error) {
	query := `SELECT ` + calibrationOpportunityColumns + ` FROM calibration_opportunities WHERE 1=1`
	var args []any
	if matchID = strings.TrimSpace(matchID); matchID != "" {
		query += ` AND match_id = ?`
		args = append(args, matchID)
	}
	if detectorID = strings.ToUpper(strings.TrimSpace(detectorID)); detectorID != "" {
		query += ` AND detector_id = ?`
		args = append(args, detectorID)
	}
	query += ` ORDER BY reviewed_at DESC, opportunity_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalibrationOpportunity
	for rows.Next() {
		item, err := scanCalibrationOpportunity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCalibrationOpportunity(ctx context.Context, opportunityID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM calibration_opportunities WHERE opportunity_id = ?`, strings.TrimSpace(opportunityID))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DetectorPromotion is a fail-closed approval for exactly one detector in a
// named profile. The desktop never honors review mode without an active row.
type DetectorPromotion struct {
	DetectorID        string          `json:"detector_id"`
	ProfileName       string          `json:"profile_name"`
	Status            string          `json:"status"`
	Metrics           json.RawMessage `json:"metrics"`
	ConfigFingerprint string          `json:"config_fingerprint"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

func (s *Store) StoreDetectorPromotion(ctx context.Context, in DetectorPromotion) (DetectorPromotion, error) {
	in.DetectorID = strings.ToUpper(strings.TrimSpace(in.DetectorID))
	in.ProfileName = strings.TrimSpace(in.ProfileName)
	in.Status = strings.ToLower(strings.TrimSpace(in.Status))
	if in.DetectorID == "" || in.ProfileName == "" {
		return in, errors.New("detector_id and profile_name are required")
	}
	if in.Status != PromotionCandidate && in.Status != PromotionActive && in.Status != PromotionRolledBack {
		return in, fmt.Errorf("invalid promotion status %q", in.Status)
	}
	if len(in.Metrics) == 0 {
		in.Metrics = json.RawMessage(`{}`)
	}
	if !json.Valid(in.Metrics) {
		return in, errors.New("promotion metrics must be valid JSON")
	}
	now := nowUTC()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `INSERT INTO detector_promotions
		(detector_id, profile_name, status, metrics_json, config_fingerprint, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(detector_id) DO UPDATE SET profile_name=excluded.profile_name,
		 status=excluded.status, metrics_json=excluded.metrics_json,
		 config_fingerprint=excluded.config_fingerprint, updated_at=excluded.updated_at`,
		in.DetectorID, in.ProfileName, in.Status, string(in.Metrics), in.ConfigFingerprint,
		fmtDBTime(in.CreatedAt), fmtDBTime(in.UpdatedAt))
	if err != nil {
		return in, err
	}
	return in, nil
}

func scanDetectorPromotion(row rowScanner) (DetectorPromotion, error) {
	var out DetectorPromotion
	var metrics, created, updated string
	err := row.Scan(&out.DetectorID, &out.ProfileName, &out.Status, &metrics,
		&out.ConfigFingerprint, &created, &updated)
	out.Metrics = json.RawMessage(metrics)
	out.CreatedAt, out.UpdatedAt = parseDBTimeLenient(created), parseDBTimeLenient(updated)
	return out, err
}

func (s *Store) GetDetectorPromotion(ctx context.Context, detectorID string) (DetectorPromotion, bool, error) {
	out, err := scanDetectorPromotion(s.db.QueryRowContext(ctx,
		`SELECT detector_id, profile_name, status, metrics_json, config_fingerprint, created_at, updated_at
		 FROM detector_promotions WHERE detector_id = ?`, strings.ToUpper(strings.TrimSpace(detectorID))))
	if err == sql.ErrNoRows {
		return DetectorPromotion{}, false, nil
	}
	return out, err == nil, err
}

func (s *Store) ListDetectorPromotions(ctx context.Context) ([]DetectorPromotion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT detector_id, profile_name, status,
		metrics_json, config_fingerprint, created_at, updated_at
		FROM detector_promotions ORDER BY updated_at DESC, detector_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DetectorPromotion
	for rows.Next() {
		item, err := scanDetectorPromotion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) RollBackDetectorPromotion(ctx context.Context, detectorID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE detector_promotions SET status = ?, updated_at = ?
		WHERE detector_id = ? AND status IN (?, ?)`, PromotionRolledBack, fmtDBTime(nowUTC()),
		strings.ToUpper(strings.TrimSpace(detectorID)), PromotionCandidate, PromotionActive)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
