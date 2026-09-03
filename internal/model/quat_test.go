package model

import (
	"math"
	"testing"
)

const quatTestEps = 1e-9

func vecClose(a, b Vec3, eps float64) bool {
	return a.Sub(b).Magnitude() <= eps
}

// yawBasis returns the (forward, left, up) triple of a body yawed by deg about +Y.
// reflected=false gives left = up x forward (det +1); reflected=true gives
// left = -(up x forward) (det -1), the convention where "left" really points left
// in a left-handed frame.
func yawBasis(deg float64, reflected bool) (forward, left, up Vec3) {
	r := deg * math.Pi / 180
	forward = Vec3{math.Sin(r), 0, math.Cos(r)}
	up = Vec3{0, 1, 0}
	left = up.Cross(forward)
	if reflected {
		left = left.Scale(-1)
	}
	return
}

func TestQuatFromDirectionVectors_ProperIdentity(t *testing.T) {
	// Proper (det +1) identity basis: left = up x forward = +X.
	q, quality := QuatFromDirectionVectorsChecked(Vec3{0, 0, 1}, Vec3{1, 0, 0}, Vec3{0, 1, 0})
	if quality != BasisProper {
		t.Fatalf("quality = %v, want proper", quality)
	}
	if !q.IsUnit() || math.Abs(math.Abs(q.W())-1) > quatTestEps {
		t.Fatalf("expected identity, got %v", q)
	}
}

func TestQuatFromDirectionVectors_ReflectedIsFlaggedAndExact(t *testing.T) {
	// The triple every Echo VR fixture uses at rest: forward=+Z, left=-X, up=+Y.
	// det(left, up, forward) = -1, so it must be flagged, and the result must be
	// the same rotation a proper basis (left=+X) would give: identity.
	q, quality := QuatFromDirectionVectorsChecked(Vec3{0, 0, 1}, Vec3{-1, 0, 0}, Vec3{0, 1, 0})
	if !quality.Reflected() {
		t.Fatalf("expected reflected flag, got %v", quality)
	}
	if quality.Degenerate() || quality.NonOrthonormal() {
		t.Fatalf("unexpected extra flags: %v", quality)
	}
	if math.Abs(math.Abs(q.W())-1) > quatTestEps {
		t.Fatalf("reflected identity basis should map to identity, got %v", q)
	}

	// Every yaw must give the same quaternion for the reflected and the proper
	// convention (that was the bug: reflected yaws collapsed to identity).
	for _, deg := range []float64{10, 30, 45, 90, 120, 179} {
		fp, lp, up := yawBasis(deg, false)
		fr, lr, ur := yawBasis(deg, true)
		qp, qualP := QuatFromDirectionVectorsChecked(fp, lp, up)
		qr, qualR := QuatFromDirectionVectorsChecked(fr, lr, ur)
		if qualP != BasisProper {
			t.Errorf("yaw %v proper basis flagged %v", deg, qualP)
		}
		if !qualR.Reflected() {
			t.Errorf("yaw %v reflected basis not flagged (%v)", deg, qualR)
		}
		if d := qp.AngularDistance(qr); d > 1e-9 {
			t.Errorf("yaw %v: proper vs reflected differ by %.3g rad", deg, d)
		}
		want := deg * math.Pi / 180
		if d := QuatIdentity().AngularDistance(qr); math.Abs(d-want) > 1e-9 {
			t.Errorf("yaw %v: angle from identity = %.6f, want %.6f", deg, d, want)
		}
	}
}

func TestQuatFromDirectionVectors_Handedness(t *testing.T) {
	// The quaternion maps +Z to forward, +Y to up and +X to the proper left axis
	// (up x forward). Verified by applying it to basis vectors.
	forward, left, up := yawBasis(90, false)
	q, _ := QuatFromDirectionVectorsChecked(forward, left, up)
	if got := q.Rotate(Vec3{0, 0, 1}); !vecClose(got, forward, 1e-9) {
		t.Errorf("q*Z = %v, want forward %v", got, forward)
	}
	if got := q.Rotate(Vec3{0, 1, 0}); !vecClose(got, up, 1e-9) {
		t.Errorf("q*Y = %v, want up %v", got, up)
	}
	if got := q.Rotate(Vec3{1, 0, 0}); !vecClose(got, left, 1e-9) {
		t.Errorf("q*X = %v, want left %v", got, left)
	}
	// +90 deg about +Y (right-handed) sends +Z to +X: the sign of the axis matters.
	if q[1] < 0 || math.Abs(q[1]-math.Sin(math.Pi/4)) > 1e-9 {
		t.Errorf("expected rotation about +Y by +90deg, got %v", q)
	}

	// Reflected input with the same forward/up must produce the same mapping.
	fr, lr, ur := yawBasis(90, true)
	qr, _ := QuatFromDirectionVectorsChecked(fr, lr, ur)
	if got := qr.Rotate(Vec3{0, 0, 1}); !vecClose(got, forward, 1e-9) {
		t.Errorf("reflected: q*Z = %v, want forward %v", got, forward)
	}
}

func TestQuatFromDirectionVectors_SmallSteps(t *testing.T) {
	// Consecutive 1-degree steps must read 1 degree in both conventions.
	for _, reflected := range []bool{false, true} {
		for deg := 0.0; deg < 360; deg += 37 {
			f0, l0, u0 := yawBasis(deg, reflected)
			f1, l1, u1 := yawBasis(deg+1, reflected)
			q0 := QuatFromDirectionVectors(f0, l0, u0)
			q1 := QuatFromDirectionVectors(f1, l1, u1)
			want := math.Pi / 180
			if d := q0.AngularDistance(q1); math.Abs(d-want) > 1e-6 {
				t.Errorf("reflected=%v deg=%v: step = %.6f rad, want %.6f", reflected, deg, d, want)
			}
		}
	}
	// 1-degree pitch steps in the reflected convention too.
	for deg := 0.0; deg < 80; deg += 13 {
		r0, r1 := deg*math.Pi/180, (deg+1)*math.Pi/180
		f0 := Vec3{0, math.Sin(r0), math.Cos(r0)}
		u0 := Vec3{0, math.Cos(r0), -math.Sin(r0)}
		f1 := Vec3{0, math.Sin(r1), math.Cos(r1)}
		u1 := Vec3{0, math.Cos(r1), -math.Sin(r1)}
		q0 := QuatFromDirectionVectors(f0, u0.Cross(f0).Scale(-1), u0)
		q1 := QuatFromDirectionVectors(f1, u1.Cross(f1).Scale(-1), u1)
		if d := q0.AngularDistance(q1); math.Abs(d-math.Pi/180) > 1e-6 {
			t.Errorf("pitch %v: step = %.6f rad", deg, d)
		}
	}
}

func TestQuatFromDirectionVectors_NonOrthonormalInputs(t *testing.T) {
	forward, left, up := yawBasis(90, false)
	ref := QuatFromDirectionVectors(forward, left, up)

	// Scale-2 vectors: same rotation, flagged non-orthonormal.
	q, quality := QuatFromDirectionVectorsChecked(forward.Scale(2), left.Scale(2), up.Scale(2))
	if !quality.NonOrthonormal() || quality.Degenerate() {
		t.Errorf("scaled input quality = %v", quality)
	}
	if d := ref.AngularDistance(q); d > 1e-9 {
		t.Errorf("scaled input changed rotation by %.3g rad", d)
	}

	// Missing left: orientation recoverable from forward/up, flagged.
	q, quality = QuatFromDirectionVectorsChecked(forward, Vec3{}, up)
	if !quality.NonOrthonormal() || quality.Reflected() {
		t.Errorf("zero-left quality = %v", quality)
	}
	if d := ref.AngularDistance(q); d > 1e-9 {
		t.Errorf("zero-left input changed rotation by %.3g rad", d)
	}

	// Slightly non-perpendicular up: Gram-Schmidt keeps forward exact.
	q, quality = QuatFromDirectionVectorsChecked(forward, left, up.Add(forward.Scale(0.05)))
	if !quality.NonOrthonormal() {
		t.Errorf("skewed up not flagged: %v", quality)
	}
	if got := q.Rotate(Vec3{0, 0, 1}); !vecClose(got, forward, 1e-9) {
		t.Errorf("skewed up: forward not preserved: %v", got)
	}
}

func TestQuatFromDirectionVectors_Degenerate(t *testing.T) {
	cases := []struct {
		name    string
		f, l, u Vec3
	}{
		{"all zero", Vec3{}, Vec3{}, Vec3{}},
		{"zero forward", Vec3{}, Vec3{1, 0, 0}, Vec3{0, 1, 0}},
		{"zero up", Vec3{0, 0, 1}, Vec3{1, 0, 0}, Vec3{}},
		{"collinear", Vec3{0, 0, 1}, Vec3{1, 0, 0}, Vec3{0, 0, -1}},
		{"nan", Vec3{math.NaN(), 0, 1}, Vec3{1, 0, 0}, Vec3{0, 1, 0}},
	}
	for _, c := range cases {
		q, quality := QuatFromDirectionVectorsChecked(c.f, c.l, c.u)
		if !quality.Degenerate() {
			t.Errorf("%s: not flagged degenerate (%v)", c.name, quality)
		}
		if q != (Quat{}) || q.IsUnit() {
			t.Errorf("%s: expected zero quaternion, got %v", c.name, q)
		}
	}
}

func TestBasisQuality_String(t *testing.T) {
	if BasisProper.String() != "proper" {
		t.Errorf("proper string = %q", BasisProper.String())
	}
	q := BasisReflected | BasisNonOrthonormal
	if q.String() != "reflected|non_orthonormal" {
		t.Errorf("combined string = %q", q.String())
	}
}

func TestQuatRotate_MatchesMultiply(t *testing.T) {
	// q v q* via Multiply must agree with Rotate.
	q := Quat{0.1, 0.7, -0.2, 0.6}.Normalize()
	v := Vec3{1.5, -2, 0.25}
	pv := Quat{v[0], v[1], v[2], 0}
	r := q.Multiply(pv).Multiply(q.Conjugate())
	if !vecClose(q.Rotate(v), Vec3{r[0], r[1], r[2]}, 1e-12) {
		t.Errorf("Rotate = %v, Multiply form = %v", q.Rotate(v), r)
	}
}
