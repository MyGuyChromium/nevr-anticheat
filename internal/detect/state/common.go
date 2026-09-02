package state

import "github.com/nevr-anticheat/nevr-anticheat/internal/model"

// defaultTickRate is the bridge's nominal poll rate, used only to convert
// legacy frame-count thresholds to seconds when the match context does
// not carry a tick rate.
const defaultTickRate = 15.0

// tickRate returns the match tick rate in Hz, falling back to the nominal 15 Hz.
func tickRate(matchCtx *model.MatchContext) float64 {
	if matchCtx != nil && matchCtx.TickRate > 0 {
		return matchCtx.TickRate
	}
	return defaultTickRate
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
