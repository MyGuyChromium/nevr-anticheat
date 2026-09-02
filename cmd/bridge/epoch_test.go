package main

import (
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestFrameEpoch_StampsMonotonicIndexAndRealTime(t *testing.T) {
	e := &frameEpoch{}
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	frames := []model.PlayerTelemetryFrame{{PlayerID: "a"}, {PlayerID: "b"}}

	idx, ts := e.stamp(t0, frames)
	if idx != 0 || ts != 0 {
		t.Fatalf("first stamp = (%d, %v), want (0, 0)", idx, ts)
	}
	for _, f := range frames {
		if f.FrameIndex != 0 || f.Timestamp != 0 || f.DeltaTime != 0 {
			t.Errorf("first frame %+v", f)
		}
	}

	// 120 ms later, only player a present.
	only := []model.PlayerTelemetryFrame{{PlayerID: "a"}}
	idx, ts = e.stamp(t0.Add(120*time.Millisecond), only)
	if idx != 1 || ts < 0.119 || ts > 0.121 || only[0].DeltaTime < 0.119 || only[0].DeltaTime > 0.121 {
		t.Errorf("second stamp idx=%d ts=%v dt=%v", idx, ts, only[0].DeltaTime)
	}

	// Player b returns after 200 ms: its dt spans both samples.
	both := []model.PlayerTelemetryFrame{{PlayerID: "b"}, {PlayerID: "a"}}
	e.stamp(t0.Add(200*time.Millisecond), both)
	if both[0].DeltaTime < 0.199 || both[0].DeltaTime > 0.201 {
		t.Errorf("b dt = %v, want 0.2", both[0].DeltaTime)
	}
	if both[1].DeltaTime < 0.079 || both[1].DeltaTime > 0.081 {
		t.Errorf("a dt = %v, want 0.08", both[1].DeltaTime)
	}
	if both[0].FrameIndex != 2 || both[1].FrameIndex != 2 {
		t.Errorf("index = %d/%d, want 2", both[0].FrameIndex, both[1].FrameIndex)
	}
}

func TestFrameEpoch_ClockStepBackwardsNeverRegresses(t *testing.T) {
	e := &frameEpoch{}
	t0 := time.Now()
	f := []model.PlayerTelemetryFrame{{PlayerID: "a"}}
	e.stamp(t0, f)
	e.stamp(t0.Add(time.Second), f)
	_, ts := e.stamp(t0.Add(500*time.Millisecond), f)
	if ts != 1.0 {
		t.Errorf("ts = %v, want clamped to 1.0", ts)
	}
	if f[0].DeltaTime != 0 {
		t.Errorf("dt = %v, want 0 (unknown) after clamp", f[0].DeltaTime)
	}
}

func TestEpochRegistry_ContinuesAcrossPollerRecreation(t *testing.T) {
	r := newEpochRegistry()
	f := []model.PlayerTelemetryFrame{{PlayerID: "a"}}
	e1 := r.get("m")
	e1.stamp(time.Now(), f)
	e1.stamp(time.Now(), f)
	e2 := r.get("m")
	if e1 != e2 {
		t.Fatal("registry returned a fresh epoch for a known match")
	}
	next, _ := e2.snapshot()
	if next != 2 {
		t.Errorf("next index = %d, want 2", next)
	}
	if e1.setSession("s1") {
		t.Error("first session must not report a change")
	}
	if !e1.setSession("s2") {
		t.Error("session change not reported")
	}
}

func TestEpochRegistry_ExpiresOnlyUnseenMatches(t *testing.T) {
	r := newEpochRegistry()
	now := time.Now()
	r.get("old")
	r.get("live")
	r.touch("live", now.Add(2*time.Hour))
	if removed := r.expire(now.Add(2*time.Hour), time.Hour); removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if r.size() != 1 {
		t.Errorf("size = %d, want 1", r.size())
	}
}
