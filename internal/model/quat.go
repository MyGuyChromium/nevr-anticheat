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

// AngularDistance returns the angle in radians between two quaternions.
// Handles quaternion double-cover (q and -q represent the same rotation).
func (q Quat) AngularDistance(o Quat) float64 {
	d := math.Abs(q.Dot(o))
	if d > 1.0 {
		d = 1.0
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

// QuatFromDirectionVectors constructs a quaternion from forward, left, up direction vectors.
// This is needed for converting Echo VR's hand pose format (3 direction vectors) to quaternion.
func QuatFromDirectionVectors(forward, left, up Vec3) Quat {
	// Build rotation matrix from direction vectors:
	// Column 0 = left (X), Column 1 = up (Y), Column 2 = forward (Z)
	// Then convert rotation matrix to quaternion.
	m00, m01, m02 := left[0], up[0], forward[0]
	m10, m11, m12 := left[1], up[1], forward[1]
	m20, m21, m22 := left[2], up[2], forward[2]

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
