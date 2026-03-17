package state

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// State001 detects impossible grab distance (STATE_001).
type State001 struct {
	detect.BaseDetector
	grabDistanceThreshold float64
	closingVelocityScale  float64
	sigmoidSteepness      float64

	prevHasDisc      map[string]bool
	lastReleaseFrame map[string]int
}

// NewState001 creates a new STATE_001 Impossible Grab Distance detector.
func NewState001(params map[string]any) *State001 {
	d := &State001{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "STATE_001",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Impossible Grab Distance",
			DetectorCategory: "state",
			Inputs:           []string{"possession", "hand_tracking", "disc_state"},
			Warmup:           5,
			Weight:           0.8,
			IsAutoEnforce:    false,
		},
		grabDistanceThreshold: detect.GetFloat(params, "grab_distance_threshold", 3.0),
		closingVelocityScale:  detect.GetFloat(params, "closing_velocity_scale", 0.25),
		sigmoidSteepness:      detect.GetFloat(params, "sigmoid_steepness", 2.0),
		prevHasDisc:           make(map[string]bool),
		lastReleaseFrame:      make(map[string]int),
	}
	return d
}

func (d *State001) Reset() {
	d.prevHasDisc = make(map[string]bool)
	d.lastReleaseFrame = make(map[string]int)
}

func (d *State001) Configure(params map[string]any) error {
	d.grabDistanceThreshold = detect.GetFloat(params, "grab_distance_threshold", d.grabDistanceThreshold)
	d.closingVelocityScale = detect.GetFloat(params, "closing_velocity_scale", d.closingVelocityScale)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *State001) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	// Find disc position from any player's context or match data
	// We check for possession changes (player gains disc)
	for _, ps := range players {
		pid := ps.PlayerID
		wasHolding := d.prevHasDisc[pid]
		d.prevHasDisc[pid] = ps.HasDisc

		// Track release frames for regrab detection
		if wasHolding && !ps.HasDisc {
			d.lastReleaseFrame[pid] = frameIdx
		}

		// Only fire on possession gain
		if !ps.HasDisc || wasHolding {
			continue
		}

		// Regrab filter: if this player released the disc within 5 frames,
		// the disc position data desyncs from the actual catch moment.
		// Regrabs are a legitimate advanced technique.
		if releaseFrame, ok := d.lastReleaseFrame[pid]; ok && frameIdx-releaseFrame <= 5 {
			continue
		}

		// Measure nearest hand to disc position at the moment of possession gain
		var discPos model.Vec3
		if ps.CurrentDisc != nil {
			discPos = ps.CurrentDisc.Position
		} else {
			// Fallback: use player position if disc state unavailable
			discPos = ps.Position
		}
		leftDist := ps.LeftHand.Distance(discPos)
		rightDist := ps.RightHand.Distance(discPos)
		nearestHandDist := math.Min(leftDist, rightDist)

		// Adjust for closing velocity: player moving towards disc
		closingSpeed := ps.Speed
		adjustedDist := nearestHandDist - (closingSpeed * d.closingVelocityScale)
		if adjustedDist < 0 {
			adjustedDist = 0
		}

		// Use the configured grab distance threshold which accounts for
		// network latency, frame-rate interpolation, and regrab timing.
		// The physics GrabRange (0.8m) is the server-side constant but
		// doesn't reflect what replay data actually shows.
		if adjustedDist <= d.grabDistanceThreshold {
			continue
		}

		// Desync guard: if RAW hand-to-disc distance > 8m, this is clearly
		// a data timing artifact (disc position lags behind possession change),
		// not a real extended-reach cheat.
		if nearestHandDist > 6.0 {
			continue
		}

		excess := adjustedDist - d.grabDistanceThreshold
		severity := model.SigmoidConfidence(adjustedDist, d.grabDistanceThreshold*1.5, d.sigmoidSteepness)
		confidence := model.SigmoidConfidence(excess, 0, d.sigmoidSteepness)
		confidence = model.Clamp01(confidence * 0.85)

		if ps.IsHighPing {
			confidence *= 0.6
		}

		metrics := map[string]float64{
			"nearest_hand_distance": nearestHandDist,
			"adjusted_distance":     adjustedDist,
			"grab_range":            d.grabDistanceThreshold,
			"closing_speed":         closingSpeed,
			"excess":                excess,
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.StateEvidence{
				DetectorSpecific: "impossible_grab_distance",
				Metrics:          metrics,
			},
			fmt.Sprintf("grab_distance: %.2f m (adjusted: %.2f m)", nearestHandDist, adjustedDist),
			fmt.Sprintf("grab_distance: 0-%.2f m", d.grabDistanceThreshold),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  frameIdx - 2,
				FrameEnd:    frameIdx,
				AnomalyType: "grab_distance",
			},
		)
		events = append(events, ev)
	}

	return events
}
