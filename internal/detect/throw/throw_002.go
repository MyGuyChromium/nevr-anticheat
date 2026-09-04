package throw

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Throw002 detects an impossible disc speed jump at release (THROW_002).
//
// Approach: compare the disc speed in the last pre-release snapshot (the
// frame immediately BEFORE the release frame, as built by the feature
// extractor) against the release speed. A legitimate wrist flick can impart
// at most ~18.9 m/s (the engine-enforced cap). A sudden jump larger than
// maxSpeedDelta is physically impossible regardless of player motion.
//
// Snapshots whose source frame carried no disc state are unusable and skipped.
type Throw002 struct {
	detect.BaseDetector
	maxSpeedDelta float64
}

func NewThrow002(params map[string]any) *Throw002 {
	return &Throw002{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_002", DetectorVersion: "2.0.0",
			DetectorName: "Impossible Disc Acceleration", DetectorCategory: "throw",
			Inputs: []string{"throw_event"}, Warmup: 5, Weight: 0.7,
		},
		// 22.0 m/s: above the 18.9 m/s physics cap, well below
		// any cheat-injected value. Calibrate against real match data.
		maxSpeedDelta: detect.GetFloat(params, "max_speed_delta", 22.0),
	}
}

func (d *Throw002) Reset() {}
func (d *Throw002) Configure(params map[string]any) error {
	d.maxSpeedDelta = detect.GetFloat(params, "max_speed_delta", d.maxSpeedDelta)
	return nil
}

func (d *Throw002) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	for _, pid := range sortedPlayerIDs(players) {
		ps := players[pid]
		t := throwAt(ps, frameIdx)
		if t == nil || len(t.PreReleaseFrames) < 1 {
			continue
		}

		lastPre := t.PreReleaseFrames[len(t.PreReleaseFrames)-1]
		if lastPre.DiscMissing || lastPre.FrameIndex >= t.FrameIndex {
			// No disc observation before release (or a malformed snapshot
			// that includes the release frame): nothing to compare against.
			continue
		}
		lastPreSpd := lastPre.DiscVelocity.Magnitude()
		delta := t.ReleaseSpeed - lastPreSpd

		if delta <= d.maxSpeedDelta {
			continue
		}

		severity := model.SigmoidConfidence(delta, d.maxSpeedDelta, 0.05)
		confidence := severity
		if t.Attribution.Confidence > 0 {
			confidence *= t.Attribution.Confidence
		}

		ev := d.MakeEvent(matchCtx, pid, frameIdx, t.Timestamp, severity, confidence,
			model.DiscAccelerationEvidence{
				SpeedDelta:       delta,
				ReleaseSpeed:     t.ReleaseSpeed,
				PreReleaseSpeed:  lastPreSpd,
				PreReleaseFrames: t.PreReleaseFrames,
			},
			fmt.Sprintf("speed_delta: %.2f m/s (pre: %.2f → release: %.2f)", delta, lastPreSpd, t.ReleaseSpeed),
			fmt.Sprintf("speed_delta: < %.2f m/s", d.maxSpeedDelta),
			model.CausalKey{PlayerID: pid, FrameStart: lastPre.FrameIndex, FrameEnd: frameIdx + 2, AnomalyType: "disc_acceleration"},
		)
		ev.Attribution = &t.Attribution
		events = append(events, ev)
	}
	return events
}
