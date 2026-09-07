package model

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAssessPlayerEventsIncludesShadowWithoutMutatingEvents(t *testing.T) {
	const playerID = "test-player"
	events := make([]DetectionEvent, 0, 6)
	for i := 0; i < 5; i++ {
		events = append(events, DetectionEvent{
			PlayerID: playerID, DetectorID: "THROW_001", IsShadow: true,
			MergedCount: 10, FrameIndex: i,
		})
	}
	events = append(events, DetectionEvent{PlayerID: "someone-else", DetectorID: "MOV_006"})
	before, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	got := AssessPlayerEvents(playerID, events)
	want := ReviewAssessment{
		Status: ReviewStatusReviewNeeded, SignalCount: 5, ShadowSignals: 5,
		Detectors: []DetectorReviewSignals{{DetectorID: "THROW_001", SignalCount: 5, ShadowSignals: 5}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("assessment = %+v, want %+v", got, want)
	}
	after, err := json.Marshal(events)
	if err != nil || string(before) != string(after) {
		t.Fatalf("assessment mutated events: %s -> %s, err=%v", before, after, err)
	}
}

func TestAssessPlayerEventsNoSignalsAndEmptyPlayer(t *testing.T) {
	want := ReviewAssessment{Status: ReviewStatusNoSignals, Detectors: []DetectorReviewSignals{}}
	for _, test := range []struct {
		name   string
		player string
		events []DetectionEvent
	}{
		{name: "no events", player: "test-player"},
		{name: "other player", player: "test-player", events: []DetectionEvent{{PlayerID: "other"}}},
		{name: "empty player is not wildcard", events: []DetectionEvent{{PlayerID: ""}, {PlayerID: "other"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := AssessPlayerEvents(test.player, test.events); !reflect.DeepEqual(got, want) {
				t.Fatalf("assessment = %+v, want %+v", got, want)
			}
		})
	}
}

func TestAssessPlayerEventsAllDetectorsDeterministic(t *testing.T) {
	events := []DetectionEvent{
		{PlayerID: "P", DetectorID: "Z", IsShadow: true},
		{PlayerID: "P", DetectorID: "B"},
		{PlayerID: "P", DetectorID: "A", IsShadow: true},
		{PlayerID: "P", DetectorID: "Z"},
		{PlayerID: "P", DetectorID: "D", IsShadow: true},
	}
	want := ReviewAssessment{
		Status: ReviewStatusReviewNeeded, SignalCount: 5, ScoredSignals: 2, ShadowSignals: 3,
		Detectors: []DetectorReviewSignals{
			{DetectorID: "Z", SignalCount: 2, ScoredSignals: 1, ShadowSignals: 1},
			{DetectorID: "A", SignalCount: 1, ShadowSignals: 1},
			{DetectorID: "B", SignalCount: 1, ScoredSignals: 1},
			{DetectorID: "D", SignalCount: 1, ShadowSignals: 1},
		},
	}
	if got := AssessPlayerEvents("P", events); !reflect.DeepEqual(got, want) {
		t.Fatalf("assessment = %+v, want %+v", got, want)
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	if got := AssessPlayerEvents("P", events); !reflect.DeepEqual(got, want) {
		t.Fatalf("reordered assessment = %+v, want %+v", got, want)
	}
}
