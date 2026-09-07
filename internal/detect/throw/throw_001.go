package throw

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	// artifactCapMultiple: releases faster than this multiple of the physics
	// cap are reported as suspected telemetry artifacts (low severity, own
	// anomaly type) instead of as over-cap throws. Whether such values are
	// timing artifacts or blatant injection is unresolved on real data; they
	// are surfaced so calibration can see them, never silently dropped.
	artifactCapMultiple = 2.0
	artifactSeverity    = 0.2

	// autoEnforceMinExcess: over-cap margin (m/s) required before an event
	// may carry AutoEnforce (only when the detector's auto-enforce is on).
	autoEnforceMinExcess = 5.0
)

// Throw001 detects impossible disc release velocities (THROW_001).
//
// Release speed is the higher of the engine's last_throw.total_speed and the
// game-reported disc velocity magnitude for local-client throws, otherwise
// the disc magnitude alone. Neither speed calculation uses a position delta;
// sampling gaps can still obscure the true release/contact or attribution.
type Throw001 struct {
	detect.BaseDetector
	baseTolerance       float64
	pingToleranceScalar float64
	maxSpeedRatio       float64
	sigmoidSteepness    float64
	artifactCounts      map[string]int // per-player count of >2x-cap releases
}

func NewThrow001(params map[string]any) *Throw001 {
	d := &Throw001{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_001", DetectorVersion: "1.5.1",
			DetectorName: "Impossible Release Velocity", DetectorCategory: "throw",
			Inputs: []string{"throw_event", "disc_state"}, Warmup: 5,
			Weight: 0.8,
			// Auto-enforcement stays off until the tolerances are validated
			// on real telemetry (config: auto_enforce = false). Phase-2
			// config wiring can turn it on via SetAutoEnforce.
			IsAutoEnforce: false,
		},
		baseTolerance:       detect.GetFloat(params, "base_tolerance", 0.0),
		pingToleranceScalar: detect.GetFloat(params, "ping_tolerance_scalar", 0.0),
		maxSpeedRatio:       detect.GetFloat(params, "max_speed_ratio", 3.0),
		sigmoidSteepness:    detect.GetFloat(params, "sigmoid_steepness", 2.0),
	}
	d.Reset()
	return d
}

func (d *Throw001) Reset() {
	d.artifactCounts = make(map[string]int)
}

func (d *Throw001) Configure(params map[string]any) error {
	d.baseTolerance = detect.GetFloat(params, "base_tolerance", d.baseTolerance)
	d.pingToleranceScalar = detect.GetFloat(params, "ping_tolerance_scalar", d.pingToleranceScalar)
	d.maxSpeedRatio = detect.GetFloat(params, "max_speed_ratio", d.maxSpeedRatio)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Throw001) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	for _, pid := range sortedPlayerIDs(players) {
		ps := players[pid]
		t := throwAt(ps, frameIdx)
		if t == nil {
			continue
		}
		if t.GameLastThrow != nil && t.GameLastThrow.Valid() {
			normalized := *t
			normalized.ReleaseSpeed = math.Max(t.ReleaseSpeed, t.GameLastThrow.TotalSpeed)
			t = &normalized
		}
		if math.IsNaN(t.ReleaseSpeed) || math.IsInf(t.ReleaseSpeed, 0) || t.ReleaseSpeed <= 0 {
			continue
		}

		pingTolerance := (ps.EstimatedPingMs / 1000.0) * d.pingToleranceScalar
		effectiveCap := matchCtx.Physics.DiscSpeedCap + d.baseTolerance + pingTolerance
		speedExcess := t.ReleaseSpeed - effectiveCap
		playerSpeed := t.PlayerVelocity.Magnitude()
		// Project onto the sampled direction, not ReleaseSpeed: the latter
		// may be a larger independent engine last_throw scalar. Mixing those
		// measurements scales the projection incorrectly. This is context
		// only; it is not added to or subtracted from the configured cap.
		alignedMovementSpeed := t.PlayerVelocity.Dot(t.ReleaseVelocity.Normalized())
		playerRelativeVelocity := t.ReleaseVelocity.Sub(t.PlayerVelocity)
		playerRelativeSpeed := playerRelativeVelocity.Magnitude()
		handSpeed := t.HandSpeed
		if t.HandKinematicsValid {
			handSpeed = math.Max(handSpeed, 0.01)
		}

		sampledDiscSpeed := t.SampledDiscSpeed
		if sampledDiscSpeed <= 0 {
			sampledDiscSpeed = t.ReleaseVelocity.Magnitude()
		}
		evidence := model.ThrowEvidence{
			ReleaseVelocity: t.ReleaseVelocity, ReleaseSpeed: t.ReleaseSpeed,
			SampledDiscSpeed: sampledDiscSpeed, GameLastThrow: t.GameLastThrow,
			ReleasePosition: t.ReleasePosition,
			PlayerVelocity:  t.PlayerVelocity, PlayerSpeed: playerSpeed,
			AlignedMovementSpeed:   alignedMovementSpeed,
			PlayerRelativeVelocity: playerRelativeVelocity, PlayerRelativeSpeed: playerRelativeSpeed,
			HandVelocity: t.HandVelocity,
			HandSpeed:    t.HandSpeed, HandRelativeVelocity: t.HandRelativeVelocity,
			HandRelativeSpeed:         t.HandRelativeSpeed,
			HandKinematicsValid:       t.HandKinematicsValid,
			HandAttributionConfidence: t.HandAttributionConfidence,
			HandAttributionAnchor:     t.HandAttributionAnchor,
			EffectiveCap:              effectiveCap, PingMs: ps.EstimatedPingMs,
		}

		// Suspected artifact: > 2x the physics cap when only a sampled disc
		// velocity is available. An engine-authored last_throw value is direct
		// corroboration, so it remains on the hard over-cap path regardless of
		// magnitude.
		engineCorroborated := t.GameLastThrow != nil && t.GameLastThrow.Valid() &&
			matchCtx.Physics.DiscSpeedCap > 0 && t.GameLastThrow.TotalSpeed > matchCtx.Physics.DiscSpeedCap*artifactCapMultiple
		if !engineCorroborated && matchCtx.Physics.DiscSpeedCap > 0 && t.ReleaseSpeed > matchCtx.Physics.DiscSpeedCap*artifactCapMultiple {
			d.artifactCounts[pid]++
			evidence.ArtifactSuspected = true
			evidence.ArtifactCount = d.artifactCounts[pid]
			if t.HandKinematicsValid {
				evidence.SpeedRatio = t.ReleaseSpeed / handSpeed
			}
			confidence := artifactSeverity
			if t.Attribution.Confidence > 0 {
				confidence *= t.Attribution.Confidence
			}
			ev := d.MakeEvent(matchCtx, pid, frameIdx, t.Timestamp, artifactSeverity, confidence, evidence,
				fmt.Sprintf("disc_speed: %.1f m/s (%.1fx cap; suspected telemetry artifact #%d)",
					t.ReleaseSpeed, t.ReleaseSpeed/matchCtx.Physics.DiscSpeedCap, d.artifactCounts[pid]),
				fmt.Sprintf("disc_speed: 0-%.1f m/s (cap %.1f + tolerance %.1f)", effectiveCap, matchCtx.Physics.DiscSpeedCap, d.baseTolerance+pingTolerance),
				model.CausalKey{PlayerID: pid, FrameStart: frameIdx - 2, FrameEnd: frameIdx + 2, AnomalyType: "disc_speed_artifact"},
			)
			// A suspected artifact is an observation for calibration, never
			// an enforceable finding: only the hard over-cap path below may
			// carry AutoEnforce.
			ev.AutoEnforce = false
			ev.Attribution = &t.Attribution
			events = append(events, ev)
			continue
		}

		if speedExcess <= 0 {
			// A repeatable near-cap throw is legal skill, not evidence of a
			// modified client. Only an actual over-cap release is observable.
			continue
		}

		// Over the effective cap. The hand-speed ratio is only meaningful
		// here: a slow throw with a tiny wrist flick has a huge but legitimate
		// ratio.
		speedRatio := 0.0
		severity := model.SigmoidConfidence(t.ReleaseSpeed, effectiveCap, d.sigmoidSteepness)
		if t.HandKinematicsValid {
			speedRatio = t.ReleaseSpeed / handSpeed
			if speedRatio > d.maxSpeedRatio {
				ratioSev := model.SigmoidConfidence(speedRatio, d.maxSpeedRatio, 1.0)
				severity = math.Max(severity, ratioSev)
			}
		}
		confidence := severity
		if t.Attribution.Confidence > 0 {
			confidence *= t.Attribution.Confidence
		}
		evidence.SpeedRatio = speedRatio

		observed := fmt.Sprintf("disc_speed: %.2f m/s (player-relative %.2f; aligned movement %+.2f; hand ratio unavailable)",
			t.ReleaseSpeed, playerRelativeSpeed, alignedMovementSpeed)
		if t.HandKinematicsValid {
			observed = fmt.Sprintf("disc_speed: %.2f m/s (player-relative %.2f; aligned movement %+.2f; hand ratio %.1f)",
				t.ReleaseSpeed, playerRelativeSpeed, alignedMovementSpeed, speedRatio)
		}
		if t.GameLastThrow != nil {
			observed += fmt.Sprintf("; engine last_throw: arm %.2f, movement %.2f, wrist %.2f m/s (sampled disc %.2f)",
				t.GameLastThrow.SpeedFromArm, t.GameLastThrow.SpeedFromMovement,
				t.GameLastThrow.SpeedFromWrist, sampledDiscSpeed)
		}
		ev := d.MakeEvent(matchCtx, pid, frameIdx, t.Timestamp, severity, confidence, evidence,
			observed,
			fmt.Sprintf("disc_speed: 0-%.1f m/s (cap %.1f + tolerance %.1f)", effectiveCap, matchCtx.Physics.DiscSpeedCap, d.baseTolerance+pingTolerance),
			model.CausalKey{PlayerID: pid, FrameStart: frameIdx - 2, FrameEnd: frameIdx + 2, AnomalyType: "disc_speed"},
		)
		// Hard-impossibility auto-enforcement: only when enabled on the
		// detector, the release is well over the cap, the event is near
		// certain and the thrower attribution is possession-tracked.
		ev.AutoEnforce = d.AutoEnforce() && speedExcess > autoEnforceMinExcess &&
			severity > 0.95 && t.Attribution.Confidence >= 0.9
		ev.Attribution = &t.Attribution
		events = append(events, ev)
	}
	return events
}
