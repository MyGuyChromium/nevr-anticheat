package bio

import (
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// excessSeverity maps a measurement that exceeds a physical threshold to a
// severity in (0, 1). It is a sigmoid on the RATIO value/threshold centred
// at 1.2x, so that (with the default steepness of 8.5) 10% over the limit
// reads ~0.3, 20% over reads 0.5 and 50% over reads ~0.93. Centring on the
// ratio rather than on 1.5x the threshold in absolute units keeps the band
// just above the limit from collapsing to ~0 severity.
func excessSeverity(value, threshold, steepness float64) float64 {
	if threshold <= 0 {
		return 0
	}
	return model.SigmoidConfidence(value/threshold, 1.2, steepness)
}

// sustainedConfidence grows with the number of consecutive violation
// frames: 0.45 at the minimum streak, ~0.79 two frames later, ~0.89 four
// frames later. The 0.9 ceiling leaves room for corroboration from other
// detectors (PAT_004 gates on >= 0.7, auto-enforce on >= 0.95).
func sustainedConfidence(consecutive, minFrames int) float64 {
	return model.Clamp01(model.SigmoidConfidence(float64(consecutive), float64(minFrames), 1.0) * 0.9)
}

// logRatioSeverity ranks how far BELOW a human-baseline threshold a
// variance is: one decade below (10x smaller) scores 1/decades, so with
// decades = 2 a window 10x below threshold is 0.5 and 100x below is 1.0. A
// window barely under threshold scores ~0.
func logRatioSeverity(threshold, variance, decades float64) float64 {
	if variance <= 0 || threshold <= 0 || decades <= 0 {
		return 0
	}
	return model.Clamp01(math.Log10(threshold/variance) / decades)
}

// streak tracks one hand's run of consecutive violation frames and when it
// last produced an event, so confidence can keep growing while the run
// continues instead of being zeroed at every emission.
//
// "Consecutive" means consecutive FRAME INDICES: the run is broken by any
// frame the detector did not count for this hand — a stunned or immune
// frame, a frame with unknown dt, or a frame on which the player was stale
// (rejected by the validator, absent from the tick). Without that, three
// isolated samples seconds apart would be reported as one sustained run
// whose causal range points at frames that never violated.
type streak struct {
	consecutive int
	lastEmitAt  int // consecutive count at the last emission; 0 = none yet
	lastFrame   int // frame index of the last counted sample (valid when consecutive > 0)
	stats       model.WelfordAccumulator
	maxObserved float64
}

// advance counts one evaluated sample at frameIdx. A run that does not
// continue on frameIdx == lastFrame+1 is restarted, so a violating sample
// after a skipped frame starts a fresh run of length 1.
func (s *streak) advance(frameIdx int, violating bool) {
	if s.consecutive > 0 && frameIdx != s.lastFrame+1 {
		s.reset()
	}
	if violating {
		s.consecutive++
		s.lastFrame = frameIdx
	} else {
		s.reset()
	}
}

// shouldEmit reports whether the streak has reached the minimum length and
// either has never emitted or has grown by another minFrames since the
// last emission (per-hand emission cooldown).
func (s *streak) shouldEmit(minFrames int) bool {
	if s.consecutive < minFrames {
		return false
	}
	if s.lastEmitAt == 0 {
		return true
	}
	return s.consecutive-s.lastEmitAt >= minFrames
}

func (s *streak) reset() {
	s.consecutive = 0
	s.lastEmitAt = 0
}

// resetHands breaks both hands' runs for a player on a frame the detector
// does not evaluate (stunned, immune, unknown dt).
func resetHands(left, right map[string]*streak, pid string) {
	getStreak(left, pid).reset()
	getStreak(right, pid).reset()
}

func getStreak(m map[string]*streak, pid string) *streak {
	s, ok := m[pid]
	if !ok {
		s = &streak{}
		m[pid] = s
	}
	return s
}

// pushBoolHistory appends and trims a bool window.
func pushBoolHistory(hist *[]bool, v bool, maxSize int) {
	*hist = append(*hist, v)
	if len(*hist) > maxSize {
		copy(*hist, (*hist)[len(*hist)-maxSize:])
		*hist = (*hist)[:maxSize]
	}
}

func countTrue(hist []bool) int {
	n := 0
	for _, b := range hist {
		if b {
			n++
		}
	}
	return n
}

// windowRun counts consecutive zero-jitter windows for one hand. Windows are
// consecutive only when the next full window ends exactly windowFrames after
// the previous counted one: any interruption — the activity gate closing,
// a tracking-loss drop, a stale frame — leaves a longer distance and the
// count restarts, so a coasting window at t=0 and another minutes later can
// never add up to "multiple consecutive windows".
type windowRun struct {
	count   int
	lastEnd int // frame index at which the last counted window ended
	counted bool
}

// add registers a full window ending at frameIdx and returns the run
// length; zero says whether this window was under the jitter threshold.
func (r *windowRun) add(frameIdx, windowFrames int, zero bool) int {
	if r.counted && frameIdx-r.lastEnd != windowFrames {
		r.count = 0
	}
	r.counted = true
	r.lastEnd = frameIdx
	if zero {
		r.count++
	} else {
		r.count = 0
	}
	return r.count
}

func (r *windowRun) reset() {
	r.count = 0
	r.counted = false
}

func getRun(m map[string]*windowRun, pid string) *windowRun {
	r, ok := m[pid]
	if !ok {
		r = &windowRun{}
		m[pid] = r
	}
	return r
}
