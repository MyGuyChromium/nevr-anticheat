package model

import "sort"

const (
	ReviewStatusNoSignals    = "no_signals"
	ReviewStatusReviewNeeded = "review_needed"
)

// ReviewAssessment describes detector findings independently of suspicion
// scoring. Shadow observations remain visible even though they add no score.
// A review_needed status is not a cheating verdict or a moderator case status:
// this projection has no ground-truth labels or review decisions as inputs.
type ReviewAssessment struct {
	Status        string                  `json:"status"`
	SignalCount   int                     `json:"signal_count"`
	ScoredSignals int                     `json:"scored_signals"`
	ShadowSignals int                     `json:"shadow_signals"`
	Detectors     []DetectorReviewSignals `json:"detectors"`
}

// DetectorReviewSignals counts stored incidents, not the raw emissions that
// the pipeline may have merged into each incident.
type DetectorReviewSignals struct {
	DetectorID    string `json:"detector_id"`
	SignalCount   int    `json:"signal_count"`
	ScoredSignals int    `json:"scored_signals"`
	ShadowSignals int    `json:"shadow_signals"`
}

// AssessPlayerEvents projects all of a player's supplied events, including
// shadow events, without changing events, scores, or enforcement decisions.
// Callers supply one row per incident (as GetMatchEvents does); counting rows
// preserves parity with the existing scored/shadow counters. Empty player IDs
// never act as a wildcard. NoSignals means no supplied detector event, not
// verified fair play or complete detector coverage.
func AssessPlayerEvents(playerID string, events []DetectionEvent) ReviewAssessment {
	out := ReviewAssessment{Status: ReviewStatusNoSignals, Detectors: []DetectorReviewSignals{}}
	if playerID == "" {
		return out
	}
	byDetector := make(map[string]DetectorReviewSignals)
	for _, event := range events {
		if event.PlayerID != playerID {
			continue
		}
		out.SignalCount++
		detector := byDetector[event.DetectorID]
		detector.DetectorID = event.DetectorID
		detector.SignalCount++
		if event.IsShadow {
			out.ShadowSignals++
			detector.ShadowSignals++
		} else {
			out.ScoredSignals++
			detector.ScoredSignals++
		}
		byDetector[event.DetectorID] = detector
	}
	if out.SignalCount > 0 {
		out.Status = ReviewStatusReviewNeeded
	}
	for _, detector := range byDetector {
		out.Detectors = append(out.Detectors, detector)
	}
	sort.Slice(out.Detectors, func(i, j int) bool {
		if out.Detectors[i].SignalCount != out.Detectors[j].SignalCount {
			return out.Detectors[i].SignalCount > out.Detectors[j].SignalCount
		}
		return out.Detectors[i].DetectorID < out.Detectors[j].DetectorID
	})
	return out
}
