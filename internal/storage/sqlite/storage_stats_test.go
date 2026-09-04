package sqlite

import (
	"context"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestStorageStatsDeleteAndRestoreRawTicks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	frames := []model.PlayerTelemetryFrame{{PlayerID: "P1", FrameIndex: 1, Timestamp: 1}}
	raw := map[int]string{1: `{"sessionid":"M1"}`, 2: `{"sessionid":"M1","n":2}`}
	if _, err := s.StoreTelemetryFramesWithRaw(ctx, "M1", frames, raw); err != nil {
		t.Fatal(err)
	}
	stats, err := s.GetMatchStorageStats(ctx, "M1")
	if err != nil || stats.RawTicks != 2 || stats.RawTickBytes == 0 || stats.NormalizedFrames != 1 || stats.NormalizedBytes == 0 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	if removed, err := s.DeleteMatchRawTicks(ctx, "M1"); err != nil || removed != 2 {
		t.Fatalf("delete = %d, %v", removed, err)
	}
	if ticks, err := s.GetMatchRawTicks(ctx, "M1", 0, 9); err != nil || len(ticks) != 0 {
		t.Fatalf("ticks after delete = %+v, %v", ticks, err)
	}
	if inserted, present, err := s.RestoreMatchRawTicks(ctx, "M1", raw); err != nil || inserted != 2 || present != 0 {
		t.Fatalf("restore = %d/%d, %v", inserted, present, err)
	}
	if inserted, present, err := s.RestoreMatchRawTicks(ctx, "M1", raw); err != nil || inserted != 0 || present != 2 {
		t.Fatalf("second restore = %d/%d, %v", inserted, present, err)
	}
}
