package state

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	catchHistoryLimit = 32
	catchPlayerLimit  = 16
)

// State008 retains receiver-associated free-disc trajectory anomalies for
// review. It cannot identify an input macro, the actor responsible for a bend,
// or a verified violation of Echo VR's catch physics. All thresholds below are
// provisional sampling filters. In particular, ordinary held-disc attachment
// is NEVER used to measure a correction. Missing contact/pose evidence causes
// abstention, and every emitted event remains observation-only under any config.
type State008 struct {
	detect.BaseDetector
	baselineSamples, minCorrectionSamples int
	maxSampleGap, maxWindow, maxStepError float64
	minLateral, minAngle, maxAngle        float64
	contactMargin, minImprovement         float64
	maxApproach, regrabGrace              float64
	previous                              *catchSample
	history                               []catchSample
	baseline                              *catchBaseline
	pending                               *model.DetectionEvent
	pendingHolder                         string
	lastRelease                           float64
}

type catchPose struct {
	id                      string
	body, head, left, right model.Vec3
}

type catchSample struct {
	frame              int
	timestamp          float64
	position, velocity model.Vec3
	bounce             int
	holder             string
	poses              []catchPose
}

type catchBaseline struct {
	anchor                   catchSample
	velocity                 model.Vec3
	start                    int
	corrections              int
	maxLateral, maxStepError float64
	previousLateral          model.Vec3
	angleSum                 float64
}

func NewState008(params map[string]any) *State008 {
	d := &State008{BaseDetector: detect.BaseDetector{
		DetectorID: "STATE_008", DetectorVersion: "0.1.0", DetectorName: "Autopocket Catch Review",
		DetectorCategory: "state", Inputs: []string{"disc_state", "possession", "hand_tracking", "head_position"},
		Warmup: 0, Weight: 0, IsAutoEnforce: false, TraceBranches: true,
	}, baselineSamples: 4, minCorrectionSamples: 2, maxSampleGap: .12, maxWindow: 1.5,
		maxStepError: .20, minLateral: .30, minAngle: 4, maxAngle: 20,
		contactMargin: .65, minImprovement: .50, maxApproach: 1.5, regrabGrace: .35}
	_ = d.Configure(params)
	d.Reset()
	return d
}

// These controls deliberately cannot promote an unvalidated catch observation.
func (d *State008) SetWeight(float64)                 { d.Weight = 0 }
func (d *State008) SetAutoEnforce(bool)               { d.IsAutoEnforce = false }
func (d *State008) AutoEnforce() bool                 { return false }
func (d *State008) DefaultEnforcementWeight() float64 { return 0 }

func (d *State008) Configure(p map[string]any) error {
	d.baselineSamples = catchBoundInt(detect.GetInt(p, "baseline_samples", d.baselineSamples), 4, 12)
	d.minCorrectionSamples = catchBoundInt(detect.GetInt(p, "min_correction_samples", d.minCorrectionSamples), 2, 8)
	d.maxSampleGap = catchBound(detect.GetFloat(p, "max_sample_gap_s", d.maxSampleGap), .02, .20, .12)
	d.maxWindow = catchBound(detect.GetFloat(p, "max_window_s", d.maxWindow), .25, 2, 1.5)
	d.maxStepError = catchBound(detect.GetFloat(p, "max_step_error_m", d.maxStepError), .01, .5, .20)
	d.minLateral = catchBound(detect.GetFloat(p, "min_lateral_deviation_m", d.minLateral), .1, 3, .30)
	d.minAngle = catchBound(detect.GetFloat(p, "min_correction_angle_deg", d.minAngle), 1, 10, 4)
	d.maxAngle = catchBound(detect.GetFloat(p, "max_turn_angle_deg", d.maxAngle), 10, 30, 20)
	d.contactMargin = catchBound(detect.GetFloat(p, "contact_margin_m", d.contactMargin), .5, 2, .65)
	d.minImprovement = catchBound(detect.GetFloat(p, "min_miss_improvement_m", d.minImprovement), .25, 3, .50)
	d.maxApproach = catchBound(detect.GetFloat(p, "max_catch_approach_m", d.maxApproach), .5, 2, 1.5)
	d.regrabGrace = catchBound(detect.GetFloat(p, "regrab_grace_s", d.regrabGrace), .25, 1, .35)
	d.Reset()
	return nil
}

func catchBound(v, low, high, fallback float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fallback
	}
	return math.Max(low, math.Min(high, v))
}

func catchBoundInt(v, low, high int) int {
	return max(low, min(high, v))
}

func (d *State008) Reset() {
	d.previous = nil
	d.clearFlight()
	d.pending = nil
	d.pendingHolder = ""
	d.lastRelease = math.Inf(-1)
}

func (d *State008) clearFlight() {
	d.history = nil
	d.baseline = nil
}

func (d *State008) trace(players map[string]*model.PlayerState, frame int, reason string) {
	for _, ps := range detect.ActivePlayers(players, frame) {
		d.TraceDecision(ps.PlayerID, frame, reason)
	}
}

func (d *State008) Evaluate(mc *model.MatchContext, players map[string]*model.PlayerState, frame int) []model.DetectionEvent {
	sample, reason := catchReadSample(players, frame)
	if reason != "" {
		d.Reset()
		d.trace(players, frame, reason)
		return nil
	}
	d.trace(players, frame, "catch_inputs_ready")
	previous := d.previous
	d.previous = &sample
	if previous != nil {
		dt := sample.timestamp - previous.timestamp
		if frame != previous.frame+1 || dt <= 0 || dt > d.maxSampleGap || !catchSameRoster(*previous, sample) {
			d.Reset()
			d.previous = &sample
			d.trace(players, frame, "catch_sample_gap")
			return nil
		}
		if previous.bounce != sample.bounce {
			d.clearFlight()
			d.pending = nil
			d.pendingHolder = ""
			d.trace(players, frame, "catch_bounce_changed")
			return nil
		}
		if !catchPoseContinuity(*previous, sample, dt) {
			d.clearFlight()
			d.pending = nil
			d.pendingHolder = ""
			d.trace(players, frame, "catch_tracking_discontinuity")
			return nil
		}
	}

	// Confirmation uses only holder identity/continuity. The attached disc's
	// new position and velocity do not contribute to the observation strength.
	if d.pendingHolder != "" {
		pending := d.pending
		holder := d.pendingHolder
		d.pending = nil
		d.pendingHolder = ""
		if previous != nil && previous.holder == holder && sample.holder == holder {
			d.TraceDecision(holder, frame, "catch_approach_evaluated")
			if pending != nil {
				pending.IsShadow, pending.AutoEnforce, pending.EnforcementWeight = true, false, 0
				evidence := pending.Evidence.(model.StateEvidence)
				evidence.Metrics["confirmed_catch_frame"] = float64(frame)
				d.TraceDecision(holder, frame, "catch_candidate_confirmed")
				return []model.DetectionEvent{*pending}
			}
		} else {
			d.trace(players, frame, "catch_possession_unconfirmed")
		}
	}
	if sample.holder != "" {
		if previous != nil && previous.holder == "" && d.baseline != nil {
			d.pending, reason = d.catchCandidate(mc, sample)
			d.pendingHolder = sample.holder
			if reason == "" {
				reason = "catch_confirmation_pending"
			}
			d.TraceDecision(sample.holder, frame, reason)
		} else {
			d.trace(players, frame, "catch_no_free_approach")
		}
		d.clearFlight()
		return nil
	}
	if previous != nil && previous.holder != "" {
		d.lastRelease = sample.timestamp
		d.clearFlight()
	}
	if sample.timestamp-d.lastRelease < d.regrabGrace {
		d.clearFlight()
		d.trace(players, frame, "catch_release_grace")
		return nil
	}
	if sample.velocity.Magnitude() < 2 {
		d.clearFlight()
		d.trace(players, frame, "catch_disc_too_slow")
		return nil
	}
	if len(d.history) == 0 {
		d.history = append(d.history, sample)
		d.trace(players, frame, "catch_baseline_pending")
		return nil
	}
	last := d.history[len(d.history)-1]
	dt := sample.timestamp - last.timestamp
	if len(d.history) == catchHistoryLimit || sample.timestamp-d.history[0].timestamp > d.maxWindow {
		d.clearFlight()
		d.history = append(d.history, sample)
		d.trace(players, frame, "catch_window_expired")
		return nil
	}
	if len(d.history) > 1 {
		priorDt := last.timestamp - d.history[len(d.history)-2].timestamp
		if dt > 2*priorDt || priorDt > 2*dt {
			d.clearFlight()
			d.trace(players, frame, "catch_interval_jitter")
			return nil
		}
	}
	step := sample.position.Sub(last.position)
	meanVelocity := sample.velocity.Add(last.velocity).Scale(.5)
	stepError := step.Sub(meanVelocity.Scale(dt)).Magnitude()
	angle := last.velocity.AngleBetweenDeg(sample.velocity)
	speedRatio := sample.velocity.Magnitude() / last.velocity.Magnitude()
	if stepError > d.maxStepError || angle > d.maxAngle || speedRatio < .75 || speedRatio > 1.25 {
		d.clearFlight()
		d.trace(players, frame, "catch_collision_or_discontinuity")
		return nil
	}
	if d.baseline == nil {
		if angle > 3 || d.history[0].velocity.AngleBetweenDeg(sample.velocity) > 3 ||
			math.Abs(sample.velocity.Magnitude()/d.history[0].velocity.Magnitude()-1) > .1 {
			d.history = []catchSample{sample}
			d.trace(players, frame, "catch_baseline_unstable")
			return nil
		}
		d.history = append(d.history, sample)
		if len(d.history) >= d.baselineSamples {
			var velocity model.Vec3
			for _, s := range d.history {
				velocity = velocity.Add(s.velocity)
			}
			velocity = velocity.Scale(1 / float64(len(d.history)))
			// The whole baseline must agree in position as well as velocity.
			first := d.history[0]
			fitError := sample.position.Sub(first.position).Sub(velocity.Scale(sample.timestamp - first.timestamp)).Magnitude()
			if fitError > d.maxStepError {
				d.history = []catchSample{sample}
				d.trace(players, frame, "catch_baseline_unstable")
				return nil
			}
			d.baseline = &catchBaseline{anchor: sample, velocity: velocity, start: len(d.history) - 1, maxStepError: math.Max(fitError, stepError)}
		}
		d.trace(players, frame, "catch_baseline_pending")
		return nil
	}

	base := d.baseline
	expected := base.anchor.position.Add(base.velocity.Scale(sample.timestamp - base.anchor.timestamp))
	residual := sample.position.Sub(expected)
	direction := base.velocity.Normalized()
	lateral := residual.Sub(direction.Scale(residual.Dot(direction)))
	prior := d.history[len(d.history)-2]
	previousStep := last.position.Sub(prior.position)
	secantAngle := previousStep.AngleBetweenDeg(step)
	correcting := angle >= d.minAngle && secantAngle >= d.minAngle*.4 &&
		lateral.Magnitude()+.01 >= base.previousLateral.Magnitude() && lateral.Dot(base.previousLateral) >= 0
	if correcting || base.corrections > 0 {
		if catchSweptContact(last, sample, d.contactMargin) {
			d.clearFlight()
			d.trace(players, frame, "catch_possible_contact")
			return nil
		}
	}
	if correcting {
		base.corrections++
		base.angleSum += angle
	} else if base.corrections > 0 && base.corrections < d.minCorrectionSamples {
		d.clearFlight()
		d.trace(players, frame, "catch_correction_not_sustained")
		return nil
	}
	base.previousLateral = lateral
	base.maxLateral = math.Max(base.maxLateral, lateral.Magnitude())
	base.maxStepError = math.Max(base.maxStepError, stepError)
	d.history = append(d.history, sample)
	d.trace(players, frame, "catch_free_trajectory_tracked")
	return nil
}

func catchFinite(v model.Vec3) bool {
	return !v.HasNaN() && !v.HasInf() && !math.IsInf(v.Magnitude(), 0)
}

func catchReadSample(players map[string]*model.PlayerState, frame int) (catchSample, string) {
	s := catchSample{frame: frame}
	var disc *model.DiscState
	holders := 0
	for _, ps := range detect.SortedPlayers(players) {
		if ps.LastFrameIdx != frame {
			continue
		}
		if len(s.poses) == catchPlayerLimit {
			return s, "catch_input_unavailable"
		}
		dc := ps.CurrentDisc
		if dc == nil || !dc.PossessionKnown || dc.PossessionConflict || dc.BounceCount == nil || *dc.BounceCount < 0 ||
			dc.SampledPlayerCount < 1 || dc.SampledPlayerCount > catchPlayerLimit ||
			!catchFinite(dc.Position) || !catchFinite(dc.Velocity) || math.IsNaN(dc.Speed) || math.IsInf(dc.Speed, 0) || dc.Speed < 0 ||
			math.IsNaN(ps.LastTimestamp) || math.IsInf(ps.LastTimestamp, 0) || ps.LastTimestamp < 0 {
			return s, "catch_input_unavailable"
		}
		if math.Abs(dc.Speed-dc.Velocity.Magnitude()) > .01 {
			return s, "catch_inconsistent_snapshot"
		}
		if ps.HeadPosition == nil || ps.HeadPosition.IsZero() || !catchFinite(*ps.HeadPosition) ||
			ps.LeftHand.IsZero() || ps.RightHand.IsZero() || ps.Position.IsZero() ||
			!catchFinite(ps.LeftHand) || !catchFinite(ps.RightHand) || !catchFinite(ps.Position) || ps.IsImmune || ps.IsHighPing {
			return s, "catch_tracking_unavailable"
		}
		if disc == nil {
			disc = dc
			s.timestamp, s.position, s.velocity, s.bounce = ps.LastTimestamp, dc.Position, dc.Velocity, *dc.BounceCount
		} else if ps.LastTimestamp != s.timestamp || dc.Position != disc.Position || dc.Velocity != disc.Velocity ||
			dc.IsHeld != disc.IsHeld || dc.PossessorID != disc.PossessorID || dc.Speed != disc.Speed ||
			*dc.BounceCount != s.bounce || dc.SampledPlayerCount != disc.SampledPlayerCount {
			return s, "catch_inconsistent_snapshot"
		}
		if ps.HasDisc {
			holders++
			s.holder = ps.PlayerID
		}
		s.poses = append(s.poses, catchPose{ps.PlayerID, ps.Position, *ps.HeadPosition, ps.LeftHand, ps.RightHand})
	}
	if disc == nil || len(s.poses) != disc.SampledPlayerCount {
		return s, "catch_roster_incomplete"
	}
	if holders > 1 || disc.IsHeld != (holders == 1) || disc.PossessorID != s.holder {
		return s, "catch_possession_conflict"
	}
	return s, ""
}

func catchSameRoster(a, b catchSample) bool {
	if len(a.poses) != len(b.poses) {
		return false
	}
	for i := range a.poses {
		if a.poses[i].id != b.poses[i].id {
			return false
		}
	}
	return true
}

func catchPoseContinuity(a, b catchSample, dt float64) bool {
	for i, old := range a.poses {
		now := b.poses[i]
		bodyStep := now.body.Sub(old.body)
		if bodyStep.Magnitude()/dt > 100 {
			return false
		}
		for j, p := range []model.Vec3{now.left, now.right, now.head} {
			previous := []model.Vec3{old.left, old.right, old.head}[j]
			if p.Sub(previous).Sub(bodyStep).Magnitude()/dt > 50 {
				return false
			}
		}
	}
	return true
}

// catchRelativeSweep minimizes the distance between two linearly interpolated
// motions at the SAME time, including contacts between sample endpoints.
func catchRelativeSweep(d0, d1, h0, h1 model.Vec3) float64 {
	r0 := d0.Sub(h0)
	delta := d1.Sub(h1).Sub(r0)
	t := 0.0
	if length := delta.MagnitudeSq(); length > 1e-12 {
		t = math.Max(0, math.Min(1, -r0.Dot(delta)/length))
	}
	return r0.Add(delta.Scale(t)).Magnitude()
}

func catchSweptContact(a, b catchSample, margin float64) bool {
	for i, p := range a.poses {
		q := b.poses[i]
		for j, start := range []model.Vec3{p.left, p.right, p.head, p.body} {
			end := []model.Vec3{q.left, q.right, q.head, q.body}[j]
			if catchRelativeSweep(a.position, b.position, start, end) <= margin {
				return true
			}
		}
	}
	return false
}

func (d *State008) catchCandidate(mc *model.MatchContext, caught catchSample) (*model.DetectionEvent, string) {
	base := d.baseline
	if base.corrections < d.minCorrectionSamples || base.maxLateral < d.minLateral || caught.timestamp-d.lastRelease < d.regrabGrace {
		return nil, "catch_no_sustained_correction"
	}
	last := d.history[len(d.history)-1]
	receiver := -1
	for i, p := range last.poses {
		if p.id == caught.holder {
			receiver = i
			break
		}
	}
	if receiver < 0 {
		return nil, "catch_roster_incomplete"
	}
	bestImprovement, bestActual, bestExpected := 0.0, 0.0, 0.0
	for hand := 0; hand < 2; hand++ {
		actualMiss, expectedMiss := math.Inf(1), math.Inf(1)
		for i := base.start + 1; i < len(d.history); i++ {
			a, b := d.history[i-1], d.history[i]
			h0, h1 := a.poses[receiver].left, b.poses[receiver].left
			if hand == 1 {
				h0, h1 = a.poses[receiver].right, b.poses[receiver].right
			}
			e0 := base.anchor.position.Add(base.velocity.Scale(a.timestamp - base.anchor.timestamp))
			e1 := base.anchor.position.Add(base.velocity.Scale(b.timestamp - base.anchor.timestamp))
			actualMiss = math.Min(actualMiss, catchRelativeSweep(a.position, b.position, h0, h1))
			expectedMiss = math.Min(expectedMiss, catchRelativeSweep(e0, e1, h0, h1))
		}
		endHand := last.poses[receiver].left
		if hand == 1 {
			endHand = last.poses[receiver].right
		}
		finalDistance := last.position.Distance(endHand)
		improvement := expectedMiss - actualMiss
		if finalDistance <= d.maxApproach && finalDistance <= actualMiss+d.maxStepError &&
			expectedMiss > d.contactMargin+d.minImprovement && improvement > bestImprovement {
			bestImprovement, bestActual, bestExpected = improvement, actualMiss, expectedMiss
		}
	}
	if bestImprovement < d.minImprovement {
		return nil, "catch_counterfactual_not_missed"
	}
	metrics := map[string]float64{
		"baseline_samples": float64(d.baselineSamples), "free_samples": float64(len(d.history)),
		"correction_samples": float64(base.corrections), "cumulative_turn_deg": base.angleSum,
		"max_lateral_deviation_m": base.maxLateral, "max_fit_error_m": base.maxStepError,
		"actual_hand_miss_m": bestActual, "expected_hand_miss_m": bestExpected, "miss_improvement_m": bestImprovement,
		"baseline_speed_mps": base.velocity.Magnitude(), "free_duration_s": last.timestamp - d.history[0].timestamp,
		"last_free_frame": float64(last.frame), "first_held_frame": float64(caught.frame),
		"bounce_count": float64(last.bounce), "sampled_players": float64(len(last.poses)),
		"min_lateral_deviation_m": d.minLateral, "min_miss_improvement_m": d.minImprovement,
		"contact_margin_m": d.contactMargin, "max_step_error_m": d.maxStepError,
		"max_catch_approach_m": d.maxApproach, "attribution_verified": 0,
	}
	trajectory := make([]model.CatchTrajectorySample, 0, len(d.history))
	for _, s := range d.history {
		trajectory = append(trajectory, model.CatchTrajectorySample{
			FrameIndex: s.frame, Timestamp: s.timestamp, DiscPosition: s.position, DiscVelocity: s.velocity,
			ExpectedPosition: base.anchor.position.Add(base.velocity.Scale(s.timestamp - base.anchor.timestamp)),
			LeftHand:         s.poses[receiver].left, RightHand: s.poses[receiver].right,
		})
	}
	severity := math.Min(.5, .2+.1*(base.maxLateral/d.minLateral-1))
	ev := d.MakeEvent(mc, caught.holder, caught.frame, caught.timestamp, severity, .25,
		model.StateEvidence{DetectorSpecific: "receiver_associated_catch_trajectory", Metrics: metrics, CatchTrajectory: trajectory,
			Attribution: "Receiver-associated trajectory anomaly; cause and actor unverified.",
			Limitations: []string{
				"Sampling thresholds are provisional engineering filters, not verified catch-physics limits.",
				"Obstacle geometry, authoritative contact events, remote controller inputs and netcode corrections are unavailable.",
				"The first held-disc attachment is excluded from trajectory measurements; normal input automation may be unobservable.",
			}},
		fmt.Sprintf("receiver-associated trajectory anomaly: %.2f m lateral deviation; cause and actor unverified", base.maxLateral),
		"provisional free-flight review filters; not a verified catch-physics limit",
		model.CausalKey{PlayerID: caught.holder, FrameStart: d.history[0].frame, FrameEnd: caught.frame, AnomalyType: "catch_trajectory_review"})
	ev.IsShadow, ev.AutoEnforce, ev.EnforcementWeight = true, false, 0
	return &ev, ""
}
