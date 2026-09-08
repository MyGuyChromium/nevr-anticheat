package movement

import (
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// boostSample is only a continuity witness. Explicit availability does not
// authenticate a producer; these telemetry-dependent checks remain disabled.
type boostSample struct {
	frame     int
	timestamp float64
	source    *model.ObservationContext
}

func readBoostSample(ps *model.PlayerState, frame int) *boostSample {
	if !ps.IsBoostingKnown || frame < 0 || ps.FrameDt <= 0 || math.IsNaN(ps.FrameDt) || math.IsInf(ps.FrameDt, 0) || ps.LastTimestamp < 0 || math.IsNaN(ps.LastTimestamp) || math.IsInf(ps.LastTimestamp, 0) || ps.Speed < 0 || math.IsNaN(ps.Speed) || math.IsInf(ps.Speed, 0) {
		return nil
	}
	if ps.Observation != nil && (!ps.Observation.Valid() || ps.Observation.FrameIndex != frame || ps.Observation.Timestamp != ps.LastTimestamp) {
		return nil
	}
	return &boostSample{frame: frame, timestamp: ps.LastTimestamp, source: ps.Observation.Clone()}
}

func continuousBoostSample(previous, current *boostSample, dt float64) bool {
	if previous == nil || current == nil || current.frame != previous.frame+1 || current.timestamp <= previous.timestamp || current.timestamp-previous.timestamp > dt+1e-6 {
		return false
	}
	if previous.source == nil && current.source == nil {
		return true
	}
	return previous.source.SameSource(current.source)
}
