package model

import (
	"math"
	"testing"
)

func TestHistogramAddIsSafe(t *testing.T) {
	h := NewHistogram(0, 10, 5)
	h.Add(math.NaN())
	h.Add(math.Inf(1))
	if h.TotalCount != 0 {
		t.Errorf("NaN/Inf must be dropped, TotalCount = %d", h.TotalCount)
	}
	h.Add(3)
	if h.Counts[1] != 1 || h.TotalCount != 1 {
		t.Errorf("bucket 1 = %d total = %d", h.Counts[1], h.TotalCount)
	}

	// Zero value with BucketCount set (or truncated JSON Counts) must not panic.
	var z Histogram
	z.BucketCount, z.BucketMin, z.BucketMax = 4, 0, 4
	z.Counts = []int64{1}
	z.Add(3.5)
	if len(z.Counts) != 4 || z.Counts[3] != 1 || z.Counts[0] != 1 {
		t.Errorf("counts = %v", z.Counts)
	}
}

func TestBhattacharyyaRequiresSameLayout(t *testing.T) {
	a := NewHistogram(0, 10, 5)
	b := NewHistogram(0, 20, 5)
	for _, v := range []float64{1, 3, 5, 7, 9} {
		a.Add(v)
		b.Add(v)
	}
	if bc := a.BhattacharyyaCoefficient(&b); bc != 0 {
		t.Errorf("different ranges must not compare: %.3f", bc)
	}
	c := NewHistogram(0, 10, 5)
	for _, v := range []float64{1, 3, 5, 7, 9} {
		c.Add(v)
	}
	if bc := a.BhattacharyyaCoefficient(&c); math.Abs(bc-1) > 1e-9 {
		t.Errorf("identical histograms should score 1, got %.3f", bc)
	}
	if bc := a.BhattacharyyaCoefficient(nil); bc != 0 {
		t.Errorf("nil other should score 0")
	}
}

func TestVec3Rotate(t *testing.T) {
	q := Quat{0, math.Sin(math.Pi / 4), 0, math.Cos(math.Pi / 4)} // +90 deg about Y
	got := Vec3{1, 0, 0}.Rotate(q)
	want := Vec3{0, 0, -1}
	if got.Distance(want) > 1e-9 {
		t.Errorf("rotate = %v, want %v", got, want)
	}
	back := got.Rotate(q.Conjugate())
	if back.Distance(Vec3{1, 0, 0}) > 1e-9 {
		t.Errorf("conjugate rotation did not invert: %v", back)
	}
	if v := (Vec3{1, 2, 3}).Rotate(Quat{}); v != (Vec3{1, 2, 3}) {
		t.Errorf("degenerate quaternion must leave the vector unchanged: %v", v)
	}
}
