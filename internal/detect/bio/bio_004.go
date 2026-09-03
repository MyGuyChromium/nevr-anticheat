package bio

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Bio004 detects zero aim wobble, indicating non-human aiming (BIO_004).
// The activity gate is windowed exactly as in BIO_003.
type Bio004 struct {
	detect.BaseDetector
	wobbleWindowFrames    int
	maxWobbleVariance     float64
	minActiveFrames       int
	severityDecades       float64
	minConsecutiveWindows int

	leftHandRotHistory   map[string][]model.Quat
	rightHandRotHistory  map[string][]model.Quat
	activeHistory        map[string][]bool
	consecutiveZeroLeft  map[string]*windowRun
	consecutiveZeroRight map[string]*windowRun
}

// NewBio004 creates a new BIO_004 Zero Aim Wobble detector.
func NewBio004(params map[string]any) *Bio004 {
	d := &Bio004{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "BIO_004",
			DetectorVersion:  "2.1.0",
			DetectorName:     "Zero Aim Wobble",
			DetectorCategory: "bio",
			Inputs:           []string{"hand_tracking", "hand_rotation"},
			Warmup:           90,
			Weight:           0.7,
			IsAutoEnforce:    false,
		},
		wobbleWindowFrames:    detect.GetInt(params, "wobble_window_frames", 90),
		maxWobbleVariance:     detect.GetFloat(params, "max_wobble_variance", 0.00005),
		minActiveFrames:       detect.GetInt(params, "min_active_frames", 60),
		severityDecades:       detect.GetFloat(params, "severity_decades", 2.0),
		minConsecutiveWindows: detect.GetInt(params, "min_consecutive_windows", 2),
		leftHandRotHistory:    make(map[string][]model.Quat),
		rightHandRotHistory:   make(map[string][]model.Quat),
		activeHistory:         make(map[string][]bool),
		consecutiveZeroLeft:   make(map[string]*windowRun),
		consecutiveZeroRight:  make(map[string]*windowRun),
	}
	d.sanitize()
	return d
}

func (d *Bio004) sanitize() {
	if d.wobbleWindowFrames < 2 {
		d.wobbleWindowFrames = 2
	}
	if d.minActiveFrames > d.wobbleWindowFrames {
		d.minActiveFrames = d.wobbleWindowFrames
	}
	if d.minConsecutiveWindows < 1 {
		d.minConsecutiveWindows = 1
	}
}

func (d *Bio004) Reset() {
	d.leftHandRotHistory = make(map[string][]model.Quat)
	d.rightHandRotHistory = make(map[string][]model.Quat)
	d.activeHistory = make(map[string][]bool)
	d.consecutiveZeroLeft = make(map[string]*windowRun)
	d.consecutiveZeroRight = make(map[string]*windowRun)
}

func (d *Bio004) Configure(params map[string]any) error {
	d.wobbleWindowFrames = detect.GetInt(params, "wobble_window_frames", d.wobbleWindowFrames)
	d.maxWobbleVariance = detect.GetFloat(params, "max_wobble_variance", d.maxWobbleVariance)
	d.minActiveFrames = detect.GetInt(params, "min_active_frames", d.minActiveFrames)
	d.severityDecades = detect.GetFloat(params, "severity_decades", d.severityDecades)
	d.minConsecutiveWindows = detect.GetInt(params, "min_consecutive_windows", d.minConsecutiveWindows)
	d.sanitize()
	return nil
}

func (d *Bio004) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID

		// A non-unit hand rotation means no rotation data for that hand
		// this frame; break that hand's window instead of measuring it.
		if !ps.LeftHandRot.IsUnit() {
			d.dropHand(pid, "left")
		} else {
			leftHist := d.leftHandRotHistory[pid]
			model.PushQuatHistory(&leftHist, ps.LeftHandRot, d.wobbleWindowFrames)
			d.leftHandRotHistory[pid] = leftHist
		}
		if !ps.RightHandRot.IsUnit() {
			d.dropHand(pid, "right")
		} else {
			rightHist := d.rightHandRotHistory[pid]
			model.PushQuatHistory(&rightHist, ps.RightHandRot, d.wobbleWindowFrames)
			d.rightHandRotHistory[pid] = rightHist
		}

		// Require meaningful movement — players floating or drifting slowly
		// naturally have near-zero aim wobble.
		active := !ps.IsStunned && ps.Speed > 1.0
		actHist := d.activeHistory[pid]
		pushBoolHistory(&actHist, active, d.wobbleWindowFrames)
		d.activeHistory[pid] = actHist
		activeInWindow := countTrue(actHist)
		if activeInWindow < d.minActiveFrames {
			// Gate closed: nothing is measured, so no window counted while
			// it stays closed can be "consecutive" with an earlier one.
			getRun(d.consecutiveZeroLeft, pid).reset()
			getRun(d.consecutiveZeroRight, pid).reset()
			continue
		}

		hands := []struct {
			name    string
			history []model.Quat
		}{
			{"left", d.leftHandRotHistory[pid]},
			{"right", d.rightHandRotHistory[pid]},
		}

		for _, h := range hands {
			if len(h.history) < d.wobbleWindowFrames {
				continue
			}

			variance := model.ComputeRotationVariance(h.history)

			// Frozen data guard: exactly 0.0 variance means rotation data
			// is not updating (replay artifact / tracking loss), not bot input.
			if variance < 1e-10 {
				d.clearHand(pid, h.name)
				continue
			}

			// Consecutive zero-wobble windows per hand; windowRun restarts
			// the count unless this window ends exactly one window after
			// the last counted one (see common.go).
			run := getRun(d.consecutiveZeroLeft, pid)
			if h.name == "right" {
				run = getRun(d.consecutiveZeroRight, pid)
			}

			if run.add(frameIdx, d.wobbleWindowFrames, variance < d.maxWobbleVariance) < d.minConsecutiveWindows {
				d.clearHand(pid, h.name)
				continue
			}

			severity := logRatioSeverity(d.maxWobbleVariance, variance, d.severityDecades)
			confidence := model.Clamp01(severity * 0.85)
			stddevDeg := model.RadToDeg(math.Sqrt(variance))

			ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
				severity, confidence,
				model.ZeroWobbleEvidence{
					Hand:             h.name,
					RotationVariance: variance,
					WindowFrames:     d.wobbleWindowFrames,
					Threshold:        d.maxWobbleVariance,
					PlayerSpeed:      ps.Speed,
					StdDevDegrees:    stddevDeg,
				},
				fmt.Sprintf("aim_wobble_variance: %.8f (%s)", variance, h.name),
				fmt.Sprintf("aim_wobble_variance: >%.6f", d.maxWobbleVariance),
				model.CausalKey{
					PlayerID:    pid,
					FrameStart:  frameIdx - d.wobbleWindowFrames*d.minConsecutiveWindows,
					FrameEnd:    frameIdx,
					AnomalyType: "zero_wobble",
				},
			)
			events = append(events, ev)

			run.reset()
			d.clearHand(pid, h.name)
		}
	}

	return events
}

func (d *Bio004) clearHand(pid, hand string) {
	if hand == "left" {
		d.leftHandRotHistory[pid] = nil
	} else {
		d.rightHandRotHistory[pid] = nil
	}
}

// dropHand discards one hand's window for missing rotation data and breaks
// its run.
func (d *Bio004) dropHand(pid, hand string) {
	d.clearHand(pid, hand)
	if hand == "left" {
		getRun(d.consecutiveZeroLeft, pid).reset()
	} else {
		getRun(d.consecutiveZeroRight, pid).reset()
	}
}
