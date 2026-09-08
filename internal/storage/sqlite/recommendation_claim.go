package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// StoreEnforcementActionOnce atomically verifies referenced score-eligible
// evidence and claims a recommendation ID. This is only a durable moderator
// recommendation record, not an execution outbox or permission to ban anyone.
// A process crash after commit can lose a callback; retries never duplicate it.
func (s *Store) StoreEnforcementActionOnce(ctx context.Context, action model.EnforcementAction) (bool, error) {
	if action.ActionID == "" || action.PlayerID == "" || len(action.EvidenceIDs) == 0 {
		return false, fmt.Errorf("recommendation requires identity and persisted evidence")
	}
	unique := func(values []string) []string {
		set := make(map[string]bool)
		for _, value := range values {
			if value != "" {
				set[value] = true
			}
		}
		out := make([]string, 0, len(set))
		for value := range set {
			out = append(out, value)
		}
		sort.Strings(out)
		return out
	}
	action.EvidenceIDs, action.MatchIDs = unique(action.EvidenceIDs), unique(action.MatchIDs)
	if len(action.EvidenceIDs) == 0 || len(action.MatchIDs) == 0 {
		return false, fmt.Errorf("recommendation requires evidence and match scope")
	}
	evidenceJSON, _ := json.Marshal(action.EvidenceIDs)
	matchJSON, _ := json.Marshal(action.MatchIDs)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, id := range action.EvidenceIDs {
		var player, match string
		if err := tx.QueryRowContext(ctx, `SELECT de.player_id,de.match_id FROM detection_events de WHERE de.event_id=? AND `+eligibleScoringEventSQL, id).Scan(&player, &match); err != nil {
			return false, fmt.Errorf("recommendation evidence unavailable: %w", err)
		}
		found := false
		for _, mid := range action.MatchIDs {
			found = found || mid == match
		}
		if player != action.PlayerID || !found {
			return false, fmt.Errorf("recommendation evidence scope mismatch")
		}
	}
	var duration *float64
	if action.Duration > 0 {
		value := action.Duration.Seconds()
		duration = &value
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO enforcement_actions
		(action_id,player_id,action_type,reason,duration_seconds,issued_by,issued_at,evidence_ids,match_ids,score_at_time,notes,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(action_id) DO NOTHING`, action.ActionID, action.PlayerID, action.ActionType, action.Reason, duration, action.IssuedBy, fmtDBTime(action.IssuedAt), string(evidenceJSON), string(matchJSON), action.ScoreAtTime, action.Notes, fmtDBTime(time.Now()))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		var player, kind, evidence, matches string
		if err := tx.QueryRowContext(ctx, `SELECT player_id,action_type,evidence_ids,match_ids FROM enforcement_actions WHERE action_id=?`, action.ActionID).Scan(&player, &kind, &evidence, &matches); err != nil {
			return false, err
		}
		if player != action.PlayerID || kind != action.ActionType || evidence != string(evidenceJSON) || matches != string(matchJSON) {
			return false, fmt.Errorf("recommendation identity collision")
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}
