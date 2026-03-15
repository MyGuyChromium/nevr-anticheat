package bio

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Bio002 detects impossible hand speeds (BIO_002).
type Bio002 struct {
	detect.BaseDetector
	maxHandSpeed       float64
	minViolationFrames int
	sigmoidSteepness   float64

	leftViolations  map[string]int
	rightViolations map[string]int
}

// NewBio002 creates a new BIO_002 Impossible Hand Speed detector.
func NewBio002(params map[string]any) *Bio002 {
	d := &Bio002{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "BIO_002",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Impossible Hand Speed",
			DetectorCategory: "bio",
			Inputs:           []string{"hand_tracking"},
			Warmup:           5,
			Weight:           0.8,
			IsAutoEnforce:    false,
		},
		maxHandSpeed:          detect.GetFloat(params, "max_hand_speed", 15.0),
		minViolationFrames:    detect.GetInt(params, "min_violation_frames", 2),
		sigmoidSteepness:      detect.GetFloat(params, "sigmoid_steepness", 0.5),
		leftViolations:  make(map[string]int),
		rightViolations: make(map[string]int),
	}
	return d
}

func (d *Bio002) Reset() {
	d.leftViolations = make(map[string]int)
	d.rightViolations = make(map[string]int)
}

func (d *Bio002) Configure(params map[string]any) error {
	d.maxHandSpeed = detect.GetFloat(params, "max_hand_speed", d.maxHandSpeed)
	d.minViolationFrames = detect.GetInt(params, "min_violation_frames", d.minViolationFrames)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Bio002) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		if ps.IsStunned {
			continue
		}
		if ps.FrameDt < 0.01 {
			continue
		}

		pid := ps.PlayerID
		d.checkHand(matchCtx, ps, pid, "left", ps.LeftHandSpeed, d.leftViolations, frameIdx, &events)
		d.checkHand(matchCtx, ps, pid, "right", ps.RightHandSpeed, d.rightViolations, frameIdx, &events)
	}

	return events
}

func (d *Bio002) checkHand(
	matchCtx *model.MatchContext,
	ps *model.PlayerState,
	pid, handName string,
	speed float64,
	violations map[string]int,
	frameIdx int,
	events *[]model.DetectionEvent,
) {
	if speed > d.maxHandSpeed {
		violations[pid]++
	} else {
		violations[pid] = 0
		return
	}

	consecutive := violations[pid]
	if consecutive < d.minViolationFrames {
		return
	}

	severity := model.SigmoidConfidence(speed, d.maxHandSpeed*1.5, d.sigmoidSteepness)
	confidence := model.SigmoidConfidence(float64(consecutive), float64(d.minViolationFrames), 1.0)
	confidence = model.Clamp01(confidence * 0.9)

	speedRatio := 0.0
	if ps.Speed > 0.01 {
		speedRatio = speed / ps.Speed
	}

	ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
		severity, confidence,
		model.HandSpeedEvidence{
			Hand:              handName,
			Speed:             speed,
			ConsecutiveFrames: consecutive,
			FrameDt:           ps.FrameDt,
			PhysicalLimit:     d.maxHandSpeed,
			PlayerSpeed:       ps.Speed,
			SpeedRatio:        speedRatio,
		},
		fmt.Sprintf("hand_speed: %.1f m/s (%s, %d frames)", speed, handName, consecutive),
		fmt.Sprintf("hand_speed: 0-%.1f m/s", d.maxHandSpeed),
		model.CausalKey{
			PlayerID:    pid,
			FrameStart:  frameIdx - consecutive,
			FrameEnd:    frameIdx,
			AnomalyType: "hand_speed",
		},
	)
	*events = append(*events, ev)

	violations[pid] = 0
}
