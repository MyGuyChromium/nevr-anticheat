package pipeline

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
