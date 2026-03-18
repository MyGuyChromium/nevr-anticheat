package pattern

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Pat001 detects frame-perfect throw timing (PAT_001).
//
// STATUS: UNSAFE — regrab rhythm in Echo VR produces low coefficient of
// variation naturally. Skilled players will false-positive. Disabled by default.
type Pat001 struct {
	detect.BaseDetector
	minThrowCount    int
	maxCoV           float64
	maxStddev        float64
	sigmoidSteepness float64

	throwFrames map[string][]int
}

// NewPat001 creates a new PAT_001 Frame-Perfect Timing detector.
func NewPat001(params map[string]any) *Pat001 {
	d := &Pat001{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "PAT_001",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Frame-Perfect Timing",
			DetectorCategory: "pattern",
			Inputs:           []string{"throw_event"},
			Warmup:           60,
			Weight:           0.7,
			IsAutoEnforce:    false,
		},
		minThrowCount:    detect.GetInt(params, "min_throw_count", 8),
		maxCoV:           detect.GetFloat(params, "max_cov", 0.05),
		maxStddev:        detect.GetFloat(params, "max_stddev", 3.0),
		sigmoidSteepness: detect.GetFloat(params, "sigmoid_steepness", 20.0),
		throwFrames:      make(map[string][]int),
	}
	return d
}

func (d *Pat001) Reset() {
	d.throwFrames = make(map[string][]int)
}

func (d *Pat001) Configure(params map[string]any) error {
	d.minThrowCount = detect.GetInt(params, "min_throw_count", d.minThrowCount)
	d.maxCoV = detect.GetFloat(params, "max_cov", d.maxCoV)
	d.maxStddev = detect.GetFloat(params, "max_stddev", d.maxStddev)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Pat001) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range players {
		pid := ps.PlayerID

		// Detect throw event by checking ThrowCount increment
		if ps.ThrowCount <= 0 {
			continue
		}
		prevCount := len(d.throwFrames[pid])
		if ps.ThrowCount <= prevCount {
			continue
		}

		// New throw detected
		d.throwFrames[pid] = append(d.throwFrames[pid], frameIdx)
		if len(d.throwFrames[pid]) > 50 {
			d.throwFrames[pid] = d.throwFrames[pid][len(d.throwFrames[pid])-50:]
		}

		frames := d.throwFrames[pid]
		if len(frames) < d.minThrowCount {
			continue
		}

		// Compute intervals
		intervals := make([]float64, len(frames)-1)
		for i := 1; i < len(frames); i++ {
			intervals[i-1] = float64(frames[i] - frames[i-1])
		}

		cov := model.CoefficientOfVariation(intervals)
		stddev := model.StdDev(intervals)
		meanInterval := model.Mean(intervals)

		if cov >= d.maxCoV || stddev >= d.maxStddev {
			continue
		}

		// Lower CoV = more suspicious
		severity := model.SigmoidConfidence(d.maxCoV-cov, 0, d.sigmoidSteepness)
		confidence := model.Clamp01(severity * 0.85)

		history := make([]float64, len(intervals))
		copy(history, intervals)

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.PatternEvidence{
				DetectorSpecific: "frame_perfect_timing",
				Metrics: map[string]float64{
					"cov":            cov,
					"stddev":         stddev,
					"mean_interval":  meanInterval,
					"throw_count":    float64(len(frames)),
					"max_cov":        d.maxCoV,
					"max_stddev":     d.maxStddev,
				},
				History: history,
			},
			fmt.Sprintf("throw_timing: CoV=%.4f stddev=%.2f (%d throws)", cov, stddev, len(frames)),
			fmt.Sprintf("throw_timing: CoV>%.3f AND stddev>%.1f", d.maxCoV, d.maxStddev),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  frames[0],
				FrameEnd:    frameIdx,
				AnomalyType: "frame_perfect_timing",
			},
		)
		events = append(events, ev)

		// Reset after detection
		d.throwFrames[pid] = nil
	}

	return events
}
