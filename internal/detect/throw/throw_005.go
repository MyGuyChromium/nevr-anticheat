package throw

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	// Speed-accuracy correlation gate. The premise (human throws deviate
	// more the faster they are) is UNVALIDATED on Echo data, so the gate is
	// deliberately conservative: at least corrMinPairs goal-directed throws
	// and the upper bound of the 95% Fisher-z confidence interval of the
	// Pearson correlation must lie below corrMaxUpperCI.
	corrMinPairs    = 30
	corrMaxUpperCI  = 0.1
	corrHistory     = 50
	corrZ95         = 1.959964
	deviationsLimit = 50
)

// Throw005 detects superhuman target precision (THROW_005).
type Throw005 struct {
	detect.BaseDetector
	maxMeanDev   float64
	maxStddevDev float64
	minThrows    int

	deviations          map[string][]float64
	firstFrame          map[string]int
	speedDeviationPairs map[string][][2]float64 // per-player (speed, deviation) pairs
	pairsFirstFrame     map[string]int
}

func NewThrow005(params map[string]any) *Throw005 {
	d := &Throw005{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_005", DetectorVersion: "1.1.0",
			DetectorName: "Superhuman Target Precision", DetectorCategory: "throw",
			Inputs: []string{"throw_event"}, Warmup: 5, Weight: 0.7,
		},
		maxMeanDev:   detect.GetFloat(params, "max_mean_deviation", 2.0),
		maxStddevDev: detect.GetFloat(params, "max_stddev_deviation", 1.5),
		minThrows:    detect.GetInt(params, "min_throws_for_pattern", 8),
	}
	d.Reset()
	return d
}

func (d *Throw005) Reset() {
	d.deviations = make(map[string][]float64)
	d.firstFrame = make(map[string]int)
	d.speedDeviationPairs = make(map[string][][2]float64)
	d.pairsFirstFrame = make(map[string]int)
}
func (d *Throw005) Configure(params map[string]any) error {
	d.maxMeanDev = detect.GetFloat(params, "max_mean_deviation", d.maxMeanDev)
	d.maxStddevDev = detect.GetFloat(params, "max_stddev_deviation", d.maxStddevDev)
	d.minThrows = detect.GetInt(params, "min_throws_for_pattern", d.minThrows)
	return nil
}

func (d *Throw005) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	for _, pid := range sortedPlayerIDs(players) {
		ps := players[pid]
		t := throwAt(ps, frameIdx)
		if t == nil {
			continue
		}
		if t.TargetPosition == nil || t.TargetDeviation > 30.0 || math.IsNaN(t.TargetDeviation) {
			continue
		}
		if _, ok := d.firstFrame[pid]; !ok {
			d.firstFrame[pid] = frameIdx
		}
		if _, ok := d.pairsFirstFrame[pid]; !ok {
			d.pairsFirstFrame[pid] = frameIdx
		}
		d.deviations[pid] = append(d.deviations[pid], t.TargetDeviation)
		if len(d.deviations[pid]) > deviationsLimit {
			d.deviations[pid] = d.deviations[pid][len(d.deviations[pid])-deviationsLimit:]
		}
		d.speedDeviationPairs[pid] = append(d.speedDeviationPairs[pid], [2]float64{t.ReleaseSpeed, t.TargetDeviation})
		if len(d.speedDeviationPairs[pid]) > corrHistory {
			d.speedDeviationPairs[pid] = d.speedDeviationPairs[pid][len(d.speedDeviationPairs[pid])-corrHistory:]
		}

		// Speed-accuracy correlation check.
		// TargetDeviation = angle from goal, higher = less accurate.
		// Human: faster throws = more deviation, so corr(speed, deviation)
		// should be positive. Bot: accuracy independent of speed. Fires at
		// most once per window; the window resets after firing.
		pairs := d.speedDeviationPairs[pid]
		if len(pairs) >= corrMinPairs {
			if corr, upper, ok := correlationUpperCI(pairs); ok && upper < corrMaxUpperCI {
				corrSeverity := model.Clamp01(0.3 + (corrMaxUpperCI-corr)*2.0)
				corrConfidence := corrSeverity * math.Min(1.0, float64(len(pairs))/float64(corrHistory))
				ev := d.MakeEvent(matchCtx, pid, frameIdx, t.Timestamp, corrSeverity, corrConfidence,
					model.PrecisionEvidence{
						GoalDirectedThrows: len(pairs), MeanDeviation: model.Mean(d.deviations[pid]),
						StddevDeviation: model.StdDev(d.deviations[pid]),
						PairCount:       len(pairs), SpeedAccuracyCorrelation: corr, CorrelationUpperCI: upper,
					},
					fmt.Sprintf("speed_accuracy_corr: %.3f (95%% CI upper %.3f) over %d throws (expected positive)", corr, upper, len(pairs)),
					fmt.Sprintf("speed_accuracy_corr: 95%% CI upper > %.1f (human variance)", corrMaxUpperCI),
					model.CausalKey{PlayerID: pid, FrameStart: d.pairsFirstFrame[pid], FrameEnd: frameIdx, AnomalyType: "speed_accuracy_correlation"},
				)
				ev.Attribution = &t.Attribution
				events = append(events, ev)
				d.speedDeviationPairs[pid] = nil
				delete(d.pairsFirstFrame, pid)
			}
		}

		devs := d.deviations[pid]
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

		ev := d.MakeEvent(matchCtx, pid, frameIdx, t.Timestamp, severity, confidence,
			model.PrecisionEvidence{
				GoalDirectedThrows: len(devs), MeanDeviation: meanDev,
				StddevDeviation: stddevDev, MinDeviation: model.MinFloat(devs),
				MaxDeviation: model.MaxFloat(devs), DeviationHistory: devs,
			},
			fmt.Sprintf("mean_target_dev: %.2f deg (stddev: %.2f) over %d throws", meanDev, stddevDev, len(devs)),
			fmt.Sprintf("mean_target_dev: > %.1f deg, stddev: > %.1f deg", d.maxMeanDev, d.maxStddevDev),
			model.CausalKey{PlayerID: pid, FrameStart: d.firstFrame[pid], FrameEnd: frameIdx, AnomalyType: "target_precision"},
		)
		ev.Attribution = &t.Attribution
		events = append(events, ev)
		// Start a fresh window for both statistics.
		d.deviations[pid] = nil
		delete(d.firstFrame, pid)
		d.speedDeviationPairs[pid] = nil
		delete(d.pairsFirstFrame, pid)
	}
	return events
}

// pearsonCorrelation computes the Pearson correlation coefficient for (x, y)
// pairs. ok is false when fewer than 3 pairs are given or either variable is
// (numerically) constant, in which case the correlation is undefined.
func pearsonCorrelation(pairs [][2]float64) (r float64, ok bool) {
	n := float64(len(pairs))
	if n < 3 {
		return 0, false
	}
	var sumX, sumY, sumXY, sumX2, sumY2 float64
	for _, p := range pairs {
		sumX += p[0]
		sumY += p[1]
		sumXY += p[0] * p[1]
		sumX2 += p[0] * p[0]
		sumY2 += p[1] * p[1]
	}
	varX := n*sumX2 - sumX*sumX
	varY := n*sumY2 - sumY*sumY
	if varX < 1e-9 || varY < 1e-9 {
		return 0, false
	}
	r = (n*sumXY - sumX*sumY) / math.Sqrt(varX*varY)
	if math.IsNaN(r) {
		return 0, false
	}
	return model.Clamp(r, -1, 1), true
}

// correlationUpperCI returns the Pearson correlation of the pairs and the
// upper bound of its 95% confidence interval via the Fisher z-transform.
// ok is false when the correlation is undefined or n < 4.
func correlationUpperCI(pairs [][2]float64) (r, upper float64, ok bool) {
	r, ok = pearsonCorrelation(pairs)
	n := float64(len(pairs))
	if !ok || n < 4 {
		return 0, 0, false
	}
	rc := model.Clamp(r, -0.999999, 0.999999)
	z := math.Atanh(rc)
	se := 1.0 / math.Sqrt(n-3)
	upper = math.Tanh(z + corrZ95*se)
	return r, upper, true
}
