package state

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// State003 detects excessive shield duration (STATE_003).
//
// STATUS: TELEMETRY_DEPENDENT — requires ShieldActive field, which is not
// confirmed in standard Echo VR API or .echoreplay format. Disabled by default.
type State003 struct {
	detect.BaseDetector
	suspiciousFrames int
	highFrames       int
	impossibleFrames int
	sigmoidSteepness float64

	consecutiveShield map[string]int
	firedTier         map[string]int
	lastFrame         map[string]int // frame index of the last counted sample
}

// NewState003 creates a new STATE_003 Shield Duration detector.
func NewState003(params map[string]any) *State003 {
	d := &State003{
		BaseDetector: detect.BaseDetector{
			DetectorID:       "STATE_003",
			DetectorVersion:  "2.1.0",
			DetectorName:     "Shield Duration Abuse",
			DetectorCategory: "state",
			Inputs:           []string{"shield_state"},
			Warmup:           5,
			Weight:           0.8,
			IsAutoEnforce:    false,
		},
		suspiciousFrames:  detect.GetInt(params, "suspicious_frames", 300),
		highFrames:        detect.GetInt(params, "high_frames", 375),
		impossibleFrames:  detect.GetInt(params, "impossible_frames", 600),
		sigmoidSteepness:  detect.GetFloat(params, "sigmoid_steepness", 0.02),
		consecutiveShield: make(map[string]int),
		firedTier:         make(map[string]int),
		lastFrame:         make(map[string]int),
	}
	return d
}

func (d *State003) Reset() {
	d.consecutiveShield = make(map[string]int)
	d.firedTier = make(map[string]int)
	d.lastFrame = make(map[string]int)
}

func (d *State003) Configure(params map[string]any) error {
	d.suspiciousFrames = detect.GetInt(params, "suspicious_frames", d.suspiciousFrames)
	d.highFrames = detect.GetInt(params, "high_frames", d.highFrames)
	d.impossibleFrames = detect.GetInt(params, "impossible_frames", d.impossibleFrames)
	d.sigmoidSteepness = detect.GetFloat(params, "sigmoid_steepness", d.sigmoidSteepness)
	return nil
}

func (d *State003) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent

	for _, ps := range detect.ActivePlayers(players, frameIdx) {
		pid := ps.PlayerID

		// "Consecutive" is by frame index: a frame the detector did not
		// count (stale player, rejected frame) breaks the run rather than
		// letting two separate shield holds add up to one long one.
		if d.consecutiveShield[pid] > 0 && frameIdx != d.lastFrame[pid]+1 {
			d.consecutiveShield[pid] = 0
			d.firedTier[pid] = 0
		}
		d.lastFrame[pid] = frameIdx

		if ps.ShieldActive {
			d.consecutiveShield[pid]++
		} else {
			d.consecutiveShield[pid] = 0
			d.firedTier[pid] = 0
			continue
		}

		consecutive := d.consecutiveShield[pid]

		// Determine tier
		tier := 0
		var tierName string
		var severity, confidence float64

		switch {
		case consecutive >= d.impossibleFrames:
			tier = 3
			tierName = "impossible"
			severity = 0.95
			confidence = 0.95
		case consecutive >= d.highFrames:
			tier = 2
			tierName = "high"
			severity = 0.75
			confidence = 0.8
		case consecutive >= d.suspiciousFrames:
			tier = 1
			tierName = "suspicious"
			severity = 0.5
			confidence = 0.6
		default:
			continue
		}

		// Only fire once per tier escalation
		if tier <= d.firedTier[pid] {
			continue
		}
		d.firedTier[pid] = tier

		metrics := map[string]float64{
			"consecutive_frames": float64(consecutive),
			"suspicious_thresh":  float64(d.suspiciousFrames),
			"high_thresh":        float64(d.highFrames),
			"impossible_thresh":  float64(d.impossibleFrames),
			"tier":               float64(tier),
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, ps.LastTimestamp,
			severity, confidence,
			model.StateEvidence{
				DetectorSpecific: "shield_duration_" + tierName,
				Metrics:          metrics,
			},
			fmt.Sprintf("shield_frames: %d consecutive (%s)", consecutive, tierName),
			fmt.Sprintf("shield_frames: <%d (suspicious), <%d (high), <%d (impossible)", d.suspiciousFrames, d.highFrames, d.impossibleFrames),
			model.CausalKey{
				PlayerID:    pid,
				FrameStart:  frameIdx - consecutive,
				FrameEnd:    frameIdx,
				AnomalyType: "shield_duration",
			},
		)
		events = append(events, ev)
	}

	return events
}
