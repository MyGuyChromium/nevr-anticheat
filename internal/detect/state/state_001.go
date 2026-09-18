package state

import (
	"crypto/sha256"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/mechanics"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	grabReviewPlayerLimit  = 16
	grabReviewHistoryLimit = 8
	grabReviewActivePhase  = "active_play"
)

// State001 reviews explicitly observed disc acquisitions against the project
// rule. Current feeds do not verify grab geometry, acquisition timing or error
// bounds, so they produce inconclusive diagnostics and NEVER scored events.
// Player/geometry grips, sticky possession, first-seen held discs and same-player
// hand transfers are not treated as disc acquisitions.
// Direct changes between different known holders are retained separately as
// inconclusive transfers: no unobserved free-flight sample is invented.
//
// Threat model (docs/threat_model.md; Windows build 34.4.631547 only; Quest
// not checked). The native server's ownership arbiter validates session,
// sender handle, sequence, a per-team move permission, a per-player blocking
// state and stale owner/stamp. It takes no hand or disc position and performs
// no distance, velocity or line-of-sight check, so a geometry review on our
// side is the only defence against a long-range grab. That makes this review
// necessary; it does not make it verified, and it stays an unscored
// diagnostic. Two native behaviours shape what it may conclude:
//   - a direct holder-to-holder transfer is a native arbiter path, so it is
//     not by itself evidence of tampering and stays inconclusive;
//   - clients predict ownership locally before the server answers, so a
//     recording can show a brief possession that was then rolled back. Such a
//     blip must not be counted as two acquisitions.
type State001 struct {
	detect.BaseDetector
	previous map[string]grabReviewSample
	history  map[string][]grabReviewSample
	pending  map[string]pendingGrabReview
	phase    string
	observer detect.MechanicsObserver
}

type pendingGrabReview struct {
	assessment model.MechanicsAssessment
	firstHeld  grabReviewSample
	phase      string
	transfer   bool
}

type grabReviewSample struct {
	raw         model.MechanicsRawSample
	attachment  *model.DiscAttachment
	observation *model.ObservationContext
}

func NewState001(_ map[string]any) *State001 {
	d := &State001{BaseDetector: detect.BaseDetector{
		DetectorID: "STATE_001", DetectorVersion: "3.1.2", DetectorName: "Disc Grab Geometry Review",
		DetectorCategory: "state", Inputs: []string{"disc_attachment", "hand_tracking", "disc_state"},
		Warmup: 0, Weight: 0, IsAutoEnforce: false,
	}}
	d.Reset()
	return d
}

func (d *State001) Reset() {
	d.previous = make(map[string]grabReviewSample)
	d.history = make(map[string][]grabReviewSample)
	d.pending = make(map[string]pendingGrabReview)
	d.phase = ""
}
func (d *State001) Configure(map[string]any) error                  { return nil }
func (d *State001) SetWeight(float64)                               { d.Weight = 0 }
func (d *State001) SetAutoEnforce(bool)                             { d.IsAutoEnforce = false }
func (d *State001) AutoEnforce() bool                               { return false }
func (d *State001) DefaultEnforcementWeight() float64               { return 0 }
func (d *State001) SetMechanicsObserver(o detect.MechanicsObserver) { d.observer = o }

func (d *State001) Evaluate(mc *model.MatchContext, players map[string]*model.PlayerState, frame int) []model.DetectionEvent {
	return d.evaluateReview(mc, players, frame, grabReviewActivePhase)
}

// EvaluateReviewPhase retains non-play attachment observations without running
// legality checks or detector scoring. The pipeline supplies the actual phase;
// it must not call ordinary Evaluate for the same non-active snapshot.
func (d *State001) EvaluateReviewPhase(mc *model.MatchContext, players map[string]*model.PlayerState, frame int, phase string) []model.DetectionEvent {
	phase = strings.TrimSpace(phase)
	if phase == "" || len(phase) > 64 || phase == grabReviewActivePhase || model.IsActiveGamePhase(phase) {
		phase = "non_active_unspecified"
	}
	return d.evaluateReview(mc, players, frame, phase)
}

func (d *State001) evaluateReview(mc *model.MatchContext, players map[string]*model.PlayerState, frame int, phase string) []model.DetectionEvent {
	if d.phase != "" && d.phase != phase {
		d.finishAllGrabs("grab_possession_unconfirmed_phase_changed", frame)
		d.previous = make(map[string]grabReviewSample)
		d.history = make(map[string][]grabReviewSample)
	}
	d.phase = phase
	active := detect.ActivePlayers(players, frame)
	if len(active) > grabReviewPlayerLimit {
		d.finishAllGrabs("grab_possession_unconfirmed_roster_unavailable", frame)
		d.Reset()
		return nil
	}
	present := make(map[string]bool, len(active))
	// Validate the entire identity set before consuming any row. Otherwise one
	// of two conflicting copies can confirm a pending acquisition simply by
	// being visited first (equal-ID ordering is not a reliable tie breaker).
	for _, ps := range active {
		if ps.PlayerID == "" || present[ps.PlayerID] {
			d.finishAllGrabs("grab_possession_unconfirmed_roster_unavailable", frame)
			d.Reset()
			return nil
		}
		present[ps.PlayerID] = true
	}
	for _, ps := range active {
		id := ps.PlayerID
		now := readGrabReview(ps, frame)
		previous, exists := d.previous[id]
		if exists && now.raw.FrameIndex == previous.raw.FrameIndex && now.raw.Timestamp == previous.raw.Timestamp && reflect.DeepEqual(now, previous) {
			continue // exact duplicate snapshot, not another acquisition
		}
		if exists && now.raw.FrameIndex <= previous.raw.FrameIndex {
			d.finishGrab(id, "grab_possession_unconfirmed_duplicate_conflict", frame, nil, false)
			delete(d.previous, id) // conflicting duplicate/out-of-order state breaks continuity
			delete(d.history, id)
			continue
		}
		if pending, ok := d.pending[id]; ok {
			reason := grabConfirmationReason(id, pending.firstHeld, now)
			var terminal *grabReviewSample
			if grabReviewContinuous(pending.firstHeld, now) {
				terminal = &now
			}
			d.finishGrab(id, reason, frame, terminal, reason == "")
		}
		d.previous[id] = now
		continuous := exists && grabReviewContinuous(previous, now)
		if !continuous || !grabSameAttachment(previous.attachment, now.attachment) {
			// No interpolation across a catch, transfer, unknown state or gap.
			// A candidate below takes its baseline before this new run starts.
			priorHistory := d.history[id]
			d.history[id] = nil
			if continuous && now.attachment.HeldBy(id) && (previous.attachment.Free() ||
				(previous.attachment.Known() && previous.attachment.State == "held" && previous.attachment.HolderID != id)) {
				d.beginGrab(mc, id, previous, now, priorHistory, phase)
			}
		}
		if !now.attachment.Known() || !grabRawTimeValid(now.raw) {
			delete(d.history, id)
			continue
		}
		history := append(d.history[id], now)
		if len(history) > grabReviewHistoryLimit {
			copy(history, history[len(history)-grabReviewHistoryLimit:])
			history = history[:grabReviewHistoryLimit]
		}
		d.history[id] = history
	}
	for _, id := range sortedGrabKeys(d.previous) {
		if !present[id] {
			d.finishGrab(id, "grab_possession_unconfirmed_player_unavailable", frame, nil, false)
			delete(d.previous, id)
			delete(d.history, id)
		}
	}
	return nil
}

func grabRawTimeValid(raw model.MechanicsRawSample) bool {
	return raw.FrameIndex >= 0 && !math.IsNaN(raw.Timestamp) && !math.IsInf(raw.Timestamp, 0) && raw.Timestamp >= 0
}

func grabSameAttachment(a, b *model.DiscAttachment) bool {
	return a.Known() && b.Known() && a.State == b.State && a.HolderID == b.HolderID
}

func grabConfirmationReason(player string, first, now grabReviewSample) string {
	if !first.observation.Valid() || !now.observation.Valid() {
		return "grab_possession_unconfirmed_source_unavailable"
	}
	if !first.observation.SameSource(now.observation) {
		return "grab_possession_unconfirmed_source_changed"
	}
	if !grabReviewContinuous(first, now) {
		return "grab_possession_unconfirmed_sample_gap"
	}
	if !now.attachment.Known() {
		return "grab_possession_unconfirmed_attachment_unknown"
	}
	if !now.attachment.HeldBy(player) {
		return "grab_possession_unconfirmed_holder_changed"
	}
	return ""
}

func (d *State001) beginGrab(mc *model.MatchContext, player string, previous, first grabReviewSample, history []grabReviewSample, phase string) {
	r := grabAssessment(mc, player, previous, first)
	if len(history) == 0 {
		history = []grabReviewSample{previous}
	}
	r.RawSamples = make([]model.MechanicsRawSample, 0, len(history)+2)
	for _, sample := range history {
		r.RawSamples = append(r.RawSamples, sample.raw)
	}
	r.RawSamples = append(r.RawSamples, first.raw)
	r.Metrics["pre_acquisition_samples"] = float64(len(history))
	r.Metrics["pre_acquisition_start_frame"] = float64(history[0].raw.FrameIndex)
	r.Metrics["first_held_frame"] = float64(first.raw.FrameIndex)
	r.Metrics["sampled_possession_confirmed"] = 0
	r.Metrics["review_active_phase"] = 0
	if phase == grabReviewActivePhase {
		r.Metrics["review_active_phase"] = 1
	}
	transfer := previous.attachment.Known() && previous.attachment.State == "held" && previous.attachment.HolderID != player
	r.Metrics["direct_holder_transfer"] = 0
	if transfer {
		r.Metrics["direct_holder_transfer"] = 1
	}
	r.Limitations = append(r.Limitations, "A second consecutive same-holder sample confirms only sampled possession, not the server's ownership grant or legal acquisition geometry.")
	if phase != grabReviewActivePhase {
		r.Limitations = append(r.Limitations, fmt.Sprintf("Recorded game phase: %s. This is a non-play attachment observation; gameplay legality is not evaluated and stale/reset disc geometry remains possible.", phase))
	}
	d.pending[player] = pendingGrabReview{assessment: r.Clone(), firstHeld: first, phase: phase, transfer: transfer}
	if !first.observation.Valid() {
		d.finishGrab(player, "grab_possession_unconfirmed_source_unavailable", first.raw.FrameIndex, nil, false)
	}
}

func (d *State001) finishGrab(player, reason string, frame int, terminal *grabReviewSample, confirmed bool) {
	pending, ok := d.pending[player]
	if !ok {
		return
	}
	delete(d.pending, player)
	r := pending.assessment.Clone()
	if frame < r.FrameIndex {
		frame = r.FrameIndex
	}
	r.Metrics["review_end_frame"] = float64(frame)
	if terminal != nil && grabRawTimeValid(terminal.raw) {
		r.RawSamples = append(r.RawSamples, terminal.raw)
	}
	if confirmed {
		r.Metrics["sampled_possession_confirmed"] = 1
		r.Metrics["confirmation_frame"] = float64(frame)
		r.Metrics["confirmation_time_s"] = terminal.raw.Timestamp
		if !pending.transfer {
			r.ReasonDescription = "The recorded holder persisted on a second consecutive sample. Grab timing, geometry and legality remain unverified."
		}
		if pending.phase != grabReviewActivePhase {
			r.Reason = "grab_non_play_observation"
			if pending.transfer {
				r.Reason = "grab_non_play_transfer_observation"
			}
			r.ReasonDescription = "Sampled possession persisted outside active play; this is retained for review only, not adjudicated as a gameplay violation."
		}
	} else {
		r.Reason = reason
		r.ReasonDescription = grabUnconfirmedDescription(reason)
		if pending.transfer {
			r.ReasonDescription += " The original transition was a direct holder transfer, not an observed free-disc acquisition."
		}
		if pending.phase != grabReviewActivePhase {
			r.ReasonDescription += " It occurred outside active play; gameplay legality was not evaluated."
		}
	}
	r.Result = model.MechanicsInconclusive
	if d.observer != nil {
		d.observer(d.ID(), player, r.Clone())
	}
}

func grabUnconfirmedDescription(reason string) string {
	switch reason {
	case "grab_possession_unconfirmed_holder_changed":
		return "The next sample did not retain the same holder; this may be a prediction rollback, brief touch or transfer, not a confirmed sampled possession."
	case "grab_possession_unconfirmed_attachment_unknown":
		return "Attachment became unavailable before sampled possession could be confirmed."
	case "grab_possession_unconfirmed_source_changed":
		return "The recording source changed before sampled possession could be confirmed; sources were not combined."
	case "grab_possession_unconfirmed_source_unavailable":
		return "Source provenance was unavailable, so sampled possession could not be confirmed continuously."
	case "grab_possession_unconfirmed_sample_gap":
		return "A frame or time discontinuity interrupted sampled possession confirmation."
	case "grab_possession_unconfirmed_duplicate_conflict":
		return "Conflicting or out-of-order observations interrupted sampled possession confirmation."
	case "grab_possession_unconfirmed_phase_changed":
		return "The game phase changed before sampled possession could be confirmed; phase intervals were not combined."
	case "grab_possession_unconfirmed_roster_unavailable":
		return "The sampled roster had missing or duplicate identities, or exceeded the bounded review capacity before possession confirmation."
	case "grab_possession_unconfirmed_player_unavailable":
		return "The player had no fresh sample before possession confirmation."
	case "grab_possession_unconfirmed_end_of_stream":
		return "The recording ended before a second consecutive same-holder sample; possession remains unconfirmed."
	default:
		return "The observed holder transition was not confirmed by a second consecutive same-holder sample."
	}
}

func (d *State001) finishAllGrabs(reason string, frame int) {
	for _, player := range sortedGrabKeys(d.pending) {
		d.finishGrab(player, reason, frame, nil, false)
	}
}

func (d *State001) FlushTracks(_ *model.MatchContext, frame int) []model.DetectionEvent {
	d.finishAllGrabs("grab_possession_unconfirmed_end_of_stream", frame)
	d.Reset() // finalization is a continuity boundary, even if this instance is reused
	return nil
}
func (d *State001) FlushSource(_ *model.MatchContext, frame int) []model.DetectionEvent {
	d.finishAllGrabs("grab_possession_unconfirmed_source_changed", frame)
	d.Reset()
	return nil
}
func (d *State001) ResetSource() { d.FlushSource(nil, -1) }
func (d *State001) FlushPhase(_ *model.MatchContext, frame int) []model.DetectionEvent {
	d.finishAllGrabs("grab_possession_unconfirmed_phase_changed", frame)
	d.Reset()
	return nil
}

func sortedGrabKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func readGrabReview(ps *model.PlayerState, frame int) grabReviewSample {
	s := grabReviewSample{raw: model.MechanicsRawSample{FrameIndex: frame, Timestamp: ps.LastTimestamp}, observation: ps.Observation.Clone()}
	s.attachment = ps.DiscAttachment.Clone()
	if ps.CurrentDisc != nil {
		if s.attachment == nil {
			s.attachment = ps.CurrentDisc.Attachment.Clone()
		} else if ps.CurrentDisc.Attachment != nil && !reflect.DeepEqual(s.attachment, ps.CurrentDisc.Attachment) {
			s.attachment = nil // conflicting explicit observations are unknown
		}
		if ps.CurrentDisc.PossessionConflict {
			s.attachment = nil // explicit unresolved ownership cannot confirm possession
		}
		s.raw.DiscPosition = grabVector(ps.CurrentDisc.Position, false)
		s.raw.DiscVelocity = grabVector(ps.CurrentDisc.Velocity, false)
	}
	s.raw.LeftHand, s.raw.RightHand = grabVector(ps.LeftHand, true), grabVector(ps.RightHand, true)
	s.raw.PlayerPosition = grabVector(ps.Position, true)
	s.raw.PlayerVelocity = grabVector(ps.Velocity, false)
	if ps.HasReportedVelocity {
		s.raw.ReportedVelocity = grabVector(ps.ReportedVelocity, false)
	}
	if s.attachment.Known() {
		s.raw.Attachment = s.attachment.State
	} else {
		s.raw.Attachment = "unknown"
	}
	if ps.HeldItems != nil {
		if ps.HeldItems.Left != nil {
			s.raw.LeftHolding = *ps.HeldItems.Left
		}
		if ps.HeldItems.Right != nil {
			s.raw.RightHolding = *ps.HeldItems.Right
		}
	}
	return s
}

func grabVector(value model.Vec3, zeroUnavailable bool) *model.Vec3 {
	if value.HasNaN() || value.HasInf() || (zeroUnavailable && value.IsZero()) {
		return nil
	}
	copy := value
	return &copy
}

func grabReviewContinuous(previous, now grabReviewSample) bool {
	dt := now.raw.Timestamp - previous.raw.Timestamp
	// This is an engineering sampling continuity gate, NOT a legal grab bound.
	if !grabRawTimeValid(previous.raw) || !grabRawTimeValid(now.raw) ||
		now.raw.FrameIndex != previous.raw.FrameIndex+1 || math.IsNaN(dt) || math.IsInf(dt, 0) || dt <= 0 || dt > .2 {
		return false
	}
	if previous.observation == nil && now.observation == nil {
		return true // source unavailable is retained as an explicit limitation
	}
	return previous.observation.SameSource(now.observation) &&
		previous.observation.FrameIndex == previous.raw.FrameIndex && previous.observation.Timestamp == previous.raw.Timestamp &&
		now.observation.FrameIndex == now.raw.FrameIndex && now.observation.Timestamp == now.raw.Timestamp
}

func grabAssessment(mc *model.MatchContext, player string, previous, now grabReviewSample) model.MechanicsAssessment {
	rules := model.DefaultProjectRules()
	matchID, source := "", "unknown"
	if mc != nil {
		matchID, source = mc.MatchID, mc.Source
		if mc.ProjectRules.Version != "" {
			rules = mc.ProjectRules
		}
	}
	out := model.MechanicsAssessment{FrameIndex: now.raw.FrameIndex, Timestamp: now.raw.Timestamp,
		PlayerID: player, SessionID: matchID, IntervalStart: previous.raw.Timestamp, IntervalEnd: now.raw.Timestamp,
		Hand: strings.Join(now.attachment.HandCandidates, "|"), Source: source, Authority: "unverified", CoordinateSpace: "reported_world",
		Metrics:    map[string]float64{"acquisition_interval_s": now.raw.Timestamp - previous.raw.Timestamp},
		RawSamples: []model.MechanicsRawSample{previous.raw, now.raw},
		Limitations: []string{
			"The 0.25 m project rule has no verified engine-build geometry definition; tracked hand origins and disc centers are descriptive only.",
			"The acquisition instant lies between sampled states; authoritative timing and measurement error bounds are unavailable.",
			"The first held position may include attachment snap and is never interpolated with the free position.",
			"Reported source metadata does not establish engine authority or prove the cause of an observed acquisition.",
		},
	}
	if observation := now.observation; observation != nil {
		out.Source, out.Authority, out.SessionID, out.TimeBasis = observation.Source, observation.Authority, observation.SessionID, observation.TimeBasis
	}
	// Source instance details affect identity but are hashed, not exposed as
	// raw endpoint/path metadata in the public assessment.
	sourceID, sourcePlayer := "", ""
	var sourceEpoch uint64
	if now.observation != nil {
		sourceID, sourcePlayer = now.observation.SourceID, now.observation.SourcePlayerID
		sourceEpoch = now.observation.SourceEpoch
	}
	identity := fmt.Sprintf("%q|%q|%q|%q|%q|%q|%q|%q|%q|%d|%d|%.17g", matchID, out.Source, sourceID, sourcePlayer, out.SessionID, out.Authority, out.TimeBasis, player, "disc_acquisition", sourceEpoch, now.raw.FrameIndex, now.raw.Timestamp)
	out.EventID = fmt.Sprintf("grab:%x", sha256.Sum256([]byte(identity)))
	previousName := "last_free"
	transfer := previous.attachment.Known() && previous.attachment.State == "held" && previous.attachment.HolderID != player
	if transfer {
		previousName = "previous_held"
		out.Limitations = append(out.Limitations, "The disc changed recorded holders without a free-flight sample; a legal handoff, steal or unsampled release cannot be distinguished and grab range cannot be reconstructed.")
	}
	for _, snapshot := range []struct {
		name string
		raw  model.MechanicsRawSample
	}{{previousName, previous.raw}, {"first_held", now.raw}} {
		if snapshot.raw.DiscPosition == nil {
			continue
		}
		for _, hand := range []struct {
			name     string
			position *model.Vec3
		}{{"left", snapshot.raw.LeftHand}, {"right", snapshot.raw.RightHand}} {
			if hand.position != nil {
				distance := hand.position.Distance(*snapshot.raw.DiscPosition)
				if !math.IsNaN(distance) && !math.IsInf(distance, 0) {
					out.Metrics[snapshot.name+"_"+hand.name+"_origin_to_disc_center_m"] = distance
				}
			}
		}
	}
	// No runtime metadata/config value can construct verified GrabKnowledge.
	out = mechanics.EvaluateGrab(mechanics.GrabInput{Rules: rules, Assessment: out})
	if transfer {
		out.Result, out.Reason = model.MechanicsInconclusive, "grab_transfer_without_free_sample"
		out.ReasonDescription = "Disc changed recorded holders without a free-flight sample. This may be a legal handoff or steal; grab range cannot be reconstructed."
	}
	return out
}
