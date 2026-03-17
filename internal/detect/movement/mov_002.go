package movement

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Mov002 detects teleportation - instant position changes (MOV_002).
type Mov002 struct {
	detect.BaseDetector
	teleportThreshold      float64
	velocityMismatchFactor float64
	maxFrameGap            int
	sigmoidSteepness       float64
	minSoloTeleporters     int // minimum simultaneous teleporters before suppression
	clusterWindow          int // frames to look back for multi-player teleport clustering
	goalCooldownFrames     int // frames to suppress after a score change (goal reset)

	minIncidents           int // require multiple teleport incidents before flagging

	prevPosition      map[string]model.Vec3
	prevVelocity      map[string]model.Vec3
	prevFrame         map[string]int
	playerIncidents   map[string]int // per-player teleport incident count
	recentTeleportFrame int  // last frame where a teleport candidate was seen
	recentTeleportCount int  // teleport candidates within the cluster window
	lastScoreBlue       int
	lastScoreOrange     int
	goalCooldownUntil   int  // suppress teleport detection until this frame
}

// NewMov002 creates a new MOV_002 Teleportation detector.
func NewMov002(params map[string]any) *Mov002 {
	d := &Mov002{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "MOV_002",
			DetectorVersion:  "2.0.0",
			DetectorName:     "Teleportation",
			DetectorCategory: "movement",
			Inputs:           []string{"position", "velocity"},
			Warmup:           2,
			Weight:           1.0,
			IsAutoEnforce:    true,
		},
		teleportThreshold:      detect.GetFloat(params, "teleport_threshold", 5.0),
		velocityMismatchFactor: detect.GetFloat(params, "velocity_mismatch_factor", 3.0),
		maxFrameGap:            detect.GetInt(params, "max_frame_gap", 5),
		sigmoidSteepness:       detect.GetFloat(params, "sigmoid_steepness", 0.5),
		minSoloTeleporters:     detect.GetInt(params, "min_solo_teleporters", 2),
		clusterWindow:          detect.GetInt(params, "cluster_window", 60),
		goalCooldownFrames:     detect.GetInt(params, "goal_cooldown_frames", 150),
		minIncidents:           detect.GetInt(params, "min_incidents", 5),
		prevPosition:           make(map[string]model.Vec3),
		prevVelocity:           make(map[string]model.Vec3),
		prevFrame:              make(map[string]int),
		playerIncidents:        make(map[string]int),
	}
	return d
}

func (d *Mov002) Reset() {
	d.prevPosition = make(map[string]model.Vec3)
	d.prevVelocity = make(map[string]model.Vec3)
	d.prevFrame = make(map[string]int)
	d.playerIncidents = make(map[string]int)
	d.recentTeleportFrame = -100
	d.recentTeleportCount = 0
	d.lastScoreBlue = 0
	d.lastScoreOrange = 0
	d.goalCooldownUntil = -1
}

func (d *Mov002) Configure(params map[string]any) error {
	d.teleportThreshold = detect.GetFloat(params, "teleport_threshold", d.teleportThreshold)
	d.velocityMismatchFactor = detect.GetFloat(params, "velocity_mismatch_factor", d.velocityMismatchFactor)
	d.maxFrameGap = detect.GetInt(params, "max_frame_gap", d.maxFrameGap)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *Mov002) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	// Detect score changes (goals) — all players teleport to spawn after a goal.
	// Suppress teleport detection for goalCooldownFrames after any score change.
	for _, ps := range players {
		if ps.PrevBlueScore != d.lastScoreBlue || ps.PrevOrangeScore != d.lastScoreOrange {
			d.lastScoreBlue = ps.PrevBlueScore
			d.lastScoreOrange = ps.PrevOrangeScore
			d.goalCooldownUntil = frameIdx + d.goalCooldownFrames
		}
		break // only need one player's score data
	}
	if frameIdx < d.goalCooldownUntil {
		// Still update positions so we don't get false jumps when cooldown ends.
		for _, ps := range players {
			d.prevPosition[ps.PlayerID] = ps.Position
			d.prevVelocity[ps.PlayerID] = ps.Velocity
			d.prevFrame[ps.PlayerID] = frameIdx
		}
		return nil
	}

	// Two-pass approach: first identify all teleporters this frame, then suppress
	// if multiple players teleport simultaneously (indicates a game event like
	// goal reset, not individual cheating).
	type candidate struct {
		pid          string
		actualDist   float64
		expectedDist float64
		dt           float64
		frameGap     int
		prevF        int
		isHighPing   bool
		lastTS       float64
	}
	var candidates []candidate

	for _, ps := range players {
		pid := ps.PlayerID

		prevPos, hasPrev := d.prevPosition[pid]
		prevVel := d.prevVelocity[pid]
		prevF := d.prevFrame[pid]

		d.prevPosition[pid] = ps.Position
		d.prevVelocity[pid] = ps.Velocity
		d.prevFrame[pid] = frameIdx

		if !hasPrev {
			continue
		}

		// Skip large frame gaps (respawns, reconnects)
		frameGap := frameIdx - prevF
		if frameGap > d.maxFrameGap {
			continue
		}

		// Skip players with post-respawn immunity — they just teleported
		// to a spawn point. This is the primary respawn filter.
		if ps.IsImmune {
			continue
		}

		// Skip stunned players — stun can cause position corrections
		if ps.IsStunned {
			continue
		}

		delta := ps.Position.Sub(prevPos)
		actualDist := delta.Magnitude()

		// Expected delta based on previous velocity and frame dt
		dt := ps.FrameDt
		if dt <= 0 {
			dt = 1.0 / 60.0
		}
		expectedDelta := prevVel.Scale(dt * float64(frameGap))
		expectedDist := expectedDelta.Magnitude()

		// Game-event guard: displacements > 18m in a single frame gap are
		// physically impossible even for cheaters — at max speed (55 m/s)
		// and max dt (0.2s), max physical displacement is ~11m.
		// Anything beyond 18m is clearly a game-event teleport.
		if actualDist > 12.0 {
			continue
		}

		if actualDist <= d.teleportThreshold {
			continue
		}
		if actualDist <= expectedDist*d.velocityMismatchFactor {
			continue
		}

		candidates = append(candidates, candidate{
			pid: pid, actualDist: actualDist, expectedDist: expectedDist,
			dt: dt, frameGap: frameGap, prevF: prevF,
			isHighPing: ps.IsHighPing, lastTS: ps.LastTimestamp,
		})
	}

	// Track teleport candidates across a short window of frames.
	// Respawns after goals cause players to teleport on the same or
	// consecutive frames. If we see multiple teleporters within the
	// cluster window, suppress all of them.
	if len(candidates) > 0 {
		if frameIdx-d.recentTeleportFrame <= d.clusterWindow {
			d.recentTeleportCount += len(candidates)
		} else {
			d.recentTeleportCount = len(candidates)
		}
		d.recentTeleportFrame = frameIdx
	}

	// Suppress if multiple players teleported within the cluster window.
	// A real teleport hack affects one player; a round reset affects many.
	if d.recentTeleportCount >= d.minSoloTeleporters {
		return nil
	}

	var events []model.DetectionEvent
	for _, c := range candidates {
		// Require multiple teleport incidents per player before flagging.
		// Single game-event teleports (respawn, round start) are common FPs.
		d.playerIncidents[c.pid]++
		if d.playerIncidents[c.pid] < d.minIncidents {
			continue
		}

		severity := model.SigmoidConfidence(c.actualDist, d.teleportThreshold*2, d.sigmoidSteepness)
		confidence := model.SigmoidConfidence(c.actualDist/model.MaxF(c.expectedDist, 0.01), d.velocityMismatchFactor, 0.5)

		if c.isHighPing {
			confidence *= 0.5
		}

		metrics := map[string]float64{
			"actual_distance":   c.actualDist,
			"expected_distance": c.expectedDist,
			"frame_gap":         float64(c.frameGap),
			"frame_dt":          c.dt,
			"mismatch_ratio":    c.actualDist / model.MaxF(c.expectedDist, 0.01),
		}

		ev := d.MakeEvent(matchCtx, c.pid, frameIdx, c.lastTS,
			severity, confidence,
			model.MovementEvidence{
				DetectorSpecific: "teleportation",
				Metrics:          metrics,
			},
			fmt.Sprintf("teleport_distance: %.2f m (expected %.2f m)", c.actualDist, c.expectedDist),
			fmt.Sprintf("position_delta: <%.1f m or <%.1fx expected", d.teleportThreshold, d.velocityMismatchFactor),
			model.CausalKey{
				PlayerID:    c.pid,
				FrameStart:  c.prevF,
				FrameEnd:    frameIdx,
				AnomalyType: "teleportation",
			},
		)
		events = append(events, ev)
	}

	return events
}
