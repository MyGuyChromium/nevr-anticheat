package throw

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

type Throw007 struct {
	detect.BaseDetector
	expectedLoss    float64
	tolerance       float64
	minEntrySpeed   float64
	wasInPenalty    bool
	entrySpeed      float64
	entryFrame      int
	entryTimestamp  float64
}

func NewThrow007(params map[string]any) *Throw007 {
	return &Throw007{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_007", DetectorVersion: "1.0.0",
			DetectorName: "Penalty Field Tampering", DetectorCategory: "throw",
			Inputs: []string{"disc_state"}, Warmup: 5, Weight: 0.6,
		},
		expectedLoss:  detect.GetFloat(params, "expected_penalty_speed_loss", 0.5),
		tolerance:     detect.GetFloat(params, "penalty_tolerance", 0.1),
		minEntrySpeed: detect.GetFloat(params, "min_entry_speed", 5.0),
	}
}

func (d *Throw007) Reset() { d.wasInPenalty = false; d.entrySpeed = 0 }
func (d *Throw007) Configure(params map[string]any) error {
	d.expectedLoss = detect.GetFloat(params, "expected_penalty_speed_loss", d.expectedLoss)
	d.tolerance = detect.GetFloat(params, "penalty_tolerance", d.tolerance)
	return nil
}

func (d *Throw007) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	// This detector requires disc penalty field data which may not be available
	// in all telemetry sources. It monitors disc speed entering/exiting penalty zones.
	// Disabled-by-default via shadow mode. Requires InPenaltyField to be populated.
	var events []model.DetectionEvent
	// Get disc state from first player that has it
	for _, ps := range players {
		if ps.LastThrow == nil {
			continue
		}
		// Penalty field detection would go here when disc state with penalty field info is available
		_ = ps
	}
	_ = fmt.Sprintf // suppress unused import
	return events
}
