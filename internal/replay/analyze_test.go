package replay

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

const syntheticReplay = "../../tests/fixtures/synthetic_session.echoreplay"

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.General.LogLevel = "error"
	store, err := sqlite.NewStore(filepath.Join(t.TempDir(), "engine.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return NewEngine(cfg, store)
}

// TestEngine_ConfigWiring: the engine derives scorer, levels, physics and
// analysis options from one config so every caller classifies alike.
func TestEngine_ConfigWiring(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Scoring.ReviewThreshold = 45
	store, err := sqlite.NewStore(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := NewEngine(cfg, store)
	if e.Levels().HighRisk != 45 || e.ScorerConfig().Levels.HighRisk != 45 ||
		e.AnalysisOptions().Levels.HighRisk != 45 || e.CrossMatchConfig().Levels.HighRisk != 45 {
		t.Errorf("review threshold did not reach every level table: %+v", e.Levels())
	}
	if e.Physics() != cfg.Physics.Constants() {
		t.Errorf("physics = %+v", e.Physics())
	}
	if e.NewPipeline() == nil || e.Store() != store || e.Config() != cfg || e.Logger() == nil {
		t.Error("engine accessors")
	}
	if len(e.AnalysisOptions().DetectorNames) == 0 {
		t.Error("analysis options carry no detector names")
	}
}

// TestAnalyzeFile_SyntheticReplay: parse, detect and store the committed
// synthetic .echoreplay; refuse a re-run without force; replace with force.
func TestAnalyzeFile_SyntheticReplay(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	res, err := e.AnalyzeFile(ctx, syntheticReplay, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.MatchCtx.MatchID != "SYN-FIXTURE-001" || res.Result.MatchID != "SYN-FIXTURE-001" {
		t.Errorf("match id %q / %q", res.MatchCtx.MatchID, res.Result.MatchID)
	}
	if res.Frames != 480 || res.Result.FramesProcessed != 120 || res.Result.InvalidFrames != 0 {
		t.Errorf("frames parsed=%d processed=%d invalid=%d", res.Frames, res.Result.FramesProcessed, res.Result.InvalidFrames)
	}
	if res.Diagnostics == nil || res.Diagnostics.FramesRejected != 0 {
		t.Errorf("diagnostics %+v", res.Diagnostics)
	}
	if !res.RawStored || res.Telemetry.Inserted != 480 || res.Telemetry.TicksInserted != 120 {
		t.Errorf("telemetry %+v raw=%v", res.Telemetry, res.RawStored)
	}
	if res.Replaced || len(res.Warnings()) != 0 {
		t.Errorf("replaced=%v warnings=%v", res.Replaced, res.Warnings())
	}
	if len(res.Summary.FramesByPlayer) != 4 || res.Summary.FramesByPlayer["echovr:1001"] != 120 {
		t.Errorf("frames by player %v", res.Summary.FramesByPlayer)
	}
	if res.Summary.LastTimestamp <= res.Summary.FirstTimestamp {
		t.Errorf("timestamps %v..%v", res.Summary.FirstTimestamp, res.Summary.LastTimestamp)
	}
	if res.MatchCtx.PlayerNames["echovr:1001"] != "BlueOne" || res.MatchCtx.Duration <= 0 {
		t.Errorf("context names=%v duration=%v", res.MatchCtx.PlayerNames, res.MatchCtx.Duration)
	}
	if exists, err := e.Store().HasMatch(ctx, "SYN-FIXTURE-001"); err != nil || !exists {
		t.Errorf("match not stored: %v %v", exists, err)
	}

	// Already stored: refused before anything is written.
	_, err = e.AnalyzeFile(ctx, syntheticReplay, false)
	var stored *MatchStoredError
	if !errors.Is(err, ErrMatchAlreadyStored) || !errors.As(err, &stored) || stored.MatchID != "SYN-FIXTURE-001" {
		t.Fatalf("second run: %v", err)
	}

	// Force: previous analysis cleared, telemetry already present.
	res, err = e.AnalyzeFile(ctx, syntheticReplay, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Replaced || res.Telemetry.Ignored != 480 || res.Telemetry.TicksIgnored != 120 {
		t.Errorf("forced run: replaced=%v telemetry=%+v", res.Replaced, res.Telemetry)
	}
}

func TestAnalyzeFile_Errors(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	if _, err := e.AnalyzeFile(ctx, filepath.Join(t.TempDir(), "missing.echoreplay"), false); err == nil {
		t.Error("missing file accepted")
	}
	if _, err := AnalyzeFile(ctx, e.Store(), syntheticReplay, AnalyzeOptions{}); err == nil {
		t.Error("nil NewPipeline accepted")
	}
	if !IsEchoReplay("a.ECHOREPLAY") || IsEchoReplay("a.json") {
		t.Error("IsEchoReplay")
	}
}
