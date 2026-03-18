package pattern

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// HistoryProvider supplies cross-match detection history.
type HistoryProvider interface {
	GetPlayerDetections(playerID string, matchLimit int) ([]model.DetectionEvent, error)
}

// Pat003 detects cross-match consistency patterns (PAT_003).
//
// STATUS: CROSS_MATCH_DEPENDENT — requires accumulated detection history in DB.
//
// HistoryProvider is wired in cmd/anticheat and cmd/server via
// sqlite.NewStoreHistoryProvider. The detector is functional but produces no
// events until min_matches (default 3) worth of prior detection history exists.
// Disabled by default for initial deployments; enable once sufficient match
// history has been collected.
type Pat003 struct {
	detect.BaseDetector
	minMatches         int
	minAvgConfidence   float64
	matchLimit         int
	sigmoidSteepness   float64
	historyProvider    HistoryProvider

	checkedPlayers map[string]bool
}

// NewPat003 creates a new PAT_003 Cross-Match Consistency detector.
func NewPat003(params map[string]any) *Pat003 {
	d := &Pat003{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "PAT_003",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Cross-Match Consistency",
			DetectorCategory: "pattern",
			Inputs:           []string{"history"},
			Warmup:           0,
			Weight:           0.9,
			IsAutoEnforce:    false,
		},
		minMatches:       detect.GetInt(params, "min_matches", 3),
		minAvgConfidence: detect.GetFloat(params, "min_avg_confidence", 0.6),
		matchLimit:       detect.GetInt(params, "match_limit", 20),
		sigmoidSteepness: detect.GetFloat(params, "sigmoid_steepness", 2.0),
		checkedPlayers:   make(map[string]bool),
	}

	// Accept HistoryProvider from params if provided
	if hp, ok := params["history_provider"]; ok {
		if provider, ok2 := hp.(HistoryProvider); ok2 {
			d.historyProvider = provider
		}
	}

	return d
}

func (d *Pat003) Reset() {
	d.checkedPlayers = make(map[string]bool)
}

func (d *Pat003) Configure(params map[string]any) error {
	d.minMatches = detect.GetInt(params, "min_matches", d.minMatches)
	d.minAvgConfidence = detect.GetFloat(params, "min_avg_confidence", d.minAvgConfidence)
	d.matchLimit = detect.GetInt(params, "match_limit", d.matchLimit)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)

	if hp, ok := params["history_provider"]; ok {
		if provider, ok2 := hp.(HistoryProvider); ok2 {
			d.historyProvider = provider
		}
	}
	return nil
}

func (d *Pat003) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	// Skip silently if no history provider
	if d.historyProvider == nil {
		return nil
	}

	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID

		// Only check once per player per match (on first frame)
		if d.checkedPlayers[pid] {
			continue
		}
		d.checkedPlayers[pid] = true

		history, err := d.historyProvider.GetPlayerDetections(pid, d.matchLimit)
		if err != nil || len(history) == 0 {
			continue
		}

		// Group by detector ID, count distinct matches
		type detectorStats struct {
			matchIDs       map[string]bool
			totalConf      float64
			count          int
		}
		byDetector := make(map[string]*detectorStats)

		for _, evt := range history {
			// Skip events from current match
			if evt.MatchID == matchCtx.MatchID {
				continue
			}
			stats, ok := byDetector[evt.DetectorID]
			if !ok {
				stats = &detectorStats{matchIDs: make(map[string]bool)}
				byDetector[evt.DetectorID] = stats
			}
			stats.matchIDs[evt.MatchID] = true
			stats.totalConf += evt.Confidence
			stats.count++
		}

		for detID, stats := range byDetector {
			matchCount := len(stats.matchIDs)
			avgConf := stats.totalConf / float64(stats.count)

			if matchCount < d.minMatches {
				continue
			}
			if avgConf < d.minAvgConfidence {
				continue
			}

			severity := model.SigmoidConfidence(float64(matchCount), float64(d.minMatches), 0.5)
			confidence := model.Clamp01(avgConf * model.SigmoidConfidence(float64(matchCount), float64(d.minMatches), 0.5))

			ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
				severity, confidence,
				model.PatternEvidence{
					DetectorSpecific: "cross_match_consistency",
					Metrics: map[string]float64{
						"repeat_detector_matches": float64(matchCount),
						"avg_confidence":          avgConf,
						"total_events":            float64(stats.count),
						"min_matches":             float64(d.minMatches),
					},
				},
				fmt.Sprintf("cross_match: detector %s in %d matches, avg_conf=%.2f", detID, matchCount, avgConf),
				fmt.Sprintf("cross_match: same detector in <%d matches or avg_conf<%.1f", d.minMatches, d.minAvgConfidence),
				model.CausalKey{
					PlayerID:    pid,
					FrameStart:  frameIdx,
					FrameEnd:    frameIdx,
					AnomalyType: "cross_match_" + detID,
				},
			)
			events = append(events, ev)
		}
	}

	return events
}
