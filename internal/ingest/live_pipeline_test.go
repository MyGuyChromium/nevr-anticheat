package ingest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/metrics"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/review"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlite.RunMigrationsV2(store.DB(), quietLogger()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func newTestManager(t *testing.T) (*MatchManager, *sqlite.Store, *metrics.Metrics) {
	t.Helper()
	cfg := config.DefaultConfig()
	for id, dc := range cfg.Detectors {
		dc.Mode = "enforce"
		cfg.Detectors[id] = dc
	}
	cfg.Shadow.ShadowDetectors = nil
	// One detector firing once scores 15 points; the shipped review
	// threshold (60) needs several categories. Lowering it through the
	// config proves the configured tier table reaches the live scorer.
	cfg.Scoring.ReviewThreshold = 15
	store := newTestStore(t)
	factory := func() []detect.Detector {
		return []detect.Detector{movement.NewMov001(cfg.GetDetectorConfig("MOV_001").Params)}
	}
	mm := NewMatchManager(cfg, store, factory, quietLogger())
	m := metrics.NewMetrics()
	mm.SetMetrics(m)
	return mm, store, m
}

// speedHack oscillates on Z at ~75 m/s.
func speedHack(pid string, i int) model.PlayerTelemetryFrame {
	f := goodFrame(pid, i)
	f.Position[2] = 5.0 + float64(i%6)*5.0
	f.Team = "blue"
	return f
}

func countRows(t *testing.T, store *sqlite.Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := store.DB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestMatchManager_LiveDetectionAcrossOneFrameBatches is the end-to-end
// regression for F2/F4/F5: one frame per batch, as cmd/bridge sends, must
// still produce kinematic detections, and the match must be fully persisted
// (F3/F41/F205) so reprocess-match can find it.
func TestMatchManager_LiveDetectionAcrossOneFrameBatches(t *testing.T) {
	mm, store, m := newTestManager(t)
	ctx := context.Background()

	for i := 0; i < 90; i++ {
		res := mm.HandleFrames("M1", []model.PlayerTelemetryFrame{speedHack("P1", i)})
		if res.Accepted != 1 || res.Rejected != 0 {
			t.Fatalf("batch %d: %+v", i, res)
		}
		if i == 0 {
			// Context is persisted on creation, before match end.
			if _, err := store.GetMatchContext(ctx, "M1"); err != nil {
				t.Fatalf("match context not persisted on creation: %v", err)
			}
		}
	}
	live := mm.GetMatchContext("M1")
	if live == nil || live.TeamAssignments["P1"] != "blue" || len(live.PlayerIDs) != 1 {
		t.Fatalf("live context wrong: %+v", live)
	}
	if ps := mm.matches["M1"].Players["P1"]; ps == nil || ps.FrameCount != 90 {
		t.Fatalf("PlayerState not persistent across batches: %+v", ps)
	}

	mm.EndMatch("M1")
	if mm.ActiveMatchCount() != 0 {
		t.Error("match still active after EndMatch")
	}

	events, err := store.GetMatchEvents(ctx, "M1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("no detection events stored from one-frame batches")
	}
	for _, ev := range events {
		if ev.DetectorID != "MOV_001" || ev.IsShadow {
			t.Errorf("unexpected event %+v", ev)
		}
	}
	mc, err := store.GetMatchContext(ctx, "M1")
	if err != nil {
		t.Fatal(err)
	}
	if mc.Duration <= 0 || len(mc.PlayerIDs) != 1 || mc.TeamAssignments["P1"] != "blue" || mc.Source != "live_telemetry" {
		t.Errorf("stored context: %+v", mc)
	}
	if n := countRows(t, store, `SELECT COUNT(*) FROM match_summaries WHERE match_id = ?`, "M1"); n != 1 {
		t.Errorf("match_summaries rows=%d", n)
	}
	if n := countRows(t, store, `SELECT COUNT(*) FROM telemetry_frames WHERE match_id = ?`, "M1"); n != 90 {
		t.Errorf("telemetry rows=%d", n)
	}
	// Score snapshot hygiene (F120/F152): rows only on tier change / end, never zero.
	snaps := countRows(t, store, `SELECT COUNT(*) FROM suspicion_scores WHERE player_id = ?`, "P1")
	if snaps == 0 || snaps > 6 {
		t.Errorf("suspicion_scores rows=%d, want a handful (tier changes + final), not one per batch", snaps)
	}
	if n := countRows(t, store, `SELECT COUNT(*) FROM suspicion_scores WHERE total_score <= 0`); n != 0 {
		t.Errorf("zero-score snapshot rows=%d", n)
	}
	sc, err := store.GetPlayerScore(ctx, "P1")
	if err != nil || sc.TotalScore <= 0 {
		t.Errorf("latest score: %+v err=%v", sc, err)
	}
	if m.DetectionEvents.Get("MOV_001") == 0 || m.FramesProcessed.Get() != 90 || m.MatchesEnded.Get() != 1 {
		t.Errorf("metrics not wired: events=%d frames=%d ended=%d",
			m.DetectionEvents.Get("MOV_001"), m.FramesProcessed.Get(), m.MatchesEnded.Get())
	}
}

// TestMatchManager_RebasesNonMonotonicBatches covers contract 2 / F22/F53.
func TestMatchManager_RebasesNonMonotonicBatches(t *testing.T) {
	mm, store, m := newTestManager(t)
	ctx := context.Background()
	batch := func(lo, hi int) []model.PlayerTelemetryFrame {
		var out []model.PlayerTelemetryFrame
		for i := lo; i < hi; i++ {
			out = append(out, goodFrame("P1", i))
		}
		return out
	}
	mm.HandleFrames("M1", batch(0, 10))
	res := mm.HandleFrames("M1", batch(0, 10)) // producer restarted its counter
	if res.Accepted != 10 || res.Ignored != 0 {
		t.Fatalf("rebased batch result %+v", res)
	}
	maxIdx, err := store.GetMaxFrameIndex(ctx, "M1")
	if err != nil || maxIdx != 19 {
		t.Errorf("max frame index=%d err=%v, want 19", maxIdx, err)
	}
	if n := countRows(t, store, `SELECT COUNT(*) FROM telemetry_frames WHERE match_id = ?`, "M1"); n != 20 {
		t.Errorf("rows=%d want 20", n)
	}
	if m.FramesRebased.Get() != 10 {
		t.Errorf("FramesRebased=%d", m.FramesRebased.Get())
	}

	// A new manager (server restart) seeds its counter from the store.
	mm2 := NewMatchManager(mm.cfg, store, mm.detectorFn, quietLogger())
	mm2.HandleFrames("M1", batch(0, 5))
	if maxIdx, _ := store.GetMaxFrameIndex(ctx, "M1"); maxIdx != 24 {
		t.Errorf("after restart max index=%d want 24", maxIdx)
	}
	if n := countRows(t, store, `SELECT COUNT(*) FROM telemetry_frames WHERE match_id = ?`, "M1"); n != 25 {
		t.Errorf("rows=%d want 25", n)
	}
	// Frames with a genuinely repeated (match_id, player_id, frame_index) are
	// reported as ignored by the store, never as accepted.
	if n, _ := store.GetMatchFrameCount(ctx, "M1"); n != 25 {
		t.Errorf("GetMatchFrameCount=%d", n)
	}
}

// TestMatchManager_Caps covers contract 11 (match cap) and the player cap.
func TestMatchManager_Caps(t *testing.T) {
	mm, _, _ := newTestManager(t)
	mm.SetLimits(1, 2)
	if res := mm.HandleFrames("M1", []model.PlayerTelemetryFrame{goodFrame("A", 0), goodFrame("B", 0), goodFrame("C", 0)}); res.Accepted != 2 || res.Rejected != 1 {
		t.Errorf("player cap: %+v", res)
	}
	if res := mm.HandleFrames("M2", []model.PlayerTelemetryFrame{goodFrame("A", 0)}); res.Rejected != 1 || res.Accepted != 0 {
		t.Errorf("match cap: %+v", res)
	}
	if mm.ActiveMatchCount() != 1 {
		t.Errorf("active=%d", mm.ActiveMatchCount())
	}
	mm.EndMatch("M1")
	if res := mm.HandleFrames("M2", []model.PlayerTelemetryFrame{goodFrame("A", 0)}); res.Accepted != 1 {
		t.Errorf("after EndMatch the slot should free up: %+v", res)
	}
}

// TestMatchManager_ControlMessages covers match_start metadata and match_end
// finalization (contract 3 / F41).
func TestMatchManager_ControlMessages(t *testing.T) {
	mm, store, _ := newTestManager(t)
	ctx := context.Background()
	mm.HandleControl(model.ControlMessage{Type: model.ControlMatchStart, MatchID: "M1", ServerID: "eu-2",
		GameMode: "Echo_Arena", Map: "mpl_arena_a", IsPrivate: true,
		Teams: map[string]string{"P1": "Blue", "P2": "ORANGE", "S1": "spectators"}})
	mc, err := store.GetMatchContext(ctx, "M1")
	if err != nil {
		t.Fatalf("match_start must persist context: %v", err)
	}
	if mc.Map != "mpl_arena_a" || !mc.IsPrivate || mc.TeamAssignments["P1"] != "blue" || mc.TeamAssignments["P2"] != "orange" {
		t.Errorf("context from match_start: %+v", mc)
	}
	if _, ok := mc.TeamAssignments["S1"]; ok {
		t.Error("spectator must not get a team assignment")
	}
	mm.HandleFrames("M1", []model.PlayerTelemetryFrame{goodFrame("P1", 0), goodFrame("P2", 0)})
	mm.HandleControl(model.ControlMessage{Type: model.ControlMatchEnd, MatchID: "M1", Reason: "poller_stopped"})
	if mm.ActiveMatchCount() != 0 {
		t.Error("match_end should finalize the match")
	}
	if n := countRows(t, store, `SELECT COUNT(*) FROM match_summaries WHERE match_id = ?`, "M1"); n != 1 {
		t.Errorf("summary rows=%d", n)
	}
	// Frames after match_end start a fresh match rather than being dropped.
	if res := mm.HandleFrames("M1", []model.PlayerTelemetryFrame{goodFrame("P1", 1)}); res.Accepted != 1 {
		t.Errorf("post-end frames: %+v", res)
	}
}

// TestMatchManager_StaleCleanupAndClosePersist covers F3/F54/F88: idle reaping
// and shutdown finalize instead of discarding.
func TestMatchManager_StaleCleanupAndClosePersist(t *testing.T) {
	mm, store, _ := newTestManager(t)
	mm.HandleFrames("STALE", []model.PlayerTelemetryFrame{goodFrame("P1", 0)})
	mm.HandleFrames("LIVE", []model.PlayerTelemetryFrame{goodFrame("P1", 0)})
	mm.matches["STALE"].LastActivity = time.Now().Add(-time.Hour)

	mm.CleanupStaleMatches(30 * time.Minute)
	if mm.ActiveMatchCount() != 1 {
		t.Fatalf("active=%d after cleanup", mm.ActiveMatchCount())
	}
	if n := countRows(t, store, `SELECT COUNT(*) FROM match_summaries WHERE match_id = ?`, "STALE"); n != 1 {
		t.Errorf("stale match not summarized: rows=%d", n)
	}
	mm.Close()
	if mm.ActiveMatchCount() != 0 {
		t.Error("Close must finalize every match")
	}
	if n := countRows(t, store, `SELECT COUNT(*) FROM match_summaries`); n != 2 {
		t.Errorf("summaries=%d want 2", n)
	}
}

// TestMatchManager_PhysicsFromConfig covers the live half of F42: a live
// match context carries the [physics] block (config.PhysicsConfig.Constants).
func TestMatchManager_PhysicsFromConfig(t *testing.T) {
	mm, _, _ := newTestManager(t)
	mm.cfg.Physics.MaxPlayerSpeed = 42
	mm.cfg.Physics.DiscSpeedCap = 0 // unset -> default
	mm.HandleFrames("M1", []model.PlayerTelemetryFrame{goodFrame("P1", 0)})
	mc := mm.GetMatchContext("M1")
	if mc == nil {
		t.Fatal("match not created")
	}
	ph := mc.Physics
	if ph.MaxPlayerSpeed != 42 || ph.DiscSpeedCap != model.DefaultPhysics().DiscSpeedCap || ph.ArenaLength != model.DefaultPhysics().ArenaLength {
		t.Errorf("physics=%+v", ph)
	}
	if ph != mm.cfg.Physics.Constants() {
		t.Errorf("live context physics %+v != cfg.Physics.Constants() %+v", ph, mm.cfg.Physics.Constants())
	}
}

// TestMatchManager_AckAccountingInvariant covers contract 3 on the handler
// side: every frame of a batch is counted exactly once, so the ack the server
// derives from FrameResult (accepted+rejected+ignored) equals the frames it
// received. A duplicate row is ignored, never also accepted.
func TestMatchManager_AckAccountingInvariant(t *testing.T) {
	mm, _, m := newTestManager(t)
	batch := []model.PlayerTelemetryFrame{goodFrame("P1", 0), goodFrame("P1", 0), goodFrame("P1", 1), goodFrame("P2", 1)}
	res := mm.HandleFrames("M1", batch)
	if res.Accepted+res.Rejected+res.Ignored != len(batch) {
		t.Fatalf("counts do not cover the batch: %+v for %d frames", res, len(batch))
	}
	if res.Accepted != 3 || res.Ignored != 1 || res.Rejected != 0 {
		t.Errorf("want accepted=3 ignored=1 rejected=0, got %+v", res)
	}
	if m.FramesIgnored.Get() != 1 {
		t.Errorf("FramesIgnored metric=%d", m.FramesIgnored.Get())
	}
	// Player cap rejections are counted too, and only once.
	mm.SetLimits(10, 2)
	res = mm.HandleFrames("M1", []model.PlayerTelemetryFrame{goodFrame("P1", 2), goodFrame("P3", 2)})
	if res.Accepted != 1 || res.Rejected != 1 || res.Ignored != 0 {
		t.Errorf("cap batch: %+v", res)
	}
}

// TestMatchManager_ReviewCaseAtMatchEnd: the live path creates single-match
// review cases through the same mechanism as the offline paths
// (review.CreateCasesFromResult with the scorer's table) when the match ends,
// under the deterministic RC-<match>-<player> id, and reports them through
// OnReviewCase and the summary's flagged list.
func TestMatchManager_ReviewCaseAtMatchEnd(t *testing.T) {
	mm, store, _ := newTestManager(t)
	ctx := context.Background()
	var reported []model.ReviewCase
	mm.OnReviewCase = func(rc model.ReviewCase) { reported = append(reported, rc) }

	for i := 0; i < 120; i++ {
		mm.HandleFrames("M1", []model.PlayerTelemetryFrame{speedHack("P1", i), goodFrame("P2", i)})
	}
	live := mm.matches["M1"]
	score := live.Scorer.GetScore("P1")
	levels := live.Scorer.Levels()
	if !levels.LevelFor(score.TotalScore).AtLeast(model.LevelHighRisk) {
		t.Fatalf("setup: P1 should reach the review tier, score=%.1f table=%+v", score.TotalScore, levels)
	}
	mm.EndMatch("M1")

	rc, err := store.GetReviewCase(ctx, review.CaseID("M1", "P1"))
	if err != nil {
		t.Fatalf("review case not created at match end: %v", err)
	}
	if rc.PlayerID != "P1" || rc.MatchID != "M1" || rc.Status != model.CaseStatusPending || len(rc.DetectorsTriggered) == 0 {
		t.Errorf("case = %+v", rc)
	}
	if rc.DetectorsTriggered[0].DetectorName == "" || rc.DetectorsTriggered[0].DetectorName == "MOV_001" {
		t.Errorf("detector name not supplied to the case: %+v", rc.DetectorsTriggered[0])
	}
	if _, err := store.GetReviewCase(ctx, review.CaseID("M1", "P2")); err == nil {
		t.Error("clean player must not get a case")
	}
	if len(reported) != 1 || reported[0].CaseID != rc.CaseID {
		t.Errorf("OnReviewCase reported %+v", reported)
	}
	var flagged string
	if err := store.DB().QueryRow(`SELECT flagged_players FROM match_summaries WHERE match_id = ?`, "M1").Scan(&flagged); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flagged, "P1") {
		t.Errorf("summary flagged_players=%q", flagged)
	}
}
