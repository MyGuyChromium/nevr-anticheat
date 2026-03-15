package pattern

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Pat004 detects composite multi-cheat patterns (PAT_004).
// Fires when a player triggers detectors from 3+ distinct categories in a single match.
type Pat004 struct {
	detect.BaseDetector
	minCategories    int
	sigmoidSteepness float64

	// Track distinct categories fired per player
	playerCategories map[string]map[string]bool
	fired            map[string]bool
}

// NewPat004 creates a new PAT_004 Composite Multi-Cheat detector.
func NewPat004(params map[string]any) *Pat004 {
	d := &Pat004{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "PAT_004",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Composite Multi-Cheat",
			DetectorCategory: "pattern",
			Inputs:           []string{"detection_events"},
			Warmup:           0,
			Weight:           1.0,
			IsAutoEnforce:    false,
		},
		minCategories:    detect.GetInt(params, "min_categories", 3),
		sigmoidSteepness: detect.GetFloat(params, "sigmoid_steepness", 1.0),
		playerCategories: make(map[string]map[string]bool),
		fired:            make(map[string]bool),
	}
	return d
}

func (d *Pat004) Reset() {
	d.playerCategories = make(map[string]map[string]bool)
	d.fired = make(map[string]bool)
}

func (d *Pat004) Configure(params map[string]any) error {
	d.minCategories = detect.GetInt(params, "min_categories", d.minCategories)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

// RecordDetection is called by the pipeline to track which categories have fired for each player.
func (d *Pat004) RecordDetection(playerID, category string) {
	if d.playerCategories[playerID] == nil {
		d.playerCategories[playerID] = make(map[string]bool)
	}
	d.playerCategories[playerID][category] = true
}

func (d *Pat004) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID

		cats := d.playerCategories[pid]
		if len(cats) < d.minCategories {
			continue
		}

		if d.fired[pid] {
			continue
		}
		d.fired[pid] = true

		catCount := len(cats)
		severity := model.SigmoidConfidence(float64(catCount), float64(d.minCategories), d.sigmoidSteepness)
		confidence := model.Clamp01(severity * 0.9)

		// List categories
		catList := ""
		for c := range cats {
			if catList != "" {
				catList += ", "
			}
			catList += c
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.PatternEvidence{
				DetectorSpecific: "composite_multi_cheat",
				Metrics: map[string]float64{
					"category_count": float64(catCount),
					"min_categories": float64(d.minCategories),
				},
			},
			fmt.Sprintf("multi_cheat: %d categories [%s]", catCount, catList),
			fmt.Sprintf("categories: <%d distinct", d.minCategories),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  0,
				FrameEnd:    frameIdx,
				AnomalyType: "composite_multi_cheat",
			},
		)
		events = append(events, ev)
	}

	return events
}
