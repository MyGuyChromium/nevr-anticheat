// Package baseline provides behavioral baseline modeling and threshold tuning.
package baseline

import (
	"context"
	"math"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// BaselineProvider supplies behavioral baselines for comparison.
type BaselineProvider interface {
	GetPopulationBaseline(ctx context.Context, metric string) (*model.Baseline, error)
	GetPlayerBaseline(ctx context.Context, playerID string, metric string) (*model.Baseline, error)
	UpdatePopulationBaseline(ctx context.Context, metric string, observations []float64) error
	UpdatePlayerBaseline(ctx context.Context, playerID, metric string, observations []float64) error
	GetMinBaselineMatches() int
}

// ComputeBaseline computes a Baseline from raw observations.
func ComputeBaseline(metric string, observations []float64) *model.Baseline {
	if len(observations) == 0 {
		return nil
	}
	sorted := make([]float64, len(observations))
	copy(sorted, observations)
	sort.Float64s(sorted)

	n := float64(len(sorted))
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	mean := sum / n

	sumSq := 0.0
	for _, v := range sorted {
		d := v - mean
		sumSq += d * d
	}
	stddev := math.Sqrt(sumSq / n)

	return &model.Baseline{
		Metric:      metric,
		Mean:        mean,
		StdDev:      stddev,
		P50:         model.Percentile(sorted, 0.50),
		P90:         model.Percentile(sorted, 0.90),
		P95:         model.Percentile(sorted, 0.95),
		P99:         model.Percentile(sorted, 0.99),
		P999:        model.Percentile(sorted, 0.999),
		Min:         sorted[0],
		Max:         sorted[len(sorted)-1],
		SampleCount: int64(len(sorted)),
	}
}

// Winsorize clamps extreme values at specified percentiles.
func Winsorize(data []float64, lowerPct, upperPct float64) []float64 {
	if len(data) == 0 {
		return data
	}
	sorted := make([]float64, len(data))
	copy(sorted, data)
	sort.Float64s(sorted)

	n := float64(len(sorted))
	loIdx := int(math.Floor(lowerPct * n))
	hiIdx := int(math.Floor(upperPct * n))
	if hiIdx >= len(sorted) {
		hiIdx = len(sorted) - 1
	}
	loVal := sorted[loIdx]
	hiVal := sorted[hiIdx]

	result := make([]float64, len(data))
	for i, v := range data {
		if v < loVal {
			result[i] = loVal
		} else if v > hiVal {
			result[i] = hiVal
		} else {
			result[i] = v
		}
	}
	return result
}
