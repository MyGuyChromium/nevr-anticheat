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
// non-shadow detection events stored at or after the given time.
func (s *Store) GetDistinctPlayersWithEvents(ctx context.Context, since time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT player_id FROM detection_events
		 WHERE created_at >= ? AND is_shadow = 0
		 ORDER BY player_id`,
		fmtDBTime(since),
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

// GetAllPlayerEvents returns all non-shadow detection events for a player,
// ordered by match then frame. StoredAt carries created_at.
func (s *Store) GetAllPlayerEvents(ctx context.Context, playerID string) ([]model.DetectionEvent, error) {
	return s.queryEvents(ctx,
		`SELECT `+eventColumns+` FROM detection_events
		 WHERE player_id = ? AND is_shadow = 0
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
	// Now is the reference time for decay; zero means time.Now().
	Now time.Time
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
	AnchorFallbacks int `json:"anchor_fallbacks"`
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
// Shadow events must be filtered before calling (GetAllPlayerEvents does).
func ComputePlayerCrossMatchSummary(events []model.DetectionEvent, matchStarts map[string]time.Time, cfg CrossMatchConfig) PlayerCrossMatchSummary {
	s := PlayerCrossMatchSummary{
		ByDetector:       make(map[string]int),
		ByMatch:          make(map[string]int),
		PointsByDetector: make(map[string]float64),
		PointsByCategory: make(map[string]float64),
		MatchIDs:         []string{},
		Level:            model.LevelClean,
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
	s.TotalEvents = len(sorted)

	type detKey struct{ match, detector string }
	perDetectorPerMatch := make(map[detKey]int)

	var totalSev, totalConf, decayedSum float64
	for _, ev := range sorted {
		s.ByDetector[ev.DetectorID]++
		s.ByMatch[ev.MatchID]++
		totalSev += ev.Severity
		totalConf += ev.Confidence

		k := detKey{ev.MatchID, ev.DetectorID}
		if cfg.MaxContribPerDetectorPerMatch > 0 && perDetectorPerMatch[k] >= cfg.MaxContribPerDetectorPerMatch {
			continue
		}
		perDetectorPerMatch[k]++
		s.ScoredEvents++

		points := ev.Severity * ev.Confidence * ev.EnforcementWeight * 100.0
		if cfg.MaxSingleContribution > 0 && points > cfg.MaxSingleContribution {
			points = cfg.MaxSingleContribution
		}
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
	s.AvgSeverity = totalSev / float64(len(sorted))
	s.AvgConfidence = totalConf / float64(len(sorted))
	s.DecayedScore = math.Min(decayedSum, 100)

	for mid := range s.ByMatch {
		s.MatchIDs = append(s.MatchIDs, mid)
	}
	sort.Strings(s.MatchIDs)

	sc := model.SuspicionScore{TotalScore: s.DecayedScore}
	s.Level = sc.Level()
	return s
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
// is preserved and created_at keeps its original value.
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
	_, err = s.db.ExecContext(ctx,
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
			updated_at = excluded.updated_at`,
		rc.CaseID, rc.PlayerID, string(matchIDsJSON), rc.MatchCount,
		rc.Severity, rc.CumulativeScore, rc.DecayedScore,
		string(detectorsJSON), rc.Explanation, status,
		fmtDBTime(rc.CreatedAt), fmtDBTime(time.Now()),
	)
	return err
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
// label using the same tiers as model.SuspicionScore.Level():
// critical (>= critical tier), high (>= high_risk), medium (>= suspicious), low.
func SeverityForScore(score float64) string {
	sc := model.SuspicionScore{TotalScore: math.Min(score, 100)}
	switch sc.Level() {
	case model.LevelActionWorthy, model.LevelCritical:
		return "critical"
	case model.LevelHighRisk:
		return "high"
	case model.LevelSuspicious:
		return "medium"
	default:
		return "low"
	}
}

// BuildCrossMatchReviewCase creates an aggregate review case from a cross-match
// summary when the (capped, decayed) score reaches reviewThreshold. The
// threshold is the configured review threshold (scoring.review_threshold),
// which maps onto the high_risk tier; severity is derived from the same tier
// table as Level().
func BuildCrossMatchReviewCase(summary PlayerCrossMatchSummary, reviewThreshold float64) *CrossMatchReviewCase {
	if summary.DecayedScore < reviewThreshold {
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
		Severity:        SeverityForScore(summary.DecayedScore),
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
