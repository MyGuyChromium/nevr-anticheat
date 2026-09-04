package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	MatchLabelKnownClean     = "known_clean"
	MatchLabelSuspected      = "suspected"
	MatchLabelConfirmedCheat = "confirmed_cheat"
)

// MatchLabel is human-supplied ground truth for an entire replay. It indexes
// the local calibration library; it never changes scores or enforcement.
type MatchLabel struct {
	MatchID           string    `json:"match_id"`
	Label             string    `json:"label"`
	Comment           string    `json:"comment"`
	ReviewerID        string    `json:"reviewer_id"`
	AppVersion        string    `json:"app_version"`
	ConfigFingerprint string    `json:"config_fingerprint"`
	ReviewedAt        time.Time `json:"reviewed_at"`
}

func validMatchLabel(label string) bool {
	switch label {
	case MatchLabelKnownClean, MatchLabelSuspected, MatchLabelConfirmedCheat:
		return true
	default:
		return false
	}
}

// StoreMatchLabel creates or replaces a match's calibration-library label.
func (s *Store) StoreMatchLabel(ctx context.Context, matchID, label, comment, reviewerID, appVersion, configFingerprint string) (MatchLabel, error) {
	matchID = strings.TrimSpace(matchID)
	label = strings.ToLower(strings.TrimSpace(label))
	comment = strings.TrimSpace(comment)
	reviewerID = strings.TrimSpace(reviewerID)
	if matchID == "" {
		return MatchLabel{}, fmt.Errorf("match id is required")
	}
	if !validMatchLabel(label) {
		return MatchLabel{}, fmt.Errorf("invalid match label %q (want known_clean, suspected, or confirmed_cheat)", label)
	}
	if len(comment) > 4000 {
		return MatchLabel{}, fmt.Errorf("comment exceeds 4000 characters")
	}
	if reviewerID == "" {
		reviewerID = "local-owner"
	}
	if ok, err := s.HasMatch(ctx, matchID); err != nil {
		return MatchLabel{}, fmt.Errorf("checking match %s: %w", matchID, err)
	} else if !ok {
		return MatchLabel{}, fmt.Errorf("match %s: %w", matchID, ErrNotFound)
	}

	out := MatchLabel{MatchID: matchID, Label: label, Comment: comment, ReviewerID: reviewerID,
		AppVersion: strings.TrimSpace(appVersion), ConfigFingerprint: strings.TrimSpace(configFingerprint), ReviewedAt: nowUTC()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO match_labels
		(match_id, label, comment, reviewer_id, app_version, config_fingerprint, reviewed_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(match_id) DO UPDATE SET label=excluded.label, comment=excluded.comment,
		reviewer_id=excluded.reviewer_id, app_version=excluded.app_version,
		config_fingerprint=excluded.config_fingerprint, reviewed_at=excluded.reviewed_at`,
		out.MatchID, out.Label, out.Comment, out.ReviewerID, out.AppVersion, out.ConfigFingerprint, fmtDBTime(out.ReviewedAt))
	if err != nil {
		return MatchLabel{}, fmt.Errorf("storing match label: %w", err)
	}
	return out, nil
}

const matchLabelColumns = `match_id, label, comment, reviewer_id, app_version, config_fingerprint, reviewed_at`

func scanMatchLabel(row rowScanner) (MatchLabel, error) {
	var out MatchLabel
	var reviewedAt string
	err := row.Scan(&out.MatchID, &out.Label, &out.Comment, &out.ReviewerID, &out.AppVersion, &out.ConfigFingerprint, &reviewedAt)
	out.ReviewedAt = parseDBTimeLenient(reviewedAt)
	return out, err
}

// GetMatchLabel returns a match label or (zero, false, nil) when unlabeled.
func (s *Store) GetMatchLabel(ctx context.Context, matchID string) (MatchLabel, bool, error) {
	out, err := scanMatchLabel(s.db.QueryRowContext(ctx,
		`SELECT `+matchLabelColumns+` FROM match_labels WHERE match_id = ?`, matchID))
	if err == sql.ErrNoRows {
		return MatchLabel{}, false, nil
	}
	return out, err == nil, err
}

// GetMatchLabels returns labels keyed by match ID. An empty input means all.
func (s *Store) GetMatchLabels(ctx context.Context, matchIDs []string) (map[string]MatchLabel, error) {
	query := `SELECT ` + matchLabelColumns + ` FROM match_labels`
	var args []any
	if len(matchIDs) > 0 {
		query += ` WHERE match_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(matchIDs)), ",") + `)`
		args = make([]any, len(matchIDs))
		for i, id := range matchIDs {
			args[i] = id
		}
	}
	query += ` ORDER BY reviewed_at DESC, match_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]MatchLabel)
	for rows.Next() {
		label, err := scanMatchLabel(rows)
		if err != nil {
			return nil, err
		}
		out[label.MatchID] = label
	}
	return out, rows.Err()
}

// MatchLabelCounts returns the calibration-library totals by label.
func (s *Store) MatchLabelCounts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{
		MatchLabelKnownClean: 0, MatchLabelSuspected: 0, MatchLabelConfirmedCheat: 0,
	}
	rows, err := s.db.QueryContext(ctx, `SELECT label, COUNT(*) FROM match_labels GROUP BY label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var label string
		var count int
		if err := rows.Scan(&label, &count); err != nil {
			return nil, err
		}
		out[label] = count
	}
	return out, rows.Err()
}
