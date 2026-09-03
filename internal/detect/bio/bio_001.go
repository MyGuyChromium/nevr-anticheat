package bio

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// minSustainedWristFrames is the floor applied to min_violation_frames.
// At 15 Hz a 2-frame streak is a single pair of samples, which one
// interpolation hiccup or a tracking re-acquire can produce; three
// consecutive frames (~0.2 s) is the shortest run we treat as evidence.
const minSustainedWristFrames = 3

// Bio001 detects impossible wrist rotation speeds (BIO_001).
//
// Sampling-rate caveat: the feature extractor computes the rate as
// Quat.AngularDistance(prev, cur)/dt and AngularDistance is bounded by pi,
// so the largest rate any source can express is pi/dt — 46.9 rad/s at the
// bridge's 15 Hz poll (dt = 0.067 s), 94 rad/s at 30 Hz, 188 rad/s at 60 Hz.
// The metric SATURATES at that value: a hand flipping 180 degrees every
// frame and a hand spinning ten times per frame look identical. With the
// default 50 rad/s threshold the detector is therefore unreachable on 15 Hz
// telemetry; Reachable(dt) reports this so operators can see it. The
// threshold is left at the documented physical limit rather than lowered
// below the saturation point, because choosing a 15 Hz-attainable value
// is a calibration decision that needs real data.
type Bio001 struct {
	detect.BaseDetector
	maxWristAngularVelocity float64
	minViolationFrames      int
	sigmoidSteepness        float64 // slope of the severity sigmoid on the rate/threshold ratio

	left  map[string]*streak
	right map[string]*streak
}

// NewBio001 creates a new BIO_001 Impossible Wrist Rotation detector.
func NewBio001(params map[string]any) *Bio001 {
	d := &Bio001{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "BIO_001",
			DetectorVersion:  "2.1.0",
			DetectorName:     "Impossible Wrist Rotation",
			DetectorCategory: "bio",
			Inputs:           []string{"hand_tracking", "wrist_angular_rate"},
			Warmup:           5,
			Weight:           0.8,
			IsAutoEnforce:    false,
		},
		maxWristAngularVelocity: detect.GetFloat(params, "max_wrist_angular_velocity", 50.0),
		minViolationFrames:      detect.GetInt(params, "min_violation_frames", minSustainedWristFrames),
		sigmoidSteepness:        detect.GetFloat(params, "sigmoid_steepness", 8.5),
		left:                    make(map[string]*streak),
		right:                   make(map[string]*streak),
	}
	d.applyFloor()
	return d
}

func (d *Bio001) applyFloor() {
	if d.minViolationFrames < minSustainedWristFrames {
		d.minViolationFrames = minSustainedWristFrames
	}
}

func (d *Bio001) Reset() {
	d.left = make(map[string]*streak)
	d.right = make(map[string]*streak)
}

func (d *Bio001) Configure(params map[string]any) error {
	d.maxWristAngularVelocity = detect.GetFloat(params, "max_wrist_angular_velocity", d.maxWristAngularVelocity)
	d.minViolationFrames = detect.GetInt(params, "min_violation_frames", d.minViolationFrames)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	d.applyFloor()
	return nil
}

// SaturationRate returns the largest wrist rate expressible at a sampling
// interval dt (pi/dt), or +Inf for dt <= 0.
func (d *Bio001) SaturationRate(dt float64) float64 {
	if dt <= 0 {
		return math.Inf(1)
	}
	return math.Pi / dt
}

// Reachable reports whether the configured threshold can be exceeded at
// all on a source with sampling interval dt.
func (d *Bio001) Reachable(dt float64) bool {
	return d.SaturationRate(dt) > d.maxWristAngularVelocity
}

func (d *Bio001) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID
		// Frames the detector does not evaluate break the run on both
		// hands: a "sustained" violation is one the detector saw on every
		// frame in between. Post-respawn immunity additionally means hand
		// poses jump to the spawn pose, which looks like an instantaneous
		// rotation. Same guards as BIO_002.
		if ps.IsStunned || ps.FrameDt < 0.01 || ps.IsImmune {
			resetHands(d.left, d.right, pid)
			continue
		}

		d.checkHand(matchCtx, ps, pid, "left", ps.LeftWristAngularRate, getStreak(d.left, pid), frameIdx, &events)
		d.checkHand(matchCtx, ps, pid, "right", ps.RightWristAngularRate, getStreak(d.right, pid), frameIdx, &events)
	}

	return events
}

func (d *Bio001) checkHand(
	matchCtx *model.MatchContext,
	ps *model.PlayerState,
	pid, handName string,
	rate float64,
	s *streak,
	frameIdx int,
	events *[]model.DetectionEvent,
) {
	// Per-hand wrist-rate baseline in rad/s, kept by the detector itself so
	// the evidence compares like with like (PlayerState only carries
	// hand-SPEED accumulators in m/s).
	s.stats.Update(rate)
	if rate > s.maxObserved {
		s.maxObserved = rate
	}

	s.advance(frameIdx, rate > d.maxWristAngularVelocity)
	if s.consecutive == 0 {
		return
	}

	if !s.shouldEmit(d.minViolationFrames) {
		return
	}
	s.lastEmitAt = s.consecutive
	consecutive := s.consecutive

	severity := excessSeverity(rate, d.maxWristAngularVelocity, d.sigmoidSteepness)
	confidence := sustainedConfidence(consecutive, d.minViolationFrames)

	ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
		severity, confidence,
		model.WristRotationEvidence{
			Hand:              handName,
			AngularVelocity:   rate,
			ConsecutiveFrames: consecutive,
			RunningMean:       s.stats.Mean,
			RunningStdDev:     s.stats.StdDev(),
			MaxObserved:       s.maxObserved,
			FrameDt:           ps.FrameDt,
			PhysicalLimit:     d.maxWristAngularVelocity,
		},
		fmt.Sprintf("wrist_angular_rate: %.1f rad/s (%s, %d frames)", rate, handName, consecutive),
		fmt.Sprintf("wrist_angular_rate: 0-%.1f rad/s", d.maxWristAngularVelocity),
		model.CausalKey{
			PlayerID:    pid,
			FrameStart:  frameIdx - consecutive + 1,
			FrameEnd:    frameIdx,
			AnomalyType: "wrist_rotation",
		},
	)
	*events = append(*events, ev)
}
