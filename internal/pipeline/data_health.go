package pipeline

import (
	"math"
	"sort"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Eight consecutive usable observations clear an operational health fault. This
// is an engineering recovery window, NOT an Echo physics threshold or accuracy
// claim. State is bounded by the pipeline's admitted player roster and a fixed
// set of reason codes. Live batches do not reset it.
const healthRecoverySamples = 8

type healthEntry struct {
	summary      model.DataHealth
	faults       map[string]int
	lastAffected map[string]int
}

func healthAffects(reason, id string) bool {
	switch reason {
	case "missing_disc":
		return strings.HasPrefix(id, "THROW_") || id == "STATE_001" || id == "STATE_008"
	case "missing_reported_velocity":
		return id == "MOV_006"
	case "missing_hand_tracking":
		return strings.HasPrefix(id, "BIO_") || id == "MOV_006" || id == "PAT_002" || id == "PAT_005" || id == "STATE_001" || id == "STATE_007" || id == "STATE_008" || id == "THROW_003"
	case "invalid_hand_rotation", "shared_orientation_jump":
		return id == "BIO_001" || id == "BIO_004"
	default:
		return true
	}
}

func (p *Pipeline) observeHealth(f *model.PlayerTelemetryFrame, previous *model.PlayerState, rejected bool, sanitized []string, sharedJump bool) {
	if p.health == nil {
		p.health = make(map[string]*healthEntry)
	}
	h := p.health[f.PlayerID]
	if h == nil {
		h = &healthEntry{summary: model.DataHealth{Version: 1}, faults: make(map[string]int), lastAffected: make(map[string]int)}
		p.health[f.PlayerID] = h
	}
	faults := make(map[string]bool)
	if rejected {
		faults["rejected_observation"] = true
	}
	if !f.Observation.Valid() {
		faults["missing_source_context"] = true
	} else if f.Observation.FrameIndex != f.FrameIndex || f.Observation.Timestamp != f.Timestamp {
		faults["source_binding_invalid"] = true
	}
	if previous != nil && previous.FrameCount > 0 {
		if f.Timestamp <= previous.LastTimestamp || f.FrameIndex <= previous.LastFrameIdx {
			faults["non_advancing_observation"] = true
		}
		if !sameObservationSource(previous.Observation, f.Observation) {
			faults["source_discontinuity"] = true
		}
		maxDt := p.cfg.Pipeline.MaxFrameDt
		if maxDt <= 0 {
			maxDt = .25
		}
		if f.Timestamp-previous.LastTimestamp > maxDt {
			faults["sampling_gap"] = true
		}
	}
	if f.Disc == nil {
		faults["missing_disc"] = true
	}
	if f.ReportedVelocity == nil {
		faults["missing_reported_velocity"] = true
	}
	if f.LeftHandPosition.IsZero() || f.RightHandPosition.IsZero() {
		faults["missing_hand_tracking"] = true
	}
	if f.LeftHandRotationValid == nil || !*f.LeftHandRotationValid || !f.LeftHandRotation.IsUnit() || f.RightHandRotationValid == nil || !*f.RightHandRotationValid || !f.RightHandRotation.IsUnit() {
		faults["invalid_hand_rotation"] = true
	}
	for _, reason := range sanitized {
		switch reason {
		case SanitizedDisc, SanitizedDiscOutOfRange, SanitizedDiscObservation:
			faults["missing_disc"] = true
		case SanitizedReportedVelocity:
			faults["missing_reported_velocity"] = true
		case SanitizedHandPosition, SanitizedFarHand, SanitizedHeadPosition:
			faults["missing_hand_tracking"] = true
		case SanitizedRotation, SanitizedZeroRotation:
			faults["invalid_hand_rotation"] = true
		default:
			faults["sanitized_observation"] = true
		}
	}
	if sharedJump {
		faults["shared_orientation_jump"] = true
	}
	for reason := range faults {
		h.faults[reason] = healthRecoverySamples
	}
	for reason, remaining := range h.faults {
		if faults[reason] {
			continue
		}
		if remaining <= 1 {
			delete(h.faults, reason)
		} else {
			h.faults[reason]--
		}
	}
	h.summary.Reasons = nil
	h.summary.AffectedDetectors = nil
	h.summary.RecoverySamplesRemaining = 0
	h.summary.State = model.HealthHealthy
	for reason, remaining := range h.faults {
		h.summary.Reasons = append(h.summary.Reasons, reason)
		if remaining > h.summary.RecoverySamplesRemaining {
			h.summary.RecoverySamplesRemaining = remaining
		}
		if reason == "missing_source_context" || reason == "source_binding_invalid" || reason == "rejected_observation" || reason == "non_advancing_observation" {
			h.summary.State = model.HealthBlind
		}
	}
	if len(h.faults) > 0 && h.summary.State != model.HealthBlind {
		h.summary.State = model.HealthDegraded
	}
	for _, d := range p.detectors {
		if p.capabilities[d.ID()] == nil {
			continue
		}
		for reason := range h.faults {
			if healthAffects(reason, d.ID()) {
				h.summary.AffectedDetectors = append(h.summary.AffectedDetectors, d.ID())
				break
			}
		}
	}
	sort.Strings(h.summary.Reasons)
	sort.Strings(h.summary.AffectedDetectors)
	if f.FrameIndex > h.summary.LastFrame {
		h.summary.LastFrame = f.FrameIndex
	}
	h.summary.Revision++
	for _, id := range h.summary.AffectedDetectors {
		h.lastAffected[id] = h.summary.LastFrame
	}
	switch h.summary.State {
	case model.HealthHealthy:
		h.summary.HealthySamples++
	case model.HealthDegraded:
		h.summary.DegradedSamples++
	case model.HealthBlind:
		h.summary.BlindSamples++
	}
}

// A shared large orientation discontinuity is an operator investigation signal,
// never a finding against a player or proof of a faulty broadcaster. Only
// simultaneously observed actors from the SAME source/epoch can corroborate it.
// q/-q and whole-scene rotation preserve the shortest angular displacement.
func (p *Pipeline) sharedOrientationJumps(frames []model.PlayerTelemetryFrame) map[string]bool {
	var candidates []int
	for i := range frames {
		f := &frames[i]
		prev := p.players[f.PlayerID]
		if prev == nil || prev.FrameCount == 0 || !f.Observation.SameSource(prev.Observation) || f.Timestamp <= prev.LastTimestamp || !f.Rotation.IsUnit() || !prev.Rotation.IsUnit() {
			continue
		}
		if prev.Rotation.AngularDistance(f.Rotation) > .75*math.Pi {
			candidates = append(candidates, i)
		}
	}
	out := make(map[string]bool)
	for _, i := range candidates {
		identities := make(map[string]bool)
		for _, j := range candidates {
			if frames[i].Timestamp == frames[j].Timestamp && frames[i].Observation.SameSource(frames[j].Observation) {
				identities[frames[j].PlayerID] = true
			}
		}
		if len(identities) >= 3 {
			for id := range identities {
				out[id] = true
			}
		}
	}
	return out
}

func (p *Pipeline) applyEvidenceSafety(ev *model.DetectionEvent) {
	if p.capabilities[ev.DetectorID] == nil {
		return
	} // explicitly injected non-catalog test/plugin detectors
	ev.AutoEnforce = false // no current build/source has passed enforcement validation
	switch ev.DetectorID {
	case "STATE_002", "STATE_003", "STATE_004", "STATE_005", "STATE_006", "STATE_007", "PAT_001":
		// These feeds lack presence-valid event edges/attribution. Preserve
		// descriptive stateful diagnostics but do not score a default-false
		// transition or sampled regularity as a verified input-timing fact.
		ev.IsShadow, ev.EnforcementWeight = true, 0
		p.traceEvent(*ev, "unsupported_input_abstention")
	}
	if h := p.health[ev.PlayerID]; h != nil {
		// Delayed/rolling findings must not escape quarantine merely because
		// the feed recovered before emission. A fixed per-detector watermark
		// is bounded and never forgets earlier faults. It intentionally also
		// abstains retrospective findings entirely preceding the latest fault.
		start := ev.FrameRangeStart
		if ev.CausalKey.FrameStart < start {
			start = ev.CausalKey.FrameStart
		}
		if last, affected := h.lastAffected[ev.DetectorID]; affected && start <= last {
			ev.IsShadow = true
			ev.EnforcementWeight = 0
			p.traceEvent(*ev, "source_health_abstention")
		}
	}
}

func (p *Pipeline) finishHealth(coverage *coverageTracker, result *MatchResult) {
	for id, entry := range p.health {
		player := coverage.player(id)
		player.DataHealth = entry.summary.Clone()
		if entry.summary.State == model.HealthBlind {
			player.Status = model.ReviewStatusInsufficientData
			for i := range player.Detectors {
				if player.Detectors[i].Enabled {
					player.Detectors[i].Status = model.ReviewStatusInsufficientData
				}
			}
		}
	}
	if p.skipReset {
		// Live quality is source-scoped. Never manufacture a 100/100 quality
		// score because this particular call contains only a single frame.
		r := TelemetryQualityReport{Grade: "source_scoped", ConfidenceMultiplier: 1, Players: len(p.health), Reasons: []string{"Consult per-player data_health; sample usability is not gameplay authority or detection accuracy."}}
		for _, entry := range p.health {
			h := entry.summary
			r.Rows += h.HealthySamples + h.DegradedSamples + h.BlindSamples
			r.Score += float64(h.HealthySamples)
		}
		if r.Rows > 0 {
			r.Score = 100 * r.Score / float64(r.Rows)
		}
		p.quality = r
		result.TelemetryQuality = r
	}
}

func detectorCapabilities(detectors []detect.Detector) map[string]*model.DetectorCapability {
	out := make(map[string]*model.DetectorCapability)
	for _, d := range detectors {
		out[d.ID()] = detect.CapabilityFor(d.ID())
	}
	return out
}
