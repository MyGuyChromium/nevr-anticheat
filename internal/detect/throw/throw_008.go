package throw

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	// settleFrames: frames after release that are tracked but not judged
	// (interpolation noise right after the hand opens).
	settleFrames = 3
	// bounceAngleDeg: a direction change above this in one frame is treated
	// as a collision; speed spikes on such frames are ignored.
	bounceAngleDeg = 10.0
	// minSpeedViolations: speed increases required before an event fires.
	minSpeedViolations = 4
)

type speedTrack struct {
	throwerID        string
	releaseFrame     int
	releaseTimestamp float64
	releasePos       model.Vec3
	prevDiscSpeed    float64
	prevDiscVelocity model.Vec3
	violations       int
	maxIncrease      float64
	trackedFrames    int
	samples          [][2]float64
}

// Throw008 detects discs maintaining or increasing speed during free flight.
type Throw008 struct {
	detect.BaseDetector
	speedIncreaseTol float64
	maxTrackFrames   int
	// Per-player tracking — keyed by thrower player ID.
	// Only one active track per player (most recent throw).
	activeTracks map[string]*speedTrack
}

func NewThrow008(params map[string]any) *Throw008 {
	return &Throw008{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_008", DetectorVersion: "1.3.0",
			DetectorName: "Speed-Distance Anomaly", DetectorCategory: "throw",
			Inputs: []string{"disc_state"}, Warmup: 5, Weight: 0.5,
		},
		speedIncreaseTol: detect.GetFloat(params, "speed_increase_tolerance", 5.0),
		maxTrackFrames:   detect.GetInt(params, "max_tracking_frames", 30),
		activeTracks:     make(map[string]*speedTrack),
	}
}

func (d *Throw008) Reset() {
	d.activeTracks = make(map[string]*speedTrack)
}

func (d *Throw008) Configure(params map[string]any) error {
	d.speedIncreaseTol = detect.GetFloat(params, "speed_increase_tolerance", d.speedIncreaseTol)
	d.maxTrackFrames = detect.GetInt(params, "max_tracking_frames", d.maxTrackFrames)
	return nil
}

func (d *Throw008) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	disc, held := currentDisc(players, frameIdx)

	// Start tracking on new throws; a re-throw while a track is open
	// finalizes the earlier one first instead of dropping it.
	for _, pid := range sortedPlayerIDs(players) {
		t := throwAt(players[pid], frameIdx)
		if t == nil {
			continue
		}
		if old, ok := d.activeTracks[pid]; ok {
			if ev := d.finalizeTrack(matchCtx, old, frameIdx); ev != nil {
				events = append(events, *ev)
			}
		}
		d.activeTracks[pid] = &speedTrack{
			throwerID:        pid,
			releaseFrame:     frameIdx,
			releaseTimestamp: t.Timestamp,
			releasePos:       t.ReleasePosition,
			prevDiscSpeed:    t.ReleaseSpeed,
			prevDiscVelocity: t.ReleaseVelocity,
		}
	}

	if disc == nil && !held {
		return events
	}

	// Update all active tracks
	for _, pid := range sortedKeys(d.activeTracks) {
		track := d.activeTracks[pid]
		framesSinceRelease := frameIdx - track.releaseFrame

		// Track disc speed during free flight. The first settleFrames frames
		// only advance the previous-frame state so the first judged frame
		// compares against its immediate predecessor (with bounce filtering).
		if !held && disc.Speed > 0 && framesSinceRelease > 0 {
			if framesSinceRelease >= settleFrames {
				track.trackedFrames++
				speedDelta := disc.Speed - track.prevDiscSpeed

				isBounceFrame := false
				if track.prevDiscVelocity.Magnitude() > 0.1 && disc.Velocity.Magnitude() > 0.1 {
					angleChange := model.RadToDeg(track.prevDiscVelocity.AngleBetween(disc.Velocity))
					if !math.IsNaN(angleChange) && angleChange > bounceAngleDeg {
						isBounceFrame = true
					}
				}

				if speedDelta > d.speedIncreaseTol && !isBounceFrame {
					track.violations++
					if speedDelta > track.maxIncrease {
						track.maxIncrease = speedDelta
					}
				}
				// Distance from the thrower: game-reported when available,
				// otherwise distance from the release point.
				dist := disc.DistanceFromThrower
				if dist <= 0 {
					dist = disc.Position.Distance(track.releasePos)
				}
				track.samples = append(track.samples, [2]float64{dist, disc.Speed})
			}
			track.prevDiscSpeed = disc.Speed
			track.prevDiscVelocity = disc.Velocity
		}

		// Finalize on disc caught (is_held or any fresh player's
		// has_possession) or max frames.
		if held || framesSinceRelease > d.maxTrackFrames {
			if ev := d.finalizeTrack(matchCtx, track, frameIdx); ev != nil {
				events = append(events, *ev)
			}
			delete(d.activeTracks, pid)
		}
	}

	return events
}

// FlushTracks finalizes every open speed track as of frameIdx and clears
// them. Call it at match end so an in-flight throw is judged, not dropped.
func (d *Throw008) FlushTracks(matchCtx *model.MatchContext, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	for _, pid := range sortedKeys(d.activeTracks) {
		if ev := d.finalizeTrack(matchCtx, d.activeTracks[pid], frameIdx); ev != nil {
			events = append(events, *ev)
		}
	}
	d.activeTracks = make(map[string]*speedTrack)
	return events
}

func (d *Throw008) finalizeTrack(matchCtx *model.MatchContext, track *speedTrack, frameIdx int) *model.DetectionEvent {
	if track.violations < minSpeedViolations {
		return nil
	}
	severity := math.Min(1.0, float64(track.violations)/5.0)
	confidence := math.Min(1.0, float64(track.violations)/3.0) * 0.8

	ev := d.MakeEvent(matchCtx, track.throwerID, track.releaseFrame, track.releaseTimestamp, severity, confidence,
		model.SpeedDistanceEvidence{
			SpeedIncreaseCount:   track.violations,
			MaxSpeedIncrease:     track.maxIncrease,
			TrackedFrames:        track.trackedFrames,
			SpeedDistanceSamples: track.samples,
		},
		fmt.Sprintf("speed_increases: %d of %d free-flight frames (max: %.2f m/s)", track.violations, track.trackedFrames, track.maxIncrease),
		fmt.Sprintf("disc speed should not increase > %.1f m/s during free flight", d.speedIncreaseTol),
		model.CausalKey{PlayerID: track.throwerID, FrameStart: track.releaseFrame, FrameEnd: frameIdx, AnomalyType: "speed_distance"},
	)
	return &ev
}
