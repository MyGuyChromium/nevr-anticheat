package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestDeleteMatchAnalysisSnapshotsEventsAtomically(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	event := model.DetectionEvent{
		EventID: "E-SNAPSHOT", DetectorID: "THROW_001", DetectorVersion: "1.5.0",
		MatchID: "M-SNAPSHOT", PlayerID: "P1", FrameIndex: 42, FrameRangeStart: 42, FrameRangeEnd: 42,
		Timestamp: 2.8, Severity: .8, Confidence: .9,
		Evidence:      model.ThrowEvidence{ReleaseSpeed: 20, EffectiveCap: 18.9},
		ObservedValue: "20.00 m/s", ExpectedRange: "<= 18.90 m/s",
		CausalKey: model.CausalKey{PlayerID: "P1", AnomalyType: "THROW_001"}, IsShadow: true,
	}
	if err := store.StoreDetectionEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreMatchSuspicionScore(ctx, event.MatchID, model.SuspicionScore{PlayerID: "P1", TotalScore: 12, EventCount: 1, SnapshotTime: time.Now()}); err != nil {
		t.Fatal(err)
	}
	events, scores, err := store.DeleteMatchAnalysis(ctx, event.MatchID)
	if err != nil || events != 1 || scores != 1 {
		t.Fatalf("delete=(%d,%d,%v)", events, scores, err)
	}
	snapshot, err := store.GetLatestAnalysisSnapshot(ctx, event.MatchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 || snapshot.Events[0].EventID != event.EventID || snapshot.Scores["P1"].TotalScore != 12 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if current, _ := store.GetMatchEvents(ctx, event.MatchID); len(current) != 0 {
		t.Fatalf("current events=%d", len(current))
	}
}
