package pipeline

import (
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// MaxDecisionReasons bounds additional storage per player/detector regardless
// of replay length. It is not an event cap and never affects detector output.
const MaxDecisionReasons = 48

func (c *coverageTracker) trace(detectorID, playerID string, frame int, reason string) {
	index, ok := c.index[detectorID]
	if !ok || playerID == "" {
		return
	}
	t := c.player(playerID).Detectors[index].DecisionTrace
	if t == nil {
		return
	}
	// Keep diagnostic keys small and machine-readable even for an optional
	// third-party detector; never retain arbitrary messages or telemetry here.
	valid := len(reason) > 0 && len(reason) <= 64
	for _, r := range reason {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			valid = false
			break
		}
	}
	if !valid {
		t.OverflowCount++
		return
	}
	// STATE_008 checks the whole sampled roster, not just one player's pose.
	// Count inputs only after that detector actually passes its global checks.
	// Repeated diagnostic calls for one frame must not inflate coverage.
	catchInputs := detectorID == "STATE_008" && reason == "catch_inputs_ready"
	for i := range t.Reasons {
		r := &t.Reasons[i]
		if r.Code == reason {
			if catchInputs && frame > r.LastFrame {
				c.player(playerID).Detectors[index].InputFrames++
			}
			r.Count++
			if frame < r.FirstFrame {
				r.FirstFrame = frame
			}
			if frame > r.LastFrame {
				r.LastFrame = frame
			}
			return
		}
	}
	if len(t.Reasons) >= MaxDecisionReasons {
		t.OverflowCount++
		return
	}
	if catchInputs {
		c.player(playerID).Detectors[index].InputFrames++
	}
	t.Reasons = append(t.Reasons, model.DetectorDecisionReason{Code: reason, Description: detect.DecisionReasonDescription(reason), Count: 1, FirstFrame: frame, LastFrame: frame})
}

func (c *coverageTracker) allEnabled(playerID string, frame int, reason string) {
	for _, d := range c.detectors {
		if d.Enabled {
			c.trace(d.DetectorID, playerID, frame, reason)
		}
	}
}

func (p *Pipeline) traceEvent(event model.DetectionEvent, reason string) {
	if p.decisionCoverage != nil {
		p.decisionCoverage.trace(event.DetectorID, event.PlayerID, event.FrameIndex, reason)
	}
}

func (p *Pipeline) attachDecisionCoverage(c *coverageTracker) func() {
	p.decisionCoverage = c
	for _, d := range p.detectors {
		if observed, ok := d.(detect.DecisionObservable); ok {
			observed.SetDecisionObserver(c.trace)
		}
	}
	p.dedup.decisionObserver = func(e model.DetectionEvent) { p.traceEvent(e, "emission_merged") }
	p.rateLimiter.decisionObserver = func(e model.DetectionEvent) { p.traceEvent(e, "incident_rate_limited") }
	return func() {
		for _, d := range p.detectors {
			if observed, ok := d.(detect.DecisionObservable); ok {
				observed.SetDecisionObserver(nil)
			}
		}
		p.dedup.decisionObserver, p.rateLimiter.decisionObserver = nil, nil
		p.decisionCoverage = nil
	}
}
