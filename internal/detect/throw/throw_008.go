package throw

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

type speedTrack struct {
	throwerID       string
	releaseFrame    int
	releaseTimestamp float64
	prevDiscSpeed   float64
	violations      int
	maxIncrease     float64
	samples         [][2]float64
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
			DetectorID: "THROW_008", DetectorVersion: "1.2.0",
			DetectorName: "Speed-Distance Anomaly", DetectorCategory: "throw",
			Inputs: []string{"disc_state"}, Warmup: 5, Weight: 0.5,
		},
		speedIncreaseTol: detect.GetFloat(params, "speed_increase_tolerance", 0.5),
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

	// Get disc state
	var disc *model.DiscState
	for _, ps := range players {
		if ps.CurrentDisc != nil {
			disc = ps.CurrentDisc
			break
		}
	}

	// Start tracking on new throws
	for _, ps := range players {
		if ps.LastThrow != nil && ps.LastThrow.FrameIndex == frameIdx {
			d.activeTracks[ps.PlayerID] = &speedTrack{
				throwerID:       ps.PlayerID,
				releaseFrame:    frameIdx,
				releaseTimestamp: ps.LastThrow.Timestamp,
				prevDiscSpeed:   ps.LastThrow.ReleaseSpeed,
			}
		}
	}

	if disc == nil {
		return events
	}

	// Update all active tracks
	for pid, track := range d.activeTracks {
		// Skip first 3 frames after release (interpolation noise)
		if frameIdx-track.releaseFrame < 3 {
			continue
		}

		// Track disc speed during free flight
		if !disc.IsHeld && disc.Speed > 0 {
			speedDelta := disc.Speed - track.prevDiscSpeed
			if speedDelta > d.speedIncreaseTol {
				track.violations++
				if speedDelta > track.maxIncrease {
					track.maxIncrease = speedDelta
				}
			}
			if disc.DistanceFromThrower > 0 {
				track.samples = append(track.samples, [2]float64{disc.DistanceFromThrower, disc.Speed})
			}
			track.prevDiscSpeed = disc.Speed
		}

		// Finalize on disc caught or max frames
		if disc.IsHeld || frameIdx-track.releaseFrame > d.maxTrackFrames {
			if track.violations > 0 {
				severity := math.Min(1.0, float64(track.violations)/5.0)
				confidence := math.Min(1.0, float64(track.violations)/3.0) * 0.8

				ev := model.DetectionEvent{
					EventID: model.NewEventID(), DetectorID: "THROW_008", DetectorVersion: "1.2.0",
					MatchID: matchCtx.MatchID, PlayerID: track.throwerID,
					FrameIndex: track.releaseFrame, FrameRangeStart: track.releaseFrame, FrameRangeEnd: frameIdx,
					Timestamp: track.releaseTimestamp,
					Severity: model.Clamp01(severity), Confidence: model.Clamp01(confidence),
					Evidence: model.SpeedDistanceEvidence{
						SpeedIncreaseCount:   track.violations,
						MaxSpeedIncrease:     track.maxIncrease,
						SpeedDistanceSamples: track.samples,
					},
					ObservedValue: fmt.Sprintf("speed_increases: %d (max: %.2f m/s)", track.violations, track.maxIncrease),
					ExpectedRange: fmt.Sprintf("disc speed should not increase > %.1f m/s during free flight", d.speedIncreaseTol),
					CausalKey:         model.CausalKey{PlayerID: track.throwerID, FrameStart: track.releaseFrame, FrameEnd: frameIdx, AnomalyType: "speed_distance"},
					EnforcementWeight: 0.5,
				}
				events = append(events, ev)
			}
			delete(d.activeTracks, pid)
		}
	}

	return events
}
