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
	// loIdx=1 -> 2, hiIdx=9 -> 100 ; 1 clamps to 2, nothing exceeds 100
	if got[0] != 2 || got[9] != 100 {
		t.Fatalf("winsorized: %v", got)
	}
	got = Winsorize(data, 0, 0.5) // hiIdx=5 -> 6
	if got[9] != 6 || got[0] != 1 {
		t.Fatalf("upper clamp wrong: %v", got)
	}
	if out := Winsorize(nil, 0, 1); out != nil {
		t.Fatal("nil in, nil out")
	}
	if got := Winsorize([]float64{5}, 1, 1); got[0] != 5 {
		t.Fatal("single element")
	}
}

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
