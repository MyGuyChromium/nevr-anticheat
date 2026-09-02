// Package baseline provides behavioral baseline modeling and threshold tuning.
//
// STATUS: EXPERIMENTAL / NOT IMPLEMENTED END-TO-END. BaselineProvider has no
// implementation in this repository: the population_baselines and
// player_profiles tables are never written, the config Baseline.* keys are
// not consumed, and DetectionEvent.BaselineComparison is never populated.
// Only the pure statistics helpers (ComputeBaseline, Winsorize) are live and
// tested. Do not describe the system as producing baseline-relative
// evidence until a provider exists and is wired into the pipeline.
package baseline

import (
	"context"
	"math"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// BaselineProvider supplies behavioral baselines for comparison.
// There is currently no implementation; see the package doc.
type BaselineProvider interface {
	GetPopulationBaseline(ctx context.Context, metric string) (*model.Baseline, error)
	GetPlayerBaseline(ctx context.Context, playerID string, metric string) (*model.Baseline, error)
	UpdatePopulationBaseline(ctx context.Context, metric string, observations []float64) error
	UpdatePlayerBaseline(ctx context.Context, playerID, metric string, observations []float64) error
	GetMinBaselineMatches() int
}

// ComputeBaseline computes a Baseline from raw observations. NaN and Inf
// observations are ignored; nil is returned when nothing usable remains.
func ComputeBaseline(metric string, observations []float64) *model.Baseline {
	sorted := make([]float64, 0, len(observations))
	for _, v := range observations {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		sorted = append(sorted, v)
	}
	if len(sorted) == 0 {
		return nil
	}
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

// Winsorize clamps extreme values at the given percentiles (fractions in
// [0,1]). Percentiles outside that range are clamped, and a lower percentile
// above the upper one is swapped, so the function never panics.
func Winsorize(data []float64, lowerPct, upperPct float64) []float64 {
	if len(data) == 0 {
		return data
	}
	if math.IsNaN(lowerPct) {
		lowerPct = 0
	}
	if math.IsNaN(upperPct) {
		upperPct = 1
	}
	lowerPct = math.Max(0, math.Min(1, lowerPct))
	upperPct = math.Max(0, math.Min(1, upperPct))
	if lowerPct > upperPct {
		lowerPct, upperPct = upperPct, lowerPct
	}

	sorted := make([]float64, len(data))
	copy(sorted, data)
	sort.Float64s(sorted)

	n := len(sorted)
	loIdx := clampIndex(int(math.Floor(lowerPct*float64(n))), n)
	hiIdx := clampIndex(int(math.Floor(upperPct*float64(n))), n)
	loVal := sorted[loIdx]
	hiVal := sorted[hiIdx]

	result := make([]float64, len(data))
	for i, v := range data {
		switch {
		case v < loVal:
			result[i] = loVal
		case v > hiVal:
			result[i] = hiVal
		default:
			result[i] = v
		}
	}
	return result
}

func clampIndex(i, n int) int {
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}
