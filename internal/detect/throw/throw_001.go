package throw

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

type Throw001 struct {
	detect.BaseDetector
	baseTolerance       float64
	pingToleranceScalar float64
	maxSpeedRatio       float64
	sigmoidSteepness    float64
	throwSpeeds         map[string][]float64 // per-player release speed history
}

func NewThrow001(params map[string]any) *Throw001 {
	d := &Throw001{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_001", DetectorVersion: "1.0.0",
			DetectorName: "Impossible Release Velocity", DetectorCategory: "throw",
			Inputs: []string{"throw_event", "disc_state"}, Warmup: 5,
			Weight: 0.8, IsAutoEnforce: true,
		},
		baseTolerance:       detect.GetFloat(params, "base_tolerance", 1.3),
		pingToleranceScalar: detect.GetFloat(params, "ping_tolerance_scalar", 5.0),
		maxSpeedRatio:       detect.GetFloat(params, "max_speed_ratio", 3.0),
		sigmoidSteepness:    detect.GetFloat(params, "sigmoid_steepness", 2.0),
		throwSpeeds:         make(map[string][]float64),
	}
	return d
}

func (d *Throw001) Reset() {
	d.throwSpeeds = make(map[string][]float64)
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
	for _, ps := range players {
		if ps.LastThrow == nil || ps.LastThrow.FrameIndex != frameIdx {
			continue
		}
		t := ps.LastThrow

		// Data artifact guard: disc speeds > 2x the physics cap (e.g. 37+ m/s
		// when cap is 18.7) are telemetry timing artifacts where the disc position
		// jumped between frames. Real speed hacks produce speeds near the cap
		// (20-35 m/s range), not 2x+ it.
		if t.ReleaseSpeed > matchCtx.Physics.DiscSpeedCap*2.0 {
			continue
		}

		pingTolerance := (ps.EstimatedPingMs / 1000.0) * d.pingToleranceScalar
		effectiveCap := matchCtx.Physics.DiscSpeedCap + d.baseTolerance + pingTolerance
		speedExcess := t.ReleaseSpeed - effectiveCap
		handSpeed := math.Max(t.HandSpeed, 0.01)
		speedRatio := t.ReleaseSpeed / handSpeed

		// Only evaluate speed ratio when disc speed actually exceeds the cap.
		// A slow throw (e.g. 5 m/s) with a tiny wrist flick (0.08 m/s hand speed)
		// produces a huge ratio (68x) that is completely legitimate.
		if speedExcess <= 0 {
			speedRatio = 0
		}

		alreadyFired := false
		if speedExcess <= 0 && speedRatio <= d.maxSpeedRatio {
			// Cap-riding detection: consistent near-cap speeds across many throws
			d.throwSpeeds[ps.PlayerID] = append(d.throwSpeeds[ps.PlayerID], t.ReleaseSpeed)
			if len(d.throwSpeeds[ps.PlayerID]) > 50 {
				d.throwSpeeds[ps.PlayerID] = d.throwSpeeds[ps.PlayerID][len(d.throwSpeeds[ps.PlayerID])-50:]
			}
			speeds := d.throwSpeeds[ps.PlayerID]
			if len(speeds) >= 8 {
				mean := model.Mean(speeds)
				stddev := model.StdDev(speeds)
				if mean > (effectiveCap-2.0) && stddev < 1.0 {
					// Low-severity: suspicious consistency near cap
					capSeverity := 0.3
					throwCountFactor := math.Min(1.0, float64(len(speeds))/16.0)
					capConfidence := capSeverity * throwCountFactor
					ev := d.MakeEvent(matchCtx, ps.PlayerID, frameIdx, t.Timestamp, capSeverity, capConfidence,
						model.ThrowEvidence{
							ReleaseVelocity: t.ReleaseVelocity, ReleaseSpeed: t.ReleaseSpeed,
							ReleasePosition: t.ReleasePosition, HandVelocity: t.HandVelocity,
							HandSpeed: t.HandSpeed, SpeedRatio: speedRatio,
							EffectiveCap: effectiveCap, PingMs: ps.EstimatedPingMs,
						},
						fmt.Sprintf("cap_riding: mean=%.1f stddev=%.2f over %d throws", mean, stddev, len(speeds)),
						fmt.Sprintf("disc_speed: variance expected near cap %.1f m/s", effectiveCap),
						model.CausalKey{PlayerID: ps.PlayerID, FrameStart: frameIdx - 2, FrameEnd: frameIdx + 2, AnomalyType: "cap_riding"},
					)
					events = append(events, ev)
					alreadyFired = true
				}
			}
			if !alreadyFired {
				continue
			}
			continue
		}
		// Also track speeds for throws that exceed the cap
		d.throwSpeeds[ps.PlayerID] = append(d.throwSpeeds[ps.PlayerID], t.ReleaseSpeed)
		if len(d.throwSpeeds[ps.PlayerID]) > 50 {
			d.throwSpeeds[ps.PlayerID] = d.throwSpeeds[ps.PlayerID][len(d.throwSpeeds[ps.PlayerID])-50:]
		}

		severity := 0.0
		if speedExcess > 0 {
			severity = model.SigmoidConfidence(t.ReleaseSpeed, effectiveCap, d.sigmoidSteepness)
		}
		if speedRatio > d.maxSpeedRatio {
			ratioSev := model.SigmoidConfidence(speedRatio, d.maxSpeedRatio, 1.0)
			severity = math.Max(severity, ratioSev)
		}

		confidence := severity
		if t.Attribution.Confidence > 0 {
			confidence *= t.Attribution.Confidence
		}
		// Replay-rate data (15fps = 0.067s dt) produces unreliable disc speed
		// computations from position deltas. Skip entirely — disc speed detection
		// is only reliable with live server telemetry at 60fps+.
		if ps.FrameDt > 0.05 {
			continue
		}

		autoEnforce := speedExcess > 5.0 && confidence > 0.95
		ev := d.MakeEvent(matchCtx, ps.PlayerID, frameIdx, t.Timestamp, severity, confidence,
			model.ThrowEvidence{
				ReleaseVelocity: t.ReleaseVelocity, ReleaseSpeed: t.ReleaseSpeed,
				ReleasePosition: t.ReleasePosition, HandVelocity: t.HandVelocity,
				HandSpeed: t.HandSpeed, SpeedRatio: speedRatio,
				EffectiveCap: effectiveCap, PingMs: ps.EstimatedPingMs,
			},
			fmt.Sprintf("disc_speed: %.1f m/s (ratio: %.1f)", t.ReleaseSpeed, speedRatio),
			fmt.Sprintf("disc_speed: 0-%.1f m/s (cap %.1f + tolerance %.1f)", effectiveCap, matchCtx.Physics.DiscSpeedCap, d.baseTolerance+pingTolerance),
			model.CausalKey{PlayerID: ps.PlayerID, FrameStart: frameIdx - 2, FrameEnd: frameIdx + 2, AnomalyType: "disc_speed"},
		)
		ev.AutoEnforce = autoEnforce
		ev.Attribution = &t.Attribution
		events = append(events, ev)
	}
	return events
}
