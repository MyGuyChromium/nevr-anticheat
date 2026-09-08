package bio

import "testing"

func TestBio001OrientationUnknownDoesNotEnterBaselineOrSustainedStreak(t *testing.T) {
	d := NewBio001(nil)
	reasons := map[string]int{}
	d.SetDecisionObserver(func(_, _ string, _ int, reason string) { reasons[reason]++ })
	for i := 0; i < 3; i++ {
		ps := active("p1", i)
		ps.LeftWristAngularRate, ps.RightWristAngularRate = 100, 100
		ps.LeftWristAngularRateValid, ps.RightWristAngularRateValid = false, false
		if events := d.Evaluate(ctx(), players(ps), i); len(events) != 0 {
			t.Fatal("unknown wrist rate produced evidence")
		}
	}
	if reasons["wrist_rotation_unknown"] != 6 || reasons["wrist_at_or_below_threshold"] != 0 || d.left["p1"].stats.Mean != 0 {
		t.Fatalf("unknown rate counted as a normal observation: reasons=%v stats=%+v", reasons, d.left["p1"].stats)
	}
	for i, valid := range []bool{true, true, false, true, true} {
		ps := active("p1", i+3)
		ps.LeftWristAngularRate, ps.LeftWristAngularRateValid = 100, valid
		if events := d.Evaluate(ctx(), players(ps), i+3); len(events) != 0 {
			t.Fatal("unknown sample bridged a sustained wrist streak")
		}
	}
}
