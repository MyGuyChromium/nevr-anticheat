package pipeline

import "github.com/nevr-anticheat/nevr-anticheat/internal/model"

// sanitizeObservedHandRotation does not mutate a possibly shared provenance
// pointer. Unknown/false provenance cannot be upgraded by a plausible value;
// invalid numerical input cannot be upgraded by an explicit true flag.
func sanitizeObservedHandRotation(q model.Quat, observed *bool) (model.Quat, *bool, bool) {
	normalized, valid := model.ObservedHandRotation(q, observed)
	if observed == nil {
		return normalized, nil, q != (model.Quat{})
	}
	return normalized, &valid, !valid && q != (model.Quat{})
}
