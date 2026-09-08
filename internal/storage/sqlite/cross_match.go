package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/scoring"
)

// GetDistinctPlayersWithEvents returns all distinct player IDs that have
// non-shadow detection events stored at or after the given time (zero = all).
func (s *Store) GetDistinctPlayersWithEvents(ctx context.Context, since time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT de.player_id FROM detection_events de
		 WHERE de.created_at >= ? AND `+eligibleScoringEventSQL+`
		 ORDER BY player_id`,
		fmtDBTimeSince(since),
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

// GetAllPlayerEvents returns independently score-eligible events for a player,
// ordered by match then frame. StoredAt carries created_at. This is the
// cross-match aggregator's input excludes zero-weight/meta/paused observations and
// evidence invalidated by the latest human review. Raw event readers do not.
func (s *Store) GetAllPlayerEvents(ctx context.Context, playerID string) ([]model.DetectionEvent, error) {
	return s.queryEvents(ctx,
		`SELECT `+eventColumns+` FROM detection_events de
		 WHERE de.player_id = ? AND `+eligibleScoringEventSQL+`
		 ORDER BY match_id, frame_index, detector_id`, playerID)
}

// CrossMatchConfig carries the scoring parameters cross-match aggregation
// shares with the in-match SuspicionScorer so the two scores are commensurable.
type CrossMatchConfig struct {
	// DecayHalfLifeHours: contribution halves every this many hours since the
	// event's anchor time. <= 0 disables decay.
	DecayHalfLifeHours float64
	// MaxSingleContribution caps one event's points (scorer: max_single_contribution).
	MaxSingleContribution float64
	// MaxContribPerDetectorPerMatch caps how many events per detector per match
	// score (scorer: max_contrib_per_detector_per_match). <= 0 means unlimited.
	MaxContribPerDetectorPerMatch int
	SameCategoryDiminishing       float64
	CooldownFrames                int
	CorrelationBonusCap           float64
	// ScoringByMatch optionally supplies the recorded historical scoring
	// configuration. Missing entries are explicitly current-config re-evaluation,
	// not reconstruction of the original persisted score.
	ScoringByMatch map[string]scoring.ScorerConfig
	// Now is the reference time for decay; zero means time.Now().
	Now time.Time
	// Levels is the tier table the summary's Level is classified with. Pass
	// the scorer's table (scoring.ScorerConfig.EffectiveLevels()) so a
	// cross-match score means the same thing as a match score; a zero table
	// behaves like model.DefaultLevelTable().
	Levels model.LevelTable
}

// PlayerCrossMatchSummary holds aggregated analysis across all matches for a player.
type PlayerCrossMatchSummary struct {
	PlayerID          string             `json:"player_id"`
	TotalEvents       int                `json:"total_events"`
	ScoredEvents      int                `json:"scored_events"` // events that passed the per-detector-per-match cap
	DistinctMatches   int                `json:"distinct_matches"`
	DistinctDetectors int                `json:"distinct_detectors"`
	CumulativeScore   float64            `json:"cumulative_score"` // capped per-event points, no decay, no 100 cap
	DecayedScore      float64            `json:"decayed_score"`    // decayed points, capped at 100: comparable to a match score
	ByDetector        map[string]int     `json:"by_detector"`      // event counts
	ByMatch           map[string]int     `json:"by_match"`         // event counts
	PointsByDetector  map[string]float64 `json:"points_by_detector"`
	PointsByCategory  map[string]float64 `json:"points_by_category"`
	MatchIDs          []string           `json:"match_ids"` // sorted
	AvgSeverity       float64            `json:"avg_severity"`
	AvgConfidence     float64            `json:"avg_confidence"`
	Level             model.ScoringLevel `json:"level"`
	// AnchorFallbacks counts events whose match had no recorded start time, so
	// decay was anchored on the event's storage time instead.
	AnchorFallbacks int    `json:"anchor_fallbacks"`
	ScoringBasis    string `json:"scoring_basis"`
}

// eventAnchorTime returns the wall-clock time a detection event is decayed
// from: the match start plus the event's match-relative timestamp when the
// match start is known; otherwise the storage time (the only wall clock left).
func eventAnchorTime(ev model.DetectionEvent, matchStarts map[string]time.Time) (time.Time, bool) {
	if start, ok := matchStarts[ev.MatchID]; ok && !start.IsZero() {
		return start.Add(time.Duration(ev.Timestamp * float64(time.Second))), true
	}
	return ev.StoredAt, false
}

// ComputePlayerCrossMatchSummary aggregates a player's detection events into a
// cross-match summary with the same dampeners as the in-match scorer:
//
//   - per event:   points = min(sev * conf * weight * 100, MaxSingleContribution)
//   - per match:   at most MaxContribPerDetectorPerMatch events per detector
//     count, chosen in frame order (deterministic)
//   - decay:       points * 0.5^(elapsed / half-life), elapsed measured from the
//     match start time (+ event offset), falling back to storage time only
//     when the match has no recorded start; never reset by reprocessing
//   - total:       capped at 100 so Level() and thresholds mean the same thing
//     as for a single-match score
//
// Store callers filter human-invalidated events first. This pure function
// independently rejects shadow, zero-weight, malformed, meta and paused
// playspacing observations, including when historical scoring config is supplied.
func ComputePlayerCrossMatchSummary(events []model.DetectionEvent, matchStarts map[string]time.Time, cfg CrossMatchConfig) PlayerCrossMatchSummary {
	s := PlayerCrossMatchSummary{
		ByDetector:       make(map[string]int),
		ByMatch:          make(map[string]int),
		PointsByDetector: make(map[string]float64),
		PointsByCategory: make(map[string]float64),
		MatchIDs:         []string{},
		Level:            model.LevelClean,
		ScoringBasis:     "current_config_reevaluation",
	}
	if len(events) == 0 {
		return s
	}
	now := cfg.Now
	if now.IsZero() {
		now = time.Now()
	}

	// Deterministic processing order: match, frame, detector, event id.
	sorted := make([]model.DetectionEvent, len(events))
	copy(sorted, events)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.MatchID != b.MatchID {
			return a.MatchID < b.MatchID
		}
		if a.FrameIndex != b.FrameIndex {
			return a.FrameIndex < b.FrameIndex
		}
		if a.DetectorID != b.DetectorID {
			return a.DetectorID < b.DetectorID
		}
		return a.EventID < b.EventID
	})

	s.PlayerID = sorted[0].PlayerID
	scorers := make(map[string]*scoring.SuspicionScorer)
	seenIDs := make(map[string]bool)
	providedConfig, currentConfig := false, false

	var totalSev, totalConf, decayedSum float64
	for _, ev := range sorted {
		if ev.PlayerID != s.PlayerID || ev.IsShadow || scoring.IsMetaDetector(ev.DetectorID) || model.IsPlayspaceDetector(ev.DetectorID) ||
			!positiveFiniteUnit(ev.Severity) || !positiveFiniteUnit(ev.Confidence) || !positiveFiniteUnit(ev.EnforcementWeight) {
			continue
		}
		if ev.EventID != "" {
			if seenIDs[ev.EventID] {
				continue
			}
			seenIDs[ev.EventID] = true
		}
		s.TotalEvents++
		s.ByDetector[ev.DetectorID]++
		s.ByMatch[ev.MatchID]++
		totalSev += ev.Severity
		totalConf += ev.Confidence

		scorer := scorers[ev.MatchID]
		if scorer == nil {
			scoringConfig, historical := cfg.ScoringByMatch[ev.MatchID]
			if historical {
				providedConfig = true
			} else {
				currentConfig = true
				scoringConfig = scoring.ScorerConfig{
					MaxSingleContribution: cfg.MaxSingleContribution, MaxContribPerDetectorPerMatch: cfg.MaxContribPerDetectorPerMatch,
					SameCategoryDiminishing: cfg.SameCategoryDiminishing, CooldownFrames: cfg.CooldownFrames,
					CorrelationBonusCap: cfg.CorrelationBonusCap, Levels: cfg.Levels,
				}
			}
			scorer = scoring.NewSuspicionScorer(scoringConfig)
			scorer.SetClock(func() time.Time { return now })
			scorers[ev.MatchID] = scorer
		}
		before := scorer.GetScore(ev.PlayerID)
		_, accepted := scorer.IngestEventWithResult(ev)
		if !accepted {
			continue
		}
		scorer.ApplyCorrelationBonus()
		after := scorer.GetScore(ev.PlayerID)
		s.ScoredEvents++
		// Decay only the contribution accepted by the same scorer used live.
		// The incremental category bonus is anchored to the event that first
		// makes it available, never re-awarded for each batch or re-analysis.
		points := after.BaseScore - before.BaseScore + after.CorrelationBonus - before.CorrelationBonus
		s.CumulativeScore += points

		anchor, anchored := eventAnchorTime(ev, matchStarts)
		if !anchored {
			s.AnchorFallbacks++
		}
		decayed := points
		if !anchor.IsZero() && cfg.DecayHalfLifeHours > 0 {
			decayed = scoring.ExponentialDecay(points, now.Sub(anchor).Hours(), cfg.DecayHalfLifeHours)
		}
		decayedSum += decayed
		s.PointsByDetector[ev.DetectorID] += decayed
		s.PointsByCategory[detectorCategory(ev.DetectorID)] += decayed
	}

	s.DistinctMatches = len(s.ByMatch)
	s.DistinctDetectors = len(s.ByDetector)
	if s.TotalEvents > 0 {
		s.AvgSeverity = totalSev / float64(s.TotalEvents)
		s.AvgConfidence = totalConf / float64(s.TotalEvents)
	}
	s.DecayedScore = math.Min(decayedSum, 100)

	for mid := range s.ByMatch {
		s.MatchIDs = append(s.MatchIDs, mid)
	}
	sort.Strings(s.MatchIDs)

	s.Level = cfg.Levels.LevelFor(s.DecayedScore)
	if providedConfig && !currentConfig {
		s.ScoringBasis = "provided_match_configuration"
	} else if providedConfig {
		s.ScoringBasis = "mixed_config_reevaluation"
	}
	return s
}

func positiveFiniteUnit(value float64) bool {
	return value > 0 && value <= 1 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// detectorCategory mirrors scoring.detectorCategory (unexported there).
func detectorCategory(detectorID string) string {
	prefix, _, _ := strings.Cut(detectorID, "_")
	switch strings.ToUpper(prefix) {
	case "THROW":
		return "throw"
	case "BIO":
		return "bio"
	case "MOV":
		return "movement"
	case "STATE":
		return "state"
	case "PAT":
		return "pattern"
	default:
		return "unknown"
	}
}

// ToSuspicionScore renders the summary as a SuspicionScore snapshot (points,
// not counts, in the per-detector/per-category maps).
func (s PlayerCrossMatchSummary) ToSuspicionScore(at time.Time) model.SuspicionScore {
	return model.SuspicionScore{
		PlayerID:        s.PlayerID,
		TotalScore:      s.DecayedScore,
		ScoreByDetector: s.PointsByDetector,
		ScoreByCategory: s.PointsByCategory,
		EventCount:      s.ScoredEvents,
		MatchCount:      s.DistinctMatches,
		SnapshotTime:    at,
	}
}

// StoreCrossMatchScore stores a cross-match score snapshot with scope
// 'cross_match'. It never appears in GetPlayerScore/GetPlayerHistory, which
// read per-match snapshots only.
func (s *Store) StoreCrossMatchScore(ctx context.Context, summary PlayerCrossMatchSummary) error {
	return s.storeScore(ctx, ScoreScopeCrossMatch, "", summary.ToSuspicionScore(time.Now()))
}

// CrossMatchReviewCase represents a review case aggregating multiple matches.
type CrossMatchReviewCase struct {
	CaseID          string         `json:"case_id"`
	PlayerID        string         `json:"player_id"`
	MatchIDs        []string       `json:"match_ids"`
	MatchCount      int            `json:"match_count"`
	Severity        string         `json:"severity"`
	CumulativeScore float64        `json:"cumulative_score"`
	DecayedScore    float64        `json:"decayed_score"`
	Detectors       map[string]int `json:"detectors"`
	Explanation     string         `json:"explanation"`
	Status          string         `json:"status"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at,omitempty"`
}

// StoreCrossMatchReviewCase inserts or refreshes the aggregate case for a
// player. On conflict the scores, matches, detectors and explanation are
// updated, but a status a moderator has set (anything other than 'pending')
// is preserved and created_at keeps its original value. Superseded negative-
// review cases keep their entire audit snapshot. A relevant current negative
// review also closes a first insert: without an exact evidence-snapshot
// fingerprint we cannot establish that a concurrent aggregate was recomputed.
func (s *Store) StoreCrossMatchReviewCase(ctx context.Context, rc CrossMatchReviewCase) error {
	matchIDsJSON, err := json.Marshal(rc.MatchIDs)
	if err != nil {
		return fmt.Errorf("marshal match ids: %w", err)
	}
	detectorsJSON, err := json.Marshal(rc.Detectors)
	if err != nil {
		return fmt.Errorf("marshal detectors: %w", err)
	}
	status := rc.Status
	if status == "" {
		status = CaseStatusPending
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if status == CaseStatusPending {
		negative, err := currentNegativeReviewTx(ctx, tx, rc.PlayerID, string(matchIDsJSON))
		if err != nil {
			return err
		}
		if negative {
			status = CaseStatusClosed
			rc.Explanation = crossMatchReviewRevoked + " " + rc.Explanation
		}
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO cross_match_review_cases
		 (case_id, player_id, match_ids, match_count, severity, cumulative_score, decayed_score,
		  detectors_json, explanation, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(case_id) DO UPDATE SET
			player_id = excluded.player_id,
			match_ids = excluded.match_ids,
			match_count = excluded.match_count,
			severity = excluded.severity,
			cumulative_score = excluded.cumulative_score,
			decayed_score = excluded.decayed_score,
			detectors_json = excluded.detectors_json,
			explanation = excluded.explanation,
			status = CASE WHEN cross_match_review_cases.status = 'pending'
			              THEN excluded.status ELSE cross_match_review_cases.status END,
			updated_at = excluded.updated_at
		 WHERE NOT (cross_match_review_cases.status = 'closed'
		   AND instr(COALESCE(cross_match_review_cases.explanation,''), ?) > 0)`,
		rc.CaseID, rc.PlayerID, string(matchIDsJSON), rc.MatchCount,
		rc.Severity, rc.CumulativeScore, rc.DecayedScore,
		string(detectorsJSON), rc.Explanation, status,
		fmtDBTime(rc.CreatedAt), fmtDBTime(time.Now()), crossMatchReviewRevoked,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

const crossMatchCaseColumns = `case_id, player_id, match_ids, match_count, severity,
	cumulative_score, decayed_score, COALESCE(detectors_json,''), COALESCE(explanation,''), status,
	created_at, COALESCE(updated_at,'')`

func scanCrossMatchCase(r rowScanner) (CrossMatchReviewCase, error) {
	var rc CrossMatchReviewCase
	var matchIDsJSON, detectorsJSON, createdStr, updatedStr string
	if err := r.Scan(&rc.CaseID, &rc.PlayerID, &matchIDsJSON, &rc.MatchCount,
		&rc.Severity, &rc.CumulativeScore, &rc.DecayedScore,
		&detectorsJSON, &rc.Explanation, &rc.Status, &createdStr, &updatedStr); err != nil {
		return rc, err
	}
	_ = json.Unmarshal([]byte(matchIDsJSON), &rc.MatchIDs)
	if detectorsJSON != "" {
		_ = json.Unmarshal([]byte(detectorsJSON), &rc.Detectors)
	}
	rc.CreatedAt = parseDBTimeLenient(createdStr)
	if updatedStr != "" {
		rc.UpdatedAt = parseDBTimeLenient(updatedStr)
	}
	return rc, nil
}

// GetPendingCrossMatchReviewCases returns pending cross-match review cases.
func (s *Store) GetPendingCrossMatchReviewCases(ctx context.Context, limit int) ([]CrossMatchReviewCase, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+crossMatchCaseColumns+` FROM cross_match_review_cases
		 WHERE status = 'pending'
		 ORDER BY decayed_score DESC, case_id LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cases []CrossMatchReviewCase
	for rows.Next() {
		rc, err := scanCrossMatchCase(rows)
		if err != nil {
			return nil, err
		}
		cases = append(cases, rc)
	}
	return cases, rows.Err()
}

// GetCrossMatchReviewCase retrieves a cross-match review case by ID.
func (s *Store) GetCrossMatchReviewCase(ctx context.Context, caseID string) (CrossMatchReviewCase, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+crossMatchCaseColumns+` FROM cross_match_review_cases WHERE case_id = ?`, caseID)
	return scanCrossMatchCase(row)
}

// SeverityForScore maps a (0-100) score onto the moderator-facing severity
// label through the given tier table (model.CaseSeverityForLevel):
// critical (>= critical tier), high (>= high_risk), medium (>= suspicious),
// low. A zero table behaves like model.DefaultLevelTable().
func SeverityForScore(score float64, table model.LevelTable) string {
	return model.CaseSeverityForLevel(table.LevelFor(math.Min(score, 100)))
}

// BuildCrossMatchReviewCase creates an aggregate review case from a cross-match
// summary when the (capped, decayed) score reaches the table's high_risk
// boundary, i.e. the configured review threshold once the scorer's table
// (scoring.ScorerConfig.EffectiveLevels()) is passed. Severity comes from the
// same table, so a cross-match case and a single-match case at the same
// score carry the same label.
func BuildCrossMatchReviewCase(summary PlayerCrossMatchSummary, table model.LevelTable) *CrossMatchReviewCase {
	if table.IsZero() {
		table = model.DefaultLevelTable()
	}
	if summary.DecayedScore < table.HighRisk {
		return nil
	}
	now := time.Now()
	// Case ID is stable per-player: re-running aggregation refreshes the existing
	// case rather than creating a new one per run (see StoreCrossMatchReviewCase).
	return &CrossMatchReviewCase{
		CaseID:          fmt.Sprintf("XM-%s", summary.PlayerID),
		PlayerID:        summary.PlayerID,
		MatchIDs:        summary.MatchIDs,
		MatchCount:      summary.DistinctMatches,
		Severity:        SeverityForScore(summary.DecayedScore, table),
		CumulativeScore: summary.CumulativeScore,
		DecayedScore:    summary.DecayedScore,
		Detectors:       summary.ByDetector,
		Explanation: fmt.Sprintf(
			"Player %s flagged across %d matches (%d events, %d scored, %d detectors). Decayed score: %.1f/100 (raw capped points: %.1f).",
			summary.PlayerID, summary.DistinctMatches, summary.TotalEvents, summary.ScoredEvents,
			summary.DistinctDetectors, summary.DecayedScore, summary.CumulativeScore,
		),
		Status:    CaseStatusPending,
		CreatedAt: now,
	}
}
