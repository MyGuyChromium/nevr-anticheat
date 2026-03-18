package pattern

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Pat002 detects identical release points across throws (PAT_002).
//
// STATUS: UNSAFE — consistent throwing form in Echo VR produces tight release
// spreads naturally. Skilled players will false-positive. Disabled by default.
type Pat002 struct {
	detect.BaseDetector
	minThrowCount    int
	minReleaseSpread float64
	sigmoidSteepness float64

	// Release positions relative to head per player
	releasePositions map[string][]model.Vec3
	prevThrowCount   map[string]int
}

// NewPat002 creates a new PAT_002 Identical Release Points detector.
func NewPat002(params map[string]any) *Pat002 {
	d := &Pat002{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "PAT_002",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Identical Release Points",
			DetectorCategory: "pattern",
			Inputs:           []string{"throw_event", "hand_tracking", "head_position"},
			Warmup:           60,
			Weight:           0.7,
			IsAutoEnforce:    false,
		},
		minThrowCount:    detect.GetInt(params, "min_throw_count", 8),
		minReleaseSpread: detect.GetFloat(params, "min_release_spread", 0.02),
		sigmoidSteepness: detect.GetFloat(params, "sigmoid_steepness", 50.0),
		releasePositions: make(map[string][]model.Vec3),
		prevThrowCount:   make(map[string]int),
	}
	return d
}

func (d *Pat002) Reset() {
	d.releasePositions = make(map[string][]model.Vec3)
	d.prevThrowCount = make(map[string]int)
}

func (d *Pat002) Configure(params map[string]any) error {
	d.minThrowCount = detect.GetInt(params, "min_throw_count", d.minThrowCount)
	d.minReleaseSpread = detect.GetFloat(params, "min_release_spread", d.minReleaseSpread)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Pat002) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID

		prev := d.prevThrowCount[pid]
		d.prevThrowCount[pid] = ps.ThrowCount

		if ps.ThrowCount <= prev {
			continue
		}

		// New throw: record release position relative to head (player position)
		// Use the dominant hand position as release point relative to body
		relPos := ps.RightHand.Sub(ps.Position)
		d.releasePositions[pid] = append(d.releasePositions[pid], relPos)
		if len(d.releasePositions[pid]) > 50 {
			d.releasePositions[pid] = d.releasePositions[pid][len(d.releasePositions[pid])-50:]
		}

		positions := d.releasePositions[pid]
		if len(positions) < d.minThrowCount {
			continue
		}

		// Compute 3D standard deviation
		variance := model.ComputePositionVariance(positions)
		stddev3d := math.Sqrt(variance)

		if stddev3d >= d.minReleaseSpread {
			continue
		}

		severity := model.SigmoidConfidence(d.minReleaseSpread-stddev3d, 0, d.sigmoidSteepness)
		confidence := model.Clamp01(severity * 0.85)

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.PatternEvidence{
				DetectorSpecific: "identical_release_points",
				Metrics: map[string]float64{
					"stddev_3d":          stddev3d,
					"variance":           variance,
					"min_release_spread": d.minReleaseSpread,
					"throw_count":        float64(len(positions)),
				},
			},
			fmt.Sprintf("release_spread: %.4f m stddev (%d throws)", stddev3d, len(positions)),
			fmt.Sprintf("release_spread: >%.3f m", d.minReleaseSpread),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  frameIdx - 300,
				FrameEnd:    frameIdx,
				AnomalyType: "identical_release",
			},
		)
		events = append(events, ev)

		// Reset after detection
		d.releasePositions[pid] = nil
	}

	return events
}
