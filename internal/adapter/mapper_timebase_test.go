package adapter

import (
	"math"
	"testing"
	"time"
)

// A clock correction keeps only a sortable coordinate, never an invented
// elapsed interval. Its new source epoch isolates all derivative histories.
func TestMapper_ClockStepIsolatesSourceEpoch(t *testing.T) {
	m := NewMapper()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	t0 := time.Date(2025, 10, 26, 2, 59, 59, 900_000_000, time.UTC)
	s := func(tm time.Time) *MappingResult {
		return m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, nil), tm)
	}

	s(t0)
	s(t0.Add(67 * time.Millisecond))
	r := s(t0.Add(134 * time.Millisecond))
	if math.Abs(r.Frames[0].Timestamp-0.134) > 1e-9 {
		t.Fatalf("ts before step = %v", r.Frames[0].Timestamp)
	}
	before := r.Frames[0].Observation.Clone()

	// DST fall-back: the recorder's local clock jumps back one hour.
	step := t0.Add(201 * time.Millisecond).Add(-time.Hour)
	r = s(step)
	f := r.Frames[0]
	if f.Timestamp != math.Nextafter(0.134, math.Inf(1)) || f.DeltaTime != 0 {
		t.Errorf("clock step manufactured elapsed time: ts=%v dt=%v", f.Timestamp, f.DeltaTime)
	}
	if before.SameSource(f.Observation) || f.Observation.SourceEpoch != 1 || f.Observation.TimeBasis != "supplied_sample_time:clock_rebased" {
		t.Fatalf("clock boundary source not isolated: %+v", f.Observation)
	}
	if m.Stats().ClockSteps != 1 || m.Stats().NonMonotonicSamples != 0 {
		t.Errorf("stats = %+v", m.Stats())
	}
	found := false
	for _, w := range r.Warnings {
		if w.Field == "sample_time" {
			found = true
		}
	}
	if !found {
		t.Errorf("no clock-step warning: %+v", r.Warnings)
	}

	// Subsequent samples continue on the shifted clock without further steps.
	r = s(step.Add(67 * time.Millisecond))
	f = r.Frames[0]
	if math.Abs(f.Timestamp-0.201) > 1e-9 || math.Abs(f.DeltaTime-0.067) > 1e-9 {
		t.Errorf("after step: ts=%v dt=%v, want 0.201/0.067", f.Timestamp, f.DeltaTime)
	}
	if m.Stats().ClockSteps != 1 {
		t.Errorf("ClockSteps = %d, want 1", m.Stats().ClockSteps)
	}

	// Small backward sample: clamped to the previous snapshot, counted.
	r = s(step.Add(60 * time.Millisecond))
	f = r.Frames[0]
	if math.Abs(f.Timestamp-0.201) > 1e-9 || f.DeltaTime != 0 {
		t.Errorf("small backward sample: ts=%v dt=%v, want 0.201/0", f.Timestamp, f.DeltaTime)
	}
	if m.Stats().NonMonotonicSamples != 1 {
		t.Errorf("NonMonotonicSamples = %d, want 1", m.Stats().NonMonotonicSamples)
	}
	if r.MatchCtx.StartTime != t0 {
		t.Errorf("StartTime moved: %v", r.MatchCtx.StartTime)
	}
}

// Timestamp is never negative, even when the second sample precedes the first.
func TestMapper_TimestampNeverNegative(t *testing.T) {
	m := NewMapper()
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, nil), t0)
	r := m.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, nil), t0.Add(-30*time.Millisecond))
	if r.Frames[0].Timestamp != 0 || r.Frames[0].DeltaTime != 0 {
		t.Errorf("ts=%v dt=%v, want 0/0", r.Frames[0].Timestamp, r.Frames[0].DeltaTime)
	}
	// A clock step before any cadence is known still invents no cadence.
	m2 := NewMapper()
	m2.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, nil), t0)
	r = m2.MapSessionAt(twoTeamSession("m", []EchoVRPlayer{a}, nil), t0.Add(-time.Hour))
	if r.Frames[0].Timestamp != math.SmallestNonzeroFloat64 || r.Frames[0].DeltaTime != 0 || m2.Stats().ClockSteps != 1 {
		t.Errorf("ts=%v steps=%d", r.Frames[0].Timestamp, m2.Stats().ClockSteps)
	}
}

// NewMatch resets the time base, frame index and per-player history while
// keeping the counters.
func TestMapper_NewMatchResetsTimeBase(t *testing.T) {
	m := NewMapper()
	m.SetDedupeIdentical(true)
	a := testPlayer("A", 1, [3]float64{1, 1.6, -10})
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	same := twoTeamSession("R1", []EchoVRPlayer{a}, nil)
	m.MapSessionAt(same, t0)
	m.MapSessionAt(same, t0.Add(67*time.Millisecond)) // duplicate, skipped
	moved := testPlayer("A", 1, [3]float64{1.5, 1.6, -10})
	m.MapSessionAt(twoTeamSession("R1", []EchoVRPlayer{moved}, nil), t0.Add(-2*time.Second)) // clock step

	m.NewMatch()
	t1 := t0.Add(10 * time.Minute)
	r := m.MapSessionAt(same, t1) // identical state must NOT be deduped across matches
	if r.SkippedDuplicate || len(r.Frames) != 1 {
		t.Fatalf("first snapshot after NewMatch: %+v", r)
	}
	f := r.Frames[0]
	if f.Timestamp != 0 || f.DeltaTime != 0 || f.FrameIndex != 0 {
		t.Errorf("after NewMatch: ts=%v dt=%v idx=%d, want 0/0/0", f.Timestamp, f.DeltaTime, f.FrameIndex)
	}
	if !r.MatchCtx.StartTime.Equal(t1) || !m.FirstSampleTime().Equal(t1) {
		t.Errorf("StartTime = %v FirstSampleTime = %v, want %v", r.MatchCtx.StartTime, m.FirstSampleTime(), t1)
	}
	r = m.MapSessionAt(twoTeamSession("R1", []EchoVRPlayer{moved}, nil), t1.Add(67*time.Millisecond))
	if math.Abs(r.Frames[0].Timestamp-0.067) > 1e-9 || math.Abs(r.Frames[0].DeltaTime-0.067) > 1e-9 || r.Frames[0].FrameIndex != 1 {
		t.Errorf("second snapshot after NewMatch: %+v", r.Frames[0])
	}
	st := m.Stats()
	if st.Snapshots != 4 || st.DuplicatesSkipped != 1 || st.ClockSteps != 1 {
		t.Errorf("counters must persist across NewMatch: %+v", st)
	}
}
