package pattern

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// maxThrowFrames bounds the per-player interval buffer.
const maxThrowFrames = 50

// Pat001 detects frame-perfect throw timing (PAT_001).
//
// STATUS: UNSAFE — regrab rhythm in Echo VR produces low coefficient of
// variation naturally. Skilled players will false-positive. Disabled by default.
//
// New throws are detected by comparing ps.ThrowCount with the count seen on
// the previous frame (per player), never with the length of the interval
// buffer: the buffer is capped and cleared after a detection while
// ThrowCount only grows, so using its length would append a phantom
// "throw" on every subsequent frame.
type Pat001 struct {
	detect.BaseDetector
	minThrowCount    int
	maxCoV           float64
	maxStddev        float64
	sigmoidSteepness float64

	throwFrames    map[string][]int
	prevThrowCount map[string]int
}

// NewPat001 creates a new PAT_001 Frame-Perfect Timing detector.
func NewPat001(params map[string]any) *Pat001 {
	d := &Pat001{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "PAT_001",
			DetectorVersion:  "2.1.0",
			DetectorName:     "Frame-Perfect Timing",
			DetectorCategory: "pattern",
			Inputs:           []string{"throw_event"},
			Warmup:           60,
			Weight:           0.7,
			IsAutoEnforce:    false,
		},
		minThrowCount:    detect.GetInt(params, "min_throw_count", 12),
		maxCoV:           detect.GetFloat(params, "max_cov", 0.05),
		maxStddev:        detect.GetFloatAlias(params, 3.0, "max_stddev", "max_stddev_frames"),
		sigmoidSteepness: detect.GetFloat(params, "sigmoid_steepness", 20.0),
	}
	d.Reset()
	d.sanitize()
	return d
}

func (d *Pat001) sanitize() {
	if d.minThrowCount < 3 {
		d.minThrowCount = 3
	}
	if d.minThrowCount > maxThrowFrames {
		d.minThrowCount = maxThrowFrames
	}
}

func (d *Pat001) Reset() {
	d.throwFrames = make(map[string][]int)
	d.prevThrowCount = make(map[string]int)
}

func (d *Pat001) Configure(params map[string]any) error {
	d.minThrowCount = detect.GetInt(params, "min_throw_count", d.minThrowCount)
	d.maxCoV = detect.GetFloat(params, "max_cov", d.maxCoV)
	d.maxStddev = detect.GetFloatAlias(params, d.maxStddev, "max_stddev", "max_stddev_frames")
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	d.sanitize()
	return nil
}

func (d *Pat001) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID

		prev, seen := d.prevThrowCount[pid]
		d.prevThrowCount[pid] = ps.ThrowCount
		if !seen && ps.ThrowCount > 1 {
			// Joined mid-count (live batch boundary / reprocess): baseline only.
			continue
		}
		if ps.ThrowCount <= prev {
			continue
		}

		// New throw detected: record the release frame.
		throwFrame := frameIdx
		if ps.LastThrow != nil && ps.LastThrow.FrameIndex > 0 && ps.LastThrow.FrameIndex <= frameIdx {
			throwFrame = ps.LastThrow.FrameIndex
		}
		frames := append(d.throwFrames[pid], throwFrame)
		if len(frames) > maxThrowFrames {
			frames = frames[len(frames)-maxThrowFrames:]
		}
		d.throwFrames[pid] = frames

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
					"cov":           cov,
					"stddev":        stddev,
					"mean_interval": meanInterval,
					"throw_count":   float64(len(frames)),
					"max_cov":       d.maxCoV,
					"max_stddev":    d.maxStddev,
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

		// Reset the interval buffer after detection; the throw count baseline stays.
		d.throwFrames[pid] = nil
	}

	return events
}
