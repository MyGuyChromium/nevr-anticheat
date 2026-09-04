package sqlite

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// DetectorCalibration is the human ground truth for one detector: moderator
// case decisions plus direct event labels from the desktop app.
//
// A detector "fired in" a case when it produced any event (shadow or not) for
// the case's player in the case's match(es). Shadow events count on purpose:
// calibration is the mechanism by which a shadow detector earns promotion.
//
// Per-detector feedback recorded on a decision (--detector ID=yes|no|uncertain)
// overrides the case-level verdict for that detector only.
type DetectorCalibration struct {
	DetectorID     string
	Confirmed      int // case confirmed_cheat, or feedback "yes"
	FalsePositive  int // case false_positive, or feedback "no"
	Inconclusive   int // case inconclusive, or feedback "uncertain"
	NeedsMoreData  int // case needs_more_data (no feedback)
	CasesReviewed  int // decisions in which the detector fired or received feedback
	FeedbackGiven  int // decisions carrying explicit feedback for this detector
	EventsReviewed int // events by this detector inside reviewed cases
	DirectLabels   int // individual desktop event labels for this detector
}

// Precision returns confirmed / (confirmed + false positives) and whether
// there was any labeled outcome to compute it from.
func (c DetectorCalibration) Precision() (float64, bool) {
	labeled := c.Confirmed + c.FalsePositive
	if labeled == 0 {
		return 0, false
	}
	return float64(c.Confirmed) / float64(labeled), true
}

// caseScope identifies the player and matches a decision's case covers.
type caseScope struct {
	playerID string
	matchIDs []string
}

func (s *Store) resolveCaseScope(ctx context.Context, caseID string) (caseScope, error) {
	if rc, err := s.GetReviewCase(ctx, caseID); err == nil {
		return caseScope{playerID: rc.PlayerID, matchIDs: []string{rc.MatchID}}, nil
	}
	xm, err := s.GetCrossMatchReviewCase(ctx, caseID)
	if err != nil {
		return caseScope{}, fmt.Errorf("case %s: %w", caseID, ErrNotFound)
	}
	return caseScope{playerID: xm.PlayerID, matchIDs: xm.MatchIDs}, nil
}

// detectorEventCounts returns detector_id -> event count for a player in the
// given matches, shadow events included.
func (s *Store) detectorEventCounts(ctx context.Context, sc caseScope) (map[string]int, error) {
	out := make(map[string]int)
	if len(sc.matchIDs) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(sc.matchIDs)), ",")
	args := make([]any, 0, len(sc.matchIDs)+1)
	args = append(args, sc.playerID)
	for _, m := range sc.matchIDs {
		args = append(args, m)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT detector_id, COUNT(*) FROM detection_events
		 WHERE player_id = ? AND match_id IN (`+placeholders+`)
		 GROUP BY detector_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var det string
		var n int
		if err := rows.Scan(&det, &n); err != nil {
			return nil, err
		}
		out[det] = n
	}
	return out, rows.Err()
}

// ComputeCalibration joins moderator decisions made at or after `since` (zero =
// all) to the detection events of the decided cases and tallies per-detector
// outcomes. Results are sorted by detector ID.
//
// A case counts once: only its latest decision in the window is used, so a
// re-recorded verdict (an appeal outcome, or a database written before
// StoreModeratorDecision rejected repeat verdicts) does not count the same
// case twice per detector.
func (s *Store) ComputeCalibration(ctx context.Context, since time.Time) ([]DetectorCalibration, error) {
	all, err := s.ListModeratorDecisions(ctx, since, 0)
	if err != nil {
		return nil, fmt.Errorf("listing decisions: %w", err)
	}
	// ListModeratorDecisions is newest first: the first decision seen per
	// case is the latest one.
	seenCase := make(map[string]bool, len(all))
	decisions := make([]model.ModeratorDecision, 0, len(all))
	for _, d := range all {
		if seenCase[d.CaseID] {
			continue
		}
		seenCase[d.CaseID] = true
		decisions = append(decisions, d)
	}
	byDetector := make(map[string]*DetectorCalibration)
	get := func(id string) *DetectorCalibration {
		c, ok := byDetector[id]
		if !ok {
			c = &DetectorCalibration{DetectorID: id}
			byDetector[id] = c
		}
		return c
	}

	for _, d := range decisions {
		scope, err := s.resolveCaseScope(ctx, d.CaseID)
		if err != nil {
			// A decision whose case was deleted still carries detector feedback.
			scope = caseScope{}
		}
		counts, err := s.detectorEventCounts(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("events for case %s: %w", d.CaseID, err)
		}
		feedback := make(map[string]string, len(d.DetectorFeedback))
		for _, fb := range d.DetectorFeedback {
			feedback[fb.DetectorID] = fb.Correct
		}
		involved := make(map[string]bool, len(counts)+len(feedback))
		for det := range counts {
			involved[det] = true
		}
		for det := range feedback {
			involved[det] = true
		}
		for det := range involved {
			c := get(det)
			c.CasesReviewed++
			c.EventsReviewed += counts[det]
			if fb, ok := feedback[det]; ok {
				c.FeedbackGiven++
				switch fb {
				case "yes":
					c.Confirmed++
				case "no":
					c.FalsePositive++
				default:
					c.Inconclusive++
				}
				continue
			}
			switch d.Verdict {
			case VerdictConfirmedCheat:
				c.Confirmed++
			case VerdictFalsePositive:
				c.FalsePositive++
			case VerdictInconclusive:
				c.Inconclusive++
			case VerdictNeedsMoreData:
				c.NeedsMoreData++
			}
		}
	}

	// Direct event labels are independent calibration evidence. They are
	// especially important while detectors run in shadow mode and therefore do
	// not create review cases.
	direct, err := s.ListEventReviews(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("listing event reviews: %w", err)
	}
	for _, review := range direct {
		c := get(review.DetectorID)
		c.DirectLabels++
		c.EventsReviewed++
		switch review.Verdict {
		case "yes":
			c.Confirmed++
		case "no":
			c.FalsePositive++
		default:
			c.Inconclusive++
		}
	}

	out := make([]DetectorCalibration, 0, len(byDetector))
	for _, c := range byDetector {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DetectorID < out[j].DetectorID })
	return out, nil
}

// ParseDetectorFeedback parses CLI-style "DETECTOR_ID=yes|no|uncertain" pairs.
func ParseDetectorFeedback(specs []string) ([]model.DetectorVerdict, error) {
	var out []model.DetectorVerdict
	for _, spec := range specs {
		id, val, ok := strings.Cut(spec, "=")
		id = strings.TrimSpace(id)
		val = strings.ToLower(strings.TrimSpace(val))
		if !ok || id == "" {
			return nil, fmt.Errorf("invalid detector feedback %q (want ID=yes|no|uncertain)", spec)
		}
		switch val {
		case "yes", "no", "uncertain":
		default:
			return nil, fmt.Errorf("invalid detector feedback %q (want ID=yes|no|uncertain)", spec)
		}
		out = append(out, model.DetectorVerdict{DetectorID: id, Correct: val})
	}
	return out, nil
}
