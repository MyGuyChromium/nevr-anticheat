package state

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	catchHistoryLimit = 256
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
	baselineSamples, minCorrectionSamples   int
	baselineDuration, minCorrectionDuration float64
	maxSampleGap, maxWindow, maxStepError   float64
	minLateral, minTurnRate, maxTurnRate    float64
	contactMargin, minImprovement           float64
	contactAcceleration                     float64
	maxApproach, regrabGrace                float64
	previous                                *catchSample
	history                                 []catchSample
	baseline                                *catchBaseline
	pending                                 *model.DetectionEvent
	pendingHolder                           string
	pendingEligible                         bool
	pendingReview                           *model.CatchReviewRecord
	pendingReviewHolder                     string
	diagnosticPrevious                      *catchSample
	catchObserver                           detect.CatchObserver
	flightReason                            string
	lastContact                             *catchContactResult
	lastRelease                             float64
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
	correctionClosed                                             bool
	anchor                                                       catchSample
	velocity                                                     model.Vec3
	start                                                        int
	corrections                                                  int
	correctionDuration                                           float64
	correctionElapsed, firstCorrectionDT                         float64
	referenceDuration                                            float64
	referenceSamples                                             int
	maxTurnRate, maxSecantRate                                   float64
	maxLateral, maxStepError                                     float64
	previousLateral                                              model.Vec3
	angleSum                                                     float64
	minContactClearance, maxContactMargin, maxContactUncertainty float64
}

func NewState008(params map[string]any) *State008 {
	d := &State008{BaseDetector: detect.BaseDetector{
		DetectorID: "STATE_008", DetectorVersion: "0.2.0", DetectorName: "Autopocket Catch Review",
		DetectorCategory: "state", Inputs: []string{"disc_state", "possession", "hand_tracking", "head_position"},
		Warmup: 0, Weight: 0, IsAutoEnforce: false, TraceBranches: true,
	}, baselineSamples: 4, minCorrectionSamples: 2, baselineDuration: .20, minCorrectionDuration: .12,
		maxSampleGap: .12, maxWindow: 1.5, maxStepError: .20, minLateral: .30, minTurnRate: 60, maxTurnRate: 300,
		contactMargin: .65, contactAcceleration: 30, minImprovement: .50, maxApproach: 1.5, regrabGrace: .35}
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
	d.baselineDuration = catchBound(detect.GetFloat(p, "baseline_duration_s", d.baselineDuration), .10, 1, .20)
	d.minCorrectionDuration = catchBound(detect.GetFloat(p, "min_correction_duration_s", d.minCorrectionDuration), .06, 1, .12)
	d.maxSampleGap = catchBound(detect.GetFloat(p, "max_sample_gap_s", d.maxSampleGap), .02, .20, .12)
	d.maxWindow = catchBound(detect.GetFloat(p, "max_window_s", d.maxWindow), .25, 2, 1.5)
	d.maxStepError = catchBound(detect.GetFloat(p, "max_step_error_m", d.maxStepError), .01, .5, .20)
	d.minLateral = catchBound(detect.GetFloat(p, "min_lateral_deviation_m", d.minLateral), .1, 3, .30)
	d.minTurnRate = catchBound(detect.GetFloat(p, "min_turn_rate_deg_s", d.minTurnRate), 15, 180, 60)
	d.maxTurnRate = catchBound(detect.GetFloat(p, "max_turn_rate_deg_s", d.maxTurnRate), 180, 720, 300)
	d.contactMargin = catchBound(detect.GetFloat(p, "contact_margin_m", d.contactMargin), .5, 2, .65)
	d.contactAcceleration = catchBound(detect.GetFloat(p, "contact_accel_allowance_mps2", d.contactAcceleration), 0, 200, 30)
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
	d.resetTrajectory()
	d.pendingReview = nil
	d.pendingReviewHolder = ""
	d.diagnosticPrevious = nil
}

func (d *State008) resetTrajectory() {
	d.previous = nil
	d.clearFlight()
	d.pending = nil
	d.pendingHolder = ""
	d.pendingEligible = false
	d.flightReason = ""
	d.lastContact = nil
	d.lastRelease = math.Inf(-1)
}

func (d *State008) clearFlight() {
	d.history = nil
	d.baseline = nil
}

func (d *State008) trace(players map[string]*model.PlayerState, frame int, reason string) {
	// Warmup/status frames must not erase why the preceding flight was lost.
	switch reason {
	case "catch_inputs_ready", "catch_baseline_pending", "catch_free_trajectory_tracked", "catch_no_free_approach":
	default:
		d.flightReason = reason
	}
	for _, ps := range detect.ActivePlayers(players, frame) {
		d.TraceDecision(ps.PlayerID, frame, reason)
	}
}

func (d *State008) Evaluate(mc *model.MatchContext, players map[string]*model.PlayerState, frame int) []model.DetectionEvent {
	sample, reason := catchReadSample(players, frame)
	if reason != "" {
		d.observeCatchPossession(players, frame, reason)
		d.resetTrajectory()
		d.trace(players, frame, reason)
		return nil
	}
	d.trace(players, frame, "catch_inputs_ready")
	previous := d.previous
	d.previous = &sample
	if previous != nil {
		dt := sample.timestamp - previous.timestamp
		if frame != previous.frame+1 || dt <= 0 || dt > d.maxSampleGap || !catchSameRoster(*previous, sample) {
			d.observeCatchPossession(players, frame, "catch_sample_gap")
			d.resetTrajectory()
			d.previous = &sample
			d.trace(players, frame, "catch_sample_gap")
			return nil
		}
		if previous.bounce != sample.bounce {
			d.observeCatchPossession(players, frame, "catch_bounce_changed")
			d.clearFlight()
			d.pending = nil
			d.pendingHolder = ""
			d.trace(players, frame, "catch_bounce_changed")
			return nil
		}
		if !catchPoseContinuity(*previous, sample, dt) {
			d.observeCatchPossession(players, frame, "catch_tracking_discontinuity")
			d.clearFlight()
			d.pending = nil
			d.pendingHolder = ""
			d.trace(players, frame, "catch_tracking_discontinuity")
			return nil
		}
	}
	d.observeCatchPossession(players, frame, "")

	// Confirmation uses only holder identity/continuity. The attached disc's
	// new position and velocity do not contribute to the observation strength.
	if d.pendingHolder != "" {
		pending := d.pending
		holder := d.pendingHolder
		if previous != nil && previous.holder == holder && sample.holder == holder {
			if d.pendingEligible {
				d.TraceDecision(holder, frame, "catch_approach_evaluated")
			}
			d.pending, d.pendingHolder, d.pendingEligible = nil, "", false
			if pending != nil {
				pending.IsShadow, pending.AutoEnforce, pending.EnforcementWeight = true, false, 0
				evidence := pending.Evidence.(model.StateEvidence)
				evidence.Metrics["confirmed_catch_frame"] = float64(frame)
				d.TraceDecision(holder, frame, "catch_candidate_confirmed")
				return []model.DetectionEvent{*pending}
			}
		} else {
			d.pending, d.pendingHolder, d.pendingEligible = nil, "", false
			d.trace(players, frame, "catch_possession_unconfirmed")
		}
	}
	if sample.holder != "" {
		if previous != nil && previous.holder == "" {
			d.pendingEligible = d.baseline != nil
			if d.pendingEligible {
				d.pending, reason = d.catchCandidate(mc, sample)
			} else {
				reason = d.flightReason
				if reason == "" {
					reason = "catch_baseline_pending"
				}
			}
			d.pendingHolder = sample.holder
			d.beginCatchReview(sample, *previous, reason)
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
	turnRate := angle / dt
	speedRatio := sample.velocity.Magnitude() / last.velocity.Magnitude()
	if stepError > d.maxStepError || turnRate > d.maxTurnRate || speedRatio < .75 || speedRatio > 1.25 {
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
		if len(d.history) >= d.baselineSamples && sample.timestamp-d.history[0].timestamp+catchTimeEpsilon >= d.baselineDuration {
			velocity, fitError, fitReason := catchFitReference(d.history, d.maxStepError)
			if fitReason != "" {
				d.history = []catchSample{sample}
				d.trace(players, frame, fitReason)
				return nil
			}
			d.baseline = &catchBaseline{anchor: sample, velocity: velocity, start: len(d.history) - 1,
				referenceDuration: sample.timestamp - d.history[0].timestamp, referenceSamples: len(d.history),
				maxStepError: math.Max(fitError, stepError)}
			d.flightReason, d.lastContact = "", nil
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
	// A position secant describes its interval midpoint, not its endpoint.
	// Using the distance between the two midpoints also handles uneven dt.
	secantRate := catchSecantTurnRate(previousStep, step, last.timestamp-prior.timestamp, dt)
	correcting := turnRate+catchTimeEpsilon >= d.minTurnRate && secantRate+catchTimeEpsilon >= d.minTurnRate*.5 && secantRate <= d.maxTurnRate &&
		lateral.Magnitude()+.01 >= base.previousLateral.Magnitude() && lateral.Dot(base.previousLateral) >= 0
	if correcting || base.corrections > 0 {
		contact := catchContactEnvelope(last, sample, d.contactMargin, d.contactAcceleration)
		d.lastContact = &contact
		if contact.Possible {
			d.clearFlight()
			reason := "catch_possible_contact"
			if contact.Kind == catchContactUncertainGeometry {
				reason = "catch_contact_uncertain"
			}
			d.trace(players, frame, reason)
			return nil
		}
		if base.maxContactMargin == 0 || contact.ClosestDistance < base.minContactClearance {
			base.minContactClearance = contact.ClosestDistance
		}
		base.maxContactMargin = math.Max(base.maxContactMargin, contact.Margin)
		base.maxContactUncertainty = math.Max(base.maxContactUncertainty, contact.Uncertainty)
	}
	if correcting && !base.correctionClosed {
		if base.corrections == 0 {
			base.firstCorrectionDT = dt
		}
		base.corrections++
		base.correctionElapsed += dt
		// Onset and offset may occur anywhere inside their sampled intervals.
		// Credit only the interior span, never both uncertain edge intervals.
		base.correctionDuration = math.Max(0, base.correctionElapsed-base.firstCorrectionDT-dt)
		base.angleSum += angle
		base.maxTurnRate = math.Max(base.maxTurnRate, turnRate)
		base.maxSecantRate = math.Max(base.maxSecantRate, secantRate)
	} else if !correcting && base.corrections > 0 {
		if base.corrections < d.minCorrectionSamples || base.correctionDuration+catchTimeEpsilon < d.minCorrectionDuration {
			d.clearFlight()
			d.trace(players, frame, "catch_correction_not_sustained")
			return nil
		}
		// Do not extend sustained evidence across a later straight-flight gap.
		base.correctionClosed = true
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

func (d *State008) catchCandidate(mc *model.MatchContext, caught catchSample) (*model.DetectionEvent, string) {
	base := d.baseline
	if base.corrections < d.minCorrectionSamples || base.maxLateral < d.minLateral || caught.timestamp-d.lastRelease < d.regrabGrace {
		return nil, "catch_no_sustained_correction"
	}
	if base.correctionDuration+catchTimeEpsilon < d.minCorrectionDuration {
		return nil, "catch_correction_duration_pending"
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
		"baseline_samples": float64(base.referenceSamples), "free_samples": float64(len(d.history)),
		"baseline_duration_s": base.referenceDuration, "correction_duration_s": base.correctionDuration,
		"required_baseline_duration_s": d.baselineDuration, "required_correction_duration_s": d.minCorrectionDuration,
		"min_turn_rate_deg_s": d.minTurnRate, "max_turn_rate_deg_s": d.maxTurnRate,
		"observed_max_turn_rate_deg_s": base.maxTurnRate, "observed_max_secant_turn_rate_deg_s": base.maxSecantRate,
		"correction_samples": float64(base.corrections), "cumulative_turn_deg": base.angleSum,
		"correction_edge_intervals_s": base.correctionElapsed - base.correctionDuration,
		"max_lateral_deviation_m":     base.maxLateral, "max_fit_error_m": base.maxStepError,
		"actual_hand_miss_m": bestActual, "expected_hand_miss_m": bestExpected, "miss_improvement_m": bestImprovement,
		"baseline_speed_mps": base.velocity.Magnitude(), "free_duration_s": last.timestamp - d.history[0].timestamp,
		"last_free_frame": float64(last.frame), "first_held_frame": float64(caught.frame),
		"bounce_count": float64(last.bounce), "sampled_players": float64(len(last.poses)),
		"min_lateral_deviation_m": d.minLateral, "min_miss_improvement_m": d.minImprovement,
		"contact_margin_m": d.contactMargin, "max_step_error_m": d.maxStepError,
		"max_catch_approach_m": d.maxApproach, "attribution_verified": 0,
		"contact_clearance_lower_bound_m": base.minContactClearance,
		"max_contact_margin_m":            base.maxContactMargin, "max_contact_uncertainty_m": base.maxContactUncertainty,
		"contact_accel_allowance_mps2": d.contactAcceleration,
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
				"Sustained turn duration excludes uncertain first/last sampled intervals; it cannot prove continuous motion between samples.",
				"Obstacle geometry, authoritative contact events, remote controller inputs and netcode corrections are unavailable.",
				"Contact clearances are conservative interpolation-envelope lower bounds, not verified collision distances or physical acceleration limits.",
				"The first held-disc attachment is excluded from trajectory measurements; normal input automation may be unobservable.",
			}},
		fmt.Sprintf("receiver-associated trajectory anomaly: %.2f m lateral deviation; cause and actor unverified", base.maxLateral),
		"provisional free-flight review filters; not a verified catch-physics limit",
		model.CausalKey{PlayerID: caught.holder, FrameStart: d.history[0].frame, FrameEnd: caught.frame, AnomalyType: "catch_trajectory_review"})
	ev.IsShadow, ev.AutoEnforce, ev.EnforcementWeight = true, false, 0
	return &ev, ""
}

func (d *State008) SetCatchObserver(observer detect.CatchObserver) { d.catchObserver = observer }

// Diagnostics are deliberately separate from DetectionEvents: a legal or
// unobservable transition must not become an event just to make it visible.
func (d *State008) beginCatchReview(caught, previous catchSample, reason string) {
	r := model.CatchReviewRecord{FrameIndex: caught.frame, Timestamp: caught.timestamp,
		StartFrame: previous.frame, LastFreeFrame: previous.frame,
		Outcome: model.CatchReviewInsufficientData, Reason: reason, Metrics: map[string]float64{}}
	if len(d.history) > 0 {
		r.StartFrame = d.history[0].frame
		r.Metrics["free_samples"] = float64(len(d.history))
		r.Metrics["free_duration_s"] = previous.timestamp - d.history[0].timestamp
	}
	if base := d.baseline; base != nil {
		r.Metrics["baseline_samples"] = float64(base.referenceSamples)
		r.Metrics["baseline_duration_s"] = base.referenceDuration
		r.Metrics["correction_samples"] = float64(base.corrections)
		r.Metrics["correction_duration_s"] = base.correctionDuration
		r.Metrics["max_lateral_deviation_m"] = base.maxLateral
		r.Metrics["max_fit_error_m"] = base.maxStepError
		r.Metrics["cumulative_turn_deg"] = base.angleSum
		r.Metrics["observed_max_turn_rate_deg_s"] = base.maxTurnRate
		r.Metrics["observed_max_secant_turn_rate_deg_s"] = base.maxSecantRate
	}
	if contact := d.lastContact; contact != nil {
		r.Metrics["contact_clearance_lower_bound_m"] = contact.ClosestDistance
		r.Metrics["contact_margin_m"] = contact.Margin
		r.Metrics["contact_uncertainty_m"] = contact.Uncertainty
		r.Metrics["contact_kind_"+contact.Kind] = 1
	}
	if d.pending != nil {
		r.Outcome, r.Reason = model.CatchReviewObservation, "catch_candidate_confirmed"
		metrics := d.pending.Evidence.(model.StateEvidence).Metrics
		for _, key := range []string{"actual_hand_miss_m", "expected_hand_miss_m", "miss_improvement_m"} {
			r.Metrics[key] = metrics[key]
		}
	} else {
		switch reason {
		case "catch_no_sustained_correction", "catch_correction_duration_pending", "catch_counterfactual_not_missed",
			"catch_release_grace", "catch_disc_too_slow", "catch_possible_contact", "catch_bounce_changed", "catch_correction_not_sustained":
			r.Outcome = model.CatchReviewExcluded
		}
	}
	d.pendingReview, d.pendingReviewHolder = &r, caught.holder
}

func (d *State008) finishCatchReview(outcome model.CatchReviewOutcome, reason string, confirmed bool) {
	if d.pendingReview == nil {
		return
	}
	r := d.pendingReview.Clone()
	d.pendingReview = nil
	if outcome != "" {
		r.Outcome = outcome
	}
	if reason != "" {
		r.Reason = reason
	}
	r.Confirmed = confirmed
	if d.catchObserver != nil {
		d.catchObserver(d.ID(), d.pendingReviewHolder, r)
	}
	d.pendingReviewHolder = ""
}

// FlushTracks satisfies the pipeline's end-of-match hook without manufacturing
// an event from an unconfirmed attachment. Repeated flushes are idempotent.
func (d *State008) FlushTracks(_ *model.MatchContext, _ int) []model.DetectionEvent {
	d.finishCatchReview(model.CatchReviewUnconfirmed, "catch_confirmation_unavailable", false)
	d.pending, d.pendingHolder, d.pendingEligible = nil, "", false
	return nil
}
