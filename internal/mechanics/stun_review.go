package mechanics

import (
	"fmt"
	"math"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// These are bounded evidence-association budgets, not legal stun timing/range
// rules. Client state and cumulative counters do not identify a contact pair.
const (
	stunAssociationSeconds = .25
	stunPlayerLimit        = 16
	stunCounterEdgeLimit   = 128
	stunReviewVersion      = "stun-contact-review-v1"
)

type stunSample struct {
	raw  model.MechanicsRawSample
	team string
}

type stunSnapshot struct {
	frame   int
	time    float64
	match   string
	source  *model.ObservationContext // private source identity; never serialized
	players map[string]stunSample
}

type stunCounterEdge struct{ before, after stunSample }

type stunPending struct {
	record                model.MechanicsAssessment
	team                  string
	windowStart, deadline float64
	candidates            map[string]stunCounterEdge
	counterMissing        bool
	teamMissing           bool
	evidenceLimited       bool
}

// StunReviewer emits only inconclusive MechanicsAssessments. It never returns
// DetectionEvents, chooses a nearest attacker, or compares sampled distances to
// a summed collider radius. The caller must Flush on inactive phases and EOF.
type StunReviewer struct {
	previous     *stunSnapshot
	recent       []stunCounterEdge
	pending      map[string]*stunPending
	missingSince map[string]float64
	limitedUntil float64
}

func NewStunReviewer() *StunReviewer {
	r := &StunReviewer{}
	r.Reset()
	return r
}

func (r *StunReviewer) Reset() {
	r.previous, r.recent = nil, nil
	r.pending = make(map[string]*stunPending)
	r.missingSince = make(map[string]float64)
	r.limitedUntil = -1
}

// Flush retains incomplete onset reviews exactly once and clears cross-window
// state. Reasons are fixed codes: arbitrary caller strings never reach reports.
func (r *StunReviewer) Flush(reason string) []model.MechanicsAssessment {
	switch reason {
	case "stun_inactive_phase", "stun_end_of_stream", "stun_sample_gap", "stun_source_changed",
		"stun_source_unavailable", "stun_roster_changed", "stun_roster_unavailable", "stun_clock_unavailable", "stun_counter_reset":
	default:
		reason = "stun_review_interrupted"
	}
	var out []model.MechanicsAssessment
	for _, id := range stunPendingIDs(r.pending) {
		out = append(out, r.finish(id, reason))
	}
	r.Reset()
	return out
}

func (r *StunReviewer) Observe(mc *model.MatchContext, players map[string]*model.PlayerState, frame int) []model.MechanicsAssessment {
	if r.pending == nil {
		r.Reset()
	}
	next, reason := readStunSnapshot(mc, players, frame)
	if reason != "" {
		return r.Flush(reason)
	}
	var out []model.MechanicsAssessment
	if r.previous != nil {
		prev := r.previous
		switch {
		case prev.match != next.match || !prev.source.SameSource(next.source):
			reason = "stun_source_changed"
		case frame == prev.frame && next.time == prev.time:
			// A duplicate dispatch cannot extend or create evidence.
			return nil
		case frame <= prev.frame || next.time <= prev.time:
			reason = "stun_clock_unavailable"
		case frame != prev.frame+1 || next.time-prev.time > stunAssociationSeconds:
			reason = "stun_sample_gap"
		case !sameStunRoster(prev, &next):
			reason = "stun_roster_changed"
		}
		if reason != "" {
			out = append(out, r.Flush(reason)...)
		}
	}
	for id, sample := range next.players {
		if sample.raw.Stuns == nil {
			r.missingSince[id] = next.time
		}
	}
	if r.previous == nil {
		r.previous = &next
		return out
	}
	prev := r.previous
	for id, after := range next.players {
		before := prev.players[id]
		if before.raw.Stuns != nil && after.raw.Stuns != nil && *after.raw.Stuns < *before.raw.Stuns {
			// A reset ends the entire association interval. Do not stitch
			// cumulative counts from two different counter epochs together.
			out = append(out, r.Flush("stun_counter_reset")...)
			r.previous = &next
			return out
		}
	}
	var edges []stunCounterEdge
	for _, id := range stunSampleIDs(next.players) {
		before, after := prev.players[id], next.players[id]
		if before.raw.Stuns == nil || after.raw.Stuns == nil {
			r.missingSince[id] = next.time
			continue
		}
		if *after.raw.Stuns > *before.raw.Stuns {
			edges = append(edges, stunCounterEdge{before, after})
		}
	}
	// Only a short recent tail is needed for counters observed before an onset.
	kept := r.recent[:0]
	for _, edge := range r.recent {
		if edge.after.raw.Timestamp >= next.time-2*stunAssociationSeconds {
			kept = append(kept, edge)
		}
	}
	r.recent = append(kept, edges...)
	if len(r.recent) > stunCounterEdgeLimit {
		copy(r.recent, r.recent[len(r.recent)-stunCounterEdgeLimit:])
		r.recent = r.recent[:stunCounterEdgeLimit]
		r.limitedUntil = next.time + 2*stunAssociationSeconds
	}
	for _, id := range stunPendingIDs(r.pending) {
		pending := r.pending[id]
		r.collect(pending, edges, &next)
		if next.time >= pending.deadline {
			out = append(out, r.finish(id, ""))
		}
	}
	for _, id := range stunSampleIDs(next.players) {
		before, after := prev.players[id], next.players[id]
		// Unknown false is not a baseline; an already-stunned first frame is
		// not an observed onset. State and counter presence are independent.
		if before.raw.IsStunned == nil || after.raw.IsStunned == nil || *before.raw.IsStunned || !*after.raw.IsStunned {
			continue
		}
		if r.pending[id] != nil {
			out = append(out, r.finish(id, "stun_repeated_onset"))
		}
		pending := newStunPending(before, after, &next)
		r.pending[id] = pending
		r.collect(pending, r.recent, &next)
	}
	r.previous = &next
	return out
}

func (r *StunReviewer) collect(p *stunPending, edges []stunCounterEdge, snapshot *stunSnapshot) {
	p.evidenceLimited = p.evidenceLimited || snapshot.time <= r.limitedUntil
	for id, sample := range snapshot.players {
		if id == p.record.PlayerID {
			continue
		}
		if !stunPlayingTeam(p.team) || !stunPlayingTeam(sample.team) {
			p.teamMissing = true
			continue
		}
		if sample.team != p.team {
			if when, missing := r.missingSince[id]; missing && when >= p.windowStart && when <= p.deadline {
				p.counterMissing = true
			}
		}
	}
	for _, edge := range edges {
		id := edge.after.raw.PlayerID
		if id == p.record.PlayerID || !stunPlayingTeam(p.team) || !stunPlayingTeam(edge.after.team) || edge.after.team == p.team ||
			edge.after.raw.Timestamp < p.windowStart || edge.before.raw.Timestamp > p.deadline {
			continue
		}
		if current, exists := p.candidates[id]; exists {
			if edge.after.raw.FrameIndex > current.after.raw.FrameIndex && *edge.before.raw.Stuns < *current.after.raw.Stuns {
				// A counter reset hidden by missing samples cannot safely be
				// aggregated. Preserve the first raw candidate segment only.
				p.counterMissing = true
				continue
			}
			if edge.before.raw.FrameIndex < current.before.raw.FrameIndex {
				current.before = edge.before
			}
			if edge.after.raw.FrameIndex > current.after.raw.FrameIndex {
				current.after = edge.after
			}
			p.candidates[id] = current
		} else {
			p.candidates[id] = edge
		}
	}
}

func newStunPending(before, after stunSample, snapshot *stunSnapshot) *stunPending {
	a, b := before.raw, after.raw
	a.SampleRole, b.SampleRole = "stun_before", "stun_onset"
	source := snapshot.source
	record := model.MechanicsAssessment{
		Kind: model.MechanicsStunContact, Result: model.MechanicsInconclusive, RuleVersion: stunReviewVersion,
		FrameIndex: b.FrameIndex, Timestamp: b.Timestamp, PlayerID: b.PlayerID,
		EventID:       fmt.Sprintf("stun:%s:%d", b.PlayerID, b.FrameIndex),
		IntervalStart: a.Timestamp, IntervalEnd: b.Timestamp,
		Source: source.Source, Authority: source.Authority, SessionID: source.SessionID, TimeBasis: source.TimeBasis,
		CoordinateSpace: "arena_world", Object: "stun_contact", RawSamples: []model.MechanicsRawSample{a, b},
		RuleReference: model.DefaultGameRuleReference(),
		Metrics: map[string]float64{
			"association_window_seconds":   stunAssociationSeconds,
			"reference_left_hand_radius_m": .06, "reference_right_hand_radius_m": .06,
			"reference_head_radius_m": .15, "reference_punch_offset_y_m": -.04,
		},
		Limitations: []string{
			"A cumulative counter increment near a victim onset is an association candidate, not an identified attacker or punch.",
			"The 0.25 s association window is an engineering collection budget, not the game's legal stun window.",
			"Default punch dimensions are reference settings only; tracking origins, collider transforms and exact contact time are unverified.",
			"Replay timing, network delay, blocking, immunity and unsampled contacts prevent a reliable stun-radius verdict.",
			"Raw head and hand positions are observations, not reconstructed collider centers; no summed-radius legality test is applied.",
			"Counter differences are cumulative reported changes, not a count of punches against this victim.",
		},
	}
	return &stunPending{record: record, team: after.team, windowStart: math.Max(0, a.Timestamp-stunAssociationSeconds),
		deadline: b.Timestamp + stunAssociationSeconds, candidates: make(map[string]stunCounterEdge)}
}

func (r *StunReviewer) finish(id, forcedReason string) model.MechanicsAssessment {
	p := r.pending[id]
	delete(r.pending, id)
	out := p.record
	ids := make([]string, 0, len(p.candidates))
	for candidate := range p.candidates {
		ids = append(ids, candidate)
	}
	sort.Strings(ids)
	countIncrements := float64(0)
	poseMissing := false
	for _, candidate := range ids {
		edge := p.candidates[candidate]
		before, after := edge.before.raw, edge.after.raw
		before.SampleRole, after.SampleRole = "stun_candidate_before", "stun_candidate_after"
		out.StunCandidates = append(out.StunCandidates, model.StunCandidate{
			PlayerID: candidate, Team: edge.after.team, FrameStart: before.FrameIndex, FrameEnd: after.FrameIndex,
			TimeStart: before.Timestamp, TimeEnd: after.Timestamp, CounterBefore: *before.Stuns, CounterAfter: *after.Stuns,
		})
		// Sum as floating point only after safe nonnegative integer
		// subtraction, so adversarial large counters cannot wrap int.
		countIncrements += float64(*after.Stuns - *before.Stuns)
		out.RawSamples = append(out.RawSamples, before, after)
	}
	for _, sample := range out.RawSamples {
		if sample.HeadPosition == nil || sample.LeftHand == nil || sample.RightHand == nil {
			poseMissing = true
		}
	}
	out.Metrics["candidate_players"] = float64(len(ids))
	out.Metrics["candidate_counter_increments"] = countIncrements
	out.Metrics["association_start_seconds"], out.Metrics["association_end_seconds"] = p.windowStart, p.deadline
	out.Reason = forcedReason
	if out.Reason == "" {
		switch {
		case p.evidenceLimited:
			out.Reason = "stun_evidence_limit"
		case p.teamMissing:
			out.Reason = "stun_team_unavailable"
		case p.counterMissing:
			out.Reason = "stun_counter_evidence_unavailable"
		case len(ids) == 0:
			out.Reason = "stun_no_counter_candidate"
		case len(ids) > 1 || countIncrements > 1:
			out.Reason = "stun_attribution_ambiguous"
		default:
			out.Reason = "stun_contact_geometry_unverified"
		}
	}
	if poseMissing {
		out.Limitations = append(out.Limitations, "At least one retained head or hand pose is missing, non-finite or reported as tracking loss; no body-position substitute was used.")
	}
	if p.counterMissing || p.teamMissing || p.evidenceLimited {
		out.Limitations = append(out.Limitations, "Candidate enumeration is incomplete because counters, team identity or bounded history were unavailable; missing candidates do not establish innocence.")
	}
	out.ReasonDescription = "Observed victim stun-state transition with bounded counter-timing candidates; attacker identity and legal contact geometry remain unverified."
	return out.Clone()
}

func readStunSnapshot(mc *model.MatchContext, players map[string]*model.PlayerState, frame int) (stunSnapshot, string) {
	out := stunSnapshot{frame: frame, players: make(map[string]stunSample)}
	if mc == nil || mc.MatchID == "" || frame < 0 {
		return out, "stun_source_unavailable"
	}
	out.match = mc.MatchID
	ids := make([]string, 0, stunPlayerLimit)
	for id, ps := range players {
		if ps != nil && ps.LastFrameIdx == frame {
			if len(ids) >= stunPlayerLimit {
				return out, "stun_roster_unavailable"
			}
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return out, "stun_roster_unavailable"
	}
	for _, id := range ids {
		ps := players[id]
		if id == "" || ps.PlayerID != id || len(id) > 128 || !finite(ps.LastTimestamp) || ps.LastTimestamp < 0 {
			return out, "stun_clock_unavailable"
		}
		if !ps.Observation.Valid() || ps.Observation.FrameIndex != frame || ps.Observation.Timestamp != ps.LastTimestamp {
			return out, "stun_source_unavailable"
		}
		for _, descriptor := range []string{ps.Observation.Source, ps.Observation.Authority, ps.Observation.SessionID, ps.Observation.TimeBasis} {
			if len(descriptor) > 256 {
				return out, "stun_source_unavailable"
			}
		}
		if out.source == nil {
			out.time, out.source = ps.LastTimestamp, ps.Observation.Clone()
		} else if out.time != ps.LastTimestamp || !out.source.SameSource(ps.Observation) {
			return out, "stun_source_unavailable"
		}
		raw := model.MechanicsRawSample{PlayerID: id, FrameIndex: frame, Timestamp: ps.LastTimestamp,
			PlayerPosition: stunVector(ps.Position), LeftHand: stunVector(ps.LeftHand), RightHand: stunVector(ps.RightHand)}
		if ps.HeadPosition != nil {
			raw.HeadPosition = stunVector(*ps.HeadPosition)
		}
		if ps.IsStunnedKnown {
			value := ps.IsStunned
			raw.IsStunned = &value
		}
		if ps.StunsKnown && ps.PrevStuns >= 0 {
			value := ps.PrevStuns
			raw.Stuns = &value
		}
		out.players[id] = stunSample{raw: raw, team: ps.Team}
	}
	return out, ""
}

func stunVector(v model.Vec3) *model.Vec3 {
	if v.HasNaN() || v.HasInf() || v.IsZero() {
		return nil
	}
	copy := v
	return &copy
}

func stunPlayingTeam(team string) bool { return team == "blue" || team == "orange" }

func sameStunRoster(a, b *stunSnapshot) bool {
	if len(a.players) != len(b.players) {
		return false
	}
	for id, sample := range a.players {
		other, ok := b.players[id]
		if !ok || other.team != sample.team {
			return false
		}
	}
	return true
}

func stunSampleIDs(samples map[string]stunSample) []string {
	ids := make([]string, 0, len(samples))
	for id := range samples {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func stunPendingIDs(pending map[string]*stunPending) []string {
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := pending[ids[i]].record, pending[ids[j]].record
		if a.FrameIndex != b.FrameIndex {
			return a.FrameIndex < b.FrameIndex
		}
		return ids[i] < ids[j]
	})
	return ids
}
