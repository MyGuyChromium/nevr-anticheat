package throw

import (
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	artifactCapMultiple = 2.0
	artifactSeverity    = 0.2
	releaseSpeedSamples = 3
)

// Throw001 reviews reported world-frame disc speeds against the configured
// reference. Three consecutive known-free observations can corroborate sampled
// speed, not exact launch speed, contact-free flight, or client honesty. A bound
// last_throw report remains separate context, never independent corroboration.
type Throw001 struct {
	detect.BaseDetector
	baseTolerance, pingToleranceScalar, maxSpeedRatio, sigmoidSteepness float64
	artifactCounts                                                      map[string]int
	tracks                                                              map[string]*releaseSpeedTrack
	lastRelease                                                         map[string]int
	previous                                                            *releaseSpeedSnapshot
	queued                                                              []model.DetectionEvent
}

func NewThrow001(params map[string]any) *Throw001 {
	d := &Throw001{
		BaseDetector: detect.BaseDetector{DetectorID: "THROW_001", DetectorVersion: "2.0.0",
			TraceBranches: true, DetectorName: "Reported Release Speed Review", DetectorCategory: "throw",
			Inputs: []string{"throw_event", "disc_state"}, Warmup: 5, Weight: 0.8, IsAutoEnforce: false},
		baseTolerance:       detect.GetFloat(params, "base_tolerance", 0),
		pingToleranceScalar: detect.GetFloat(params, "ping_tolerance_scalar", 0),
		maxSpeedRatio:       detect.GetFloat(params, "max_speed_ratio", 3),
		sigmoidSteepness:    detect.GetFloat(params, "sigmoid_steepness", 2),
	}
	d.Reset()
	return d
}

func (d *Throw001) Reset() {
	d.artifactCounts = make(map[string]int)
	d.tracks = make(map[string]*releaseSpeedTrack)
	d.lastRelease = make(map[string]int)
	d.previous, d.queued = nil, nil
}

// ResetSource retains terminal observations with their original match, cap and
// causal identity; the next dispatch or finalization drains them exactly once.
func (d *Throw001) ResetSource() {
	d.queued = append(d.queued, d.finishSpeedTracks("release_speed_source_changed", -1)...)
	d.previous = nil
	d.lastRelease = make(map[string]int)
}

func (d *Throw001) Configure(params map[string]any) error {
	d.baseTolerance = detect.GetFloat(params, "base_tolerance", d.baseTolerance)
	d.pingToleranceScalar = detect.GetFloat(params, "ping_tolerance_scalar", d.pingToleranceScalar)
	d.maxSpeedRatio = detect.GetFloat(params, "max_speed_ratio", d.maxSpeedRatio)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Throw001) Evaluate(mc *model.MatchContext, players map[string]*model.PlayerState, frame int) []model.DetectionEvent {
	events := d.queued
	d.queued = nil
	// Retained stale entries are not sampled releases. Keep that distinction
	// observable without allowing old snapshots into the speed window.
	for _, pid := range sortedPlayerIDs(players) {
		if ps := players[pid]; ps == nil || detect.IsStale(ps, frame) {
			d.TraceDecision(pid, frame, "stale_player_context")
		}
	}
	active := detect.ActivePlayers(players, frame)
	if len(active) > shotReviewPlayerLimit {
		events = append(events, d.finishSpeedTracks("release_speed_roster_unavailable", frame)...)
		d.previous = nil
		d.lastRelease = make(map[string]int)
		return events
	}
	snapshot := readReleaseSpeedSnapshot(active, frame)
	for _, pid := range sortedKeys(d.tracks) {
		tr := d.tracks[pid]
		reason := ""
		if t := throwAt(players[pid], frame); t != nil && t.FrameIndex != tr.releaseFrame {
			reason = "release_speed_release_replaced"
		} else if ps := players[pid]; ps == nil || ps.LastFrameIdx != frame {
			reason = "release_speed_thrower_unavailable"
		} else {
			reason = tr.appendSnapshot(snapshot)
		}
		if reason != "" || len(tr.review.Samples) == releaseSpeedSamples {
			events = append(events, d.finishSpeedTrack(pid, reason, frame)...)
		}
	}
	present := make(map[string]bool, len(active))
	for _, ps := range active {
		pid := ps.PlayerID
		present[pid] = true
		t := throwAt(ps, frame)
		if t == nil {
			d.TraceDecision(pid, frame, "no_current_release")
			continue
		}
		if last, seen := d.lastRelease[pid]; seen && t.FrameIndex <= last {
			continue
		}
		d.lastRelease[pid] = t.FrameIndex
		tr := d.newSpeedTrack(mc, ps, t)
		if tr == nil {
			d.TraceDecision(pid, frame, "release_speed_unusable")
			continue
		}
		d.tracks[pid] = tr
		first := d.previous
		if snapshot.raw.FrameIndex == t.FrameIndex {
			first = &snapshot
		}
		reason := ""
		switch {
		case t.ReleaseWindow == nil || !t.ReleaseWindow.Source.Valid():
			reason = "release_speed_source_unavailable"
		case t.PossibleHeadContact:
			reason = "release_speed_contact_possible"
		case first == nil || first.raw.FrameIndex != t.FrameIndex:
			reason = "release_speed_first_sample_unavailable"
		case first.raw.Timestamp != t.Timestamp || first.raw.Velocity != t.ReleaseVelocity || first.raw.Position != t.ReleasePosition:
			reason = "release_speed_first_sample_conflict"
		case !first.source.SameSource(t.ReleaseWindow.Source):
			reason = "release_speed_source_changed"
		default:
			tr.review.Samples[0] = first.raw
			tr.last = *first
			reason = first.reason
			if reason == "" && frame != t.FrameIndex {
				reason = tr.appendSnapshot(snapshot)
			}
		}
		if reason != "" || len(tr.review.Samples) == releaseSpeedSamples {
			events = append(events, d.finishSpeedTrack(pid, reason, frame)...)
		}
	}
	for pid := range d.lastRelease {
		if !present[pid] {
			delete(d.lastRelease, pid)
		}
	}
	d.previous = &snapshot
	return events
}

func (d *Throw001) FlushPhase(_ *model.MatchContext, frame int) []model.DetectionEvent {
	events := append(d.queued, d.finishSpeedTracks("release_speed_inactive_phase", frame)...)
	d.queued, d.previous = nil, nil
	return events
}

// FlushSource runs before the pipeline closes old-source dedup incidents.
// Keeping this separate from ResetSource prevents a queued old observation
// from being merged with a nearby release recorded by the replacement source.
func (d *Throw001) FlushSource(_ *model.MatchContext, frame int) []model.DetectionEvent {
	events := append(d.queued, d.finishSpeedTracks("release_speed_source_changed", frame)...)
	d.queued, d.previous = nil, nil
	return events
}

func (d *Throw001) FlushTracks(_ *model.MatchContext, frame int) []model.DetectionEvent {
	events := append(d.queued, d.finishSpeedTracks("release_speed_end_of_stream", frame)...)
	d.queued, d.previous = nil, nil
	return events
}

func (d *Throw001) newSpeedTrack(mc *model.MatchContext, ps *model.PlayerState, t *model.ThrowEvent) *releaseSpeedTrack {
	if t.ReleaseVelocity.HasNaN() || t.ReleaseVelocity.HasInf() || t.ReleasePosition.HasNaN() || t.ReleasePosition.HasInf() ||
		!mechanicsFinite(t.Timestamp) || t.Timestamp < 0 || t.FrameIndex < 0 {
		return nil
	}
	speed := t.ReleaseVelocity.Magnitude()
	if !mechanicsFinite(speed) || speed <= 0 || mc == nil || !mechanicsFinite(mc.Physics.DiscSpeedCap) || mc.Physics.DiscSpeedCap <= 0 {
		return nil
	}
	ping := ps.EstimatedPingMs
	if !mechanicsFinite(ping) || ping < 0 {
		ping = 0
	}
	cap := mc.Physics.DiscSpeedCap + d.baseTolerance + ping/1000*d.pingToleranceScalar
	if !mechanicsFinite(cap) || cap <= 0 {
		return nil
	}
	velocity, movementSource := releasePlayerVelocity(t)
	observedFrame := t.FrameIndex
	if t.ObservedFrameIndex != nil {
		observedFrame = *t.ObservedFrameIndex
	}
	if movementSource == playerVelocityPositionDifference {
		d.TraceDecision(ps.PlayerID, observedFrame, "engine_player_velocity_unavailable")
	}
	evidence := model.ThrowEvidence{ReleaseVelocity: t.ReleaseVelocity, ReleaseSpeed: speed, SampledDiscSpeed: speed,
		ReleasePosition: t.ReleasePosition, PlayerVelocity: velocity, PlayerSpeed: velocity.Magnitude(),
		AlignedMovementSpeed: velocity.Dot(t.ReleaseVelocity.Normalized()), PlayerRelativeVelocity: t.ReleaseVelocity.Sub(velocity),
		PlayerRelativeSpeed: t.ReleaseVelocity.Sub(velocity).Magnitude(), HandVelocity: t.HandVelocity,
		HandSpeed: t.HandSpeed, HandRelativeVelocity: t.HandRelativeVelocity, HandRelativeSpeed: t.HandRelativeSpeed,
		HandKinematicsValid: t.HandKinematicsValid, HandAttributionConfidence: t.HandAttributionConfidence,
		HandAttributionAnchor: t.HandAttributionAnchor, EffectiveCap: cap, PingMs: ping}
	if t.HandKinematicsValid && mechanicsFinite(t.HandSpeed) && t.HandSpeed >= 0 {
		evidence.SpeedRatio = speed / math.Max(t.HandSpeed, .01)
		// This configurable ratio remains descriptive context, as in v1.6;
		// it never increases severity or corroborates disc-speed evidence.
		if evidence.SpeedRatio > d.maxSpeedRatio {
			d.TraceDecision(ps.PlayerID, observedFrame, "hand_ratio_above_context_limit")
		}
	} else {
		d.TraceDecision(ps.PlayerID, observedFrame, "hand_ratio_unavailable")
	}
	review := &model.ReleaseSpeedReview{ReleaseFrame: t.FrameIndex, RequiredSamples: releaseSpeedSamples,
		LocalReportStatus: "unavailable", Limitations: []string{
			"Consecutive sampled speeds do not establish exact launch velocity or authoritative client physics.",
			"Stable bounce counters cannot rule out all slaps, head contacts or collisions; complete contact geometry is unavailable.",
			"The unchanged configured cap is a review reference, not an independently validated gameplay rule.",
			"Local last_throw is a separate client-authored report, never independent corroboration of sampled disc velocity.",
		}}
	if t.GameLastThrow != nil && t.GameLastThrow.Valid() {
		copy := *t.GameLastThrow
		evidence.GameLastThrow = &copy
		evidence.GameLastThrowProvenance = speedEvidenceSource(t.GameLastThrowProvenance)
		review.LocalReportStatus = "unbound_client_report"
		if t.GameLastThrowProvenance.BoundLocalThrow(ps.PlayerID, t.FrameIndex, t.Timestamp) &&
			t.ReleaseWindow != nil && t.ReleaseWindow.Source.SameSource(t.GameLastThrowProvenance) {
			review.LocalReportStatus = "bound_local_client_report_unverified"
		}
	}
	source := (*model.ObservationContext)(nil)
	if t.ReleaseWindow != nil {
		source = t.ReleaseWindow.Source.Clone()
	}
	raw := model.ReleaseSpeedSample{FrameIndex: t.FrameIndex, Timestamp: t.Timestamp, Position: t.ReleasePosition,
		Velocity: t.ReleaseVelocity, Speed: speed, Attachment: "unknown", Source: speedEvidenceSource(source)}
	review.Samples = []model.ReleaseSpeedSample{raw}
	evidence.SpeedReview = review
	return &releaseSpeedTrack{matchID: mc.MatchID, player: ps.PlayerID, releaseFrame: t.FrameIndex, timestamp: t.Timestamp,
		physicsCap: mc.Physics.DiscSpeedCap, evidence: evidence, review: review, attribution: t.Attribution, movementSource: movementSource,
		last: releaseSpeedSnapshot{raw: raw, source: source}}
}

func (d *Throw001) finishSpeedTracks(reason string, frame int) []model.DetectionEvent {
	var events []model.DetectionEvent
	for _, pid := range sortedKeys(d.tracks) {
		events = append(events, d.finishSpeedTrack(pid, reason, frame)...)
	}
	return events
}

func (d *Throw001) finishSpeedTrack(pid, reason string, frame int) []model.DetectionEvent {
	tr := d.tracks[pid]
	if tr == nil {
		return nil
	}
	delete(d.tracks, pid)
	return tr.event(d, reason, frame)
}

const (
	playerVelocityEngineFirstFree    = "engine-reported player velocity, first free sample"
	playerVelocityEngineLastHeld     = "engine-reported player velocity, last held sample"
	playerVelocityPositionDifference = "position-difference player velocity"
)

func releasePlayerVelocity(t *model.ThrowEvent) (model.Vec3, string) {
	if w := t.ReleaseWindow; w != nil {
		for _, pick := range []struct {
			frame int
			label string
		}{{w.EndFrame, playerVelocityEngineFirstFree}, {w.StartFrame, playerVelocityEngineLastHeld}} {
			for _, sample := range w.PlayerMovement {
				if v := sample.ReportedVelocity; sample.FrameIndex == pick.frame && v != nil && !v.HasNaN() && !v.HasInf() {
					return *v, pick.label
				}
			}
		}
	}
	return t.PlayerVelocity, playerVelocityPositionDifference
}
