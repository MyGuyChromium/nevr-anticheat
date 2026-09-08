package tests

import (
	"context"
	"io"
	"log/slog"
	"math"
	"reflect"
	"sort"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/catalog"
	"github.com/nevr-anticheat/nevr-anticheat/internal/ingest"
	"github.com/nevr-anticheat/nevr-anticheat/internal/metrics"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// equivalenceMatch is the telemetry every equivalence test uses: a 4v4
// with one cheater who teleports, throws at 35 m/s and speed hacks.
func equivalenceMatch(matchID string) (*model.MatchContext, []model.PlayerTelemetryFrame) {
	return testutil.TwoByFour(matchID, 420, 1, func(fb *testutil.FrameBuilder) []model.PlayerTelemetryFrame {
		tele := fb.TeleportCheat(210, 30, 10.0)
		aim := fb.After(tele).AimbotThrows(3)
		return testutil.Concat(tele, aim, fb.After(aim).SpeedHackFrames(90, 75))
	})
}

// enforceConfig is DefaultConfig with every enabled detector in enforce
// mode (so scores are produced) and the shipped level table.
func enforceConfig() *config.Config {
	cfg := config.DefaultConfig()
	for id, dc := range cfg.Detectors {
		dc.Mode = "enforce"
		cfg.Detectors[id] = dc
	}
	return cfg
}

func sortedSignatures(events []model.DetectionEvent) []eventSignature {
	sigs := signatures(events)
	sort.Slice(sigs, func(i, j int) bool {
		a, b := sigs[i], sigs[j]
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.Player != b.Player {
			return a.Player < b.Player
		}
		if a.Detector != b.Detector {
			return a.Detector < b.Detector
		}
		return a.Frame < b.Frame
	})
	return sigs
}

// TestEquivalence_ReprocessFromStore (E): StoreTelemetryFrames ->
// GetMatchFrames -> ProcessMatch must equal direct processing: the stored
// JSON round trip loses nothing the detectors consume.
func TestEquivalence_ReprocessFromStore(t *testing.T) {
	mc, frames := equivalenceMatch("match-reprocess")
	h := testutil.NewHarness(t).WithEnabledDetectors().WithMatchContext(mc)

	p1, _ := h.NewPipeline()
	direct, err := p1.ProcessMatch(context.Background(), mc, frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(direct.DetectionEvents) == 0 {
		t.Fatal("setup: no events on the direct path")
	}
	if direct.InvalidFrames != 0 {
		t.Fatalf("setup: %d invalid frames (%v)", direct.InvalidFrames, direct.InvalidFrameReasons)
	}

	store := newTestStore(t)
	ctx := context.Background()
	n, err := store.StoreTelemetryFrames(ctx, mc.MatchID, frames)
	if err != nil || n != len(frames) {
		t.Fatalf("stored %d of %d frames: %v", n, len(frames), err)
	}
	loaded, err := store.GetMatchFrames(ctx, mc.MatchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(frames) {
		t.Fatalf("loaded %d frames, stored %d", len(loaded), len(frames))
	}
	p2, _ := h.NewPipeline()
	reprocessed, err := p2.ProcessMatch(ctx, mc, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if reprocessed.FramesProcessed != direct.FramesProcessed || reprocessed.InvalidFrames != direct.InvalidFrames {
		t.Fatalf("frames processed %d/%d vs %d/%d", reprocessed.FramesProcessed, reprocessed.InvalidFrames, direct.FramesProcessed, direct.InvalidFrames)
	}
	if a, b := sortedSignatures(direct.DetectionEvents), sortedSignatures(reprocessed.DetectionEvents); !reflect.DeepEqual(a, b) {
		t.Fatalf("event lists differ after the store round trip:\ndirect      %v\nreprocessed %v", a, b)
	}
	for pid, sc := range direct.PlayerScores {
		if got := reprocessed.PlayerScores[pid].TotalScore; math.Abs(got-sc.TotalScore) > 1e-9 {
			t.Errorf("%s: score %.4f direct vs %.4f reprocessed", pid, sc.TotalScore, got)
		}
	}
}

// liveRun pushes frames through ingest.MatchManager in the given batches,
// ends the match and returns the persisted events, the final persisted
// scores and the number of frames the manager re-based.
func liveRun(t *testing.T, cfg *config.Config, matchID string, batches [][]model.PlayerTelemetryFrame) ([]model.DetectionEvent, map[string]float64, int64) {
	t.Helper()
	store := newTestStore(t)
	if err := sqlite.RunMigrationsV2(store.DB(), quietLogger()); err != nil {
		t.Fatal(err)
	}
	factory := func() []detect.Detector { return catalog.Build(cfg, nil) }
	mm := ingest.NewMatchManager(cfg, store, factory, quietLogger())
	m := metrics.NewMetrics()
	mm.SetMetrics(m)
	for i, batch := range batches {
		res := mm.HandleFrames(matchID, "", batch)
		if res.Accepted != len(batch) || res.Rejected != 0 || res.Ignored != 0 {
			for _, frame := range batch {
				t.Logf("batch frame player=%s index=%d timestamp=%.17g", frame.PlayerID, frame.FrameIndex, frame.Timestamp)
			}
			t.Fatalf("batch %d: %+v", i, res)
		}
	}
	mm.EndMatch(matchID)
	ctx := context.Background()
	stored, err := store.GetMatchEvents(ctx, matchID)
	if err != nil {
		t.Fatal(err)
	}
	scores := map[string]float64{}
	for _, ev := range stored {
		if _, ok := scores[ev.PlayerID]; ok {
			continue
		}
		final, err := store.GetPlayerScore(ctx, ev.PlayerID)
		if err != nil {
			t.Fatalf("%s: no final score persisted: %v", ev.PlayerID, err)
		}
		scores[ev.PlayerID] = final.TotalScore
	}
	return stored, scores, m.FramesRebased.Get()
}

// offlineRun is the reference: one ProcessMatch over the whole match with
// the live path's match-context shape (physics from config, 15 Hz).
func offlineRun(t *testing.T, cfg *config.Config, mc *model.MatchContext, frames []model.PlayerTelemetryFrame) ([]model.DetectionEvent, map[string]float64) {
	t.Helper()
	ctxCopy := *mc
	ctxCopy.Physics = cfg.Physics.Constants()
	h := testutil.NewHarness(t).WithEnabledDetectors().WithMatchContext(&ctxCopy)
	p, _ := h.NewPipeline()
	res, err := p.ProcessMatch(context.Background(), &ctxCopy, frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DetectionEvents) == 0 {
		t.Fatal("setup: no events on the offline path")
	}
	scores := map[string]float64{}
	for pid, sc := range res.PlayerScores {
		if sc.EventCount > 0 {
			scores[pid] = sc.TotalScore
		}
	}
	return res.DetectionEvents, scores
}

func assertSameEvents(t *testing.T, offline, live []model.DetectionEvent, offScores, liveScores map[string]float64) {
	t.Helper()
	a, b := sortedSignatures(offline), sortedSignatures(live)
	// Stored events do not carry MergedCount; compare without it.
	for i := range a {
		a[i].Merged = 0
	}
	for i := range b {
		b[i].Merged = 0
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("live events differ from offline:\noffline %v\nlive    %v", a, b)
	}
	if len(offScores) != len(liveScores) {
		t.Fatalf("scored players: offline %v, live %v", offScores, liveScores)
	}
	for pid, want := range offScores {
		if got := liveScores[pid]; math.Abs(got-want) > 1e-9 {
			t.Errorf("%s: live final score %.4f vs offline %.4f", pid, got, want)
		}
	}
}

// TestEquivalence_LiveSinglePlayerOneFrameBatches (E): a single player
// delivered one frame per batch (what cmd/bridge sends for a solo session)
// must yield the same events and final score as the offline path.
func TestEquivalence_LiveSinglePlayerOneFrameBatches(t *testing.T) {
	cfg := enforceConfig()
	frames := player1().CompositeCheater()
	for i := range frames {
		frames[i].Team = "blue"
	}
	mc := matchContextForPlayer("player1")
	mc.MatchID = "match-live-solo"
	offEvents, offScores := offlineRun(t, cfg, mc, frames)

	batches := make([][]model.PlayerTelemetryFrame, len(frames))
	for i := range frames {
		batches[i] = []model.PlayerTelemetryFrame{frames[i]}
	}
	liveEvents, liveScores, rebased := liveRun(t, cfg, mc.MatchID, batches)
	if rebased != 0 {
		t.Fatalf("%d frames re-based on a monotonic stream", rebased)
	}
	assertSameEvents(t, offEvents, liveEvents, offScores, liveScores)
}

// TestEquivalence_LiveOneTickBatches (E): a 4v4 delivered one tick (all
// players' frames of one index) per batch, as one /session poll produces,
// must match the offline path.
func TestEquivalence_LiveOneTickBatches(t *testing.T) {
	cfg := enforceConfig()
	mc, frames := equivalenceMatch("match-live-ticks")
	offEvents, offScores := offlineRun(t, cfg, mc, frames)

	var batches [][]model.PlayerTelemetryFrame
	for i := 0; i < len(frames); {
		j := i
		for j < len(frames) && frames[j].FrameIndex == frames[i].FrameIndex {
			j++
		}
		batches = append(batches, frames[i:j])
		i = j
	}
	liveEvents, liveScores, rebased := liveRun(t, cfg, mc.MatchID, batches)
	if rebased != 0 {
		t.Fatalf("%d frames re-based on a monotonic stream", rebased)
	}
	assertSameEvents(t, offEvents, liveEvents, offScores, liveScores)
}

// TestEquivalence_LivePerPlayerFrameBatches is the regression for the
// multi-player one-frame-per-batch defect: MatchManager.HandleFrames used
// to re-base any batch whose lowest frame index was <= the last index seen,
// so the second player's frame of the SAME tick was treated as a producer
// restart and pushed to a new index (every player on its own index stream,
// per-player frame gaps of 8 > MOV_002's max_frame_gap, live events
// diverging from offline). Re-basing is now per (match, player): a batch
// that merely repeats the match's last index for another player is not a
// restart (contract F).
func TestEquivalence_LivePerPlayerFrameBatches(t *testing.T) {
	cfg := enforceConfig()
	mc, frames := equivalenceMatch("match-live-perplayer")
	offEvents, offScores := offlineRun(t, cfg, mc, frames)
	batches := make([][]model.PlayerTelemetryFrame, len(frames))
	for i := range frames {
		batches[i] = []model.PlayerTelemetryFrame{frames[i]}
	}
	liveEvents, liveScores, rebased := liveRun(t, cfg, mc.MatchID, batches)
	if rebased != 0 {
		t.Fatalf("%d frames re-based although every batch's index is >= the last seen tick", rebased)
	}
	assertSameEvents(t, offEvents, liveEvents, offScores, liveScores)
}
