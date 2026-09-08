package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// LiveAnalysisPersistenceFailure is a fixed, non-sensitive diagnostic. The
// underlying database error is logged locally, never copied to public status.
const LiveAnalysisPersistenceFailure = "Live derived analysis could not be persisted completely; scores and recommendations are withheld until successful offline re-analysis."

const (
	LivePersistenceFailureReason     = "derived_analysis_persistence_failed"
	LiveSourceIdentityConflictReason = "source_identity_conflict"
	LiveAnalysisIdentityConflict     = "Live source reused an accepted observation identity with conflicting telemetry; scores and recommendations are withheld until successful offline re-analysis."
)

func validateLiveDerivedBatch(in MatchAnalysisWrite) error {
	for _, event := range in.Events {
		if err := event.Validate(); err != nil {
			return fmt.Errorf("invalid live event: %w", err)
		}
		if event.MatchID != in.MatchID || event.CausalKey.PlayerID != event.PlayerID {
			return fmt.Errorf("live event identity does not match its analysis")
		}
		if _, err := model.MarshalEvidence(event.Evidence); err != nil {
			return fmt.Errorf("live evidence is not serializable: %w", err)
		}
	}
	for _, score := range in.Scores {
		if score.PlayerID == "" || math.IsNaN(score.TotalScore) || math.IsInf(score.TotalScore, 0) || score.TotalScore < 0 || score.TotalScore > 100 {
			return fmt.Errorf("invalid live score")
		}
		if len(score.MatchIDs) != 1 || !score.MatchIDs[in.MatchID] {
			return fmt.Errorf("live score does not belong to its analysis match")
		}
	}
	return nil
}

// MarkLiveAnalysisIncomplete preserves existing diagnostics but removes any
// claim that the stored partial analysis is complete. A total database outage
// can also prevent this best-effort marker; the in-memory latch still applies.
func (s *Store) MarkLiveAnalysisIncomplete(ctx context.Context, matchID string, playerIDs []string) error {
	return s.MarkLiveAnalysisIncompleteForReason(ctx, matchID, playerIDs, LivePersistenceFailureReason)
}

func (s *Store) MarkLiveAnalysisIncompleteForReason(ctx context.Context, matchID string, playerIDs []string, reason string) error {
	limitation := LiveAnalysisPersistenceFailure
	if reason == LiveSourceIdentityConflictReason {
		limitation = LiveAnalysisIdentityConflict
	} else if reason != LivePersistenceFailureReason {
		return fmt.Errorf("unknown analysis suspension reason")
	}
	coverage, err := s.GetMatchAnalysisCoverage(ctx, matchID)
	if err != nil {
		return err
	}
	if coverage == nil {
		coverage = make(map[string]*model.PlayerCoverage)
	}
	for _, id := range playerIDs {
		if coverage[id] == nil {
			coverage[id] = &model.PlayerCoverage{Version: 1}
		}
	}
	for _, player := range coverage {
		if player == nil {
			continue
		}
		player.Status, player.QualityGated = model.ReviewStatusInsufficientData, true
		if player.DataHealth == nil {
			player.DataHealth = &model.DataHealth{Version: 1}
		}
		player.DataHealth.State = model.HealthBlind
		foundReason := false
		for _, existing := range player.DataHealth.Reasons {
			foundReason = foundReason || existing == reason
		}
		if !foundReason {
			player.DataHealth.Reasons = append(player.DataHealth.Reasons, reason)
		}
		foundLimitation := false
		for _, existing := range player.Limitations {
			foundLimitation = foundLimitation || existing == limitation
		}
		if !foundLimitation {
			player.Limitations = append(player.Limitations, limitation)
		}
		for i := range player.Detectors {
			if player.Detectors[i].Enabled {
				player.Detectors[i].Status = model.ReviewStatusInsufficientData
			}
		}
	}
	doc, err := json.Marshal(coverage)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO match_analysis_coverage (match_id, coverage_json) VALUES (?, ?)
		ON CONFLICT(match_id) DO UPDATE SET coverage_json=excluded.coverage_json`, matchID, string(doc))
	return err
}

func hasLivePersistenceFailure(player *model.PlayerCoverage) bool {
	for _, reason := range player.Limitations {
		if reason == LiveAnalysisPersistenceFailure || reason == LiveAnalysisIdentityConflict {
			return true
		}
	}
	return false
}

// LiveAnalysisIncomplete survives a live-manager restart when the database
// remained available for the marker. A complete offline analysis replaces it.
func (s *Store) LiveAnalysisIncomplete(ctx context.Context, matchID string) (bool, error) {
	reason, err := s.LiveAnalysisSuspensionReason(ctx, matchID)
	return reason != "" || err != nil, err
}

func (s *Store) LiveAnalysisSuspensionReason(ctx context.Context, matchID string) (string, error) {
	coverage, err := s.GetMatchAnalysisCoverage(ctx, matchID)
	if err != nil {
		return "", err
	}
	found := ""
	for _, player := range coverage {
		if player == nil {
			continue
		}
		for _, limitation := range player.Limitations {
			if limitation == LiveAnalysisPersistenceFailure {
				return LivePersistenceFailureReason, nil
			}
			if limitation == LiveAnalysisIdentityConflict {
				found = LiveSourceIdentityConflictReason
			}
		}
	}
	return found, nil
}
