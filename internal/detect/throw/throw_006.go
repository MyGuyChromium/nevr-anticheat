package throw

import (
	"math"
	"reflect"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	minViolationFrames  = 5
	minTrackedFrames    = 5
	inelasticSpeedRatio = .75
	inelasticMinAngle   = 2.0
	elasticBounceAngle  = 15.0
	deflectionRatio     = 1.15
	deflectionMinAngle  = 8.0
)

type trajectoryTrack struct {
	throwerID                      string
	releaseFrame                   int
	releasePos                     model.Vec3
	assessment                     model.MechanicsAssessment
	previous                       flightReviewSample
	cumulativeAngle, maxFrameAngle float64
	violationFrames, frameCount    int
}

type flightReviewSample struct {
	frame              int
	timestamp          float64
	position, velocity model.Vec3
	bounce             int
	source             *model.ObservationContext
}

// Throw006 is a sampled free-flight review, not a "mags" or targeting-cheat
// identification. Its provisional bend filters never produce scored events.
// Unknown ownership, missing contact counters, gaps, source changes, catches
// and collision indications cancel the comparison with an inconclusive record.
type Throw006 struct {
	detect.BaseDetector
	minTrajectoryChange, maxCumulativeChange, minDistFromThrower float64
	postReleaseFrames                                            int
	activeThrows                                                 map[string]*trajectoryTrack
	lastRelease                                                  map[string]int
	observer                                                     detect.MechanicsObserver
}

func NewThrow006(params map[string]any) *Throw006 {
	d := &Throw006{BaseDetector: detect.BaseDetector{DetectorID: "THROW_006", DetectorVersion: "2.0.0",
		DetectorName: "Free-flight Trajectory Review", DetectorCategory: "throw", Inputs: []string{"disc_state", "disc_attachment"}, Warmup: 0, Weight: 0},
		minTrajectoryChange: 8, maxCumulativeChange: 130, postReleaseFrames: 15, minDistFromThrower: 2}
	_ = d.Configure(params)
	d.Reset()
	return d
}
func (d *Throw006) Reset() {
	d.activeThrows = make(map[string]*trajectoryTrack)
	d.lastRelease = make(map[string]int)
}

// ResetSource preserves a terminal diagnostic before dropping old-source
// state. Plain Reset stays silent for a fresh match or detached collector.
func (d *Throw006) ResetSource() {
	d.cancelAll("trajectory_source_changed")
	d.Reset()
}
func (d *Throw006) SetWeight(float64)                                      { d.Weight = 0 }
func (d *Throw006) SetAutoEnforce(bool)                                    { d.IsAutoEnforce = false }
func (d *Throw006) AutoEnforce() bool                                      { return false }
func (d *Throw006) DefaultEnforcementWeight() float64                      { return 0 }
func (d *Throw006) SetMechanicsObserver(observer detect.MechanicsObserver) { d.observer = observer }
func (d *Throw006) Configure(params map[string]any) error {
	d.minTrajectoryChange = flightBound(detect.GetFloat(params, "min_trajectory_change", d.minTrajectoryChange), .1, 180, 8)
	d.maxCumulativeChange = flightBound(detect.GetFloat(params, "max_cumulative_change", d.maxCumulativeChange), 1, 3600, 130)
	d.minDistFromThrower = flightBound(detect.GetFloat(params, "min_distance_from_thrower", d.minDistFromThrower), 0, 20, 2)
	d.postReleaseFrames = detect.GetInt(params, "post_release_frames", d.postReleaseFrames)
	if d.postReleaseFrames < 5 || d.postReleaseFrames > 120 {
		d.postReleaseFrames = 15
	}
	return nil
}
func flightBound(value, low, high, fallback float64) float64 {
	if !mechanicsFinite(value) || value < low || value > high {
		return fallback
	}
	return value
}

func (d *Throw006) Evaluate(mc *model.MatchContext, players map[string]*model.PlayerState, frame int) []model.DetectionEvent {
	active := detect.ActivePlayers(players, frame)
	if len(active) > shotReviewPlayerLimit {
		d.cancelAll("trajectory_roster_unavailable")
		d.Reset()
		return nil
	}
	sample, reason := readFlightReview(active, frame)
	// First terminate preceding tracks. Releasing again cannot merge two
	// independent flights or carry a pre-contact bend into the next flight.
	for _, pid := range sortedKeys(d.activeThrows) {
		tr := d.activeThrows[pid]
		if reason != "" {
			d.finishTrack(pid, reason, false)
			continue
		}
		if t := throwAt(players[pid], frame); t != nil && t.FrameIndex != tr.releaseFrame {
			d.finishTrack(pid, "trajectory_release_replaced", false)
			continue
		}
		previous := tr.previous
		if sample.frame == previous.frame && reflect.DeepEqual(sample, previous) {
			continue
		}
		dt := sample.timestamp - previous.timestamp
		if sample.frame != previous.frame+1 || !mechanicsFinite(dt) || dt <= 0 || dt > .2 {
			d.finishTrack(pid, "trajectory_sample_gap", false)
			continue
		}
		if !previous.source.SameSource(sample.source) {
			d.finishTrack(pid, "trajectory_source_changed", false)
			continue
		}
		if sample.bounce != previous.bounce {
			d.finishTrack(pid, "trajectory_contact_observed", false)
			continue
		}
		previousSpeed, speed := previous.velocity.Magnitude(), sample.velocity.Magnitude()
		if !mechanicsFinite(previousSpeed) || !mechanicsFinite(speed) || previousSpeed <= .1 || speed <= .1 {
			d.finishTrack(pid, "trajectory_motion_unavailable", false)
			continue
		}
		angle := previous.velocity.AngleBetweenDeg(sample.velocity)
		ratio := speed / previousSpeed
		if !mechanicsFinite(angle) || (ratio < inelasticSpeedRatio && angle > inelasticMinAngle) || angle > elasticBounceAngle || (ratio > deflectionRatio && angle > deflectionMinAngle) {
			d.finishTrack(pid, "trajectory_contact_possible", false)
			continue
		}
		// Keep every accepted sample, including the near-release baselines
		// used by the next measured angle. Omitting those would make the
		// cumulative turn impossible to reproduce from retained evidence.
		if !appendFlightRaw(&tr.assessment, sample) {
			d.finishTrack(pid, "trajectory_evidence_capacity", false)
			continue
		}
		tr.previous = sample
		if sample.position.Distance(tr.releasePos) >= d.minDistFromThrower {
			tr.frameCount++
			tr.cumulativeAngle += angle
			tr.maxFrameAngle = math.Max(tr.maxFrameAngle, angle)
			if angle > d.minTrajectoryChange {
				tr.violationFrames++
			}
		}
		if frame-tr.releaseFrame >= d.postReleaseFrames {
			d.finishTrack(pid, "trajectory_window_complete", true)
		}
	}
	for _, ps := range active {
		t := throwAt(ps, frame)
		if t == nil || t.ReleaseWindow == nil {
			continue
		}
		if last, exists := d.lastRelease[ps.PlayerID]; exists && t.FrameIndex <= last {
			continue
		}
		d.lastRelease[ps.PlayerID] = t.FrameIndex
		assessment := releaseMechanicsAssessment(mc, ps, t, model.MechanicsThrowPhysics)
		assessment.Limitations = []string{
			"Sampled direction changes are not proof of in-flight steering, targeting assistance, or an unauthorized grab.",
			"Bend thresholds are provisional sampling filters, not verified engine bounds or independently calibrated accuracy.",
			"Obstacle geometry and complete authoritative contact events are unavailable; a stable bounce counter cannot prove a contact-free path.",
			"No goal-plane, pocket, bank-shot or best-of-two-goals targeting model is applied.",
			"First-free velocity and later samples are observations, not verified instantaneous launch physics.",
		}
		// Reserve the configured continuous inspection window plus its initial
		// baseline. Optional oversized release context cannot evict middle-of-
		// flight inputs; retain its first entries and original release sample.
		contextLimit := model.MaxMechanicsRawSamples - d.postReleaseFrames - 1
		if len(assessment.RawSamples) > contextLimit {
			assessment.Metrics["context_raw_samples_omitted"] += float64(len(assessment.RawSamples) - contextLimit)
			last := assessment.RawSamples[len(assessment.RawSamples)-1]
			assessment.RawSamples = assessment.RawSamples[:contextLimit]
			assessment.RawSamples[len(assessment.RawSamples)-1] = last
		}
		track := &trajectoryTrack{throwerID: ps.PlayerID, releaseFrame: t.FrameIndex, releasePos: t.ReleasePosition, assessment: assessment, previous: sample}
		d.activeThrows[ps.PlayerID] = track
		switch {
		case !mechanicsReleaseKnown(ps, t):
			d.finishTrack(ps.PlayerID, "release_observation_unavailable", false)
		case reason != "":
			d.finishTrack(ps.PlayerID, reason, false)
		case !t.ReleaseWindow.Source.SameSource(sample.source):
			d.finishTrack(ps.PlayerID, "trajectory_source_changed", false)
		case t.PossibleHeadContact:
			d.finishTrack(ps.PlayerID, "trajectory_contact_possible", false)
		default:
			if !appendFlightRaw(&track.assessment, sample) {
				d.finishTrack(ps.PlayerID, "trajectory_evidence_capacity", false)
			}
		}
	}
	// Bound per-player identity state to the active roster. An absent thrower
	// cannot later receive a remote player's free-flight attribution.
	present := make(map[string]bool, len(active))
	for _, ps := range active {
		present[ps.PlayerID] = true
	}
	for pid := range d.lastRelease {
		if !present[pid] {
			d.finishTrack(pid, "trajectory_thrower_unavailable", false)
			delete(d.lastRelease, pid)
		}
	}
	return nil
}

func readFlightReview(players []*model.PlayerState, frame int) (flightReviewSample, string) {
	var out flightReviewSample
	var chosen *model.DiscState
	if len(players) == 0 {
		return out, "trajectory_roster_unavailable"
	}
	for _, ps := range players {
		disc := ps.CurrentDisc
		if disc == nil || !disc.Attachment.Known() {
			return out, "trajectory_attachment_unknown"
		}
		if !disc.Attachment.Free() {
			return out, "trajectory_disc_held"
		}
		if ps.DiscAttachment != nil && !reflect.DeepEqual(ps.DiscAttachment, disc.Attachment) {
			return out, "trajectory_attachment_unknown"
		}
		if disc.BounceCount == nil || *disc.BounceCount < 0 {
			return out, "trajectory_contact_unavailable"
		}
		if disc.Position.HasNaN() || disc.Position.HasInf() || disc.Velocity.HasNaN() || disc.Velocity.HasInf() || !mechanicsFinite(ps.LastTimestamp) || ps.LastTimestamp < 0 {
			return out, "trajectory_motion_unavailable"
		}
		if !ps.Observation.Valid() || ps.Observation.FrameIndex != frame || ps.Observation.Timestamp != ps.LastTimestamp {
			return out, "trajectory_source_unavailable"
		}
		if disc.SampledPlayerCount != len(players) {
			return out, "trajectory_roster_unavailable"
		}
		if chosen == nil {
			chosen = disc
			out = flightReviewSample{frame: frame, timestamp: ps.LastTimestamp, position: disc.Position, velocity: disc.Velocity, bounce: *disc.BounceCount, source: ps.Observation.Clone()}
		} else if disc.Position != out.position || disc.Velocity != out.velocity || *disc.BounceCount != out.bounce || ps.LastTimestamp != out.timestamp || !out.source.SameSource(ps.Observation) {
			return out, "trajectory_snapshot_conflict"
		}
	}
	return out, ""
}

func appendFlightRaw(record *model.MechanicsAssessment, sample flightReviewSample) bool {
	bounce := sample.bounce
	raw := model.MechanicsRawSample{SampleRole: "flight_sample", FrameIndex: sample.frame, Timestamp: sample.timestamp, DiscPosition: mechanicsVector(sample.position), DiscVelocity: mechanicsVector(sample.velocity), BounceCount: &bounce, Attachment: "free"}
	if len(record.RawSamples) < model.MaxMechanicsRawSamples {
		record.RawSamples = append(record.RawSamples, raw)
		return true
	}
	if record.Metrics == nil {
		record.Metrics = make(map[string]float64)
	}
	record.Metrics["raw_samples_omitted"]++
	return false
}

func (d *Throw006) finishTrack(pid, reason string, complete bool) {
	tr := d.activeThrows[pid]
	if tr == nil {
		return
	}
	delete(d.activeThrows, pid)
	r := tr.assessment.Clone()
	if r.Metrics == nil {
		r.Metrics = make(map[string]float64)
	}
	r.Reason = reason
	r.Metrics["tracked_free_samples"] = float64(tr.frameCount)
	r.Metrics["sampled_cumulative_turn_deg"] = tr.cumulativeAngle
	r.Metrics["sampled_max_turn_deg"] = tr.maxFrameAngle
	r.Metrics["above_filter_sample_count"] = float64(tr.violationFrames)
	r.Metrics["sample_turn_filter_deg"] = d.minTrajectoryChange
	r.Metrics["cumulative_turn_filter_deg"] = d.maxCumulativeChange
	r.Metrics["min_distance_from_release_m"] = d.minDistFromThrower
	r.Metrics["configured_post_release_frames"] = float64(d.postReleaseFrames)
	inspected := 0
	for _, sample := range r.RawSamples {
		if sample.SampleRole != "flight_sample" {
			continue
		}
		if inspected == 0 {
			r.Metrics["inspection_start_frame"], r.Metrics["inspection_start_time_s"] = float64(sample.FrameIndex), sample.Timestamp
		}
		r.Metrics["inspection_end_frame"], r.Metrics["inspection_end_time_s"] = float64(sample.FrameIndex), sample.Timestamp
		inspected++
	}
	r.Metrics["inspection_sample_count"] = float64(inspected)
	if tr.previous.timestamp >= r.Timestamp {
		r.Metrics["observed_free_duration_s"] = tr.previous.timestamp - r.Timestamp
	}
	if complete && tr.violationFrames >= minViolationFrames && tr.frameCount >= minTrackedFrames && tr.cumulativeAngle > d.maxCumulativeChange {
		r.Result, r.Reason = model.MechanicsAnomaly, "trajectory_sampled_bend_anomaly"
	}
	if d.observer != nil {
		d.observer(d.ID(), pid, r)
	}
}

func (d *Throw006) cancelAll(reason string) {
	for _, pid := range sortedKeys(d.activeThrows) {
		d.finishTrack(pid, reason, false)
	}
}
func (d *Throw006) FlushTracks(_ *model.MatchContext, _ int) []model.DetectionEvent {
	d.cancelAll("trajectory_end_of_stream")
	return nil
}
