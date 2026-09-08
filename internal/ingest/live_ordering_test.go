package ingest

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestMatchManagerOrderingPerturbations(t *testing.T) {
	mm, store, metrics := newTestManager(t)
	first := goodFrame("P1", 0)
	first.Observation = &model.ObservationContext{Source: "test", SourceID: "producer", Authority: "client_reported", TimeBasis: "received", SessionID: "M1", FrameIndex: 0, Timestamp: 0}
	if got := mm.HandleFrames("M1", "", []model.PlayerTelemetryFrame{first}); got.Accepted != 1 {
		t.Fatal(got)
	}
	// Loss and jitter are real gaps, not a reason to manufacture intervening
	// ticks. Sorting an as-yet-unprocessed batch preserves its exact values.
	one, three := goodFrame("P1", 1), goodFrame("P1", 3)
	one.Timestamp, three.Timestamp = .073, .215
	batch := []model.PlayerTelemetryFrame{three, one, one}
	before, _ := json.Marshal(batch)
	if got := mm.HandleFrames("M1", "", batch); got.Accepted != 2 || got.Ignored != 1 || got.Rejected != 0 {
		t.Fatal(got)
	}
	after, _ := json.Marshal(batch)
	if string(before) != string(after) {
		t.Fatal("caller batch mutated")
	}
	for _, tc := range []struct {
		name              string
		frame             model.PlayerTelemetryFrame
		ignored, rejected int
	}{
		{"retry", one, 1, 0},
		{"late_missing_tick", goodFrame("P1", 2), 0, 1},
		{"late_new_player", goodFrame("P2", 2), 0, 1},
		{"clock_rollback", func() model.PlayerTelemetryFrame { f := goodFrame("P1", 4); f.Timestamp = .1; return f }(), 0, 1},
		{"same_time_new_tick", func() model.PlayerTelemetryFrame { f := goodFrame("P1", 4); f.Timestamp = .215; return f }(), 0, 1},
		{"same_tick_different_time", func() model.PlayerTelemetryFrame { f := goodFrame("P2", 3); f.Timestamp = .216; return f }(), 0, 1},
		{"nonfinite_time", func() model.PlayerTelemetryFrame { f := goodFrame("P1", 4); f.Timestamp = math.NaN(); return f }(), 0, 1},
		{"claimed_source_restart", func() model.PlayerTelemetryFrame {
			f := first
			f.PlayerID = "new-player"
			f.Observation = first.Observation.Clone()
			f.Observation.SourceEpoch = 123
			return f
		}(), 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mm.HandleFrames("M1", "", []model.PlayerTelemetryFrame{tc.frame})
			if got.Accepted != 0 || got.Ignored != tc.ignored || got.Rejected != tc.rejected {
				t.Fatalf("%+v", got)
			}
		})
	}
	// Another player belonging to the still-open tick is valid, exactly once.
	peer := goodFrame("P2", 3)
	peer.Timestamp = math.Nextafter(.215, math.Inf(1)) // arithmetic roundoff, not a different observed tick
	if got := mm.HandleFrames("M1", "", []model.PlayerTelemetryFrame{peer}); got.Accepted != 1 {
		t.Fatal(got)
	}
	if got := mm.HandleFrames("M1", "", []model.PlayerTelemetryFrame{peer}); got.Ignored != 1 {
		t.Fatal(got)
	}
	four := goodFrame("P1", 4)
	four.Timestamp = .3
	if got := mm.HandleFrames("M1", "", []model.PlayerTelemetryFrame{four}); got.Accepted != 1 {
		t.Fatal(got)
	}
	mm.EndMatch("M1")
	frames, err := store.GetMatchFrames(context.Background(), "M1")
	if err != nil || len(frames) != 5 {
		t.Fatalf("frames=%d err=%v", len(frames), err)
	}
	var indices []int
	for _, f := range frames {
		indices = append(indices, f.FrameIndex)
	}
	if !reflect.DeepEqual(indices, []int{0, 1, 3, 3, 4}) {
		t.Fatal(indices)
	}
	if metrics.FramesRebased.Get() != 0 {
		t.Fatal("manufactured frame identities")
	}
}

func TestSameLiveTickTimeOnlyToleratesBoundedRoundoff(t *testing.T) {
	for _, timestamp := range []float64{0, .215, 14.266666666666666, 3000} {
		if !sameLiveTickTime(timestamp, math.Nextafter(timestamp, math.Inf(1))) {
			t.Fatal("one ULP rejected")
		}
		if sameLiveTickTime(timestamp, timestamp+1e-6) {
			t.Fatal("microsecond discrepancy accepted as rounding")
		}
	}
	if sameLiveTickTime(1e16, math.Nextafter(1e16, math.Inf(1))) {
		t.Fatal("large ULP manufactured seconds of tolerance")
	}
}

func TestMatchManagerMixedRetryCannotRestampRawPayload(t *testing.T) {
	mm, store, _ := newTestManager(t)
	zero := goodFrame("P1", 0)
	mm.HandleFramesWithRaw("M1", "", []model.PlayerTelemetryFrame{zero}, `{"original":true}`)
	got := mm.HandleFramesWithRaw("M1", "", []model.PlayerTelemetryFrame{zero, goodFrame("P1", 1)}, `{"retry":true}`)
	if got.Accepted != 1 || got.Ignored != 1 {
		t.Fatal(got)
	}
	raw, err := store.GetMatchRawTicks(context.Background(), "M1", 0, 1)
	if err != nil || len(raw) != 1 || raw[0] != `{"original":true}` {
		t.Fatalf("misbound raw=%v err=%v", raw, err)
	}
}
