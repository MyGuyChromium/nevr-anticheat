package antievasion

import (
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestWindowRandomizerBoundedGapsAndRate(t *testing.T) {
	for _, interval := range []int{2, 4, 8, 15, 16, 30} {
		wr := NewWindowRandomizer("match-abc")
		const frames = 6000
		evaluated := 0
		last := -1
		maxGap := 0
		perBlock := make(map[int]int)
		for f := 0; f < frames; f++ {
			if wr.ShouldEvaluate("THROW_001", f, interval) {
				evaluated++
				perBlock[f/interval]++
				if last >= 0 && f-last > maxGap {
					maxGap = f - last
				}
				last = f
			}
		}
		if evaluated != frames/interval {
			t.Errorf("interval %d: expected exactly one evaluation per block (%d), got %d", interval, frames/interval, evaluated)
		}
		for b, n := range perBlock {
			if n != 1 {
				t.Errorf("interval %d: block %d evaluated %d times", interval, b, n)
			}
		}
		if maxGap > 2*interval-1 {
			t.Errorf("interval %d: max gap %d exceeds bound %d", interval, maxGap, 2*interval-1)
		}
	}
}

func TestWindowRandomizerNotPeriodicForPowerOfTwo(t *testing.T) {
	wr := NewWindowRandomizer("m")
	// With the old scheme interval 4 evaluated exactly frames 3,7,11,... .
	// The new one must vary the offset within blocks.
	offsets := make(map[int]bool)
	for f := 0; f < 400; f++ {
		if wr.ShouldEvaluate("BIO_001", f, 4) {
			offsets[f%4] = true
		}
	}
	if len(offsets) < 3 {
		t.Fatalf("schedule is (near) periodic: offsets used %v", offsets)
	}
	// Different detectors, matches and secrets give different schedules;
	// the same inputs reproduce the schedule.
	a := NewWindowRandomizerWithSecret("m", []byte("s1"))
	b := NewWindowRandomizerWithSecret("m", []byte("s2"))
	c := NewWindowRandomizerWithSecret("m", []byte("s1"))
	same, diffSecret, diffDet := 0, 0, 0
	for f := 0; f < 3000; f++ {
		if a.ShouldEvaluate("X", f, 8) == c.ShouldEvaluate("X", f, 8) {
			same++
		}
		if a.ShouldEvaluate("X", f, 8) != b.ShouldEvaluate("X", f, 8) {
			diffSecret++
		}
		if a.ShouldEvaluate("X", f, 8) != a.ShouldEvaluate("Y", f, 8) {
			diffDet++
		}
	}
	if same != 3000 || diffSecret == 0 || diffDet == 0 {
		t.Fatalf("reproducibility/independence wrong: same=%d diffSecret=%d diffDet=%d", same, diffSecret, diffDet)
	}
	if !wr.ShouldEvaluate("X", 5, 1) || !wr.ShouldEvaluate("X", 5, 0) || !wr.ShouldEvaluate("X", -1, 4) {
		t.Fatal("interval <= 1 or negative frame must evaluate")
	}
}

func TestDelayedEnforcement(t *testing.T) {
	de := NewDelayedEnforcement(time.Minute, 2*time.Minute)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := t0
	de.SetClock(func() time.Time { return now })
	de.Queue(model.EnforcementAction{ActionID: "a"})
	de.Queue(model.EnforcementAction{ActionID: "b"})
	if len(de.Ready()) != 0 || de.Pending() != 2 {
		t.Fatal("nothing should be ready immediately")
	}
	now = t0.Add(59 * time.Second)
	if len(de.Ready()) != 0 {
		t.Fatal("nothing ready before min delay")
	}
	now = t0.Add(2 * time.Minute)
	if got := de.Ready(); len(got) != 2 || got[0].ActionID != "a" || de.Pending() != 0 {
		t.Fatalf("all should be ready at max delay: %+v", got)
	}
	if NewDelayedEnforcement(time.Minute, 0).maxDelay != time.Minute {
		t.Fatal("max < min should be normalised")
	}
}

func TestAnomalyClusterPruneRecomputes(t *testing.T) {
	ac := NewAnomalyCluster()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ac.SetClock(func() time.Time { return t0 })
	old := t0.Add(-48 * time.Hour)
	ac.RecordAt(model.DetectionEvent{PlayerID: "p", DetectorID: "THROW_001", MatchID: "m1", Severity: 0.8}, old)
	ac.RecordAt(model.DetectionEvent{PlayerID: "p", DetectorID: "MOV_001", MatchID: "m2", Severity: 0.8}, old)
	ac.RecordAt(model.DetectionEvent{PlayerID: "p", DetectorID: "MOV_001", MatchID: "m3", Severity: 0.8}, t0.Add(-time.Hour))
	if s := ac.Score("p"); s < 4.79 || s > 4.81 { // 3 matches * 2 detectors * 0.8
		t.Fatalf("score before prune: %.2f", s)
	}
	ac.Prune(24 * time.Hour)
	st := ac.Stats("p")
	if st.TotalEvents != 1 || len(st.Matches) != 1 || st.Matches[0] != "m3" || len(st.Detectors) != 1 {
		t.Fatalf("prune did not recompute totals: %+v", st)
	}
	if s := ac.Score("p"); s < 0.79 || s > 0.81 { // 1 * 1 * 0.8
		t.Fatalf("score after prune should shrink: %.2f", s)
	}
	ac.Prune(time.Minute)
	if ac.Score("p") != 0 || ac.Stats("p").TotalEvents != 0 {
		t.Fatal("fully pruned player should vanish")
	}
	// Record uses the clock, so replaying with a historical clock is prunable.
	ac.Record(model.DetectionEvent{PlayerID: "q", DetectorID: "X", MatchID: "m"})
	ac.SetClock(func() time.Time { return t0.Add(48 * time.Hour) })
	ac.Prune(24 * time.Hour)
	if ac.Stats("q").TotalEvents != 0 {
		t.Fatal("Record should stamp the clock time")
	}
}
