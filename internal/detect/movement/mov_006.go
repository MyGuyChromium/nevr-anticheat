package movement

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Mov006 detects physical playspace walking (MOV_006).
//
// Echo VR reports game locomotion separately as player.velocity. The feature
// extractor subtracts that expected movement from the tracked head/body pose;
// stacking therefore remains game velocity while a physical room-scale step
// remains in PlayspaceVelocity. At least one tracked hand must translate with
// the head so an isolated pose glitch or controller swing cannot trigger it.
//
// Replay telemetry has no feet or guardian origin, so it cannot prove that a
// residual came from walking rather than a legal lean/lunge. The shipped
// configuration therefore keeps this detector in shadow mode and uses strict
// displacement, duration and coherence gates for review candidates only.
type Mov006 struct {
	detect.BaseDetector
	minPlayspaceSpeed    float64
	minPlayspaceDistance float64
	minRigCoherence      float64
	minSustainedFrames   int
	minSustainedSeconds  float64
	maxPingMs            float64

	bursts map[string]*playspaceBurst
}

type playspaceBurst struct {
	startFrame    int
	startTime     float64
	frames        int
	maxSpeed      float64
	maxDistance   float64
	minCoherence  float64
	reportedSpeed float64
	poseSpeed     float64
	emitted       bool
}

// NewMov006 creates a physical playspace walking detector.
func NewMov006(params map[string]any) *Mov006 {
	d := &Mov006{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "MOV_006",
			DetectorVersion:  "1.1.0",
			DetectorName:     "Physical Playspace Walking",
			DetectorCategory: "movement",
			Inputs:           []string{"position", "reported_velocity", "lhand.pos", "rhand.pos"},
			Warmup:           2,
			Weight:           0.75,
			IsAutoEnforce:    false,
		},
		minPlayspaceSpeed:    detect.GetFloat(params, "min_playspace_speed", 1.0),
		minPlayspaceDistance: detect.GetFloat(params, "min_playspace_distance", 0.55),
		minRigCoherence:      detect.GetFloat(params, "min_rig_coherence", 0.65),
		minSustainedFrames:   detect.GetInt(params, "min_sustained_frames", 5),
		minSustainedSeconds:  detect.GetFloat(params, "min_sustained_seconds", 0.3),
		maxPingMs:            detect.GetFloat(params, "max_ping_ms", 150),
	}
	d.sanitize()
	d.Reset()
	return d
}

func (d *Mov006) sanitize() {
	if d.minPlayspaceSpeed < 0.1 {
		d.minPlayspaceSpeed = 0.1
	}
	if d.minPlayspaceDistance < 0.05 {
		d.minPlayspaceDistance = 0.05
	}
	d.minRigCoherence = model.Clamp01(d.minRigCoherence)
	if d.minSustainedFrames < 2 {
		d.minSustainedFrames = 2
	}
	if d.minSustainedSeconds < 0.1 {
		d.minSustainedSeconds = 0.1
	}
	if d.maxPingMs < 0 {
		d.maxPingMs = 0
	}
}

func (d *Mov006) Reset() { d.bursts = make(map[string]*playspaceBurst) }

func (d *Mov006) Configure(params map[string]any) error {
	d.minPlayspaceSpeed = detect.GetFloat(params, "min_playspace_speed", d.minPlayspaceSpeed)
	d.minPlayspaceDistance = detect.GetFloat(params, "min_playspace_distance", d.minPlayspaceDistance)
	d.minRigCoherence = detect.GetFloat(params, "min_rig_coherence", d.minRigCoherence)
	d.minSustainedFrames = detect.GetInt(params, "min_sustained_frames", d.minSustainedFrames)
	d.minSustainedSeconds = detect.GetFloat(params, "min_sustained_seconds", d.minSustainedSeconds)
	d.maxPingMs = detect.GetFloat(params, "max_ping_ms", d.maxPingMs)
	d.sanitize()
	return nil
}

func (d *Mov006) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID
		qualified := ps.PlayspaceValid && ps.PlayspaceTrackedHands > 0 &&
			ps.PlayspaceSpeed >= d.minPlayspaceSpeed &&
			ps.PlayspaceDistance >= d.minPlayspaceDistance &&
			ps.PlayspaceRigCoherence >= d.minRigCoherence
		if d.maxPingMs > 0 && ps.EstimatedPingMs > d.maxPingMs {
			qualified = false
		}
		if !qualified {
			delete(d.bursts, pid)
			continue
		}

		burst := d.bursts[pid]
		if burst == nil {
			burst = &playspaceBurst{startFrame: frameIdx, startTime: ps.LastTimestamp, minCoherence: ps.PlayspaceRigCoherence}
			d.bursts[pid] = burst
		}
		burst.frames++
		if ps.PlayspaceSpeed > burst.maxSpeed {
			burst.maxSpeed = ps.PlayspaceSpeed
		}
		if ps.PlayspaceDistance > burst.maxDistance {
			burst.maxDistance = ps.PlayspaceDistance
		}
		if ps.PlayspaceRigCoherence < burst.minCoherence {
			burst.minCoherence = ps.PlayspaceRigCoherence
		}
		burst.reportedSpeed = ps.ReportedVelocity.Magnitude()
		burst.poseSpeed = ps.Speed
		duration := ps.LastTimestamp - burst.startTime
		if duration < 0 {
			duration = 0
		}
		if burst.frames < d.minSustainedFrames || duration < d.minSustainedSeconds || burst.emitted {
			continue
		}
		burst.emitted = true

		// Crossing both configurable thresholds establishes the event. Larger
		// speed and displacement raise severity smoothly; rig coherence drives
		// confidence. High but accepted ping is retained with a confidence cut.
		speedExcess := (burst.maxSpeed - d.minPlayspaceSpeed) / d.minPlayspaceSpeed
		distanceExcess := (burst.maxDistance - d.minPlayspaceDistance) / d.minPlayspaceDistance
		strength := model.Clamp(max(speedExcess, distanceExcess), 0, 1)
		severity := 0.55 + 0.45*strength
		confidence := model.Clamp01(0.65 + 0.35*burst.minCoherence)
		if ps.IsHighPing {
			confidence *= 0.75
		}

		metrics := map[string]float64{
			"max_playspace_speed":    burst.maxSpeed,
			"max_playspace_distance": burst.maxDistance,
			"min_playspace_speed":    d.minPlayspaceSpeed,
			"min_playspace_distance": d.minPlayspaceDistance,
			"min_rig_coherence":      burst.minCoherence,
			"required_rig_coherence": d.minRigCoherence,
			"tracked_hands":          float64(ps.PlayspaceTrackedHands),
			"reported_game_speed":    burst.reportedSpeed,
			"derived_pose_speed":     burst.poseSpeed,
			"sustained_frames":       float64(burst.frames),
			"sustained_seconds":      duration,
			"estimated_ping_ms":      ps.EstimatedPingMs,
		}
		events = append(events, d.MakeEvent(
			matchCtx, pid, frameIdx, ps.LastTimestamp, severity, confidence,
			model.MovementEvidence{DetectorSpecific: "physical_playspace_walking", Metrics: metrics},
			fmt.Sprintf("playspace translation candidate: %.2f m at %.2f m/s for %.2f s / %d frames (game velocity %.2f m/s)",
				burst.maxDistance, burst.maxSpeed, duration, burst.frames, burst.reportedSpeed),
			fmt.Sprintf("playspace movement below %.2f m, %.2f m/s, or %.2f s", d.minPlayspaceDistance, d.minPlayspaceSpeed, d.minSustainedSeconds),
			model.CausalKey{PlayerID: pid, FrameStart: burst.startFrame, FrameEnd: frameIdx, AnomalyType: "playspace_walking"},
		))
	}
	return events
}
