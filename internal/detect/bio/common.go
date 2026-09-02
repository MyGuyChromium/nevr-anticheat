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
type streak struct {
	consecutive int
	lastEmitAt  int // consecutive count at the last emission; 0 = none yet
	stats       model.WelfordAccumulator
	maxObserved float64
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
