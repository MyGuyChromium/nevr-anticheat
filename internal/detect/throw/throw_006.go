package throw

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	// minViolationFrames: sustained bending required before any magnetism
	// detection. Headbutts, regrabs, wall bounces and replay interpolation
	// produce bends that pass the per-frame bounce filter but do not sustain.
	minViolationFrames = 5
	// minTrackedFrames: short throws do not carry enough data.
	minTrackedFrames = 5
	// alignmentImprovementGate: a disc that was not heading toward the goal
	// and ends up nearly aimed at it. Kept high (0.7): 0.5 fired on normal
	// throws that happened to curve slightly toward the goal.
	alignmentImprovementGate = 0.7
	// fullConfidenceViolationFrames: violation frames for full confidence.
	fullConfidenceViolationFrames = 8

	// Collision filters (per frame).
	inelasticSpeedRatio = 0.75 // speed loss > 25% with any angle change
	inelasticMinAngle   = 2.0
	elasticBounceAngle  = 15.0 // single-frame angle above this is a bounce
	deflectionRatio     = 1.15 // speed gain > 15% with angle change > 8 deg
	deflectionMinAngle  = 8.0
)

type trajectoryTrack struct {
	throwerID         string
	releaseFrame      int
	releaseTimestamp  float64
	releaseSpeed      float64
	releasePos        model.Vec3
	positions         []model.Vec3
	velocities        []model.Vec3
	cumulativeAngle   float64
	maxFrameAngle     float64
	violationAngleSum float64
	violationFrames   int
	frameCount        int
	prevVelocity      model.Vec3
	goalPos           model.Vec3 // goal fixed at release (never flips mid-flight)
	goalKnown         bool
	goalLabel         string
	initialAlignment  float64 // cosine similarity at first tracked frame
	finalAlignment    float64 // cosine similarity at last tracked frame
	alignmentSet      bool    // whether initialAlignment has been set
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
			DetectorID: "THROW_006", DetectorVersion: "1.2.0",
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

	disc := currentDisc(players, frameIdx)

	// Start tracking new throws. A re-throw while a track is still open
	// (regrab within the tracking window) finalizes the earlier track first.
	for _, pid := range sortedPlayerIDs(players) {
		t := throwAt(players[pid], frameIdx)
		if t == nil {
			continue
		}
		if old, ok := d.activeThrows[pid]; ok {
			if ev := d.finalizeTrack(matchCtx, old, frameIdx); ev != nil {
				events = append(events, *ev)
			}
		}
		track := &trajectoryTrack{
			throwerID:        pid,
			releaseFrame:     frameIdx,
			releaseTimestamp: t.Timestamp,
			releaseSpeed:     t.ReleaseSpeed,
			releasePos:       t.ReleasePosition,
			prevVelocity:     t.ReleaseVelocity,
			goalPos:          t.GoalPosition,
			goalKnown:        !t.GoalPosition.IsZero(),
			goalLabel:        t.GoalSelection,
		}
		if disc != nil && !disc.Velocity.IsZero() {
			track.prevVelocity = disc.Velocity
		}
		d.activeThrows[pid] = track
	}

	if disc == nil {
		return events
	}

	// Update active tracks with current disc state
	for _, throwerID := range sortedKeys(d.activeThrows) {
		track := d.activeThrows[throwerID]
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
			speedRatio := currSpeed / prevSpeed
			isInelasticBounce := speedRatio < inelasticSpeedRatio && angleChange > inelasticMinAngle
			isElasticBounce := angleChange > elasticBounceAngle
			isDeflection := speedRatio > deflectionRatio && angleChange > deflectionMinAngle
			isLikelyCollision := isInelasticBounce || isElasticBounce || isDeflection

			if !math.IsNaN(angleChange) && !isLikelyCollision {
				track.cumulativeAngle += angleChange
				if angleChange > track.maxFrameAngle {
					track.maxFrameAngle = angleChange
				}
				if angleChange > d.minTrajectoryChange {
					track.violationFrames++
					track.violationAngleSum += angleChange
				}
			}
		}

		// Alignment with the goal chosen at release (the goal the thrower
		// attacks when known, else the one the release pointed at). The goal
		// never changes during the flight, so crossing mid-court cannot
		// fabricate an alignment improvement.
		if track.goalKnown && currSpeed > 0.1 {
			toGoal := track.goalPos.Sub(disc.Position)
			if toGoal.Magnitude() > 0.1 {
				alignment := disc.Velocity.Normalized().Dot(toGoal.Normalized())
				if !math.IsNaN(alignment) {
					if !track.alignmentSet {
						track.initialAlignment = alignment
						track.alignmentSet = true
					}
					track.finalAlignment = alignment
				}
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

	if track.violationFrames < minViolationFrames || track.frameCount < minTrackedFrames {
		return nil
	}

	// Fire if cumulative angle exceeds threshold, OR if the disc significantly
	// improved its alignment with the goal during flight (strong magnetism
	// signal).
	if track.cumulativeAngle <= d.maxCumulativeChange && alignmentImprovement <= alignmentImprovementGate {
		return nil
	}

	// Severity reflects sustained bending: the cumulative bend against the
	// threshold and the mean per-frame bend over the violating frames. A
	// single large (sub-15 degree) frame no longer dominates.
	severity := 0.0
	if track.cumulativeAngle > d.maxCumulativeChange {
		severity = model.SigmoidConfidence(track.cumulativeAngle, d.maxCumulativeChange, 0.2)
	}
	meanViolationAngle := track.violationAngleSum / float64(track.violationFrames)
	if meanViolationAngle > d.minTrajectoryChange {
		sustainedSev := model.SigmoidConfidence(meanViolationAngle, d.minTrajectoryChange, 0.3)
		// Only shapes severity once a gate has passed; weighted by how much
		// of the tracked flight was bending.
		sustainedSev *= math.Min(1.0, float64(track.violationFrames)/float64(track.frameCount))
		severity = math.Max(severity, sustainedSev)
	}
	// Alignment improvement: disc got significantly more aligned with goal during flight
	if alignmentImprovement > alignmentImprovementGate {
		alignSev := model.SigmoidConfidence(alignmentImprovement, 0.4, 5.0)
		severity = math.Max(severity, alignSev)
	}

	// Confidence scales with the number of violation frames.
	frameFactor := math.Min(1.0, float64(track.violationFrames)/fullConfidenceViolationFrames)
	confidence := severity * frameFactor

	distanceTraveled := 0.0
	finalSpeed := 0.0
	if n := len(track.positions); n > 0 {
		distanceTraveled = track.positions[n-1].Distance(track.releasePos)
		finalSpeed = track.velocities[n-1].Magnitude()
	}
	correctionTarget := ""
	if track.goalKnown {
		correctionTarget = fmt.Sprintf("goal z=%+.1f (%s)", track.goalPos.Z(), track.goalLabel)
	}

	ev := d.MakeEvent(matchCtx, track.throwerID, track.releaseFrame, track.releaseTimestamp, severity, confidence,
		model.TrajectoryEvidence{
			CumulativeAngleChange: track.cumulativeAngle,
			MaxSingleFrameChange:  track.maxFrameAngle,
			ViolationFrameCount:   track.violationFrames,
			TotalTrackedFrames:    track.frameCount,
			DistanceTraveled:      distanceTraveled,
			ReleaseSpeed:          track.releaseSpeed,
			FinalSpeed:            finalSpeed,
			CorrectionTarget:      correctionTarget,
			InitialAlignment:      track.initialAlignment,
			FinalAlignment:        track.finalAlignment,
			AlignmentImprovement:  alignmentImprovement,
			CorrectionConfidence:  model.Clamp01(alignmentImprovement),
			TrajectoryPoints:      track.positions,
			VelocityPoints:        track.velocities,
		},
		fmt.Sprintf("trajectory_bend: %.1f deg cumulative (%d violation frames, mean %.1f deg/frame, max %.1f deg/frame)",
			track.cumulativeAngle, track.violationFrames, meanViolationAngle, track.maxFrameAngle),
		fmt.Sprintf("trajectory_bend: < %.1f deg cumulative, < %.1f deg/frame",
			d.maxCumulativeChange, d.minTrajectoryChange),
		model.CausalKey{PlayerID: track.throwerID, FrameStart: track.releaseFrame, FrameEnd: frameIdx, AnomalyType: "trajectory_bend"},
	)
	return &ev
}
