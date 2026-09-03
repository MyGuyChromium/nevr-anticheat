package bio

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// minSustainedHandFrames is the floor applied to min_violation_frames.
// A single-frame body glitch (interpolation artifact, one bad sample) moves
// the hands out and back, which is exactly two consecutive over-limit hand
// speeds; three consecutive frames (~0.2 s at 15 Hz) is the shortest run
// that cannot be produced by one bad sample. Same floor as BIO_001.
const minSustainedHandFrames = 3

// Bio002 detects impossible controller speeds relative to player translation
// (BIO_002). World-space hand velocity includes the player's own movement; a
// fast legal boost or a movement cheat must not also become independent
// biomechanical evidence merely because the hands moved with the body.
//
// "Consecutive" is by frame index: a stunned, immune, unknown-dt or stale
// frame breaks the run (see streak.advance), so an event's ConsecutiveFrames
// and causal range always describe a contiguous run the detector saw.
type Bio002 struct {
	detect.BaseDetector
	maxHandSpeed       float64
	minViolationFrames int
	sigmoidSteepness   float64 // slope of the severity sigmoid on the speed/threshold ratio

	left  map[string]*streak
	right map[string]*streak
}

// NewBio002 creates a new BIO_002 Impossible Hand Speed detector.
func NewBio002(params map[string]any) *Bio002 {
	d := &Bio002{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "BIO_002",
			DetectorVersion:  "2.2.0",
			DetectorName:     "Impossible Hand Speed",
			DetectorCategory: "bio",
			Inputs:           []string{"hand_tracking"},
			Warmup:           5,
			Weight:           0.8,
			IsAutoEnforce:    false,
		},
		maxHandSpeed:       detect.GetFloat(params, "max_hand_speed", 50.0),
		minViolationFrames: detect.GetInt(params, "min_violation_frames", minSustainedHandFrames),
		sigmoidSteepness:   detect.GetFloat(params, "sigmoid_steepness", 8.5),
		left:               make(map[string]*streak),
		right:              make(map[string]*streak),
	}
	d.applyFloor()
	return d
}

func (d *Bio002) applyFloor() {
	if d.minViolationFrames < minSustainedHandFrames {
		d.minViolationFrames = minSustainedHandFrames
	}
}

func (d *Bio002) Reset() {
	d.left = make(map[string]*streak)
	d.right = make(map[string]*streak)
}

func (d *Bio002) Configure(params map[string]any) error {
	d.maxHandSpeed = detect.GetFloat(params, "max_hand_speed", d.maxHandSpeed)
	d.minViolationFrames = detect.GetInt(params, "min_violation_frames", d.minViolationFrames)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	d.applyFloor()
	return nil
}

func (d *Bio002) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID
		// Frames the detector does not evaluate break the run on both
		// hands. Post-respawn immunity additionally means hand positions
		// jump to the spawn pose.
		if ps.IsStunned || ps.FrameDt < 0.01 || ps.IsImmune {
			resetHands(d.left, d.right, pid)
			continue
		}

		d.checkHand(matchCtx, ps, pid, "left", ps.LeftHandRelativeSpeed, ps.LeftHandSpeed, getStreak(d.left, pid), frameIdx, &events)
		d.checkHand(matchCtx, ps, pid, "right", ps.RightHandRelativeSpeed, ps.RightHandSpeed, getStreak(d.right, pid), frameIdx, &events)
	}

	return events
}

func (d *Bio002) checkHand(
	matchCtx *model.MatchContext,
	ps *model.PlayerState,
	pid, handName string,
	speed, worldSpeed float64,
	s *streak,
	frameIdx int,
	events *[]model.DetectionEvent,
) {
	s.advance(frameIdx, speed > d.maxHandSpeed)
	if s.consecutive == 0 {
		return
	}

	if !s.shouldEmit(d.minViolationFrames) {
		return
	}
	s.lastEmitAt = s.consecutive
	consecutive := s.consecutive

	severity := excessSeverity(speed, d.maxHandSpeed, d.sigmoidSteepness)
	confidence := sustainedConfidence(consecutive, d.minViolationFrames)

	speedRatio := 0.0
	if ps.Speed > 0.01 {
		speedRatio = speed / ps.Speed
	}

	ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
		severity, confidence,
		model.HandSpeedEvidence{
			Hand:              handName,
			Speed:             speed,
			WorldSpeed:        worldSpeed,
			ReferenceFrame:    "player_relative",
			ConsecutiveFrames: consecutive,
			FrameDt:           ps.FrameDt,
			PhysicalLimit:     d.maxHandSpeed,
			PlayerSpeed:       ps.Speed,
			SpeedRatio:        speedRatio,
		},
		fmt.Sprintf("relative_hand_speed: %.1f m/s (%s, world %.1f m/s, %d frames)", speed, handName, worldSpeed, consecutive),
		fmt.Sprintf("relative_hand_speed: 0-%.1f m/s", d.maxHandSpeed),
		model.CausalKey{
			PlayerID:    pid,
			FrameStart:  frameIdx - consecutive + 1,
			FrameEnd:    frameIdx,
			AnomalyType: "hand_speed",
		},
	)
	*events = append(*events, ev)
}
