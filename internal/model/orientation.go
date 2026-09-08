package model

import "math"

// NormalizedRotation accepts only finite quaternions within the existing
// near-unit tolerance. It does not repair an arbitrary norm or turn missing
// data into identity. Observation provenance is a separate prerequisite.
func (q Quat) NormalizedRotation() (Quat, bool) {
	for _, component := range q {
		if math.IsNaN(component) || math.IsInf(component, 0) {
			return Quat{}, false
		}
	}
	magnitude := q.Magnitude()
	if magnitude < .99 || magnitude > 1.01 || math.IsNaN(magnitude) || math.IsInf(magnitude, 0) {
		return Quat{}, false
	}
	return Quat{q[0] / magnitude, q[1] / magnitude, q[2] / magnitude, q[3] / magnitude}, true
}

// ObservedHandRotation preserves explicit source validity. Unknown legacy
// provenance and known fallbacks both remain unavailable even if q is identity.
func ObservedHandRotation(q Quat, observed *bool) (Quat, bool) {
	if observed == nil || !*observed {
		return Quat{}, false
	}
	return q.NormalizedRotation()
}
