package sqlite

import (
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func validateDataHealth(h *model.DataHealth) error {
	if h == nil {
		return nil
	}
	if h.Version != 1 || (h.State != model.HealthHealthy && h.State != model.HealthDegraded && h.State != model.HealthBlind) ||
		h.LastFrame < -1 || h.RecoverySamplesRemaining < 0 || h.HealthySamples < 0 || h.DegradedSamples < 0 || h.BlindSamples < 0 ||
		len(h.Reasons) > 64 || len(h.AffectedDetectors) > 128 {
		return fmt.Errorf("invalid live data-health snapshot")
	}
	return nil
}

// Incoming health is already a cumulative source/player snapshot. Replacing
// it by monotonic frame watermark avoids summing overlapping batch counters or
// claiming full denominators for the surrounding partial live diagnostics.
func mergeLiveDataHealth(target *model.PlayerCoverage, incoming *model.DataHealth) bool {
	if incoming == nil || hasLivePersistenceFailure(target) {
		return false
	}
	if !newerHealthSnapshot(target.DataHealth, incoming) {
		return false
	}
	target.DataHealth = incoming.Clone()
	target.QualityGated = incoming.State != model.HealthHealthy
	target.Status = model.ReviewStatusInsufficientData
	return true
}

func newerHealthSnapshot(old, incoming *model.DataHealth) bool {
	if incoming == nil {
		return false
	}
	return old == nil || incoming.LastFrame > old.LastFrame ||
		(incoming.LastFrame == old.LastFrame && incoming.Revision > old.Revision)
}
