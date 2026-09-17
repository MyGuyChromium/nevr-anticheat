package state

import "github.com/nevr-anticheat/nevr-anticheat/internal/model"

// thresholdFrameRate is the rate the legacy frame-count thresholds
// (min_stun_frames, min_cooldown_frames) were written against: the bridge's
// nominal 15 Hz poll. It is the ONLY rate that converts those thresholds to
// seconds.
//
// MatchContext.TickRate is now the MEASURED capture rate of a recording
// (replay ingest derives it from the median positive sample spacing; real
// recordings arrive at roughly 15, 20 and 30 Hz). Dividing a frame-count
// threshold by the measured rate would make "20 frames" mean 1.33 s on a
// 15 Hz recording and 0.67 s on a 30 Hz one, silently loosening the check
// on faster recorders. The threshold is a duration of game time, so it
// stays 20/15 s on every recorder; the detectors compare it with elapsed
// sample time, not with a frame count, whenever timestamps exist.
const thresholdFrameRate = 15.0

// tickRate returns the rate, in Hz, at which frame-count thresholds are
// denominated. It deliberately ignores MatchContext.TickRate (see
// thresholdFrameRate): every context that set it before replay ingest
// measured it carried the same nominal 15 Hz, so this changes no result.
func tickRate(_ *model.MatchContext) float64 {
	return thresholdFrameRate
}

// physicsStunDuration returns the game's stun duration in seconds (0 if unknown).
func physicsStunDuration(matchCtx *model.MatchContext) float64 {
	if matchCtx == nil {
		return 0
	}
	return matchCtx.Physics.StunDuration
}

// physicsShieldCooldown returns the game's shield cooldown in seconds (0 if unknown).
func physicsShieldCooldown(matchCtx *model.MatchContext) float64 {
	if matchCtx == nil {
		return 0
	}
	return matchCtx.Physics.ShieldCooldown
}
