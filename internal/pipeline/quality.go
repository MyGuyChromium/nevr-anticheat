package pipeline

import (
	"math"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// TelemetryQualityReport is the pre-detector reliability gate for one slice.
// A catastrophic source is kept as evidence, but every resulting event is
// forced to shadow and its confidence is reduced. This gate never creates a
// cheating signal and cannot promote an event to enforcement.
type TelemetryQualityReport struct {
	Score                float64        `json:"score"`
	Grade                string         `json:"grade"`
	Gated                bool           `json:"gated"`
	ConfidenceMultiplier float64        `json:"confidence_multiplier"`
	Rows                 int            `json:"rows"`
	DistinctFrames       int            `json:"distinct_frames"`
	Players              int            `json:"players"`
	TimestampProblems    int            `json:"timestamp_problems"`
	LargeGaps            int            `json:"large_gaps"`
	MissingHands         int            `json:"missing_hands"`
	MissingGameVelocity  int            `json:"missing_game_velocity"`
	MissingDisc          int            `json:"missing_disc"`
	Reasons              []string       `json:"reasons"`
	PerPlayerRows        map[string]int `json:"per_player_rows,omitempty"`
}

// AssessTelemetryQuality inspects the input before detectors run. It is
// deliberately source-oriented: missing optional fields reduce certainty,
// while only unusably short or badly timed input activates the hard gate.
func AssessTelemetryQuality(frames []model.PlayerTelemetryFrame, cfg *config.Config) TelemetryQualityReport {
	r := TelemetryQualityReport{Rows: len(frames), PerPlayerRows: make(map[string]int)}
	if len(frames) == 0 {
		r.Score, r.Grade, r.Gated, r.ConfidenceMultiplier = 0, "unusable", true, 0.25
		r.Reasons = []string{"no normalized telemetry rows were available"}
		return r
	}
	byPlayer := make(map[string][]model.PlayerTelemetryFrame)
	distinct := make(map[int]bool)
	for _, f := range frames {
		byPlayer[f.PlayerID] = append(byPlayer[f.PlayerID], f)
		r.PerPlayerRows[f.PlayerID]++
		distinct[f.FrameIndex] = true
		if !isFiniteNumber(f.Timestamp) || !isFiniteNumber(f.DeltaTime) {
			r.TimestampProblems++
		}
		if f.LeftHandPosition.IsZero() || f.RightHandPosition.IsZero() {
			r.MissingHands++
		}
		if f.ReportedVelocity == nil {
			r.MissingGameVelocity++
		}
		if f.Disc == nil {
			r.MissingDisc++
		}
	}
	r.DistinctFrames, r.Players = len(distinct), len(byPlayer)
	maxDt := 0.25
	if cfg != nil && cfg.Pipeline.MaxFrameDt > 0 {
		maxDt = cfg.Pipeline.MaxFrameDt
	}
	for _, playerFrames := range byPlayer {
		sort.Slice(playerFrames, func(i, j int) bool { return playerFrames[i].FrameIndex < playerFrames[j].FrameIndex })
		for i := 1; i < len(playerFrames); i++ {
			dt := playerFrames[i].Timestamp - playerFrames[i-1].Timestamp
			if !isFiniteNumber(dt) || dt <= 0 {
				r.TimestampProblems++
			} else if dt > maxDt {
				r.LargeGaps++
			}
		}
	}
	n := float64(len(frames))
	penalty := math.Min(55, 100*float64(r.TimestampProblems)/n) +
		math.Min(20, 50*float64(r.LargeGaps)/n) +
		math.Min(10, 10*float64(r.MissingHands)/n) +
		math.Min(10, 10*float64(r.MissingGameVelocity)/n) +
		math.Min(5, 5*float64(r.MissingDisc)/n)
	r.Score = model.Clamp(100-penalty, 0, 100)
	switch {
	case r.Score >= 90:
		r.Grade = "excellent"
	case r.Score >= 75:
		r.Grade = "good"
	case r.Score >= 55:
		r.Grade = "caution"
	case r.Score >= 35:
		r.Grade = "poor"
	default:
		r.Grade = "unusable"
	}
	// A short clip is weak evidence but can still be internally valid (and is
	// common in focused test clips), so it is reported without hard-gating it.
	// Hard gating is reserved for empty/single-row or severely corrupt timing.
	r.Gated = len(frames) < 2 || r.Score < 35 || r.TimestampProblems > len(frames)/4
	if r.Score >= 85 {
		r.ConfidenceMultiplier = 1
	} else {
		r.ConfidenceMultiplier = model.Clamp(0.35+r.Score/130, 0.35, 0.99)
	}
	if len(frames) < 20 {
		r.Reasons = append(r.Reasons, "too few samples for reliable detector warmup")
	}
	if r.TimestampProblems > 0 {
		r.Reasons = append(r.Reasons, "non-monotonic or non-finite timestamps were found")
	}
	if r.LargeGaps > 0 {
		r.Reasons = append(r.Reasons, "sampling gaps break finite-difference kinematics")
	}
	if r.MissingGameVelocity > len(frames)/2 {
		r.Reasons = append(r.Reasons, "game-authored velocity is mostly unavailable; playspace and stacking separation is limited")
	}
	if r.MissingHands > len(frames)/2 {
		r.Reasons = append(r.Reasons, "controller tracking is mostly unavailable")
	}
	return r
}

func isFiniteNumber(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
