package scoring

import "math"

// ExponentialDecay computes decayed score: score * 0.5^(elapsed/halfLife).
func ExponentialDecay(score, elapsedHours, halfLifeHours float64) float64 {
	if halfLifeHours <= 0 || elapsedHours <= 0 {
		return score
	}
	return score * math.Pow(0.5, elapsedHours/halfLifeHours)
}

// InMatchDecay reduces score by a fixed rate per minute of clean play.
func InMatchDecay(score, cleanMinutes, decayRate float64) float64 {
	reduction := cleanMinutes * decayRate
	result := score - reduction
	if result < 0 {
		return 0
	}
	return result
}
