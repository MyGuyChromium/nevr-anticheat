package scoring

import "math"

// ExponentialDecay computes decayed score: score * 0.5^(elapsed/halfLife).
// Used by cross-match aggregation (sqlite.ComputePlayerCrossMatchSummary),
// which is the decay path moderators actually see.
func ExponentialDecay(score, elapsedHours, halfLifeHours float64) float64 {
	if halfLifeHours <= 0 || elapsedHours <= 0 {
		return score
	}
	return score * math.Pow(0.5, elapsedHours/halfLifeHours)
}
