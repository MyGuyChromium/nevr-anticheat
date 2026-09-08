package pipeline

import (
	"encoding/json"
	"math"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

type coverageTracker struct {
	players       map[string]*model.PlayerCoverage
	detectors     []model.DetectorCoverage
	index         map[string]int
	branches      map[string]bool
	catchLogs     map[string]bool
	mechanicsLogs map[string]bool
	wristLimit    float64
}

func newCoverageTracker(detectors []detect.Detector, cfg *config.Config, roster []string) *coverageTracker {
	c := &coverageTracker{players: make(map[string]*model.PlayerCoverage), index: make(map[string]int), branches: make(map[string]bool), catchLogs: make(map[string]bool), wristLimit: 50}
	c.mechanicsLogs = make(map[string]bool)
	if dc, ok := cfg.Detectors["BIO_001"]; ok {
		c.wristLimit = detect.GetFloat(dc.Params, "max_wrist_angular_velocity", 50)
	}
	enabled := make(map[string]bool)
	for _, d := range detectors {
		enabled[d.ID()] = true
		if observed, ok := d.(detect.DecisionObservable); ok {
			c.branches[d.ID()] = observed.HasDecisionBranches()
		}
		_, c.catchLogs[d.ID()] = d.(detect.CatchObservable)
		_, c.mechanicsLogs[d.ID()] = d.(detect.MechanicsObservable)
		if d.ID() == "THROW_001" || d.ID() == "THROW_003" {
			c.mechanicsLogs[d.ID()] = true
		}
	}
	ids := make(map[string]bool)
	for id := range cfg.Detectors {
		ids[id] = true
	}
	for id := range enabled {
		ids[id] = true
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		d := model.DetectorCoverage{DetectorID: id, Enabled: enabled[id], Status: "not_evaluated", Limitations: []string{}}
		d.Capability = detect.CapabilityFor(id)
		if d.Capability != nil {
			d.InputCheck = true
			d.Limitations = append(d.Limitations, "Automatic enforcement unavailable: "+d.Capability.Limitations)
			if d.Capability.Rule != nil {
				// Owned immutable bytes preserve the actual configured parameters,
				// not just today's defaults or a pointer into a mutable config map.
				if dc, ok := cfg.Detectors[id]; ok {
					d.Capability.Rule.ConfiguredParameters, _ = json.Marshal(dc.Params)
				}
			}
		}
		if !d.Enabled {
			d.Status = "disabled"
		}
		switch id {
		case "THROW_001":
			d.InputCheck = true
			d.Limitations = append(d.Limitations, "Sampled disc speed is not an independently verified release speed; intervening contact and reference-frame assumptions require review.")
		case "THROW_003":
			d.InputCheck = true
			d.Limitations = append(d.Limitations, "Physical hand-motion/disc disagreement is not WristAngleOffset. Settings integrity needs an observed setting and verified allowed range, neither currently available.")
		case "BIO_001":
			d.InputCheck = true
			d.Limitations = append(d.Limitations, "Wrist rate saturates at pi / sample interval. Rates above that limit are indistinguishable; legal contact and tracking remain review-only.")
		case "MOV_006":
			d.InputCheck = true
			d.Limitations = append(d.Limitations, "No feet, guardian or authoritative contact telemetry: this cannot prove walking rather than a legal lean or lunge.")
		case "STATE_008":
			d.InputCheck = true
			d.Limitations = append(d.Limitations, "Experimental pre-catch trajectory review requires explicit attachment, source continuity, bounce-counter presence and a complete tracked player sample. Input availability is not a confirmed catch opportunity; inspect branch reasons. No remote grip input or authoritative collision impulses are available, so a trajectory observation cannot identify a cheater or prove input automation.")
		default:
			d.Limitations = append(d.Limitations, "Internal detector opportunity coverage is not measured; dispatch does not prove that all required inputs or situations occurred.")
		}
		c.index[id] = len(c.detectors)
		c.detectors = append(c.detectors, d)
	}
	for _, id := range roster {
		c.player(id)
	}
	return c
}

func (c *coverageTracker) player(id string) *model.PlayerCoverage {
	p := c.players[id]
	if p == nil {
		p = &model.PlayerCoverage{Version: 1, Status: "insufficient_data", Detectors: append([]model.DetectorCoverage(nil), c.detectors...), Limitations: []string{}}
		for i := range p.Detectors {
			d := &p.Detectors[i]
			d.DecisionTrace = &model.DetectorDecisionTrace{Version: 1, InternalBranches: c.branches[d.DetectorID], Reasons: []model.DetectorDecisionReason{}}
			if c.catchLogs[d.DetectorID] {
				d.CatchReview = model.NewCatchReviewLog()
			}
			if c.mechanicsLogs[d.DetectorID] {
				d.MechanicsReview = model.NewMechanicsReviewLog()
			}
		}
		c.players[id] = p
	}
	return p
}

func (c *coverageTracker) candidate(id string, ps *model.PlayerState, frame int) {
	d := &c.player(ps.PlayerID).Detectors[c.index[id]]
	d.CandidateFrames++
	usable := detect.ReviewInputsAvailable(id, ps, frame)
	switch id {
	case "THROW_001":
		usable = ps.LastThrow.ObservedAt(frame) && ps.LastThrow.ReleaseSpeed > 0
	case "MOV_006":
		usable = ps.PlayspaceValid && ps.HasReportedVelocity && ps.PlayspaceTrackedHands > 0
	case "THROW_003":
		usable = ps.LastThrow != nil && ps.LastThrow.ObservedAt(frame) && ps.LastThrow.HandKinematicsValid
	case "BIO_001":
		usable = !ps.IsStunned && !ps.IsImmune && ps.FrameDt >= .01 && math.Pi/ps.FrameDt > c.wristLimit &&
			(ps.LeftWristAngularRateValid || ps.RightWristAngularRateValid)
	case "STATE_008":
		usable = false // actual catch_inputs_ready trace owns this counter
	}
	if usable {
		d.InputFrames++
	}
}

func (c *coverageTracker) finish(quality TelemetryQualityReport) map[string]*model.PlayerCoverage {
	for _, p := range c.players {
		p.QualityGated = quality.Gated
		usableDetectors := 0
		for i := range p.Detectors {
			d := &p.Detectors[i]
			if d.DecisionTrace != nil {
				sort.Slice(d.DecisionTrace.Reasons, func(i, j int) bool {
					a, b := d.DecisionTrace.Reasons[i], d.DecisionTrace.Reasons[j]
					if a.Count != b.Count {
						return a.Count > b.Count
					}
					return a.Code < b.Code
				})
			}
			if !d.Enabled {
				continue
			}
			switch {
			case quality.Gated:
				d.Status = "insufficient_data"
				d.Limitations = append(d.Limitations, "Source quality gating prevents interpreting silence from this check.")
			case (d.DetectorID == "STATE_001" || d.DetectorID == "THROW_005" || d.DetectorID == "THROW_006") && d.MechanicsReview != nil && d.MechanicsReview.Total == d.MechanicsReview.Inconclusive:
				d.Status = "insufficient_data"
				d.Limitations = append(d.Limitations, "No conclusive mechanics comparison: inspect missing rule/source/input prerequisites and any observed transition evidence.")
			case d.CandidateFrames == 0:
				d.Limitations = append(d.Limitations, "No frames passed this detector's phase and warmup gates.")
			case d.InputCheck && d.InputFrames == 0:
				d.Status = "insufficient_data"
				d.Limitations = append(d.Limitations, "No qualifying input samples or release opportunities were observed for this check.")
			default:
				d.Status = "limited"
				usableDetectors++
			}
		}
		if p.ValidFrames >= 2 && usableDetectors > 0 && !quality.Gated {
			p.Status = "limited"
		}
		if p.Status == "insufficient_data" {
			p.Limitations = append(p.Limitations, "Too little usable telemetry, no warmed-up active detector dispatch, or source quality gating; re-record or inspect the source.")
		}
		if p.RejectedFrames > 0 {
			p.Limitations = append(p.Limitations, "Some player samples were rejected; gaps can hide incidents.")
		}
		p.Limitations = append(p.Limitations, "Coverage is partial, not validated detection accuracy. Zero signals does not establish fair play.")
	}
	return c.players
}
