package movement

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Mov002 detects teleportation - instant position changes (MOV_002).
//
// Game-event filters, in order: non-active game phases are skipped by the
// pipeline; immune (just respawned) and stunned players are skipped; a
// score change arms goal_cooldown_frames of suppression; and a candidate
// displacement above max_displacement is discarded as a round/goal reset.
// That last cap (default 12 m) was chosen from the section-6 replay
// calibration, where every > 12 m jump was a reset, NOT from physics — a
// teleport hack is not bounded by player speed, so raising the cap once
// resets are reliably filtered by the phase/cooldown gates would extend
// coverage to long-range teleports. Keep it conservative until real data
// says otherwise.
//
// Multi-player suppression counts DISTINCT teleporting players inside
// cluster_window: a round reset moves everyone, a hack moves one player,
// so one player teleporting repeatedly never suppresses itself.
//
// Gaps are judged on TELEMETRY TIME, not frame indices. The bridge assigns
// frame indices one per successful sample regardless of elapsed time, so a
// 2 s broadcaster stall arrives as index+1 with a 2 s timestamp delta: the
// player is legitimately ~10 m further along and the feature extractor has
// already cleared kinematics for that frame. A displacement is therefore
// only compared when the elapsed time since this detector last saw the
// player is known (FrameDt > 0, timestamps advancing) and at most
// max_gap_seconds (default 0.5 s, the extractor's own finite-difference
// limit), and the expected travel is prevVelocity * elapsed. max_frame_gap
// remains as a second guard for producers whose indices do skip.
type Mov002 struct {
	detect.BaseDetector
	teleportThreshold      float64
	velocityMismatchFactor float64
	maxFrameGap            int
	sigmoidSteepness       float64 // steepness of the confidence sigmoid on the mismatch ratio
	minSoloTeleporters     int     // distinct simultaneous teleporters before suppression
	clusterWindow          int     // frames to look back for multi-player teleport clustering
	goalCooldownFrames     int     // frames to suppress after a score change (goal reset)
	maxDisplacement        float64 // displacements above this are treated as game-event resets
	maxGapSeconds          float64 // elapsed telemetry time above which a displacement is a gap, not a jump

	minIncidents int // require multiple teleport incidents before flagging

	prevPosition      map[string]model.Vec3
	prevVelocity      map[string]model.Vec3
	prevFrame         map[string]int
	prevTimestamp     map[string]float64
	playerIncidents   map[string]int // per-player teleport incident count
	recentTeleporters map[string]int // pid -> last frame it was a teleport candidate
	scoreSeen         bool
	lastScoreBlue     int
	lastScoreOrange   int
	goalCooldownUntil int // suppress teleport detection until this frame
}

// defaultMaxGapSeconds mirrors pipeline.MaxFrameDt: the longest sample
// interval over which the feature extractor derives kinematics.
const defaultMaxGapSeconds = 0.5

// NewMov002 creates a new MOV_002 Teleportation detector.
func NewMov002(params map[string]any) *Mov002 {
	d := &Mov002{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "MOV_002",
			DetectorVersion:  "2.1.0",
			DetectorName:     "Teleportation",
			DetectorCategory: "movement",
			Inputs:           []string{"position", "velocity"},
			Warmup:           2,
			Weight:           1.0,
			IsAutoEnforce:    true,
		},
		teleportThreshold:      detect.GetFloat(params, "teleport_threshold", 8.0),
		velocityMismatchFactor: detect.GetFloat(params, "velocity_mismatch_factor", 3.0),
		maxFrameGap:            detect.GetInt(params, "max_frame_gap", 5),
		sigmoidSteepness:       detect.GetFloat(params, "sigmoid_steepness", 0.5),
		minSoloTeleporters:     detect.GetInt(params, "min_solo_teleporters", 2),
		clusterWindow:          detect.GetInt(params, "cluster_window", 60),
		goalCooldownFrames:     detect.GetInt(params, "goal_cooldown_frames", 150),
		maxDisplacement:        detect.GetFloat(params, "max_displacement", 12.0),
		maxGapSeconds:          detect.GetFloat(params, "max_gap_seconds", defaultMaxGapSeconds),
		minIncidents:           detect.GetInt(params, "min_incidents", 5),
	}
	d.Reset()
	d.sanitize()
	return d
}

func (d *Mov002) sanitize() {
	if d.maxDisplacement <= d.teleportThreshold {
		d.maxDisplacement = d.teleportThreshold + 1.0
	}
	if d.minIncidents < 1 {
		d.minIncidents = 1
	}
	if d.maxGapSeconds <= 0 {
		d.maxGapSeconds = defaultMaxGapSeconds
	}
}

func (d *Mov002) Reset() {
	d.prevPosition = make(map[string]model.Vec3)
	d.prevVelocity = make(map[string]model.Vec3)
	d.prevFrame = make(map[string]int)
	d.prevTimestamp = make(map[string]float64)
	d.playerIncidents = make(map[string]int)
	d.recentTeleporters = make(map[string]int)
	d.scoreSeen = false
	d.lastScoreBlue = 0
	d.lastScoreOrange = 0
	d.goalCooldownUntil = -1
}

func (d *Mov002) Configure(params map[string]any) error {
	d.teleportThreshold = detect.GetFloat(params, "teleport_threshold", d.teleportThreshold)
	d.velocityMismatchFactor = detect.GetFloat(params, "velocity_mismatch_factor", d.velocityMismatchFactor)
	d.maxFrameGap = detect.GetInt(params, "max_frame_gap", d.maxFrameGap)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	d.minSoloTeleporters = detect.GetInt(params, "min_solo_teleporters", d.minSoloTeleporters)
	d.clusterWindow = detect.GetInt(params, "cluster_window", d.clusterWindow)
	d.goalCooldownFrames = detect.GetInt(params, "goal_cooldown_frames", d.goalCooldownFrames)
	d.maxDisplacement = detect.GetFloat(params, "max_displacement", d.maxDisplacement)
	d.maxGapSeconds = detect.GetFloat(params, "max_gap_seconds", d.maxGapSeconds)
	d.minIncidents = detect.GetInt(params, "min_incidents", d.minIncidents)
	d.sanitize()
	return nil
}

// scoreSample picks the first player (by ID) that has actually received a
// frame this match; a never-updated placeholder from MatchContext.PlayerIDs
// would report a zero score forever. Falls back to the first active player.
func scoreSample(active []*model.PlayerState) *model.PlayerState {
	for _, ps := range active {
		if ps.FrameCount > 0 {
			return ps
		}
	}
	if len(active) > 0 {
		return active[0]
	}
	return nil
}

// severity spreads [teleport_threshold, max_displacement] over ~[0.1, 0.9]:
// the midpoint of the accepted range scores 0.5 and the steepness is
// derived from the range so severity is never structurally pinned near 0.
func (d *Mov002) severity(dist float64) float64 {
	mid := (d.teleportThreshold + d.maxDisplacement) / 2
	halfRange := (d.maxDisplacement - d.teleportThreshold) / 2
	if halfRange < 0.5 {
		halfRange = 0.5
	}
	k := math.Log(9) / halfRange
	return model.SigmoidConfidence(dist, mid, k)
}

func (d *Mov002) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	active := detect.ActivePlayers(players, frameIdx)

	// Detect score changes (goals) — all players teleport to spawn after a goal.
	// The score is match-level and identical on every frame of a tick, so
	// sample it from the first player (by ID) that actually had a frame
	// this tick; a stale state would report an old score and re-arm the
	// cooldown on every frame.
	if sample := scoreSample(active); sample != nil {
		if !d.scoreSeen {
			d.scoreSeen = true
			d.lastScoreBlue = sample.PrevBlueScore
			d.lastScoreOrange = sample.PrevOrangeScore
		} else if sample.PrevBlueScore != d.lastScoreBlue || sample.PrevOrangeScore != d.lastScoreOrange {
			d.lastScoreBlue = sample.PrevBlueScore
			d.lastScoreOrange = sample.PrevOrangeScore
			d.goalCooldownUntil = frameIdx + d.goalCooldownFrames
		}
	}
	if frameIdx < d.goalCooldownUntil {
		// Still update positions so we don't get false jumps when cooldown ends.
		for _, ps := range active {
			d.prevPosition[ps.PlayerID] = ps.Position
			d.prevVelocity[ps.PlayerID] = ps.Velocity
			d.prevFrame[ps.PlayerID] = frameIdx
			d.prevTimestamp[ps.PlayerID] = ps.LastTimestamp
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

	for _, ps := range active {
		pid := ps.PlayerID

		prevPos, hasPrev := d.prevPosition[pid]
		prevVel := d.prevVelocity[pid]
		prevF := d.prevFrame[pid]
		prevTS := d.prevTimestamp[pid]

		d.prevPosition[pid] = ps.Position
		d.prevVelocity[pid] = ps.Velocity
		d.prevFrame[pid] = frameIdx
		d.prevTimestamp[pid] = ps.LastTimestamp

		if !hasPrev {
			continue
		}

		// Skip large frame gaps (respawns, reconnects)
		frameGap := frameIdx - prevF
		if frameGap > d.maxFrameGap {
			continue
		}

		// Skip frames whose elapsed time is unknown or too long for a
		// finite difference (see the type comment): FrameDt == 0 means the
		// extractor had no usable dt for this frame, and a telemetry-time
		// gap above max_gap_seconds is a stall, not a jump, however the
		// producer numbered its frames.
		elapsed := ps.LastTimestamp - prevTS
		if ps.FrameDt <= 0 || elapsed <= 0 || elapsed > d.maxGapSeconds {
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

		// Expected travel: the previous velocity carried over the elapsed
		// telemetry time (not dt * frame gap, which assumes one index per
		// nominal tick).
		dt := elapsed
		expectedDelta := prevVel.Scale(elapsed)
		expectedDist := expectedDelta.Magnitude()

		// Game-event guard: see the type comment for why this cap exists
		// and why it is a calibration choice rather than a physics bound.
		if actualDist > d.maxDisplacement {
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

	// Track which players teleported within the cluster window. Respawns
	// after goals move several players on the same or consecutive frames;
	// a hack moves one player, however often.
	for _, c := range candidates {
		d.recentTeleporters[c.pid] = frameIdx
	}
	for pid, f := range d.recentTeleporters {
		if frameIdx-f > d.clusterWindow {
			delete(d.recentTeleporters, pid)
		}
	}
	if len(candidates) > 0 && len(d.recentTeleporters) >= d.minSoloTeleporters {
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

		mismatch := c.actualDist / model.MaxF(c.expectedDist, 0.01)
		severity := d.severity(c.actualDist)
		confidence := model.SigmoidConfidence(mismatch, d.velocityMismatchFactor, d.sigmoidSteepness)

		if c.isHighPing {
			confidence *= 0.5
		}

		metrics := map[string]float64{
			"actual_distance":   c.actualDist,
			"expected_distance": c.expectedDist,
			"frame_gap":         float64(c.frameGap),
			"frame_dt":          c.dt,
			"mismatch_ratio":    mismatch,
			"incident_count":    float64(d.playerIncidents[c.pid]),
			"max_displacement":  d.maxDisplacement,
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
