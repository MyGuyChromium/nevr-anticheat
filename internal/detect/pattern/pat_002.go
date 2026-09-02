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
//
// The release point is the THROWING hand's position at release (from the
// extractor's ThrowEvent, so left-handed throwers are measured on the hand
// that threw), expressed in the player's body frame: the offset from the
// player position rotated by the inverse body orientation. A macro that
// releases from a fixed body-relative point is therefore measured the
// same way whichever direction the player faces, and an idle off-hand
// resting on the body never produces a fake near-zero spread.
type Pat002 struct {
	detect.BaseDetector
	minThrowCount    int
	minReleaseSpread float64
	minReleaseSpeed  float64
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
			DetectorVersion:  "2.1.0",
			DetectorName:     "Identical Release Points",
			DetectorCategory: "pattern",
			Inputs:           []string{"throw_event", "hand_tracking", "head_position"},
			Warmup:           60,
			Weight:           0.7,
			IsAutoEnforce:    false,
		},
		minThrowCount:    detect.GetInt(params, "min_throw_count", 8),
		minReleaseSpread: detect.GetFloat(params, "min_release_spread", 0.02),
		minReleaseSpeed:  detect.GetFloat(params, "min_release_speed", 0),
		sigmoidSteepness: detect.GetFloat(params, "sigmoid_steepness", 50.0),
	}
	d.Reset()
	return d
}

func (d *Pat002) Reset() {
	d.releasePositions = make(map[string][]model.Vec3)
	d.prevThrowCount = make(map[string]int)
}

func (d *Pat002) Configure(params map[string]any) error {
	d.minThrowCount = detect.GetInt(params, "min_throw_count", d.minThrowCount)
	d.minReleaseSpread = detect.GetFloat(params, "min_release_spread", d.minReleaseSpread)
	d.minReleaseSpeed = detect.GetFloat(params, "min_release_speed", d.minReleaseSpeed)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

// releasePoint returns the throwing hand's release position in the body
// frame, and false when the throw should not be sampled.
func (d *Pat002) releasePoint(ps *model.PlayerState) (model.Vec3, bool) {
	handPos := ps.RightHand
	bodyPos := ps.Position
	if t := ps.LastThrow; t != nil {
		if d.minReleaseSpeed > 0 && t.ReleaseSpeed < d.minReleaseSpeed {
			return model.Vec3{}, false
		}
		if !t.HandPosition.IsZero() {
			handPos = t.HandPosition
		} else if t.ThrowingHand == "left" {
			handPos = ps.LeftHand
		}
		if !t.PlayerPosition.IsZero() {
			bodyPos = t.PlayerPosition
		}
	}
	if handPos.IsZero() {
		return model.Vec3{}, false
	}
	rel := handPos.Sub(bodyPos)
	if ps.Rotation.IsUnit() {
		rel = rel.Rotate(ps.Rotation.Conjugate())
	}
	return rel, true
}

func (d *Pat002) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID

		prev, seen := d.prevThrowCount[pid]
		d.prevThrowCount[pid] = ps.ThrowCount
		if !seen && ps.ThrowCount > 1 {
			continue
		}
		if ps.ThrowCount <= prev {
			continue
		}

		relPos, ok := d.releasePoint(ps)
		if !ok {
			continue
		}
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
