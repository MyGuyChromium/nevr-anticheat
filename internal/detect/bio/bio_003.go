package bio

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Bio003 detects zero hand jitter, indicating non-human input (BIO_003).
//
// The activity gate is WINDOWED: a frame counts as active when the player is
// unstunned and moving faster than 1 m/s, and the window under evaluation
// must contain at least min_active_frames such frames. (A cumulative
// counter would let 59 active frames followed by hundreds of idle ones
// arm the gate for a window made entirely of resting controllers.)
type Bio003 struct {
	detect.BaseDetector
	jitterWindowFrames    int
	maxJitterVariance     float64
	minActiveFrames       int
	severityDecades       float64
	minConsecutiveWindows int

	// Per-player hand position history relative to body
	leftHandRelHistory   map[string][]model.Vec3
	rightHandRelHistory  map[string][]model.Vec3
	activeHistory        map[string][]bool
	consecutiveZeroLeft  map[string]*windowRun
	consecutiveZeroRight map[string]*windowRun
}

// NewBio003 creates a new BIO_003 Zero Hand Jitter detector.
func NewBio003(params map[string]any) *Bio003 {
	d := &Bio003{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "BIO_003",
			DetectorVersion:  "2.1.0",
			DetectorName:     "Zero Hand Jitter",
			DetectorCategory: "bio",
			Inputs:           []string{"hand_tracking", "position"},
			Warmup:           90,
			Weight:           0.7,
			IsAutoEnforce:    false,
		},
		jitterWindowFrames:    detect.GetInt(params, "jitter_window_frames", 90),
		maxJitterVariance:     detect.GetFloat(params, "max_jitter_variance", 0.00001),
		minActiveFrames:       detect.GetInt(params, "min_active_frames", 60),
		severityDecades:       detect.GetFloat(params, "severity_decades", 2.0),
		minConsecutiveWindows: detect.GetInt(params, "min_consecutive_windows", 2),
		leftHandRelHistory:    make(map[string][]model.Vec3),
		rightHandRelHistory:   make(map[string][]model.Vec3),
		activeHistory:         make(map[string][]bool),
		consecutiveZeroLeft:   make(map[string]*windowRun),
		consecutiveZeroRight:  make(map[string]*windowRun),
	}
	d.sanitize()
	return d
}

func (d *Bio003) sanitize() {
	if d.jitterWindowFrames < 2 {
		d.jitterWindowFrames = 2
	}
	if d.minActiveFrames > d.jitterWindowFrames {
		d.minActiveFrames = d.jitterWindowFrames
	}
	if d.minConsecutiveWindows < 1 {
		d.minConsecutiveWindows = 1
	}
}

func (d *Bio003) Reset() {
	d.leftHandRelHistory = make(map[string][]model.Vec3)
	d.rightHandRelHistory = make(map[string][]model.Vec3)
	d.activeHistory = make(map[string][]bool)
	d.consecutiveZeroLeft = make(map[string]*windowRun)
	d.consecutiveZeroRight = make(map[string]*windowRun)
}

func (d *Bio003) Configure(params map[string]any) error {
	d.jitterWindowFrames = detect.GetInt(params, "jitter_window_frames", d.jitterWindowFrames)
	d.maxJitterVariance = detect.GetFloat(params, "max_jitter_variance", d.maxJitterVariance)
	d.minActiveFrames = detect.GetInt(params, "min_active_frames", d.minActiveFrames)
	d.severityDecades = detect.GetFloat(params, "severity_decades", d.severityDecades)
	d.minConsecutiveWindows = detect.GetInt(params, "min_consecutive_windows", d.minConsecutiveWindows)
	d.sanitize()
	return nil
}

func (d *Bio003) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID

		// A zero hand vector is tracking loss, not a hand at the arena
		// origin: drop that hand's window rather than measuring it.
		if ps.LeftHand.IsZero() {
			d.dropHand(pid, "left")
		} else {
			leftHist := d.leftHandRelHistory[pid]
			model.PushVec3History(&leftHist, ps.LeftHand.Sub(ps.Position), d.jitterWindowFrames)
			d.leftHandRelHistory[pid] = leftHist
		}
		if ps.RightHand.IsZero() {
			d.dropHand(pid, "right")
		} else {
			rightHist := d.rightHandRelHistory[pid]
			model.PushVec3History(&rightHist, ps.RightHand.Sub(ps.Position), d.jitterWindowFrames)
			d.rightHandRelHistory[pid] = rightHist
		}

		// Require meaningful movement — players floating or drifting slowly
		// naturally have near-zero hand jitter relative to body.
		active := !ps.IsStunned && ps.Speed > 1.0
		actHist := d.activeHistory[pid]
		pushBoolHistory(&actHist, active, d.jitterWindowFrames)
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
			history []model.Vec3
		}{
			{"left", d.leftHandRelHistory[pid]},
			{"right", d.rightHandRelHistory[pid]},
		}

		for _, h := range hands {
			if len(h.history) < d.jitterWindowFrames {
				continue
			}

			variance := model.ComputePositionVariance(h.history)

			// Frozen data guard: exactly 0.0 variance means hand tracking data
			// is not updating (replay artifact / tracking loss), not bot input.
			// A real bot would still have floating-point noise > 0.
			if variance < 1e-10 {
				d.clearHand(pid, h.name)
				continue
			}

			// Track consecutive windows of zero jitter per hand.
			// A single zero-jitter window is normal (coasting). Multiple
			// consecutive windows indicate synthetic/bot input. windowRun
			// restarts the count unless this window ends exactly one
			// window after the last counted one (see common.go).
			run := getRun(d.consecutiveZeroLeft, pid)
			if h.name == "right" {
				run = getRun(d.consecutiveZeroRight, pid)
			}

			if run.add(frameIdx, d.jitterWindowFrames, variance < d.maxJitterVariance) < d.minConsecutiveWindows {
				// Start a fresh window for this hand, but don't fire
				d.clearHand(pid, h.name)
				continue
			}

			// Multiple consecutive zero-jitter windows — flag it
			severity := logRatioSeverity(d.maxJitterVariance, variance, d.severityDecades)
			confidence := model.Clamp01(severity * 0.85)

			ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
				severity, confidence,
				model.ZeroJitterEvidence{
					Hand:             h.name,
					PositionVariance: variance,
					WindowFrames:     d.jitterWindowFrames,
					ActiveFrames:     activeInWindow,
					Threshold:        d.maxJitterVariance,
					PlayerSpeed:      ps.Speed,
				},
				fmt.Sprintf("hand_jitter_variance: %.8f (%s)", variance, h.name),
				fmt.Sprintf("hand_jitter_variance: >%.6f", d.maxJitterVariance),
				model.CausalKey{
					PlayerID:    pid,
					FrameStart:  frameIdx - d.jitterWindowFrames*d.minConsecutiveWindows,
					FrameEnd:    frameIdx,
					AnomalyType: "zero_jitter",
				},
			)
			events = append(events, ev)

			// Reset after firing
			run.reset()
			d.clearHand(pid, h.name)
		}
	}

	return events
}

// clearHand starts a fresh window for one hand (the run continues if the
// next full window ends exactly one window later).
func (d *Bio003) clearHand(pid, hand string) {
	if hand == "left" {
		d.leftHandRelHistory[pid] = nil
	} else {
		d.rightHandRelHistory[pid] = nil
	}
}

// dropHand discards one hand's window for tracking loss and breaks its run.
func (d *Bio003) dropHand(pid, hand string) {
	d.clearHand(pid, hand)
	if hand == "left" {
		getRun(d.consecutiveZeroLeft, pid).reset()
	} else {
		getRun(d.consecutiveZeroRight, pid).reset()
	}
}
