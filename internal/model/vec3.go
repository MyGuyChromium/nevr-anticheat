package model

import "math"

// Vec3 is a 3D vector.
type Vec3 [3]float64

func (v Vec3) X() float64 { return v[0] }
func (v Vec3) Y() float64 { return v[1] }
func (v Vec3) Z() float64 { return v[2] }

func (v Vec3) Add(o Vec3) Vec3 {
	return Vec3{v[0] + o[0], v[1] + o[1], v[2] + o[2]}
}

func (v Vec3) Sub(o Vec3) Vec3 {
	return Vec3{v[0] - o[0], v[1] - o[1], v[2] - o[2]}
}

func (v Vec3) Scale(s float64) Vec3 {
	return Vec3{v[0] * s, v[1] * s, v[2] * s}
}

func (v Vec3) Dot(o Vec3) float64 {
	return v[0]*o[0] + v[1]*o[1] + v[2]*o[2]
}

func (v Vec3) Cross(o Vec3) Vec3 {
	return Vec3{
		v[1]*o[2] - v[2]*o[1],
		v[2]*o[0] - v[0]*o[2],
		v[0]*o[1] - v[1]*o[0],
	}
}

func (v Vec3) Magnitude() float64 {
	return math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])
}

func (v Vec3) MagnitudeSq() float64 {
	return v[0]*v[0] + v[1]*v[1] + v[2]*v[2]
}

func (v Vec3) Normalized() Vec3 {
	m := v.Magnitude()
	if m < 1e-12 {
		return Vec3{}
	}
	return Vec3{v[0] / m, v[1] / m, v[2] / m}
}

func (v Vec3) Distance(o Vec3) float64 {
	return v.Sub(o).Magnitude()
}

func (v Vec3) IsZero() bool {
	return v[0] == 0 && v[1] == 0 && v[2] == 0
}

func (v Vec3) HasNaN() bool {
	return math.IsNaN(v[0]) || math.IsNaN(v[1]) || math.IsNaN(v[2])
}

func (v Vec3) HasInf() bool {
	return math.IsInf(v[0], 0) || math.IsInf(v[1], 0) || math.IsInf(v[2], 0)
}

// AngleBetween returns the angle in radians between two vectors.
func (v Vec3) AngleBetween(o Vec3) float64 {
	vm := v.Magnitude()
	om := o.Magnitude()
	if vm < 1e-12 || om < 1e-12 {
		return 0
	}
	cosAngle := v.Dot(o) / (vm * om)
	cosAngle = Clamp(cosAngle, -1.0, 1.0)
	return math.Acos(cosAngle)
}

// AngleBetweenDeg returns the angle in degrees between two vectors.
func (v Vec3) AngleBetweenDeg(o Vec3) float64 {
	return RadToDeg(v.AngleBetween(o))
}

// Lerp linearly interpolates between v and o.
func (v Vec3) Lerp(o Vec3, t float64) Vec3 {
	return Vec3{
		v[0] + (o[0]-v[0])*t,
		v[1] + (o[1]-v[1])*t,
		v[2] + (o[2]-v[2])*t,
	}
}
