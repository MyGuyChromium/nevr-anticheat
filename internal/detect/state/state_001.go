package state

import (
	"crypto/sha256"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/mechanics"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const grabReviewPlayerLimit = 16

// State001 reviews explicitly observed disc acquisitions against the project
// rule. Current feeds do not verify grab geometry, acquisition timing or error
// bounds, so they produce inconclusive diagnostics and NEVER scored events.
// Player/geometry grips, sticky possession, first-seen held discs and same-player
// hand transfers are not treated as disc acquisitions.
// Direct changes between different known holders are retained separately as
// inconclusive transfers: no unobserved free-flight sample is invented.
type State001 struct {
	detect.BaseDetector
	previous map[string]grabReviewSample
	observer detect.MechanicsObserver
}

type grabReviewSample struct {
	raw         model.MechanicsRawSample
	attachment  *model.DiscAttachment
	observation *model.ObservationContext
}

func NewState001(_ map[string]any) *State001 {
	d := &State001{BaseDetector: detect.BaseDetector{
		DetectorID: "STATE_001", DetectorVersion: "3.0.1", DetectorName: "Disc Grab Geometry Review",
		DetectorCategory: "state", Inputs: []string{"disc_attachment", "hand_tracking", "disc_state"},
		Warmup: 0, Weight: 0, IsAutoEnforce: false,
	}}
	d.Reset()
	return d
}

func (d *State001) Reset()                                          { d.previous = make(map[string]grabReviewSample) }
func (d *State001) Configure(map[string]any) error                  { return nil }
func (d *State001) SetWeight(float64)                               { d.Weight = 0 }
func (d *State001) SetAutoEnforce(bool)                             { d.IsAutoEnforce = false }
func (d *State001) AutoEnforce() bool                               { return false }
func (d *State001) DefaultEnforcementWeight() float64               { return 0 }
func (d *State001) SetMechanicsObserver(o detect.MechanicsObserver) { d.observer = o }

func (d *State001) Evaluate(mc *model.MatchContext, players map[string]*model.PlayerState, frame int) []model.DetectionEvent {
	active := detect.ActivePlayers(players, frame)
	if len(active) > grabReviewPlayerLimit {
		d.Reset()
		return nil
	}
	present := make(map[string]bool, len(active))
	for _, ps := range active {
		id := ps.PlayerID
		present[id] = true
		now := readGrabReview(ps, frame)
		previous, exists := d.previous[id]
		if exists && now.raw.FrameIndex == previous.raw.FrameIndex && now.raw.Timestamp == previous.raw.Timestamp && reflect.DeepEqual(now, previous) {
			continue // exact duplicate snapshot, not another acquisition
		}
		if exists && now.raw.FrameIndex <= previous.raw.FrameIndex {
			delete(d.previous, id) // conflicting duplicate/out-of-order state breaks continuity
			continue
		}
		d.previous[id] = now
		if !exists || !now.attachment.HeldBy(id) || !grabReviewContinuous(previous, now) {
			continue
		}
		previousOtherHolder := previous.attachment.Known() && previous.attachment.State == "held" && previous.attachment.HolderID != id
		if !previous.attachment.Free() && !previousOtherHolder {
			continue
		}
		if d.observer != nil {
			d.observer(d.ID(), id, grabAssessment(mc, id, previous, now))
		}
	}
	for id := range d.previous {
		if !present[id] {
			delete(d.previous, id)
		}
	}
	return nil
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
	if now.raw.FrameIndex != previous.raw.FrameIndex+1 || math.IsNaN(dt) || math.IsInf(dt, 0) || dt <= 0 || dt > .2 || previous.raw.Timestamp < 0 {
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
	if now.observation != nil {
		sourceID, sourcePlayer = now.observation.SourceID, now.observation.SourcePlayerID
	}
	identity := fmt.Sprintf("%q|%q|%q|%q|%q|%q|%q|%q|%q|%d|%.17g", matchID, out.Source, sourceID, sourcePlayer, out.SessionID, out.Authority, out.TimeBasis, player, "disc_acquisition", now.raw.FrameIndex, now.raw.Timestamp)
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
