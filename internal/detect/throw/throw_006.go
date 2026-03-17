package throw

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

type trajectoryTrack struct {
	throwerID        string
	releaseFrame     int
	releaseTimestamp float64
	releaseSpeed     float64
	releasePos       model.Vec3
	positions        []model.Vec3
	velocities       []model.Vec3
	cumulativeAngle  float64
	maxFrameAngle    float64
	violationFrames  int
	frameCount       int
	prevVelocity     model.Vec3
	initialAlignment float64 // cosine similarity at first tracked frame
	finalAlignment   float64 // cosine similarity at last tracked frame
	alignmentSet     bool    // whether initialAlignment has been set
}

// Throw006 detects disc trajectory bending after release (magnetism cheat).
type Throw006 struct {
	detect.BaseDetector
	minTrajectoryChange float64 // degrees per frame
	maxCumulativeChange float64 // total degrees
	postReleaseFrames   int
	minDistFromThrower  float64 // meters
	activeThrows        map[string]*trajectoryTrack
}

func NewThrow006(params map[string]any) *Throw006 {
	return &Throw006{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_006", DetectorVersion: "1.1.0",
			DetectorName: "Trajectory Correction (Mags)", DetectorCategory: "throw",
			Inputs: []string{"disc_state"}, Warmup: 5, Weight: 0.8,
		},
		minTrajectoryChange: detect.GetFloat(params, "min_trajectory_change", 8.0),
		maxCumulativeChange: detect.GetFloat(params, "max_cumulative_change", 130.0),
		postReleaseFrames:   detect.GetInt(params, "post_release_frames", 15),
		minDistFromThrower:  detect.GetFloat(params, "min_distance_from_thrower", 2.0),
		activeThrows:        make(map[string]*trajectoryTrack),
	}
}

func (d *Throw006) Reset() { d.activeThrows = make(map[string]*trajectoryTrack) }
func (d *Throw006) Configure(params map[string]any) error {
	d.minTrajectoryChange = detect.GetFloat(params, "min_trajectory_change", d.minTrajectoryChange)
	d.maxCumulativeChange = detect.GetFloat(params, "max_cumulative_change", d.maxCumulativeChange)
	d.postReleaseFrames = detect.GetInt(params, "post_release_frames", d.postReleaseFrames)
	d.minDistFromThrower = detect.GetFloat(params, "min_distance_from_thrower", d.minDistFromThrower)
	return nil
}

func (d *Throw006) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	// Get disc state from any player (disc is global)
	var disc *model.DiscState
	for _, ps := range players {
		if ps.CurrentDisc != nil {
			disc = ps.CurrentDisc
			break
		}
	}

	// Start tracking new throws
	for _, ps := range players {
		if ps.LastThrow != nil && ps.LastThrow.FrameIndex == frameIdx {
			t := ps.LastThrow
			track := &trajectoryTrack{
				throwerID:        ps.PlayerID,
				releaseFrame:     frameIdx,
				releaseTimestamp: t.Timestamp,
				releaseSpeed:     t.ReleaseSpeed,
				releasePos:       t.ReleasePosition,
			}
			if disc != nil {
				track.prevVelocity = disc.Velocity
			}
			d.activeThrows[ps.PlayerID] = track
		}
	}

	if disc == nil {
		return events
	}

	// Update active tracks with current disc state
	for throwerID, track := range d.activeThrows {
		// Check if disc was caught or max frames reached
		if disc.IsHeld || frameIdx-track.releaseFrame > d.postReleaseFrames {
			ev := d.finalizeTrack(matchCtx, track, frameIdx)
			if ev != nil {
				events = append(events, *ev)
			}
			delete(d.activeThrows, throwerID)
			continue
		}

		// Skip frames too close to release (interpolation wobble)
		distFromRelease := disc.Position.Distance(track.releasePos)
		if distFromRelease < d.minDistFromThrower {
			track.prevVelocity = disc.Velocity
			continue
		}

		track.frameCount++
		track.positions = append(track.positions, disc.Position)
		track.velocities = append(track.velocities, disc.Velocity)

		// Compute angle change between consecutive velocity vectors
		prevSpeed := track.prevVelocity.Magnitude()
		currSpeed := disc.Velocity.Magnitude()
		if prevSpeed > 0.1 && currSpeed > 0.1 {
			angleChange := model.RadToDeg(track.prevVelocity.AngleBetween(disc.Velocity))

			// Collision filter: bounces cause sudden large direction changes.
			// Magnetism produces gradual, sustained bending (< 15 deg/frame).
			// Filter 1: speed loss > 30% with any angle change (inelastic bounce)
			// Filter 2: any single-frame angle > 30 deg (elastic bounce off wall/player)
			// Filter 3: speed increase > 20% with angle change (player deflection)
			speedRatio := currSpeed / prevSpeed
			isInelasticBounce := speedRatio < 0.7 && angleChange > 2.0
			isElasticBounce := angleChange > 15.0
			isDeflection := speedRatio > 1.2 && angleChange > 10.0
			isLikelyCollision := isInelasticBounce || isElasticBounce || isDeflection

			if !math.IsNaN(angleChange) && !isLikelyCollision {
				track.cumulativeAngle += angleChange
				if angleChange > track.maxFrameAngle {
					track.maxFrameAngle = angleChange
				}
				if angleChange > d.minTrajectoryChange {
					track.violationFrames++
				}
			}
		}

		// Track alignment with nearest goal for net-alignment-improvement detection
		if disc.Velocity.Magnitude() > 0.1 && matchCtx.Physics.ArenaLength > 0 {
			halfLen := matchCtx.Physics.ArenaLength / 2.0
			// CONFIRMED from real replay: goals are on the Z axis, not X.
			goalPosA := model.Vec3{0, 0, halfLen}
			goalPosB := model.Vec3{0, 0, -halfLen}
			toGoalA := goalPosA.Sub(disc.Position)
			toGoalB := goalPosB.Sub(disc.Position)
			// Pick nearer goal
			toGoal := toGoalA
			if toGoalB.Magnitude() < toGoalA.Magnitude() {
				toGoal = toGoalB
			}
			// Cosine similarity between velocity and direction-to-goal
			velNorm := disc.Velocity.Normalized()
			goalNorm := toGoal.Normalized()
			alignment := velNorm.Dot(goalNorm)
			if !math.IsNaN(alignment) {
				if !track.alignmentSet {
					track.initialAlignment = alignment
					track.alignmentSet = true
				}
				track.finalAlignment = alignment
			}
		}

		track.prevVelocity = disc.Velocity
	}

	return events
}

func (d *Throw006) finalizeTrack(matchCtx *model.MatchContext, track *trajectoryTrack, frameIdx int) *model.DetectionEvent {
	// Net alignment improvement check: detect smooth magnetism
	alignmentImprovement := 0.0
	if track.alignmentSet {
		alignmentImprovement = track.finalAlignment - track.initialAlignment
	}

	// Require at least 4 violation frames for any magnetism detection.
	// Headbutts, regrabs, wall bounces, and replay interpolation all produce
	// trajectory bends that can pass the per-frame bounce filter but don't
	// sustain across 4+ frames. Real magnetism cheats produce continuous bending.
	if track.violationFrames < 5 {
		return nil
	}
	if track.cumulativeAngle <= d.maxCumulativeChange && alignmentImprovement <= 0.5 {
		return nil
	}

	severity := 0.0
	if track.cumulativeAngle > d.maxCumulativeChange {
		severity = model.SigmoidConfidence(track.cumulativeAngle, d.maxCumulativeChange, 0.2)
	}
	if track.maxFrameAngle > d.minTrajectoryChange {
		frameSev := model.SigmoidConfidence(track.maxFrameAngle, d.minTrajectoryChange, 0.3)
		severity = math.Max(severity, frameSev)
	}
	// Alignment improvement: disc got significantly more aligned with goal during flight
	if alignmentImprovement > 0.5 {
		alignSev := model.SigmoidConfidence(alignmentImprovement, 0.4, 5.0)
		severity = math.Max(severity, alignSev)
	}

	// Confidence scales with number of violation frames (need at least 3 for full confidence)
	frameFactor := math.Min(1.0, float64(track.violationFrames)/3.0)
	confidence := severity * math.Max(0.5, frameFactor)

	ev := model.DetectionEvent{
		EventID: model.NewEventID(), DetectorID: "THROW_006", DetectorVersion: "1.1.0",
		MatchID: matchCtx.MatchID, PlayerID: track.throwerID,
		FrameIndex: track.releaseFrame, FrameRangeStart: track.releaseFrame, FrameRangeEnd: frameIdx,
		Timestamp: track.releaseTimestamp,
		Severity: model.Clamp01(severity), Confidence: model.Clamp01(confidence),
		Evidence: model.TrajectoryEvidence{
			CumulativeAngleChange: track.cumulativeAngle,
			MaxSingleFrameChange:  track.maxFrameAngle,
			ViolationFrameCount:   track.violationFrames,
			TotalTrackedFrames:    track.frameCount,
			DistanceTraveled:      func() float64 { if len(track.positions) == 0 { return 0 }; return track.positions[len(track.positions)-1].Distance(track.releasePos) }(),
			ReleaseSpeed:          track.releaseSpeed,
			TrajectoryPoints:      track.positions,
			VelocityPoints:        track.velocities,
		},
		ObservedValue: fmt.Sprintf("trajectory_bend: %.1f deg cumulative (%d violation frames, max %.1f deg/frame)",
			track.cumulativeAngle, track.violationFrames, track.maxFrameAngle),
		ExpectedRange: fmt.Sprintf("trajectory_bend: < %.1f deg cumulative, < %.1f deg/frame",
			d.maxCumulativeChange, d.minTrajectoryChange),
		CausalKey:         model.CausalKey{PlayerID: track.throwerID, FrameStart: track.releaseFrame, FrameEnd: frameIdx, AnomalyType: "trajectory_bend"},
		EnforcementWeight: 0.8,
	}
	return &ev
}
