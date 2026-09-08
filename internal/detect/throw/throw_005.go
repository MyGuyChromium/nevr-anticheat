package throw

import (
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	corrMinPairs          = 30
	corrHistory           = 50
	corrZ95               = 1.959964
	shotReviewPlayerLimit = 16
)

type shotSupportSample struct {
	frame            int
	speed, deviation float64
	hasDirection     bool
	raw              model.MechanicsRawSample
}

type shotSupportHistory struct {
	lastFrame     int
	lastTimestamp float64
	lastRelease   int
	source        *model.ObservationContext
	samples       []shotSupportSample
}

// Throw005 retains descriptive release statistics. Precision, including an
// extreme speed/accuracy relationship, is not by itself evidence of an illegal
// setting, input macro or physics violation. Every output is diagnostic-only.
type Throw005 struct {
	detect.BaseDetector
	histories map[string]*shotSupportHistory
	observer  detect.MechanicsObserver
}

func NewThrow005(_ map[string]any) *Throw005 {
	d := &Throw005{BaseDetector: detect.BaseDetector{DetectorID: "THROW_005", DetectorVersion: "2.0.0",
		DetectorName: "Shot Targeting Review", DetectorCategory: "throw", Inputs: []string{"throw_event"}, Warmup: 0, Weight: 0}}
	d.Reset()
	return d
}
func (d *Throw005) Reset()                                                 { d.histories = make(map[string]*shotSupportHistory) }
func (d *Throw005) Configure(map[string]any) error                         { return nil }
func (d *Throw005) SetWeight(float64)                                      { d.Weight = 0 }
func (d *Throw005) SetAutoEnforce(bool)                                    { d.IsAutoEnforce = false }
func (d *Throw005) AutoEnforce() bool                                      { return false }
func (d *Throw005) DefaultEnforcementWeight() float64                      { return 0 }
func (d *Throw005) SetMechanicsObserver(observer detect.MechanicsObserver) { d.observer = observer }

func (d *Throw005) Evaluate(mc *model.MatchContext, players map[string]*model.PlayerState, frame int) []model.DetectionEvent {
	active := detect.ActivePlayers(players, frame)
	if len(active) > shotReviewPlayerLimit {
		d.Reset()
		return nil
	}
	present := make(map[string]bool, len(active))
	for _, ps := range active {
		pid := ps.PlayerID
		present[pid] = true
		h := d.histories[pid]
		if h == nil {
			h = &shotSupportHistory{lastFrame: -1, lastRelease: -1}
			d.histories[pid] = h
		}
		sameSource := (h.source == nil && ps.Observation == nil) || h.source.SameSource(ps.Observation)
		if h.lastFrame >= 0 && (frame < h.lastFrame || !sameSource || ps.LastTimestamp < h.lastTimestamp) {
			*h = shotSupportHistory{lastFrame: -1, lastRelease: -1}
		} else if h.lastFrame >= 0 && (frame > h.lastFrame+1 || ps.LastTimestamp-h.lastTimestamp > .2) {
			h.samples = nil // engineering continuity reset, not a human-accuracy rule
		}
		h.lastFrame, h.lastTimestamp, h.source = frame, ps.LastTimestamp, ps.Observation.Clone()
		t := throwAt(ps, frame)
		if t == nil || t.ReleaseWindow == nil || t.FrameIndex <= h.lastRelease {
			continue
		}
		h.lastRelease = t.FrameIndex
		r := releaseMechanicsAssessment(mc, ps, t, model.MechanicsShotTargeting)
		r.Limitations = []string{
			"Accuracy alone does not establish targeting assistance, a settings violation or an illegal release.",
			"Goal/pocket dimensions, unobstructed goal-plane intersection and bank-shot geometry are not verified here.",
			"Direction-to-reported-goal statistics are supporting context, not made-goal accuracy or a calibrated human-variance limit.",
			"These bounded sampled releases are not independently validated calibration opportunities; source reports do not establish authority.",
			"Supporting-release raw samples describe the bounded statistics window, not additional violations; inferred reference goals are not verified pocket geometry.",
		}
		if mechanicsReleaseKnown(ps, t) && mechanicsFinite(t.ReleaseSpeed) && t.ReleaseSpeed >= 0 {
			s := shotSupportSample{frame: t.FrameIndex, speed: t.ReleaseSpeed}
			speed := t.ReleaseSpeed
			s.raw = model.MechanicsRawSample{SampleRole: "supporting_release", EventID: t.ReleaseWindow.EventID(), FrameIndex: t.FrameIndex, Timestamp: t.Timestamp,
				DiscPosition: mechanicsVector(t.ReleasePosition), DiscVelocity: mechanicsVector(t.ReleaseVelocity), SampledSpeedMPS: &speed, Attachment: "free"}
			// Include every independent release in the denominator. A direction
			// can be described for misses/non-goal-directed throws too; no <=30°
			// selection removes the misses before statistics are accumulated.
			goal := t.GoalPosition
			if goal.IsZero() && t.TargetPosition != nil {
				goal = *t.TargetPosition
			}
			if !goal.IsZero() && !goal.HasNaN() && !goal.HasInf() && !t.ReleasePosition.HasNaN() && !t.ReleasePosition.HasInf() &&
				!t.ReleaseVelocity.HasNaN() && !t.ReleaseVelocity.HasInf() && t.ReleaseVelocity.Magnitude() > 0 && goal.Distance(t.ReleasePosition) > 0 {
				s.deviation = t.ReleaseVelocity.AngleBetweenDeg(goal.Sub(t.ReleasePosition))
				s.hasDirection = mechanicsFinite(s.deviation)
				if s.hasDirection {
					s.raw.InferredReferenceGoal = mechanicsVector(goal)
				}
			}
			h.samples = append(h.samples, s)
			if len(h.samples) > corrHistory {
				copy(h.samples, h.samples[len(h.samples)-corrHistory:])
				h.samples = h.samples[:corrHistory]
			}
			r.Reason = "shot_targeting_supporting_context"
		}
		r.Metrics["observed_release_count"] = float64(len(h.samples))
		// Reserve space for every supporting release. Production context has
		// two movement samples plus the latest disc sample; oversized optional
		// context is omitted explicitly, never the inputs behind the statistics.
		contextLimit := model.MaxMechanicsRawSamples - len(h.samples)
		if len(r.RawSamples) > contextLimit {
			r.Metrics["context_raw_samples_omitted"] += float64(len(r.RawSamples) - contextLimit)
			r.RawSamples = r.RawSamples[:contextLimit]
		}
		if len(h.samples) > 0 {
			first, last := h.samples[0].raw, h.samples[len(h.samples)-1].raw
			r.Metrics["support_start_frame"], r.Metrics["support_end_frame"] = float64(first.FrameIndex), float64(last.FrameIndex)
			r.Metrics["support_start_time_s"], r.Metrics["support_end_time_s"] = first.Timestamp, last.Timestamp
		}
		var deviations []float64
		var pairs [][2]float64
		for _, s := range h.samples {
			r.RawSamples = append(r.RawSamples, s.raw)
			if s.hasDirection {
				deviations = append(deviations, s.deviation)
				pairs = append(pairs, [2]float64{s.speed, s.deviation})
			}
		}
		r.Metrics["direction_sample_count"] = float64(len(deviations))
		if len(deviations) > 0 {
			r.Metrics["mean_reported_goal_direction_deg"] = model.Mean(deviations)
			r.Metrics["stddev_reported_goal_direction_deg"] = model.StdDev(deviations)
			r.Metrics["min_reported_goal_direction_deg"] = model.MinFloat(deviations)
			r.Metrics["max_reported_goal_direction_deg"] = model.MaxFloat(deviations)
		}
		if len(pairs) >= corrMinPairs {
			if corr, upper, ok := correlationUpperCI(pairs); ok {
				r.Metrics["speed_direction_correlation"] = corr
				r.Metrics["correlation_upper_95_ci"] = upper
			}
		}
		if d.observer != nil {
			d.observer(d.ID(), pid, r.Clone())
		}
	}
	for pid := range d.histories {
		if !present[pid] {
			delete(d.histories, pid)
		}
	}
	return nil
}

// pearsonCorrelation remains descriptive; no sign of this coefficient is a
// verified human/assisted boundary. Undefined constant inputs remain unknown.
func pearsonCorrelation(pairs [][2]float64) (r float64, ok bool) {
	n := float64(len(pairs))
	if n < 3 {
		return 0, false
	}
	var sumX, sumY, sumXY, sumX2, sumY2 float64
	for _, p := range pairs {
		if !mechanicsFinite(p[0]) || !mechanicsFinite(p[1]) {
			return 0, false
		}
		sumX += p[0]
		sumY += p[1]
		sumXY += p[0] * p[1]
		sumX2 += p[0] * p[0]
		sumY2 += p[1] * p[1]
	}
	varX, varY := n*sumX2-sumX*sumX, n*sumY2-sumY*sumY
	if varX < 1e-9 || varY < 1e-9 {
		return 0, false
	}
	r = (n*sumXY - sumX*sumY) / math.Sqrt(varX*varY)
	if !mechanicsFinite(r) {
		return 0, false
	}
	return model.Clamp(r, -1, 1), true
}

func correlationUpperCI(pairs [][2]float64) (r, upper float64, ok bool) {
	r, ok = pearsonCorrelation(pairs)
	n := float64(len(pairs))
	if !ok || n < 4 {
		return 0, 0, false
	}
	z := math.Atanh(model.Clamp(r, -.999999, .999999))
	upper = math.Tanh(z + corrZ95/math.Sqrt(n-3))
	return r, upper, mechanicsFinite(upper)
}
