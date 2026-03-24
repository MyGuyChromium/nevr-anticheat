package throw

import (
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Throw007 detects penalty field speed manipulation (THROW_007).
//
// STATUS: STUB — Evaluate() returns nil unconditionally.
//
// Requires InPenaltyField telemetry field which does not exist in any known
// Echo VR data source (standard API or .echoreplay format). Disabled by default.
//
// To activate: implement penalty field geometry detection, populate
// InPenaltyField in the feature extractor, implement the detection logic,
// and set Enabled=true in config.
type Throw007 struct {
	detect.BaseDetector
	expectedLoss  float64
	tolerance     float64
	minEntrySpeed float64
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

func (d *Throw007) Reset() {}
func (d *Throw007) Configure(params map[string]any) error {
	d.expectedLoss = detect.GetFloat(params, "expected_penalty_speed_loss", d.expectedLoss)
	d.tolerance = detect.GetFloat(params, "penalty_tolerance", d.tolerance)
	return nil
}

func (d *Throw007) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	// STUB: requires InPenaltyField telemetry field (not available).
	return nil
}
