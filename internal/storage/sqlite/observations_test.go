package sqlite

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestComputeObservationStatsSeparatesVersionsAndComputesRates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var events []model.DetectionEvent
	for i := 1; i <= 20; i++ {
		matchID := "m1"
		playerID := "p1"
		if i > 12 {
			matchID = "m2"
			playerID = "p2"
		}
		events = append(events, model.DetectionEvent{
			EventID: fmt.Sprintf("e-%02d", i), DetectorID: "MOV_001", DetectorVersion: "2.0.0",
			MatchID: matchID, PlayerID: playerID, FrameIndex: i, FrameRangeStart: i, FrameRangeEnd: i,
			Timestamp: float64(i), Severity: float64(i) / 20, Confidence: float64(i) / 20,
			EnforcementWeight: 0.5, IsShadow: i <= 15, ObservedValue: "speed", ExpectedRange: "normal",
			CausalKey: model.CausalKey{PlayerID: playerID, FrameStart: i, FrameEnd: i, AnomalyType: "speed"},
		})
	}
	events = append(events, model.DetectionEvent{
		EventID: "old", DetectorID: "MOV_001", DetectorVersion: "1.0.0",
		MatchID: "m0", PlayerID: "p0", FrameIndex: 1, FrameRangeStart: 1, FrameRangeEnd: 1,
		Timestamp: 1, Severity: 0.2, Confidence: 0.3, EnforcementWeight: 0.5, IsShadow: true,
		ObservedValue: "old", ExpectedRange: "normal",
		CausalKey: model.CausalKey{PlayerID: "p0", FrameStart: 1, FrameEnd: 1, AnomalyType: "speed"},
	})
	if _, err := s.StoreDetectionEvents(ctx, events, "initial"); err != nil {
		t.Fatal(err)
	}

	stats, err := s.ComputeObservationStats(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 || stats[0].DetectorVersion != "1.0.0" || stats[1].DetectorVersion != "2.0.0" {
		t.Fatalf("version groups = %+v", stats)
	}
	got := stats[1]
	if got.Events != 20 || got.ShadowEvents != 15 || got.ScoredEvents != 5 || got.Matches != 2 || got.Players != 2 || got.MaxEventsMatch != 12 {
		t.Errorf("counts = %+v", got)
	}
	for label, pair := range map[string][2]float64{
		"mean severity":   {got.MeanSeverity, 0.525},
		"p95 severity":    {got.P95Severity, 0.95},
		"max severity":    {got.MaxSeverity, 1.0},
		"mean confidence": {got.MeanConfidence, 0.525},
		"p05 confidence":  {got.P05Confidence, 0.05},
		"min confidence":  {got.MinConfidence, 0.05},
	} {
		if math.Abs(pair[0]-pair[1]) > 1e-9 {
			t.Errorf("%s = %.6f, want %.6f", label, pair[0], pair[1])
		}
	}
	if got.FirstSeen.IsZero() || got.LastSeen.IsZero() {
		t.Errorf("timestamps missing: %+v", got)
	}

	// A future cutoff returns a non-nil empty result, not stale observations.
	empty, err := s.ComputeObservationStats(ctx, time.Now().Add(time.Hour))
	if err != nil || len(empty) != 0 {
		t.Fatalf("future cutoff = %+v, %v", empty, err)
	}
}
