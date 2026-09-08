package model

import "math"

// Quat is a quaternion (x, y, z, w).
type Quat [4]float64

func (q Quat) X() float64 { return q[0] }
func (q Quat) Y() float64 { return q[1] }
func (q Quat) Z() float64 { return q[2] }
func (q Quat) W() float64 { return q[3] }

// QuatIdentity returns the identity quaternion.
func QuatIdentity() Quat {
	return Quat{0, 0, 0, 1}
}

func (q Quat) Dot(o Quat) float64 {
	return q[0]*o[0] + q[1]*o[1] + q[2]*o[2] + q[3]*o[3]
}

func (q Quat) Magnitude() float64 {
	return math.Sqrt(q[0]*q[0] + q[1]*q[1] + q[2]*q[2] + q[3]*q[3])
}

func (q Quat) Normalize() Quat {
	m := q.Magnitude()
	if m < 1e-12 {
		return QuatIdentity()
	}
	return Quat{q[0] / m, q[1] / m, q[2] / m, q[3] / m}
}

func (q Quat) Conjugate() Quat {
	return Quat{-q[0], -q[1], -q[2], q[3]}
}

func (q Quat) Multiply(o Quat) Quat {
	return Quat{
		q[3]*o[0] + q[0]*o[3] + q[1]*o[2] - q[2]*o[1],
		q[3]*o[1] - q[0]*o[2] + q[1]*o[3] + q[2]*o[0],
		q[3]*o[2] + q[0]*o[1] - q[1]*o[0] + q[2]*o[3],
		q[3]*o[3] - q[0]*o[0] - q[1]*o[1] - q[2]*o[2],
	}
}

// IsUnit returns true if the quaternion has unit magnitude (within tolerance).
func (q Quat) IsUnit() bool {
	m := q.Magnitude()
	return m >= 0.99 && m <= 1.01
}

// AngularDistance returns the shortest angle in radians between two finite,
// near-unit quaternions, normalized before the comparison. Invalid rotations
// return NaN; callers must preserve observation validity rather than treating
// that result as a stationary wrist. q and -q represent the same rotation.
func (q Quat) AngularDistance(o Quat) float64 {
	var valid bool
	q, valid = q.NormalizedRotation()
	if !valid {
		return math.NaN()
	}
	o, valid = o.NormalizedRotation()
	if !valid {
		return math.NaN()
	}
	d := math.Abs(q.Dot(o))
	if d >= 1-1e-15 {
		// Normalizing rounded samples can leave the self-dot a few ulps
		// below one. That is not measured motion, including for q versus -q.
		return 0
	}
	return 2.0 * math.Acos(d)
}

// Slerp performs spherical linear interpolation between q and o.
func (q Quat) Slerp(o Quat, t float64) Quat {
	d := q.Dot(o)
	// Handle double-cover
	target := o
	if d < 0 {
		target = Quat{-o[0], -o[1], -o[2], -o[3]}
		d = -d
	}
	if d > 0.9995 {
		// Very close — use linear interpolation
		result := Quat{
			q[0] + t*(target[0]-q[0]),
			q[1] + t*(target[1]-q[1]),
			q[2] + t*(target[2]-q[2]),
			q[3] + t*(target[3]-q[3]),
		}
		return result.Normalize()
	}
	theta := math.Acos(Clamp(d, -1, 1))
	sinTheta := math.Sin(theta)
	if sinTheta < 1e-12 {
		return q
	}
	a := math.Sin((1-t)*theta) / sinTheta
	b := math.Sin(t*theta) / sinTheta
	return Quat{
		a*q[0] + b*target[0],
		a*q[1] + b*target[1],
		a*q[2] + b*target[2],
		a*q[3] + b*target[3],
	}
}

// Rotate applies the rotation q to v (v' = q v q*). q is assumed to be unit.
func (q Quat) Rotate(v Vec3) Vec3 {
	axis := Vec3{q[0], q[1], q[2]}
	t := axis.Cross(v).Scale(2.0)
	return v.Add(t.Scale(q[3])).Add(axis.Cross(t))
}

// BasisQuality reports how well a (forward, left, up) direction-vector triple
// described a proper rotation. It is a bit set: several flags can be raised at
// once. BasisProper (0) means the input was an orthonormal right-handed basis.
type BasisQuality uint8

const (
	// BasisProper: orthonormal, det(left, up, forward) = +1.
	BasisProper BasisQuality = 0
	// BasisReflected: det(left, up, forward) < 0, i.e. the supplied "left" vector
	// points where a right-handed basis would put "right". The converter uses
	// -left in that case, so the returned rotation is still exact; the flag lets
	// producers learn which handedness convention the data source uses.
	BasisReflected BasisQuality = 1 << iota
	// BasisNonOrthonormal: vectors were not unit length and/or not mutually
	// perpendicular (or "left" was missing). The basis was re-orthonormalised
	// from forward and up (Gram-Schmidt) before conversion.
	BasisNonOrthonormal
	// BasisDegenerate: forward or up was zero/NaN, or they were collinear. No
	// rotation can be derived; the returned quaternion is the ZERO quaternion
	// (IsUnit() == false), never a silent identity.
	BasisDegenerate
)

func (b BasisQuality) Reflected() bool      { return b&BasisReflected != 0 }
func (b BasisQuality) NonOrthonormal() bool { return b&BasisNonOrthonormal != 0 }
func (b BasisQuality) Degenerate() bool     { return b&BasisDegenerate != 0 }

func (b BasisQuality) String() string {
	if b == BasisProper {
		return "proper"
	}
	s := ""
	add := func(name string) {
		if s != "" {
			s += "|"
		}
		s += name
	}
	if b.Degenerate() {
		add("degenerate")
	}
	if b.Reflected() {
		add("reflected")
	}
	if b.NonOrthonormal() {
		add("non_orthonormal")
	}
	return s
}

// basisTolerance is the allowed deviation from unit length / perpendicularity
// before a triple is flagged as non-orthonormal. Echo VR prints direction
// vectors with ~6 significant digits, so genuine data sits well inside 5e-3.
const basisTolerance = 5e-3

// QuatFromDirectionVectors constructs a quaternion from forward, left, up
// direction vectors (Echo VR's hand/body pose format). It is the convenience
// form of QuatFromDirectionVectorsChecked and discards the quality report.
func QuatFromDirectionVectors(forward, left, up Vec3) Quat {
	q, _ := QuatFromDirectionVectorsChecked(forward, left, up)
	return q
}

// QuatFromDirectionVectorsChecked converts a direction-vector triple into a
// unit quaternion and reports the quality of the input basis.
//
// The rotation matrix has columns (left, up, forward): the quaternion maps
// +X to left, +Y to up and +Z to forward. Only proper rotations (det = +1) can
// be represented by a quaternion, so:
//   - forward and up are normalised and up is made perpendicular to forward;
//   - left is REBUILT as up x forward, which always yields det = +1;
//   - the supplied left is used only to measure the handedness of the input:
//     left . (up x forward) < 0 means the source uses left = -(up x forward)
//     (a reflected basis) and BasisReflected is raised.
//
// Degenerate input (zero or collinear forward/up, NaN/Inf in forward or up)
// returns the zero quaternion together with BasisDegenerate so callers can
// tell "no tracking" apart from a real identity pose. A bad LEFT (zero,
// NaN/Inf) is not degenerate: the orientation is fully defined by
// forward/up, left is rebuilt as up x forward, and the input is flagged
// BasisNonOrthonormal because its handedness could not be measured.
func QuatFromDirectionVectorsChecked(forward, left, up Vec3) (Quat, BasisQuality) {
	quality := BasisProper

	if forward.HasNaN() || forward.HasInf() || up.HasNaN() || up.HasInf() {
		return Quat{}, BasisDegenerate
	}

	fMag := forward.Magnitude()
	uMag := up.Magnitude()
	lMag := left.Magnitude()
	if fMag < 1e-9 || uMag < 1e-9 || math.IsInf(fMag, 0) || math.IsInf(uMag, 0) {
		return Quat{}, BasisDegenerate
	}

	f := forward.Scale(1.0 / fMag)
	uProj := up.Sub(f.Scale(up.Dot(f)))
	uProjMag := uProj.Magnitude()
	if uProjMag < 1e-6 || math.IsNaN(uProjMag) || math.IsInf(uProjMag, 0) {
		// up is (anti)parallel to forward: no plane, no rotation.
		return Quat{}, BasisDegenerate
	}
	u := uProj.Scale(1.0 / uProjMag)
	l := u.Cross(f) // proper right-handed left axis for columns (left, up, forward)

	// Orthonormality of the raw input.
	if math.Abs(fMag-1) > basisTolerance || math.Abs(uMag-1) > basisTolerance ||
		math.Abs(forward.Dot(up)) > basisTolerance {
		quality |= BasisNonOrthonormal
	}
	if lMag < 1e-9 || left.HasNaN() || left.HasInf() {
		// Missing or non-finite left: orientation is still fully defined by
		// forward/up (the output uses the rebuilt l = up x forward) but the
		// handedness of the source cannot be measured.
		quality |= BasisNonOrthonormal
	} else {
		det := left.Dot(u.Cross(f)) / lMag
		if det < 0 {
			quality |= BasisReflected
		}
		if math.Abs(lMag-1) > basisTolerance || math.Abs(math.Abs(det)-1) > basisTolerance {
			quality |= BasisNonOrthonormal
		}
	}

	return quatFromColumns(l, u, f), quality
}

// quatFromColumns converts a proper rotation matrix with columns (c0, c1, c2)
// into a unit quaternion (x, y, z, w).
func quatFromColumns(c0, c1, c2 Vec3) Quat {
	m00, m01, m02 := c0[0], c1[0], c2[0]
	m10, m11, m12 := c0[1], c1[1], c2[1]
	m20, m21, m22 := c0[2], c1[2], c2[2]

	trace := m00 + m11 + m22
	var q Quat
	if trace > 0 {
		s := 0.5 / math.Sqrt(trace+1.0)
		q[3] = 0.25 / s
		q[0] = (m21 - m12) * s
		q[1] = (m02 - m20) * s
		q[2] = (m10 - m01) * s
	} else if m00 > m11 && m00 > m22 {
		s := 2.0 * math.Sqrt(1.0+m00-m11-m22)
		q[3] = (m21 - m12) / s
		q[0] = 0.25 * s
		q[1] = (m01 + m10) / s
		q[2] = (m02 + m20) / s
	} else if m11 > m22 {
		s := 2.0 * math.Sqrt(1.0+m11-m00-m22)
		q[3] = (m02 - m20) / s
		q[0] = (m01 + m10) / s
		q[1] = 0.25 * s
		q[2] = (m12 + m21) / s
	} else {
		s := 2.0 * math.Sqrt(1.0+m22-m00-m11)
		q[3] = (m10 - m01) / s
		q[0] = (m02 + m20) / s
		q[1] = (m12 + m21) / s
		q[2] = 0.25 * s
	}
	return q.Normalize()
}

// QuaternionMean computes the mean quaternion from a slice using iterative averaging.
func QuaternionMean(quats []Quat) Quat {
	if len(quats) == 0 {
		return QuatIdentity()
	}
	mean := quats[0]
	for i := 1; i < len(quats); i++ {
		t := 1.0 / float64(i+1)
		mean = mean.Slerp(quats[i], t)
	}
	return mean.Normalize()
}
