package state

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// State007 detects impossible punch range (STATE_007).
//
// STATUS: TELEMETRY_DEPENDENT — relies on per-frame stun count increments to
// detect punch events. If stun stats only update at round/match end rather
// than per-frame, this detector is dead. Disabled by default until stun count
// granularity is confirmed from real telemetry.
//
// Victim attribution: when a player's stun stat increments at frame F, the
// victim is the opponent whose IsStunned flag went false -> true within
// attribution_window_frames of F (the two can land on adjacent polls),
// nearest to the puncher's hands as they were at F, measured to the
// victim's position on the frame the stun started. Opponents who were
// already stunned earlier by someone else are never candidates. Team
// filtering only excludes a candidate when BOTH teams are known and equal,
// so it works when Team is populated and degrades to attribution-by-stun-
// transition (rather than to "nobody") when it is not.
type State007 struct {
	detect.BaseDetector
	punchRangeThreshold float64
	velocityAdjustScale float64
	minIncidents        int
	sigmoidSteepness    float64
	attributionWindow   int
	maxRange            float64

	seenStuns  map[string]bool
	prevStuns  map[string]int
	incidents  map[string]int
	wasStunned map[string]bool
	stunStarts map[string]stunStart
	pending    []pendingPunch
}

type stunStart struct {
	frame    int
	pos      model.Vec3
	consumed bool
}

type pendingPunch struct {
	pid       string
	team      string
	frame     int
	leftHand  model.Vec3
	rightHand model.Vec3
	speed     float64
	timestamp float64
	highPing  bool
}

// NewState007 creates a new STATE_007 Punch Range detector.
func NewState007(params map[string]any) *State007 {
	d := &State007{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "STATE_007",
			DetectorVersion:  "2.1.0",
			DetectorName:     "Impossible Punch Range",
			DetectorCategory: "state",
			Inputs:           []string{"stun_state", "hand_tracking", "position"},
			Warmup:           10,
			Weight:           0.8,
			IsAutoEnforce:    false,
		},
		punchRangeThreshold: detect.GetFloat(params, "punch_range_threshold", 10.0),
		velocityAdjustScale: detect.GetFloat(params, "velocity_adjust_scale", 0.15),
		minIncidents:        detect.GetIntAlias(params, 3, "min_incidents", "min_incidents_to_surface"),
		sigmoidSteepness:    detect.GetFloat(params, "sigmoid_steepness", 1.0),
		attributionWindow:   detect.GetInt(params, "attribution_window_frames", 2),
		maxRange:            detect.GetFloat(params, "max_range", 25.0),
	}
	d.Reset()
	d.sanitize()
	return d
}

func (d *State007) sanitize() {
	if d.attributionWindow < 0 {
		d.attributionWindow = 0
	}
	if d.minIncidents < 1 {
		d.minIncidents = 1
	}
}

func (d *State007) Reset() {
	d.seenStuns = make(map[string]bool)
	d.prevStuns = make(map[string]int)
	d.incidents = make(map[string]int)
	d.wasStunned = make(map[string]bool)
	d.stunStarts = make(map[string]stunStart)
	d.pending = nil
}

func (d *State007) Configure(params map[string]any) error {
	d.punchRangeThreshold = detect.GetFloat(params, "punch_range_threshold", d.punchRangeThreshold)
	d.velocityAdjustScale = detect.GetFloat(params, "velocity_adjust_scale", d.velocityAdjustScale)
	d.minIncidents = detect.GetIntAlias(params, d.minIncidents, "min_incidents", "min_incidents_to_surface")
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	d.attributionWindow = detect.GetInt(params, "attribution_window_frames", d.attributionWindow)
	d.maxRange = detect.GetFloat(params, "max_range", d.maxRange)
	d.sanitize()
	return nil
}

// sameTeam reports whether two players are known to be teammates.
func sameTeam(a, b string) bool {
	return a != "" && b != "" && a == b
}

func (d *State007) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	active := detect.ActivePlayers(players, frameIdx)

	// 1. Record stun-start transitions and new punches from this frame.
	for _, ps := range active {
		pid := ps.PlayerID
		// A stun start is a false -> true transition between two observed
		// frames; a player first seen already stunned is not "just stunned".
		if prevStunned, seen := d.wasStunned[pid]; seen && ps.IsStunned && !prevStunned {
			d.stunStarts[pid] = stunStart{frame: frameIdx, pos: ps.Position}
		}
		d.wasStunned[pid] = ps.IsStunned

		prev := d.prevStuns[pid]
		d.prevStuns[pid] = ps.PrevStuns
		if !d.seenStuns[pid] {
			// First observation only establishes the baseline: a player
			// joining with stuns already on the board did not just punch.
			d.seenStuns[pid] = true
			continue
		}
		if ps.PrevStuns <= prev {
			continue
		}
		d.pending = append(d.pending, pendingPunch{
			pid: pid, team: ps.Team, frame: frameIdx,
			leftHand: ps.LeftHand, rightHand: ps.RightHand,
			speed: ps.Speed, timestamp: ps.LastTimestamp, highPing: ps.IsHighPing,
		})
	}

	// 2. Resolve pending punches against stun-start transitions.
	all := detect.SortedPlayers(players)
	var still []pendingPunch
	for _, p := range d.pending {
		victim, dist, ok := d.attribute(p, all)
		if !ok {
			if frameIdx < p.frame+d.attributionWindow {
				still = append(still, p)
			}
			continue
		}
		if ev, fired := d.evaluatePunch(matchCtx, p, victim, dist, frameIdx); fired {
			events = append(events, ev)
		}
	}
	d.pending = still

	return events
}

// attribute finds the opponent whose stun started within the attribution
// window of the punch, nearest to the puncher's hands.
func (d *State007) attribute(p pendingPunch, all []*model.PlayerState) (string, float64, bool) {
	bestDist := math.MaxFloat64
	bestID := ""
	for _, other := range all {
		if other.PlayerID == p.pid {
			continue
		}
		if sameTeam(other.Team, p.team) {
			continue
		}
		ss, ok := d.stunStarts[other.PlayerID]
		if !ok || ss.consumed {
			continue
		}
		if ss.frame < p.frame-d.attributionWindow || ss.frame > p.frame+d.attributionWindow {
			continue
		}
		handDist := handToTarget(p.leftHand, p.rightHand, ss.pos)
		if handDist < bestDist {
			bestDist = handDist
			bestID = other.PlayerID
		}
	}
	if bestID == "" {
		return "", 0, false
	}
	ss := d.stunStarts[bestID]
	ss.consumed = true
	d.stunStarts[bestID] = ss
	return bestID, bestDist, true
}

// handToTarget is the nearest hand-to-target distance, ignoring zero
// (untracked) hands; +Inf when neither hand is tracked.
func handToTarget(left, right, target model.Vec3) float64 {
	dist := math.Inf(1)
	if !left.IsZero() {
		dist = left.Distance(target)
	}
	if !right.IsZero() {
		if r := right.Distance(target); r < dist {
			dist = r
		}
	}
	return dist
}

func (d *State007) evaluatePunch(matchCtx *model.MatchContext, p pendingPunch, victim string, nearestDist float64, frameIdx int) (model.DetectionEvent, bool) {
	if math.IsInf(nearestDist, 1) {
		return model.DetectionEvent{}, false
	}
	// Adjust for closing velocity
	closingSpeed := p.speed
	adjustedDist := nearestDist - (closingSpeed * d.velocityAdjustScale)
	if adjustedDist < 0 {
		adjustedDist = 0
	}

	if adjustedDist <= d.punchRangeThreshold {
		return model.DetectionEvent{}, false
	}

	// Sanity cap: distances beyond max_range are data timing artifacts, not real punches
	if adjustedDist > d.maxRange {
		return model.DetectionEvent{}, false
	}

	d.incidents[p.pid]++
	if d.incidents[p.pid] < d.minIncidents {
		return model.DetectionEvent{}, false
	}

	excess := adjustedDist - d.punchRangeThreshold
	severity := model.SigmoidConfidence(adjustedDist, d.punchRangeThreshold*1.5, d.sigmoidSteepness)
	confidence := model.SigmoidConfidence(excess, 0, d.sigmoidSteepness)
	confidence *= model.SigmoidConfidence(float64(d.incidents[p.pid]), float64(d.minIncidents), 0.5)
	if p.highPing {
		confidence *= 0.6
	}
	confidence = model.Clamp01(confidence)

	metrics := map[string]float64{
		"hand_to_victim_distance": nearestDist,
		"adjusted_distance":       adjustedDist,
		"punch_range_threshold":   d.punchRangeThreshold,
		"closing_speed":           closingSpeed,
		"incident_count":          float64(d.incidents[p.pid]),
		"excess":                  excess,
		"punch_frame":             float64(p.frame),
		"resolved_frame":          float64(frameIdx),
	}

	// The event belongs to the PUNCH frame: FrameIndex and Timestamp are
	// both taken from the frame on which the stat incremented, so frame-
	// and time-indexed consumers agree. Attribution may complete up to
	// attribution_window_frames later; that frame is the causal range end
	// and resolved_frame.
	ev := d.MakeEvent(matchCtx, p.pid, p.frame, p.timestamp,
		severity, confidence,
		model.StateEvidence{
			DetectorSpecific: "punch_range",
			Metrics:          metrics,
		},
		fmt.Sprintf("punch_range: %.2f m (adjusted: %.2f m) victim=%s", nearestDist, adjustedDist, victim),
		fmt.Sprintf("punch_range: 0-%.2f m", d.punchRangeThreshold),
		model.CausalKey{
			PlayerID:    p.pid,
			FrameStart:  p.frame - d.attributionWindow,
			FrameEnd:    frameIdx,
			AnomalyType: "punch_range",
		},
	)
	return ev, true
}
