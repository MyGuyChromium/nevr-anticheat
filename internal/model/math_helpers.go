package model

import (
	"math"
	"sort"
)

// SigmoidConfidence maps a value to [0,1] using a sigmoid centered at threshold.
func SigmoidConfidence(value, threshold, steepness float64) float64 {
	return 1.0 / (1.0 + math.Exp(-steepness*(value-threshold)))
}

// Clamp01 clamps v to [0, 1].
func Clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Clamp clamps v to [lo, hi].
func Clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Mean computes the arithmetic mean of a float64 slice.
func Mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

// StdDev computes the population standard deviation.
func StdDev(vals []float64) float64 {
	if len(vals) < 2 {
		return 0
	}
	m := Mean(vals)
	sum := 0.0
	for _, v := range vals {
		d := v - m
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(vals)))
}

// Variance computes the population variance.
func Variance(vals []float64) float64 {
	if len(vals) < 2 {
		return 0
	}
	m := Mean(vals)
	sum := 0.0
	for _, v := range vals {
		d := v - m
		sum += d * d
	}
	return sum / float64(len(vals))
}

// Median computes the median of a float64 slice (sorts a copy).
func Median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 0 {
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return sorted[n/2]
}

// MinFloat returns the minimum value in the slice.
func MinFloat(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

// MaxFloat returns the maximum value in the slice.
func MaxFloat(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	m := vals[0]
	for _, v := range vals[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

// DegToRad converts degrees to radians.
func DegToRad(deg float64) float64 {
	return deg * math.Pi / 180.0
}

// RadToDeg converts radians to degrees.
func RadToDeg(rad float64) float64 {
	return rad * 180.0 / math.Pi
}

// Percentile computes the p-th percentile (0-1) from a sorted slice.
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := p * float64(len(sorted)-1)
	lo := int(math.Floor(idx))
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[lo]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// CoefficientOfVariation computes stddev/mean.
func CoefficientOfVariation(vals []float64) float64 {
	m := Mean(vals)
	if math.Abs(m) < 1e-12 {
		return 0
	}
	return StdDev(vals) / math.Abs(m)
}

// MaxF returns the larger of a and b.
func MaxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// MinF returns the smaller of a and b.
func MinF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
