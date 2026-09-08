package model

import (
	"math"
	"testing"
)

func TestOrientationNearUnitAndDoubleCoverHaveNoInventedMotion(t *testing.T) {
	for _, original := range []Quat{QuatIdentity(), {.2, .3, .4, math.Sqrt(.71)}} {
		for _, scale := range []float64{.995, 1, 1.005} {
			q := Quat{original[0] * scale, original[1] * scale, original[2] * scale, original[3] * scale}
			negative := Quat{-q[0], -q[1], -q[2], -q[3]}
			if angle := q.AngularDistance(q); angle != 0 {
				t.Fatalf("identical near-unit quaternion gained motion: scale=%g angle=%g", scale, angle)
			}
			if angle := q.AngularDistance(negative); angle != 0 {
				t.Fatalf("quaternion sign change gained motion: scale=%g angle=%g", scale, angle)
			}
			if angle := original.AngularDistance(q); angle != 0 {
				t.Fatalf("norm rounding alone gained motion: scale=%g angle=%g", scale, angle)
			}
		}
	}
}

func TestOrientationRequiresExplicitObservationAndValidNorm(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name     string
		q        Quat
		observed *bool
		want     bool
	}{
		{"observed identity", QuatIdentity(), &yes, true},
		{"observed rounded identity", Quat{0, 0, 0, .995}, &yes, true},
		{"legacy identity unknown", QuatIdentity(), nil, false},
		{"explicit fallback identity", QuatIdentity(), &no, false},
		{"zero with true flag", Quat{}, &yes, false},
		{"bad norm", Quat{0, 0, 0, 2}, &yes, false},
		{"nan", Quat{math.NaN(), 0, 0, 1}, &yes, false},
		{"infinite", Quat{math.Inf(1), 0, 0, 1}, &yes, false},
		{"finite overflow", Quat{math.MaxFloat64, 0, 0, 1}, &yes, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, valid := ObservedHandRotation(tc.q, tc.observed)
			if valid != tc.want || (valid && math.Abs(q.Magnitude()-1) > 1e-12) || (!valid && q != (Quat{})) {
				t.Fatalf("observation=%v value=%v: got q=%v valid=%t", tc.observed, tc.q, q, valid)
			}
		})
	}
	if !math.IsNaN((Quat{}).AngularDistance(QuatIdentity())) {
		t.Fatal("invalid angular distance must not become a stationary measurement")
	}
}

func TestOrientationOverflowingBasisCannotManufactureIdentity(t *testing.T) {
	for _, basis := range [][3]Vec3{
		{{math.MaxFloat64, 0, 0}, {1, 0, 0}, {0, 1, 0}},
		{{0, 0, 1}, {1, 0, 0}, {0, math.MaxFloat64, 0}},
	} {
		q, quality := QuatFromDirectionVectorsChecked(basis[0], basis[1], basis[2])
		if q != (Quat{}) || !quality.Degenerate() {
			t.Fatalf("unresolvable basis returned apparent observation: q=%v quality=%v", q, quality)
		}
	}
}
