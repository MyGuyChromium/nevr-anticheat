package pipeline

import (
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// Current operational health can recover; it must not erase the reason a past
// acquisition could not be inspected. Keep only the validator's actual bounded
// codes in existing per-detector traces, not a reconstructed detector verdict.
func (c *coverageTracker) traceSanitizedDisc(playerID string, frame int, reason string) {
	switch reason {
	case SanitizedDisc, SanitizedDiscOutOfRange, SanitizedDiscObservation:
	default:
		return
	}
	for _, detector := range c.detectors {
		if detector.Enabled && healthAffects("missing_disc", detector.DetectorID) {
			c.trace(detector.DetectorID, playerID, frame, reason)
		}
	}
}

// DiscJumpServerRespawn is the decision-trace code for a disc discontinuity
// recognised as the game placing the disc back at a team's nest after a goal.
const DiscJumpServerRespawn = "server_respawn"

// The game's arena rules place the respawned disc at x = 0, y = 4.536 and
// z = +27.5 or -27.5 (the conceding side), where it waits unowned through
// round_start. Only the landing position identifies it; the timing inside the
// goal cycle comes from a single capture and is not used.
const (
	discRespawnX = 0.0
	discRespawnY = 4.536
	discRespawnZ = 27.5
	// discRespawnTolerance (metres, per axis) covers float metres on one
	// path, 1 mm wire quantisation on the other and a first sample taken a
	// moment after the placement. Handicapped private matches may move the
	// nest; a landing outside the tolerance is simply not classified.
	discRespawnTolerance = 0.05
	// discRespawnMinJump is the smallest single-sample displacement treated
	// as a placement rather than motion: a disc at the speed cap covers about
	// 1.3 m between two samples of the slowest (15 Hz) real recorder.
	discRespawnMinJump = 2.0
)

// classifyDiscJump names a disc displacement between two consecutive samples.
// It returns DiscJumpServerRespawn when, outside live play, the disc lands at
// a nest from somewhere else, and "" for everything else. "" means
// unclassified: it is never an anomaly, a sanitisation or a detector input.
func classifyDiscJump(previous, current model.Vec3, activePhase bool) string {
	if activePhase || previous.HasNaN() || previous.HasInf() || current.HasNaN() || current.HasInf() {
		return ""
	}
	if !atDiscRespawn(current) || atDiscRespawn(previous) {
		return ""
	}
	if current.Distance(previous) < discRespawnMinJump {
		return ""
	}
	return DiscJumpServerRespawn
}

func atDiscRespawn(p model.Vec3) bool {
	return math.Abs(p[0]-discRespawnX) <= discRespawnTolerance &&
		math.Abs(p[1]-discRespawnY) <= discRespawnTolerance &&
		math.Abs(math.Abs(p[2])-discRespawnZ) <= discRespawnTolerance
}

// traceDiscJump records a recognised server respawn in the same per-detector
// traces that carry disc sanitisation history, so a reviewer sees that the
// disc discontinuity at that frame is game choreography and not lost data.
// A missing sample on either side, live play, or a landing anywhere else
// records nothing.
func (c *coverageTracker) traceDiscJump(playerID string, frame int, previous, current *model.DiscState, activePhase bool) {
	if previous == nil || current == nil {
		return
	}
	reason := classifyDiscJump(previous.Position, current.Position, activePhase)
	if reason == "" {
		return
	}
	for _, detector := range c.detectors {
		if detector.Enabled && healthAffects("missing_disc", detector.DetectorID) {
			c.trace(detector.DetectorID, playerID, frame, reason)
		}
	}
}
