package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func TestTelemetry_RoundTripAndInsertedCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	frames := append(mkFrames("P1", 0, 10), mkFrames("P2", 0, 10)...)

	n, err := s.StoreTelemetryFrames(ctx, "M1", frames)
	if err != nil {
		t.Fatal(err)
	}
	if n != 20 {
		t.Fatalf("inserted = %d, want 20", n)
	}

	// Re-ingesting the same indices (bridge poller restart) must report zero
	// inserted, not a fake success count.
	n, err = s.StoreTelemetryFrames(ctx, "M1", frames)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("re-ingest inserted = %d, want 0", n)
	}
	res, err := s.StoreTelemetryFramesWithRaw(ctx, "M1", frames[:5], nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != 0 || res.Ignored != 5 {
		t.Fatalf("detailed = %+v, want 0 inserted / 5 ignored", res)
	}

	got, err := s.GetMatchFrames(ctx, "M1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 {
		t.Fatalf("GetMatchFrames = %d rows", len(got))
	}
	// Ordered by frame_index then player_id.
	if got[0].PlayerID != "P1" || got[1].PlayerID != "P2" || got[2].FrameIndex != 1 {
		t.Errorf("unexpected order: %s@%d %s@%d %s@%d",
			got[0].PlayerID, got[0].FrameIndex, got[1].PlayerID, got[1].FrameIndex, got[2].PlayerID, got[2].FrameIndex)
	}
	if got[0].Team != "blue" || got[0].Position[0] != 0 || got[3].Position[0] != 0.1 {
		t.Errorf("frame content lost: %+v", got[0])
	}
	cnt, err := s.GetStoredMatchCount(ctx)
	if err != nil || cnt != 1 {
		t.Errorf("GetStoredMatchCount = %d, %v", cnt, err)
	}
}

func TestTelemetry_RawTicksStoredOncePerFrame(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	frames := append(mkFrames("P1", 0, 5), mkFrames("P2", 0, 5)...)
	raw := map[int]string{}
	for i := 0; i < 5; i++ {
		raw[i] = `{"tick":` + string(rune('0'+i)) + `}`
	}

	// Legacy row API: every player row carries the tick's raw JSON.
	rows := make([]TelemetryFrameRow, len(frames))
	for i, f := range frames {
		rows[i] = TelemetryFrameRow{Frame: f, RawJSON: raw[f.FrameIndex]}
	}
	n, err := s.StoreTelemetryFrameRows(ctx, "M1", rows)
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Fatalf("inserted = %d", n)
	}
	if ticks := countRows(t, s, "match_ticks", "match_id = 'M1'"); ticks != 5 {
		t.Fatalf("match_ticks rows = %d, want 5 (one per tick, not per player)", ticks)
	}
	if n := countRows(t, s, "telemetry_frames", "match_id='M1' AND raw_json IS NOT NULL"); n != 0 {
		t.Fatalf("raw_json must not be duplicated per player row, got %d", n)
	}
	got, err := s.GetMatchRawTicks(ctx, "M1", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2] != raw[2] {
		t.Fatalf("GetMatchRawTicks = %v", got)
	}
	timestamps, err := s.GetMatchTickTimestamps(ctx, "M1")
	if err != nil {
		t.Fatal(err)
	}
	if len(timestamps) != 5 || timestamps[1] != 0.067 || timestamps[3] != 0.201 {
		t.Fatalf("GetMatchTickTimestamps = %v", timestamps)
	}
	tc, _ := s.GetMatchTickCount(ctx, "M1")
	if tc != 5 {
		t.Errorf("tick count = %d", tc)
	}

	// Legacy fallback: a match ingested before match_ticks existed keeps raw_json on frames.
	if _, err := s.DB().Exec(`INSERT INTO telemetry_frames (match_id, player_id, frame_index, timestamp, frame_json, raw_json, ingested_at)
		VALUES ('OLD','P1',7,0.1,'{}','{"legacy":true}',?)`, fmtDBTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	old, err := s.GetMatchRawTicks(ctx, "OLD", 0, 100)
	if err != nil || old[7] != `{"legacy":true}` {
		t.Errorf("legacy raw fallback = %v, %v", old, err)
	}
}

func TestTelemetry_CorruptRowIsAnError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.StoreTelemetryFrames(ctx, "M1", mkFrames("P1", 0, 3)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE telemetry_frames SET frame_json = '{not json' WHERE frame_index = 1`); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetMatchFrames(ctx, "M1")
	if err == nil || !strings.Contains(err.Error(), "frame=1") {
		t.Fatalf("expected corrupt-row error naming frame 1, got %v", err)
	}
	_, err = s.GetPlayerFrames(ctx, "P1", 10)
	if err == nil {
		t.Fatal("GetPlayerFrames must surface corrupt rows")
	}
}

func TestTelemetry_StoreErrorAbortsTransaction(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.StoreTelemetryFrames(ctx, "M1", mkFrames("P1", 0, 3))
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if n := countRows(t, s, "telemetry_frames", ""); n != 0 {
		t.Fatalf("partial batch committed: %d rows", n)
	}
}

func TestTelemetry_SynthesizedMatchContextForLiveMatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	frames := append(mkFrames("P2", 0, 3), mkFrames("P1", 0, 3)...)
	frames[0].Team = "orange"
	frames[1].Team = "orange"
	frames[2].Team = "orange"
	if _, err := s.StoreTelemetryFrames(ctx, "LIVE-1", frames); err != nil {
		t.Fatal(err)
	}

	mc, err := s.GetMatchContext(ctx, "LIVE-1")
	if err != nil {
		t.Fatalf("expected synthesized context, got %v", err)
	}
	if mc.MatchID != "LIVE-1" || mc.Source != "live_telemetry" {
		t.Errorf("context = %+v", mc)
	}
	if len(mc.PlayerIDs) != 2 || mc.PlayerIDs[0] != "P1" || mc.PlayerIDs[1] != "P2" {
		t.Errorf("player ids = %v, want sorted [P1 P2]", mc.PlayerIDs)
	}
	if mc.TeamAssignments["P2"] != "orange" || mc.TeamAssignments["P1"] != "blue" {
		t.Errorf("teams = %v", mc.TeamAssignments)
	}
	if mc.StartTime.IsZero() {
		t.Error("start time should come from earliest ingestion")
	}
	if mc.Physics.DiscSpeedCap == 0 {
		t.Error("physics should be DefaultPhysics")
	}
	if ok, _ := s.HasMatchContext(ctx, "LIVE-1"); ok {
		t.Error("HasMatchContext must be false for synthesized context")
	}
	if ok, _ := s.HasMatch(ctx, "LIVE-1"); !ok {
		t.Error("HasMatch must be true when telemetry exists")
	}
	if _, err := s.GetMatchContext(ctx, "NOPE"); err == nil {
		t.Error("unknown match must error")
	}
}

func TestTelemetry_TimeRangeUsesMatchStartAndIsHalfOpen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Three matches ingested "now" whose recorded start times differ.
	starts := map[string]time.Time{
		"M-jan": time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
		"M-feb": time.Date(2026, 2, 10, 8, 30, 0, 0, time.UTC),
		"M-mar": time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), // exactly on the until boundary
	}
	for mid, st := range starts {
		if _, err := s.StoreTelemetryFrames(ctx, mid, mkFrames("P1", 0, 2)); err != nil {
			t.Fatal(err)
		}
		mc := &model.MatchContext{MatchID: mid, StartTime: st, PlayerIDs: []string{"P1"}}
		if err := s.StoreMatchContext(ctx, mc, 2); err != nil {
			t.Fatal(err)
		}
	}
	// A live match with no context: falls back to ingestion time (now).
	if _, err := s.StoreTelemetryFrames(ctx, "M-live", mkFrames("P1", 0, 2)); err != nil {
		t.Fatal(err)
	}

	ids, err := s.GetMatchIDsByTimeRange(ctx,
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "M-jan,M-feb" {
		t.Fatalf("range = %v, want [M-jan M-feb] (until is exclusive, ordered by match time)", ids)
	}

	// Same-day range around a match's start time (the case that returned nothing before).
	ids, err = s.GetMatchIDsByTimeRange(ctx,
		time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 10, 23, 59, 59, 0, time.UTC))
	if err != nil || len(ids) != 1 || ids[0] != "M-feb" {
		t.Fatalf("same-day range = %v, %v", ids, err)
	}

	// Non-UTC offsets in the arguments compare correctly.
	loc := time.FixedZone("X", 2*3600)
	ids, err = s.GetMatchIDsByTimeRange(ctx,
		time.Date(2026, 2, 10, 10, 0, 0, 0, loc), time.Date(2026, 2, 10, 11, 0, 0, 0, loc)) // 08:00-09:00Z
	if err != nil || len(ids) != 1 || ids[0] != "M-feb" {
		t.Fatalf("offset range = %v, %v", ids, err)
	}

	// The live match is found by ingestion time (today), not by a bogus match time.
	ids, err = s.GetMatchIDsByTimeRange(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil || strings.Join(ids, ",") != "M-live" {
		t.Fatalf("today range = %v, %v", ids, err)
	}

	// Player match enumeration: most recent match time first, without loading frames.
	pm, err := s.GetPlayerMatchIDs(ctx, "P1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pm, ",") != "M-live,M-mar,M-feb,M-jan" {
		t.Fatalf("player matches = %v", pm)
	}
	pm, _ = s.GetPlayerMatchIDs(ctx, "P1", 2)
	if len(pm) != 2 {
		t.Fatalf("limit ignored: %v", pm)
	}
}

func TestTelemetry_PruneOldTelemetryBoundary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.StoreTelemetryFramesWithRaw(ctx, "M1", mkFrames("P1", 0, 3), map[int]string{0: "{}"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneOldTelemetry(ctx, time.Hour)
	if err != nil || n != 0 {
		t.Fatalf("fresh telemetry pruned: %d, %v", n, err)
	}
	backdate(t, s, "telemetry_frames", "ingested_at", "1=1", time.Now().Add(-2*time.Hour))
	backdate(t, s, "match_ticks", "ingested_at", "1=1", time.Now().Add(-2*time.Hour))
	n, err = s.PruneOldTelemetry(ctx, time.Hour)
	if err != nil || n != 3 {
		t.Fatalf("old telemetry pruned = %d, %v", n, err)
	}
	if countRows(t, s, "match_ticks", "") != 0 {
		t.Error("ticks not pruned with frames")
	}
}
