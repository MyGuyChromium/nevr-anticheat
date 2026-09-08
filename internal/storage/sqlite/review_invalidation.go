package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const crossMatchReviewRevoked = "Recommendation superseded after negative human review; historical totals are not an actionable recommendation."

// This intentionally over-abstains even when an aggregate may have omitted
// rejected evidence. Reconsideration/new scope must be explicit: the cache
// stores a match window, not the exact immutable inputs of the computation.
func currentNegativeReviewTx(ctx context.Context, tx *sql.Tx, playerID, matchIDsJSON string) (bool, error) {
	var found bool
	err := tx.QueryRowContext(ctx, `WITH scope AS (SELECT value AS match_id FROM json_each(?)),
	 reviewed_cases AS (
	   SELECT case_id FROM review_cases WHERE player_id = ? AND match_id IN (SELECT match_id FROM scope)
	   UNION ALL
	   SELECT case_id FROM cross_match_review_cases WHERE player_id = ?
	     AND EXISTS (SELECT 1 FROM json_each(match_ids) matches JOIN scope ON matches.value = scope.match_id)
	 )
	 SELECT EXISTS (
	   SELECT 1 FROM event_reviews er WHERE er.player_id = ?
	     AND er.match_id IN (SELECT match_id FROM scope) AND er.verdict = 'no'
	     AND er.event_id = (SELECT newer.event_id FROM event_reviews newer
	       WHERE newer.event_id = er.event_id OR
	         (newer.match_id = er.match_id AND newer.player_id = er.player_id
	          AND newer.detector_id = er.detector_id AND newer.detector_version = er.detector_version
	          AND newer.frame_index = er.frame_index AND newer.timestamp = er.timestamp
	          AND newer.observed_value = er.observed_value AND newer.expected_range = er.expected_range
	          AND newer.evidence_type = er.evidence_type AND newer.evidence_json = er.evidence_json)
	       ORDER BY newer.reviewed_at DESC, newer.event_id DESC LIMIT 1)
	   UNION ALL
	   SELECT 1 FROM moderator_decisions md JOIN reviewed_cases rc ON rc.case_id = md.case_id
	     WHERE md.decision_id = (SELECT newer.decision_id FROM moderator_decisions newer
	       WHERE newer.case_id = md.case_id ORDER BY newer.decided_at DESC, newer.decision_id DESC LIMIT 1)
	     AND (md.verdict = 'false_positive' OR EXISTS (
	       SELECT 1 FROM json_each(COALESCE(NULLIF(md.detector_feedback,''),'[]')) feedback
	       WHERE json_extract(feedback.value,'$.correct') = 'no'))
	 )`, matchIDsJSON, playerID, playerID, playerID).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("check aggregate review invalidation: %w", err)
	}
	return found, nil
}

func negativeModeratorDecision(d model.ModeratorDecision) bool {
	if d.Verdict == VerdictFalsePositive {
		return true
	}
	for _, feedback := range d.DetectorFeedback {
		if feedback.Correct == "no" {
			return true
		}
	}
	return false
}

// Close only untouched recommendations overlapping the rejected evidence's
// scope. Keep their original totals and all evidence/score/decision rows for
// audit; no longer expose the cached recommendation in the pending queue.
func invalidatePendingCrossMatchTx(ctx context.Context, tx *sql.Tx, playerID string, matchIDs []string, at time.Time) error {
	encoded, err := json.Marshal(matchIDs)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE cross_match_review_cases
	 SET status = 'closed', updated_at = ?,
	 explanation = ? || CASE WHEN COALESCE(explanation,'') = '' THEN '' ELSE ' ' || explanation END
	 WHERE status = 'pending' AND player_id = ?
	 AND EXISTS (SELECT 1 FROM json_each(cross_match_review_cases.match_ids) prior
	 JOIN json_each(?) affected ON prior.value = affected.value)`,
		fmtDBTime(at), crossMatchReviewRevoked, playerID, string(encoded))
	if err != nil {
		return fmt.Errorf("supersede cross-match recommendation: %w", err)
	}
	return nil
}

func invalidateCaseCrossMatchRecommendationsTx(ctx context.Context, tx *sql.Tx, caseID string, at time.Time) error {
	var playerID, matchID string
	err := tx.QueryRowContext(ctx, `SELECT player_id, match_id FROM review_cases WHERE case_id = ?`, caseID).Scan(&playerID, &matchID)
	if err == nil {
		return invalidatePendingCrossMatchTx(ctx, tx, playerID, []string{matchID}, at)
	}
	if err != sql.ErrNoRows {
		return err
	}
	var encoded string
	if err := tx.QueryRowContext(ctx, `SELECT player_id, match_ids FROM cross_match_review_cases WHERE case_id = ?`, caseID).Scan(&playerID, &encoded); err != nil {
		return err
	}
	var matchIDs []string
	if err := json.Unmarshal([]byte(encoded), &matchIDs); err != nil {
		return fmt.Errorf("decode reviewed match scope: %w", err)
	}
	return invalidatePendingCrossMatchTx(ctx, tx, playerID, matchIDs, at)
}
