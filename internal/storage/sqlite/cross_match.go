package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
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

// Outcomes of StoreCrossMatchReviewCaseResult.
const (
	// CrossMatchCaseCreated: the player's first aggregate case was inserted.
	CrossMatchCaseCreated = "created"
	// CrossMatchCaseRefreshed: an existing case was updated in place; a status
	// a moderator set is preserved.
	CrossMatchCaseRefreshed = "refreshed"
	// CrossMatchCaseNewCase: the player's latest case is a finished audit
	// record (superseded by a negative review, or decided/closed by a moderator
	// over a narrower match scope), so the aggregate opened a successor case.
	CrossMatchCaseNewCase = "new_case"
	// CrossMatchCaseRevoked: the player had no case and the aggregate still
	// contains evidence a current human review rejected, so it was inserted
	// closed (an audit record), never pending.
	CrossMatchCaseRevoked = "revoked"
	// CrossMatchCaseIgnored: nothing was written. The aggregate contains
	// rejected evidence and must not replace the player's existing case.
	CrossMatchCaseIgnored = "ignored"
)

// CrossMatchStoreResult reports what StoreCrossMatchReviewCaseResult did, so a
// caller never counts an ignored write as a created or refreshed case.
type CrossMatchStoreResult struct {
	CaseID           string `json:"case_id"` // the row written, or the untouched latest row when ignored
	Outcome          string `json:"outcome"`
	Status           string `json:"status"`
	SupersededCaseID string `json:"superseded_case_id,omitempty"` // set for CrossMatchCaseNewCase
}

// StoreCrossMatchReviewCase is StoreCrossMatchReviewCaseResult without the
// outcome.
func (s *Store) StoreCrossMatchReviewCase(ctx context.Context, rc CrossMatchReviewCase) error {
	_, err := s.StoreCrossMatchReviewCaseResult(ctx, rc)
	return err
}

// StoreCrossMatchReviewCaseResult inserts or refreshes the aggregate case for
// a player. rc.CaseID is the base id of a case lineage (BuildCrossMatchReviewCase
// uses "XM-<player>"); successors are "<base>-r2", "<base>-r3", ...
//
//   - An open case (pending, assigned, in_review, appealed) is refreshed in
//     place: scores, matches, detectors and explanation are updated, a status a
//     moderator set is preserved and created_at keeps its original value.
//   - A finished case is an audit record and is never reopened. A case a
//     negative review superseded keeps its whole snapshot; a case a moderator
//     decided or closed keeps its match scope (a false_positive verdict must
//     never grow to cover matches the moderator did not see) and is refreshed
//     only while the aggregate stays inside that scope. Otherwise the aggregate
//     opens a successor case, so one "no" label or one verdict can never freeze
//     the player's only case against later evidence.
//   - A pending aggregate is rejected only when a current negative review
//     overlaps its scope AND it still contains evidence that review made
//     ineligible, i.e. it was computed before the review. It is then ignored,
//     or, for a player without any case, inserted closed as an audit record.
//     An aggregate computed afterwards cannot contain rejected evidence
//     (GetAllPlayerEvents filters it), which the per-detector event counts
//     verify inside this transaction.
func (s *Store) StoreCrossMatchReviewCaseResult(ctx context.Context, rc CrossMatchReviewCase) (CrossMatchStoreResult, error) {
	var out CrossMatchStoreResult
	if strings.TrimSpace(rc.CaseID) == "" || strings.TrimSpace(rc.PlayerID) == "" {
		return out, fmt.Errorf("cross-match case needs a case id and a player id")
	}
	matchIDsJSON, err := json.Marshal(rc.MatchIDs)
	if err != nil {
		return out, fmt.Errorf("marshal match ids: %w", err)
	}
	detectorsJSON, err := json.Marshal(rc.Detectors)
	if err != nil {
		return out, fmt.Errorf("marshal detectors: %w", err)
	}
	status := rc.Status
	if status == "" {
		status = CaseStatusPending
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	revoked := false
	if status == CaseStatusPending {
		negative, err := currentNegativeReviewTx(ctx, tx, rc.PlayerID, string(matchIDsJSON))
		if err != nil {
			return out, err
		}
		if negative {
			if revoked, err = aggregateHasIneligibleEvidenceTx(ctx, tx, rc, string(matchIDsJSON)); err != nil {
				return out, err
			}
		}
	}
	explanation := rc.Explanation
	if revoked {
		status = CaseStatusClosed
		explanation = crossMatchReviewRevoked + " " + explanation
	}

	latest, predecessorID, found, err := latestCrossMatchCaseTx(ctx, tx, rc.CaseID, rc.PlayerID)
	if err != nil {
		return out, err
	}
	targetID, outcome := rc.CaseID, CrossMatchCaseCreated
	switch {
	case !found:
	case revoked:
		// Totals computed before a review must not replace anything: not a
		// finished audit record, and not an open case either. Every negative
		// review already closed the pending cases it overlapped, so an open
		// case was computed from eligible evidence; the next aggregation
		// refreshes it.
		return CrossMatchStoreResult{CaseID: latest.CaseID, Outcome: CrossMatchCaseIgnored, Status: latest.Status}, nil
	case !crossMatchCaseFinished(latest.Status):
		targetID, outcome = latest.CaseID, CrossMatchCaseRefreshed
	case strings.Contains(latest.Explanation, crossMatchReviewRevoked) || !matchScopeWithin(rc.MatchIDs, latest.MatchIDs):
		outcome = CrossMatchCaseNewCase
	default:
		targetID, outcome = latest.CaseID, CrossMatchCaseRefreshed
	}
	if outcome == CrossMatchCaseNewCase {
		out.SupersededCaseID, predecessorID = latest.CaseID, latest.CaseID
		if targetID, err = nextCrossMatchCaseIDTx(ctx, tx, rc.CaseID); err != nil {
			return out, err
		}
	}
	if targetID != rc.CaseID && predecessorID != "" {
		// Written on every refresh too, so the successor never loses the
		// pointer to the audit record it continues.
		explanation = fmt.Sprintf(crossMatchSuccessorNote, predecessorID) + " " + explanation
	}
	if revoked {
		outcome = CrossMatchCaseRevoked
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
		targetID, rc.PlayerID, string(matchIDsJSON), rc.MatchCount,
		rc.Severity, rc.CumulativeScore, rc.DecayedScore,
		string(detectorsJSON), explanation, status,
		fmtDBTime(rc.CreatedAt), fmtDBTime(time.Now()), crossMatchReviewRevoked,
	)
	if err != nil {
		return out, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT status FROM cross_match_review_cases WHERE case_id = ?`, targetID).Scan(&out.Status); err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	out.CaseID, out.Outcome = targetID, outcome
	return out, nil
}

// crossMatchSuccessorNote must never contain crossMatchReviewRevoked.
const crossMatchSuccessorNote = "Successor of case %s, which is kept unchanged as an audit record; this case is computed from currently eligible evidence only."

// crossMatchCaseFinished reports a status from which aggregation must not
// silently continue the same case (review.ValidTransition has no way back to
// pending from either).
func crossMatchCaseFinished(status string) bool {
	return status == CaseStatusDecided || status == CaseStatusClosed
}

func matchScopeWithin(scope, within []string) bool {
	known := make(map[string]bool, len(within))
	for _, id := range within {
		known[id] = true
	}
	for _, id := range scope {
		if !known[id] {
			return false
		}
	}
	return true
}

// latestCrossMatchCaseTx returns the newest case of a lineage: the base id or
// one of its "-r<n>" successors, for the same player (so a player whose id
// happens to end in "-r2" is never mistaken for another player's successor).
func latestCrossMatchCaseTx(ctx context.Context, tx *sql.Tx, baseID, playerID string) (latest CrossMatchReviewCase, predecessorID string, found bool, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+crossMatchCaseColumns+` FROM cross_match_review_cases
		 WHERE player_id = ? AND (case_id = ? OR substr(case_id, 1, length(?) + 2) = ? || '-r')`,
		playerID, baseID, baseID, baseID)
	if err != nil {
		return latest, "", false, err
	}
	defer rows.Close()
	ids := make(map[int]string)
	latestRevision := 0
	for rows.Next() {
		rc, err := scanCrossMatchCase(rows)
		if err != nil {
			return latest, "", false, err
		}
		revision := 1
		if rc.CaseID != baseID {
			n, err := strconv.Atoi(rc.CaseID[len(baseID)+2:])
			if err != nil || n < 2 {
				continue // not a successor id this store generated
			}
			revision = n
		}
		ids[revision] = rc.CaseID
		if revision > latestRevision {
			latest, latestRevision = rc, revision
		}
	}
	for revision := latestRevision - 1; revision >= 1 && predecessorID == ""; revision-- {
		predecessorID = ids[revision]
	}
	return latest, predecessorID, latestRevision > 0, rows.Err()
}

func nextCrossMatchCaseIDTx(ctx context.Context, tx *sql.Tx, baseID string) (string, error) {
	for revision := 2; revision < 1_000_000; revision++ {
		id := fmt.Sprintf("%s-r%d", baseID, revision)
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cross_match_review_cases WHERE case_id = ?`, id).Scan(&taken); err != nil {
			return "", err
		}
		if taken == 0 {
			return id, nil
		}
	}
	return "", fmt.Errorf("no free successor id for cross-match case %s", baseID)
}

// aggregateHasIneligibleEvidenceTx reports whether rc counts more events for
// any detector than are currently score-eligible for the player inside rc's
// match scope. That is only possible when the aggregate was computed before a
// review rejected some of its evidence (or was not computed from this database
// at all). An aggregate without per-detector counts cannot be verified and is
// treated as containing rejected evidence.
func aggregateHasIneligibleEvidenceTx(ctx context.Context, tx *sql.Tx, rc CrossMatchReviewCase, matchIDsJSON string) (bool, error) {
	if len(rc.Detectors) == 0 {
		return true, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT de.detector_id, COUNT(*) FROM detection_events de
		 WHERE de.player_id = ? AND de.match_id IN (SELECT value FROM json_each(?)) AND `+eligibleScoringEventSQL+`
		 GROUP BY de.detector_id`, rc.PlayerID, matchIDsJSON)
	if err != nil {
		return false, fmt.Errorf("verify aggregate evidence: %w", err)
	}
	defer rows.Close()
	eligible := make(map[string]int)
	for rows.Next() {
		var detector string
		var n int
		if err := rows.Scan(&detector, &n); err != nil {
			return false, err
		}
		eligible[detector] = n
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for detector, counted := range rc.Detectors {
		if counted > eligible[detector] {
			return true, nil
		}
	}
	return false, nil
}

// ListCrossMatchReviewCasesForPlayer returns every aggregate case of a player,
// oldest first: finished audit records followed by the current case.
func (s *Store) ListCrossMatchReviewCasesForPlayer(ctx context.Context, playerID string) ([]CrossMatchReviewCase, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+crossMatchCaseColumns+` FROM cross_match_review_cases
		 WHERE player_id = ? ORDER BY created_at, rowid`, playerID)
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
	// The case id is the stable per-player base of a case lineage: re-running
	// aggregation refreshes the player's open case rather than creating one per
	// run, and opens a "-r<n>" successor only once the latest case is a finished
	// audit record (see StoreCrossMatchReviewCaseResult).
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
