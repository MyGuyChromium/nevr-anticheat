package state

import (
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	catchContactClear             = "clear"
	catchContactHand              = "hand"
	catchContactHead              = "head"
	catchContactBody              = "body"
	catchContactTorsoEnvelope     = "torso_envelope"
	catchContactUncertainGeometry = "uncertain_geometry"

	// These are numerical/geometry validity guards, not verified avatar physics.
	// Implausible input causes abstention rather than a smaller exclusion volume.
	catchContactMaxTorsoLength = 3.0
	catchContactCoordinateMax  = 1e6
	// catchRelativeSweep may keep the first endpoint for relative motion <=1e-6m.
	// Lowering its witness by this slack also covers roundoff within the input cap.
	catchContactDistanceSlack = 2e-6
)

// catchContactResult describes an exclusion envelope, never a verified collision.
// ClosestDistance is a conservative clearance LOWER BOUND, not a contact location
// or an exact surface distance. In particular, the torso bound can be zero even
// when the disc does not intersect the actual interpolated head-to-body segment.
// All numeric fields stay finite so callers can retain them as review evidence.
type catchContactResult struct {
	Possible        bool
	Kind            string
	ClosestDistance float64
	Margin          float64
	Uncertainty     float64
}

// catchContactEnvelope excludes possible hand/head/body/torso interactions over
// the WHOLE interval, not just sampled instants. The dt-dependent expansion is a
// provisional interpolation allowance, NOT an asserted physical acceleration
// bound. A clear result therefore does not establish absence of real contact.
// Missing, malformed, or numerically unresolvable geometry always abstains.
func catchContactEnvelope(a, b catchSample, margin, accelAllowance float64) catchContactResult {
	result := catchContactResult{Possible: true, Kind: catchContactUncertainGeometry}
	if math.IsNaN(margin) || math.IsInf(margin, 0) || margin < 0 {
		return result
	}
	result.Margin = margin
	dt := b.timestamp - a.timestamp
	if math.IsNaN(a.timestamp) || math.IsInf(a.timestamp, 0) || a.timestamp < 0 ||
		math.IsNaN(b.timestamp) || math.IsInf(b.timestamp, 0) || dt <= 0 || math.IsInf(dt, 0) ||
		math.IsNaN(accelAllowance) || math.IsInf(accelAllowance, 0) || accelAllowance < 0 || accelAllowance > 200 {
		return result
	}
	uncertainty := (accelAllowance / 8) * dt * dt
	expanded := margin + uncertainty
	if math.IsNaN(expanded) || math.IsInf(expanded, 0) {
		return result
	}
	result.Margin, result.Uncertainty = expanded, uncertainty
	if len(a.poses) == 0 || len(a.poses) > catchPlayerLimit || !catchSameRoster(a, b) ||
		!catchContactVector(a.position) || !catchContactVector(b.position) {
		return result
	}
	ids := make(map[string]struct{}, len(a.poses))
	for i, p := range a.poses {
		q := b.poses[i]
		if p.id == "" {
			return result
		}
		if _, duplicate := ids[p.id]; duplicate {
			return result
		}
		ids[p.id] = struct{}{}
		for _, pose := range []catchPose{p, q} {
			for _, point := range []model.Vec3{pose.left, pose.right, pose.head, pose.body} {
				// Raw zero poses represent missing tracking in this telemetry contract.
				if point.IsZero() || !catchContactVector(point) {
					return result
				}
			}
			if pose.head.Distance(pose.body) > catchContactMaxTorsoLength {
				return result
			}
		}
	}

	result.Possible, result.Kind = false, catchContactClear
	result.ClosestDistance = math.MaxFloat64
	closestKind := catchContactClear
	consider := func(distance float64, kind string) {
		// Conservative numerical slack cannot narrow the original contact margin.
		distance = math.Max(0, distance-catchContactDistanceSlack)
		if distance < result.ClosestDistance {
			result.ClosestDistance, closestKind = distance, kind
		}
	}
	for i, p := range a.poses {
		q := b.poses[i]
		for j, start := range []model.Vec3{p.left, p.right, p.head, p.body} {
			end := []model.Vec3{q.left, q.right, q.head, q.body}[j]
			kind := []string{catchContactHand, catchContactHand, catchContactHead, catchContactBody}[j]
			consider(catchRelativeSweep(a.position, b.position, start, end), kind)
		}

		// At every interpolation time, the entire head/body segment lies inside
		// the sphere centered at their midpoint with radius half its length.
		// The interpolated segment length cannot exceed the longer endpoint
		// length (convexity of the norm). Sweeping that midpoint with the fixed
		// larger radius therefore encloses EVERY intermediate torso segment,
		// including rotation/shear between endpoints. No time sampling is used.
		mid0 := p.body.Add(p.head).Scale(.5)
		mid1 := q.body.Add(q.head).Scale(.5)
		radius := .5 * math.Max(p.body.Distance(p.head), q.body.Distance(q.head))
		lowerBound := catchRelativeSweep(a.position, b.position, mid0, mid1) - radius
		consider(lowerBound, catchContactTorsoEnvelope)
	}
	if result.ClosestDistance <= result.Margin {
		result.Possible, result.Kind = true, closestKind
	}
	return result
}

func catchContactVector(v model.Vec3) bool {
	return catchFinite(v) && math.Abs(v[0]) <= catchContactCoordinateMax &&
		math.Abs(v[1]) <= catchContactCoordinateMax && math.Abs(v[2]) <= catchContactCoordinateMax
}
