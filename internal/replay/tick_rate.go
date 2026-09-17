package replay

import (
	"math"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// minTickRateSamples is the fewest positive sample spacings a recording must
// offer before its capture rate is stated; shorter clips keep TickRate unset.
const minTickRateSamples = 10

// applyMeasuredTickRate sets MatchContext.TickRate to the capture rate the
// recording actually shows, so nothing downstream has to assume the bridge's
// nominal 15 Hz (real recordings arrive at about 15, 20 and 30 Hz): the
// calibration capture-rate strata, and the nominal spacing used when stored
// ticks carry no time. An unmeasurable recording leaves the context as the
// adapter produced it.
//
// The rate describes the recording. It must NOT be used to turn a
// frame-count threshold into seconds: that would make the same threshold
// shorter on faster recorders (see internal/detect/state/common.go).
func applyMeasuredTickRate(mc *model.MatchContext, frames []model.PlayerTelemetryFrame) {
	if rate := measuredTickRate(frames); mc != nil && rate > 0 {
		mc.TickRate = rate
	}
}

// measuredTickRate is 1 / (median positive spacing between consecutive
// sampled ticks), in Hz rounded to 0.01, or 0 when it cannot be measured.
// The median ignores recorder stalls, dropped duplicate snapshots and the
// zero spacings of a stepped clock; frames must be in tick order, as ingest
// produces them. It is a description of the recording, not of the game: the
// game authors no fixed telemetry rate.
func measuredTickRate(frames []model.PlayerTelemetryFrame) float64 {
	var spacings []float64
	lastIndex, lastTime, have := 0, 0.0, false
	for i := range frames {
		f := &frames[i]
		if have && f.FrameIndex == lastIndex {
			continue // another player of the same tick
		}
		if dt := f.Timestamp - lastTime; have && dt > 0 && !math.IsInf(dt, 0) && !math.IsNaN(dt) {
			spacings = append(spacings, dt)
		}
		lastIndex, lastTime, have = f.FrameIndex, f.Timestamp, true
	}
	if len(spacings) < minTickRateSamples {
		return 0
	}
	sort.Float64s(spacings)
	rate := 1 / spacings[len(spacings)/2]
	if math.IsInf(rate, 0) || math.IsNaN(rate) || rate < 1 || rate > 1000 {
		return 0
	}
	return math.Round(rate*100) / 100
}
