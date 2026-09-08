package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// MatchAnalysisWrite is one complete set of derived outputs for a match.
// When Replace is true, the previous analysis is snapshotted and replaced in
// the same transaction as every new event, score and review-case update.
type MatchAnalysisWrite struct {
	MatchID     string
	Source      string
	Replace     bool
	Events      []model.DetectionEvent
	Scores      []model.SuspicionScore
	Cases       []model.ReviewCase
	KeepPlayers []string
	CloseReason string
	Coverage    map[string]*model.PlayerCoverage
	// LiveAppend atomically appends live events and score snapshots without
	// replacing coverage, refreshing cases, or closing existing pending cases.
	LiveAppend bool
}

// MatchAnalysisWriteResult reports committed row counts.
type MatchAnalysisWriteResult struct {
	EventsStored  int
	ScoresStored  int
	CasesStored   int
	CasesClosed   int64
	ClearedEvents int64
	ClearedScores int64
}

// WriteMatchAnalysis stores or atomically replaces all derived outputs for a
// match. Any encoding, constraint, cancellation or commit failure rolls the
// entire operation back, including the pre-reprocess deletion and case edits.
func (s *Store) WriteMatchAnalysis(ctx context.Context, in MatchAnalysisWrite) (out MatchAnalysisWriteResult, retErr error) {
	defer func() {
		if retErr != nil {
			out = MatchAnalysisWriteResult{}
		}
	}()
	if strings.TrimSpace(in.MatchID) == "" {
		return out, fmt.Errorf("writing match analysis: match id required")
	}
	if in.LiveAppend {
		if in.Replace || len(in.Cases) != 0 || in.Coverage != nil {
			return out, fmt.Errorf("live append cannot replace analysis, cases, or coverage")
		}
		if err := validateLiveDerivedBatch(in); err != nil {
			return out, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("begin analysis transaction: %w", err)
	}
	defer tx.Rollback()
	// Coverage belongs to this exact analysis, so it changes atomically with
	// events and scores. Legacy callers clear it rather than retaining a stale
	// claim about a previous build's detector coverage.
	if in.LiveAppend {
		// Live diagnostic coverage is merged separately. Never erase it merely
		// because this transaction contains only events and score snapshots.
	} else if in.Coverage == nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM match_analysis_coverage WHERE match_id = ?`, in.MatchID); err != nil {
			return out, fmt.Errorf("clear analysis coverage: %w", err)
		}
	} else {
		doc, err := json.Marshal(in.Coverage)
		if err != nil {
			return out, fmt.Errorf("encode analysis coverage: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO match_analysis_coverage (match_id, coverage_json) VALUES (?, ?)
			ON CONFLICT(match_id) DO UPDATE SET coverage_json=excluded.coverage_json`, in.MatchID, string(doc)); err != nil {
			return out, fmt.Errorf("store analysis coverage: %w", err)
		}
	}

	if in.Replace {
		if err := captureAnalysisSnapshotTx(ctx, tx, in.MatchID); err != nil {
			return out, fmt.Errorf("snapshotting previous analysis: %w", err)
		}
		events, err := tx.ExecContext(ctx, `DELETE FROM detection_events WHERE match_id = ?`, in.MatchID)
		if err != nil {
			return out, fmt.Errorf("deleting previous events: %w", err)
		}
		scores, err := tx.ExecContext(ctx, `DELETE FROM suspicion_scores WHERE scope = ? AND match_id = ?`, ScoreScopeMatch, in.MatchID)
		if err != nil {
			return out, fmt.Errorf("deleting previous scores: %w", err)
		}
		out.ClearedEvents, _ = events.RowsAffected()
		out.ClearedScores, _ = scores.RowsAffected()
	}

	if len(in.Events) > 0 {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO detection_events
			(event_id, detector_id, detector_version, match_id, player_id,
			 frame_index, frame_range_start, frame_range_end, timestamp, severity, confidence,
			 observed_value, expected_range, enforcement_weight, auto_enforce, is_shadow,
			 evidence_json, evidence_type, causal_key, analysis_source, created_at, merged_count)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return out, fmt.Errorf("prepare events: %w", err)
		}
		defer stmt.Close()
		now := time.Now()
		for _, event := range in.Events {
			evidenceJSON, evidenceType := encodeEvidence(event.Evidence)
			causalJSON, err := json.Marshal(event.CausalKey)
			if err != nil {
				return out, fmt.Errorf("marshal causal key for %s: %w", event.EventID, err)
			}
			autoEnforce, shadow := 0, 0
			if event.AutoEnforce {
				autoEnforce = 1
			}
			if event.IsShadow {
				shadow = 1
			}
			createdAt := event.StoredAt
			if createdAt.IsZero() {
				createdAt = now
			}
			r, err := stmt.ExecContext(ctx,
				event.EventID, event.DetectorID, event.DetectorVersion, event.MatchID, event.PlayerID,
				event.FrameIndex, event.FrameRangeStart, event.FrameRangeEnd, event.Timestamp,
				event.Severity, event.Confidence, event.ObservedValue, event.ExpectedRange,
				event.EnforcementWeight, autoEnforce, shadow, evidenceJSON, evidenceType,
				string(causalJSON), in.Source, fmtDBTime(createdAt), event.MergedCount)
			if err != nil {
				return out, fmt.Errorf("insert event %s: %w", event.EventID, err)
			}
			n, _ := r.RowsAffected()
			out.EventsStored += int(n)
		}
	}

	if len(in.Scores) > 0 {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO suspicion_scores
			(player_id, total_score, score_by_detector, score_by_category,
			 event_count, match_count, snapshot_time, scope, match_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return out, fmt.Errorf("prepare scores: %w", err)
		}
		defer stmt.Close()
		for _, score := range in.Scores {
			detJSON, err := json.Marshal(score.ScoreByDetector)
			if err != nil {
				return out, fmt.Errorf("marshal score_by_detector for %s: %w", score.PlayerID, err)
			}
			catJSON, err := json.Marshal(score.ScoreByCategory)
			if err != nil {
				return out, fmt.Errorf("marshal score_by_category for %s: %w", score.PlayerID, err)
			}
			if _, err := stmt.ExecContext(ctx, score.PlayerID, score.TotalScore, string(detJSON), string(catJSON),
				score.EventCount, score.MatchCount, fmtDBTime(score.SnapshotTime), ScoreScopeMatch, in.MatchID); err != nil {
				return out, fmt.Errorf("insert score for %s: %w", score.PlayerID, err)
			}
			out.ScoresStored++
		}
	}

	if len(in.Cases) > 0 {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO review_cases
			(case_id, player_id, match_id, severity, suspicion_score,
			 recommended_action, explanation, status, assigned_to, detectors_json, created_at, updated_at,
			 level, threshold_version, timestamp_start, timestamp_end, close_reason)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')
			ON CONFLICT(case_id) DO UPDATE SET
			 player_id = excluded.player_id, match_id = excluded.match_id,
			 severity = excluded.severity, suspicion_score = excluded.suspicion_score,
			 recommended_action = excluded.recommended_action, explanation = excluded.explanation,
			 detectors_json = excluded.detectors_json, level = excluded.level,
			 threshold_version = excluded.threshold_version,
			 timestamp_start = excluded.timestamp_start, timestamp_end = excluded.timestamp_end,
			 status = CASE WHEN review_cases.status = 'pending' OR review_cases.close_reason != ''
				THEN excluded.status ELSE review_cases.status END,
			 close_reason = '',
			 assigned_to = COALESCE(NULLIF(review_cases.assigned_to, ''), excluded.assigned_to),
			 updated_at = excluded.updated_at`)
		if err != nil {
			return out, fmt.Errorf("prepare review cases: %w", err)
		}
		defer stmt.Close()
		now := time.Now()
		for _, rc := range in.Cases {
			detectorsJSON, err := json.Marshal(rc.DetectorsTriggered)
			if err != nil {
				return out, fmt.Errorf("marshal detectors for case %s: %w", rc.CaseID, err)
			}
			status := rc.Status
			if status == "" {
				status = CaseStatusPending
			}
			if _, err := stmt.ExecContext(ctx,
				rc.CaseID, rc.PlayerID, rc.MatchID, rc.Severity, rc.SuspicionScore,
				rc.RecommendedAction, rc.Explanation, status, rc.AssignedTo,
				string(detectorsJSON), fmtDBTime(rc.CreatedAt), fmtDBTime(now),
				rc.Level, rc.ThresholdVersion, nullableDBTime(rc.TimestampStart), nullableDBTime(rc.TimestampEnd)); err != nil {
				return out, fmt.Errorf("insert review case %s: %w", rc.CaseID, err)
			}
			out.CasesStored++
		}
	}

	if in.LiveAppend {
		if err := tx.Commit(); err != nil {
			return MatchAnalysisWriteResult{}, fmt.Errorf("commit live analysis transaction: %w", err)
		}
		return out, nil
	}

	reason := in.CloseReason
	if reason == "" {
		reason = "not flagged by re-analysis"
	}
	args := []any{reason, fmtDBTime(time.Now()), in.MatchID}
	query := `UPDATE review_cases SET status = 'closed', close_reason = ?, updated_at = ?
		WHERE match_id = ? AND status = 'pending'`
	if len(in.KeepPlayers) > 0 {
		query += ` AND player_id NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(in.KeepPlayers)), ",") + `)`
		for _, playerID := range in.KeepPlayers {
			args = append(args, playerID)
		}
	}
	closed, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return out, fmt.Errorf("close stale review cases: %w", err)
	}
	out.CasesClosed, _ = closed.RowsAffected()

	if err := tx.Commit(); err != nil {
		return MatchAnalysisWriteResult{}, fmt.Errorf("commit analysis transaction: %w", err)
	}
	return out, nil
}
