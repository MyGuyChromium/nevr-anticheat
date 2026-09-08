package ingest

import (
	"context"
	"sort"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/pipeline"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// LiveAnalysisStatus distinguishes healthy silence from a failed derived
// write. Failure details stay in local logs; the public reason is fixed.
type LiveAnalysisStatus struct {
	AnalysisIncomplete       bool   `json:"analysis_incomplete"`
	PersistenceError         string `json:"persistence_error,omitempty"`
	AnalysisSuspensionReason string `json:"analysis_suspension_reason,omitempty"`
}

// IncompleteAnalysisCount exposes only an aggregate operational health count.
func (mm *MatchManager) IncompleteAnalysisCount() int {
	mm.matchesMu.RLock()
	matches := make([]*LiveMatch, 0, len(mm.matches))
	for _, match := range mm.matches {
		matches = append(matches, match)
	}
	mm.matchesMu.RUnlock()
	count := 0
	for _, match := range matches {
		match.mu.Lock()
		if match.AnalysisIncomplete {
			count++
		}
		match.mu.Unlock()
	}
	return count
}

func (mm *MatchManager) GetMatchAnalysisStatus(matchID string) (LiveAnalysisStatus, bool) {
	mm.matchesMu.RLock()
	match, ok := mm.matches[matchID]
	mm.matchesMu.RUnlock()
	if !ok {
		return LiveAnalysisStatus{}, false
	}
	match.mu.Lock()
	defer match.mu.Unlock()
	return LiveAnalysisStatus{AnalysisIncomplete: match.AnalysisIncomplete, PersistenceError: match.PersistenceError, AnalysisSuspensionReason: match.AnalysisSuspensionReason}, true
}

func (mm *MatchManager) markAnalysisIncomplete(ctx context.Context, match *LiveMatch, err error) {
	mm.suspendAnalysis(ctx, match, sqlite.LivePersistenceFailureReason, err)
}

// suspendAnalysis accepts only fixed internal reason codes, never raw source
// text. Caller holds match.mu; raw capture continues while analysis is latched.
func (mm *MatchManager) suspendAnalysis(ctx context.Context, match *LiveMatch, reason string, err error) {
	match.AnalysisIncomplete = true
	match.AnalysisSuspensionReason = reason
	if reason == sqlite.LivePersistenceFailureReason {
		match.PersistenceError = reason
	}
	mm.logger.Error("live analysis incomplete; further scores and recommendations withheld", "match", match.MatchCtx.MatchID, "error", err)
	if mm.metrics != nil && reason == sqlite.LivePersistenceFailureReason {
		mm.metrics.StoreErrors.Inc()
	}
	if markerErr := mm.store.MarkLiveAnalysisIncompleteForReason(ctx, match.MatchCtx.MatchID, match.MatchCtx.PlayerIDs, reason); markerErr != nil {
		mm.logger.Error("could not persist incomplete-analysis marker", "match", match.MatchCtx.MatchID, "error", markerErr)
	}
}

// persistDerived commits evidence and tier/end score snapshots together. The
// scorer itself has already advanced, so a failed transaction latches this
// segment closed to recommendations; later batches cannot publish its score.
func (mm *MatchManager) persistDerived(ctx context.Context, match *LiveMatch, result *pipeline.MatchResult, final bool) bool {
	if match.AnalysisIncomplete {
		return false
	}
	ids := make([]string, 0, len(result.PlayerScores))
	for id := range result.PlayerScores {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var snapshots []model.SuspicionScore
	for _, id := range ids {
		score := result.PlayerScores[id]
		if score.TotalScore <= 0 || score.EventCount == 0 {
			continue
		}
		level, seen := match.lastLevel[id]
		if seen && level == score.Level() && (!final || score.TotalScore == match.lastScore[id]) {
			continue
		}
		if score.SnapshotTime.IsZero() {
			score.SnapshotTime = time.Now()
		}
		snapshots = append(snapshots, score)
	}
	if len(result.DetectionEvents) != 0 || len(snapshots) != 0 {
		_, err := mm.store.WriteMatchAnalysis(ctx, sqlite.MatchAnalysisWrite{
			MatchID: match.MatchCtx.MatchID, Source: AnalysisSourceInitial,
			Events: result.DetectionEvents, Scores: snapshots, LiveAppend: true,
		})
		if err != nil {
			mm.markAnalysisIncomplete(ctx, match, err)
			return false
		}
	}
	match.segmentEvents = append(match.segmentEvents, result.DetectionEvents...)
	for _, score := range snapshots {
		match.lastLevel[score.PlayerID] = score.Level()
		match.lastScore[score.PlayerID] = score.TotalScore
		if mm.metrics != nil {
			mm.metrics.ScoreSnapshotsStored.Inc()
		}
	}
	return true
}
