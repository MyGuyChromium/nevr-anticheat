package baseline

import (
	"math"
	"testing"
)

func TestWinsorizeNeverPanics(t *testing.T) {
	data := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 100}
	cases := []struct{ lo, hi float64 }{
		{0.1, 0.9}, {1, 1}, {0, 0}, {-0.5, 2}, {0.9, 0.1}, {math.NaN(), math.NaN()}, {1.5, -1},
	}
	for _, c := range cases {
		got := Winsorize(data, c.lo, c.hi)
		if len(got) != len(data) {
			t.Fatalf("lo=%v hi=%v: length changed", c.lo, c.hi)
		}
	}
	got := Winsorize(data, 0.1, 0.9)
	// Interpolated P10 = 1.9 and P90 = 18.1 (same definition as
	// ComputeBaseline): the bottom value is raised and the 100 outlier is
	// clamped down, which is what winsorizing the top 10% means.
	if !near(got[0], 1.9) || !near(got[9], 18.1) || got[1] != 2 || got[8] != 9 {
		t.Fatalf("winsorized: %v", got)
	}
	got = Winsorize(data, 0, 0.5) // P50 = 5.5
	if !near(got[9], 5.5) || got[0] != 1 || got[4] != 5 {
		t.Fatalf("upper clamp wrong: %v", got)
	}
	// Input order is preserved and the outlier is clamped wherever it sits.
	got = Winsorize([]float64{100, 9, 8, 7, 6, 5, 4, 3, 2, 1}, 0.1, 0.9)
	if !near(got[0], 18.1) || !near(got[9], 1.9) {
		t.Fatalf("input order not preserved: %v", got)
	}
	if out := Winsorize(nil, 0, 1); out != nil {
		t.Fatal("nil in, nil out")
	}
	if got := Winsorize([]float64{5}, 1, 1); got[0] != 5 {
		t.Fatal("single element")
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestComputeBaseline(t *testing.T) {
	if ComputeBaseline("m", nil) != nil || ComputeBaseline("m", []float64{math.NaN(), math.Inf(1)}) != nil {
		t.Fatal("no usable observations should give nil")
	}
	b := ComputeBaseline("speed", []float64{4, 1, 3, 2, math.NaN()})
	if b == nil || b.SampleCount != 4 || b.Min != 1 || b.Max != 4 || b.Mean != 2.5 {
		t.Fatalf("baseline wrong: %+v", b)
	}
	if math.Abs(b.StdDev-math.Sqrt(1.25)) > 1e-12 {
		t.Fatalf("stddev wrong: %v", b.StdDev)
	}
}
