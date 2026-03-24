package throw

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

type Throw005 struct {
	detect.BaseDetector
	maxMeanDev          float64
	maxStddevDev        float64
	minThrows           int
	deviations          map[string][]float64
	firstFrame          map[string]int
	speedDeviationPairs map[string][][2]float64 // per-player (speed, deviation) pairs
}

func NewThrow005(params map[string]any) *Throw005 {
	return &Throw005{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_005", DetectorVersion: "1.0.0",
			DetectorName: "Superhuman Target Precision", DetectorCategory: "throw",
			Inputs: []string{"throw_event"}, Warmup: 5, Weight: 0.7,
		},
		maxMeanDev:          detect.GetFloat(params, "max_mean_deviation", 2.0),
		maxStddevDev:        detect.GetFloat(params, "max_stddev_deviation", 1.5),
		minThrows:           detect.GetInt(params, "min_throws_for_pattern", 8),
		deviations:          make(map[string][]float64),
		firstFrame:          make(map[string]int),
		speedDeviationPairs: make(map[string][][2]float64),
	}
}

func (d *Throw005) Reset() {
	d.deviations = make(map[string][]float64)
	d.firstFrame = make(map[string]int)
	d.speedDeviationPairs = make(map[string][][2]float64)
}
func (d *Throw005) Configure(params map[string]any) error {
	d.maxMeanDev = detect.GetFloat(params, "max_mean_deviation", d.maxMeanDev)
	d.maxStddevDev = detect.GetFloat(params, "max_stddev_deviation", d.maxStddevDev)
	d.minThrows = detect.GetInt(params, "min_throws_for_pattern", d.minThrows)
	return nil
}

func (d *Throw005) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	for _, ps := range players {
		if ps.LastThrow == nil || ps.LastThrow.FrameIndex != frameIdx {
			continue
		}
		t := ps.LastThrow
		if t.TargetPosition == nil || t.TargetDeviation > 30.0 {
			continue
		}
		if _, ok := d.firstFrame[ps.PlayerID]; !ok {
			d.firstFrame[ps.PlayerID] = frameIdx
		}
		d.deviations[ps.PlayerID] = append(d.deviations[ps.PlayerID], t.TargetDeviation)
		if len(d.deviations[ps.PlayerID]) > 50 {
			d.deviations[ps.PlayerID] = d.deviations[ps.PlayerID][len(d.deviations[ps.PlayerID])-50:]
		}
		d.speedDeviationPairs[ps.PlayerID] = append(d.speedDeviationPairs[ps.PlayerID],
			[2]float64{t.ReleaseSpeed, t.TargetDeviation})
		if len(d.speedDeviationPairs[ps.PlayerID]) > 50 {
			d.speedDeviationPairs[ps.PlayerID] = d.speedDeviationPairs[ps.PlayerID][len(d.speedDeviationPairs[ps.PlayerID])-50:]
		}

		// Speed-accuracy correlation check
		// TargetDeviation = angle from goal, higher = less accurate.
		// Human: faster throws = more deviation, so corr(speed, deviation) should be positive.
		// Bot: accuracy independent of speed, so corr near 0 or negative.
		pairs := d.speedDeviationPairs[ps.PlayerID]
		if len(pairs) >= 10 {
			corr := pearsonCorrelation(pairs)
			if corr < 0.1 && !math.IsNaN(corr) {
				corrSeverity := model.Clamp01(0.3 + (0.1-corr)*2.0)
				corrConfidence := corrSeverity * math.Min(1.0, float64(len(pairs))/20.0)
				ev := d.MakeEvent(matchCtx, ps.PlayerID, frameIdx, t.Timestamp, corrSeverity, corrConfidence,
					model.PrecisionEvidence{
						GoalDirectedThrows: len(pairs), MeanDeviation: model.Mean(d.deviations[ps.PlayerID]),
						StddevDeviation: model.StdDev(d.deviations[ps.PlayerID]),
					},
					fmt.Sprintf("speed_accuracy_corr: %.3f over %d throws (expected positive)", corr, len(pairs)),
					"speed_accuracy_corr: > 0.1 (human variance)",
					model.CausalKey{PlayerID: ps.PlayerID, FrameStart: d.firstFrame[ps.PlayerID], FrameEnd: frameIdx, AnomalyType: "speed_accuracy_correlation"},
				)
				events = append(events, ev)
			}
		}

		devs := d.deviations[ps.PlayerID]
		if len(devs) < d.minThrows {
			continue
		}
		meanDev := model.Mean(devs)
		stddevDev := model.StdDev(devs)
		if meanDev >= d.maxMeanDev || stddevDev >= d.maxStddevDev {
			continue
		}
		meanSev := 1.0 - (meanDev / d.maxMeanDev)
		stdSev := 1.0 - (stddevDev / d.maxStddevDev)
		severity := (meanSev + stdSev) / 2.0
		throwFactor := math.Min(1.0, float64(len(devs))/float64(d.minThrows*2))
		confidence := severity * throwFactor

		ev := d.MakeEvent(matchCtx, ps.PlayerID, frameIdx, t.Timestamp, severity, confidence,
			model.PrecisionEvidence{
				GoalDirectedThrows: len(devs), MeanDeviation: meanDev,
				StddevDeviation: stddevDev, MinDeviation: model.MinFloat(devs),
				MaxDeviation: model.MaxFloat(devs), DeviationHistory: devs,
			},
			fmt.Sprintf("mean_target_dev: %.2f deg (stddev: %.2f) over %d throws", meanDev, stddevDev, len(devs)),
			fmt.Sprintf("mean_target_dev: > %.1f deg, stddev: > %.1f deg", d.maxMeanDev, d.maxStddevDev),
			model.CausalKey{PlayerID: ps.PlayerID, FrameStart: d.firstFrame[ps.PlayerID], FrameEnd: frameIdx, AnomalyType: "target_precision"},
		)
		events = append(events, ev)
		d.deviations[ps.PlayerID] = nil
		d.firstFrame[ps.PlayerID] = 0
	}
	return events
}

// pearsonCorrelation computes the Pearson correlation coefficient for (x, y) pairs.
func pearsonCorrelation(pairs [][2]float64) float64 {
	n := float64(len(pairs))
	if n < 2 {
		return 0
	}
	var sumX, sumY, sumXY, sumX2, sumY2 float64
	for _, p := range pairs {
		sumX += p[0]
		sumY += p[1]
		sumXY += p[0] * p[1]
		sumX2 += p[0] * p[0]
		sumY2 += p[1] * p[1]
	}
	num := n*sumXY - sumX*sumY
	den := math.Sqrt((n*sumX2 - sumX*sumX) * (n*sumY2 - sumY*sumY))
	if den < 1e-12 {
		return 0
	}
	return num / den
}
