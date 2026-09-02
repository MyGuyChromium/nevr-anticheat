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

	// Cap-riding statistic: sub-cap throws only.
	capRidingHistory   = 50
	capRidingMinThrows = 8
	capRidingSeverity  = 0.3
	// capRidingMeanMargin / capRidingMaxStddev: mean within this many m/s of
	// the effective cap with a spread below this stddev.
	capRidingMeanMargin = 2.0
	capRidingMaxStddev  = 1.0
	// defaultCapRidingCooldownFrames: at most one cap-riding event per
	// player per cooldown window (~60 s at 15 fps).
	defaultCapRidingCooldownFrames = 900

	// autoEnforceMinExcess: over-cap margin (m/s) required before an event
	// may carry AutoEnforce (only when the detector's auto-enforce is on).
	autoEnforceMinExcess = 5.0
)

// throwSample is one release used by the cap-riding statistic.
type throwSample struct {
	speed float64
	frame int
}

// Throw001 detects impossible disc release velocities (THROW_001).
//
// Release speed is the game-reported disc velocity magnitude at the release
// frame (feature extractor), not a position delta, so the detector does not
// depend on the sampling interval.
type Throw001 struct {
	detect.BaseDetector
	baseTolerance           float64
	pingToleranceScalar     float64
	maxSpeedRatio           float64
	sigmoidSteepness        float64
	capRidingCooldownFrames int

	subCapSpeeds   map[string][]throwSample // per-player sub-cap release history
	lastCapRiding  map[string]int           // per-player frame of last cap-riding event
	artifactCounts map[string]int           // per-player count of >2x-cap releases
}

func NewThrow001(params map[string]any) *Throw001 {
	d := &Throw001{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_001", DetectorVersion: "1.1.0",
			DetectorName: "Impossible Release Velocity", DetectorCategory: "throw",
			Inputs: []string{"throw_event", "disc_state"}, Warmup: 5,
			Weight: 0.8,
			// Auto-enforcement stays off until the tolerances are validated
			// on real telemetry (config: auto_enforce = false). Phase-2
			// config wiring can turn it on via SetAutoEnforce.
			IsAutoEnforce: false,
		},
		baseTolerance:           detect.GetFloat(params, "base_tolerance", 1.3),
		pingToleranceScalar:     detect.GetFloat(params, "ping_tolerance_scalar", 5.0),
		maxSpeedRatio:           detect.GetFloat(params, "max_speed_ratio", 3.0),
		sigmoidSteepness:        detect.GetFloat(params, "sigmoid_steepness", 2.0),
		capRidingCooldownFrames: detect.GetInt(params, "cap_riding_cooldown_frames", defaultCapRidingCooldownFrames),
	}
	d.Reset()
	return d
}

func (d *Throw001) Reset() {
	d.subCapSpeeds = make(map[string][]throwSample)
	d.lastCapRiding = make(map[string]int)
	d.artifactCounts = make(map[string]int)
}

func (d *Throw001) Configure(params map[string]any) error {
	d.baseTolerance = detect.GetFloat(params, "base_tolerance", d.baseTolerance)
	d.pingToleranceScalar = detect.GetFloat(params, "ping_tolerance_scalar", d.pingToleranceScalar)
	d.maxSpeedRatio = detect.GetFloat(params, "max_speed_ratio", d.maxSpeedRatio)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	d.capRidingCooldownFrames = detect.GetInt(params, "cap_riding_cooldown_frames", d.capRidingCooldownFrames)
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
		if math.IsNaN(t.ReleaseSpeed) || math.IsInf(t.ReleaseSpeed, 0) || t.ReleaseSpeed <= 0 {
			continue
		}

		pingTolerance := (ps.EstimatedPingMs / 1000.0) * d.pingToleranceScalar
		effectiveCap := matchCtx.Physics.DiscSpeedCap + d.baseTolerance + pingTolerance
		speedExcess := t.ReleaseSpeed - effectiveCap
		handSpeed := math.Max(t.HandSpeed, 0.01)

		evidence := model.ThrowEvidence{
			ReleaseVelocity: t.ReleaseVelocity, ReleaseSpeed: t.ReleaseSpeed,
			ReleasePosition: t.ReleasePosition, HandVelocity: t.HandVelocity,
			HandSpeed: t.HandSpeed, EffectiveCap: effectiveCap, PingMs: ps.EstimatedPingMs,
		}

		// Suspected artifact: > 2x the physics cap. Reported separately at
		// low severity and excluded from every statistic.
		if matchCtx.Physics.DiscSpeedCap > 0 && t.ReleaseSpeed > matchCtx.Physics.DiscSpeedCap*artifactCapMultiple {
			d.artifactCounts[pid]++
			evidence.ArtifactSuspected = true
			evidence.ArtifactCount = d.artifactCounts[pid]
			evidence.SpeedRatio = t.ReleaseSpeed / handSpeed
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
			ev.Attribution = &t.Attribution
			events = append(events, ev)
			continue
		}

		if speedExcess <= 0 {
			// Sub-cap release: feed the cap-riding statistic only.
			if ev := d.checkCapRiding(matchCtx, ps, t, frameIdx, effectiveCap, pingTolerance, evidence); ev != nil {
				events = append(events, *ev)
			}
			continue
		}

		// Over the effective cap. The hand-speed ratio is only meaningful
		// here: a slow throw with a tiny wrist flick has a huge but legitimate
		// ratio.
		speedRatio := t.ReleaseSpeed / handSpeed
		severity := model.SigmoidConfidence(t.ReleaseSpeed, effectiveCap, d.sigmoidSteepness)
		if speedRatio > d.maxSpeedRatio {
			ratioSev := model.SigmoidConfidence(speedRatio, d.maxSpeedRatio, 1.0)
			severity = math.Max(severity, ratioSev)
		}
		confidence := severity
		if t.Attribution.Confidence > 0 {
			confidence *= t.Attribution.Confidence
		}
		evidence.SpeedRatio = speedRatio

		ev := d.MakeEvent(matchCtx, pid, frameIdx, t.Timestamp, severity, confidence, evidence,
			fmt.Sprintf("disc_speed: %.1f m/s (ratio: %.1f)", t.ReleaseSpeed, speedRatio),
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

// checkCapRiding records a sub-cap release and emits at most one cap-riding
// event per player per cooldown window when the recent sub-cap releases sit
// tightly just under the effective cap.
func (d *Throw001) checkCapRiding(matchCtx *model.MatchContext, ps *model.PlayerState, t *model.ThrowEvent, frameIdx int, effectiveCap, pingTolerance float64, evidence model.ThrowEvidence) *model.DetectionEvent {
	pid := ps.PlayerID
	hist := append(d.subCapSpeeds[pid], throwSample{speed: t.ReleaseSpeed, frame: frameIdx})
	if len(hist) > capRidingHistory {
		hist = hist[len(hist)-capRidingHistory:]
	}
	d.subCapSpeeds[pid] = hist
	if len(hist) < capRidingMinThrows {
		return nil
	}
	speeds := make([]float64, len(hist))
	for i, s := range hist {
		speeds[i] = s.speed
	}
	mean := model.Mean(speeds)
	stddev := model.StdDev(speeds)
	if mean <= effectiveCap-capRidingMeanMargin || stddev >= capRidingMaxStddev {
		return nil
	}
	if last, ok := d.lastCapRiding[pid]; ok && frameIdx-last < d.capRidingCooldownFrames {
		return nil
	}
	d.lastCapRiding[pid] = frameIdx

	throwCountFactor := math.Min(1.0, float64(len(hist))/float64(capRidingMinThrows*2))
	ev := d.MakeEvent(matchCtx, pid, frameIdx, t.Timestamp, capRidingSeverity, capRidingSeverity*throwCountFactor,
		evidence,
		fmt.Sprintf("cap_riding: mean=%.1f stddev=%.2f over %d sub-cap throws", mean, stddev, len(hist)),
		fmt.Sprintf("disc_speed: variance expected near cap %.1f m/s", effectiveCap),
		model.CausalKey{PlayerID: pid, FrameStart: hist[0].frame, FrameEnd: frameIdx, AnomalyType: "cap_riding"},
	)
	ev.Attribution = &t.Attribution
	return &ev
}
