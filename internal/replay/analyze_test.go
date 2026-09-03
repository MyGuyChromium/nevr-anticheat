package replay

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
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
	if res.Replaced || len(res.Warnings()) != 0 || res.PersistError() != nil {
		t.Errorf("replaced=%v warnings=%v persist=%v", res.Replaced, res.Warnings(), res.PersistError())
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

// writeCorruptCopy writes the first n lines of the synthetic fixture followed
// by a line that exceeds the parser's line limit, so the replay parses its
// first ticks (match id known, the stored match check has run) and then fails.
func writeCorruptCopy(t *testing.T, n int) string {
	t.Helper()
	src, err := os.Open(syntheticReplay)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(t.TempDir(), "corrupt.echoreplay")
	dst, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	w := bufio.NewWriter(dst)
	for i := 0; i < n && sc.Scan(); i++ {
		w.WriteString(sc.Text())
		w.WriteString("\n")
	}
	w.WriteString("2026/03/01 12:00:09.000\t")
	w.WriteString(strings.Repeat("x", 9*1024*1024))
	w.WriteString("\n")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return path
}

type derivedCounts struct{ events, scores, cases, pending int }

func countDerived(t *testing.T, store *sqlite.Store, matchID string) derivedCounts {
	t.Helper()
	var c derivedCounts
	for _, q := range []struct {
		sql string
		dst *int
	}{
		{`SELECT COUNT(*) FROM detection_events WHERE match_id = ?`, &c.events},
		{`SELECT COUNT(*) FROM suspicion_scores WHERE match_id = ?`, &c.scores},
		{`SELECT COUNT(*) FROM review_cases WHERE match_id = ?`, &c.cases},
		{`SELECT COUNT(*) FROM review_cases WHERE match_id = ? AND status = 'pending'`, &c.pending},
	} {
		if err := store.DB().QueryRow(q.sql, matchID).Scan(q.dst); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// TestAnalyzeFile_ForceClearsOnlyAfterSuccess: with Force the stored analysis
// is replaced only once the replacement parsed completely, was analyzed and
// had its source data stored. A corrupt replay and an unwritable store both
// leave the previous events, score snapshots and cases exactly as they were;
// a good replay then replaces them exactly once and closes (never deletes)
// the pending case its analysis no longer justifies.
func TestAnalyzeFile_ForceClearsOnlyAfterSuccess(t *testing.T) {
	e := newTestEngine(t)
	store := e.Store()
	ctx := context.Background()
	const matchID = "SYN-FIXTURE-001"
	if _, err := e.AnalyzeFile(ctx, syntheticReplay, false); err != nil {
		t.Fatal(err)
	}
	// Stand in for a previous run whose configuration produced a detection
	// (the fixture produces none under the defaults).
	mc, err := store.GetMatchContext(ctx, matchID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StoreMatchAnalysis(ctx, store, mc, matchResult(matchID, map[string]float64{"echovr:1001": 70}),
		"initial", AnalysisOptions{Logger: quietLogger()}); err != nil {
		t.Fatal(err)
	}
	before := countDerived(t, store, matchID)
	if before != (derivedCounts{1, 1, 1, 1}) {
		t.Fatalf("setup: %+v", before)
	}

	// Corrupt replay: the parse fails after the match id is known.
	corrupt := writeCorruptCopy(t, 30)
	if _, err := e.AnalyzeFile(ctx, corrupt, true); err == nil || !strings.Contains(err.Error(), "reading echoreplay") {
		t.Fatalf("corrupt forced analyze: err = %v, want a parse error", err)
	}
	if after := countDerived(t, store, matchID); after != before {
		t.Errorf("failed Force destroyed the previous analysis: %+v -> %+v", before, after)
	}
	if _, err := e.AnalyzeFile(ctx, corrupt, false); !errors.Is(err, ErrMatchAlreadyStored) {
		t.Errorf("non-forced analyze of a stored match: %v", err)
	}

	// Unwritable store: the replay parses and is analyzed, but its source
	// data cannot be stored, so the previous analysis is kept and the
	// failure is reported on the result.
	if _, err := store.DB().Exec("PRAGMA query_only = 1"); err != nil {
		t.Fatal(err)
	}
	res, err := e.AnalyzeFile(ctx, syntheticReplay, true)
	if err != nil {
		t.Fatalf("read-only forced analyze returned a hard error: %v", err)
	}
	if res.Replaced || res.TelemetryErr == nil || res.PersistError() == nil ||
		res.AnalysisErr == nil || !strings.Contains(res.AnalysisErr.Error(), "previous analysis kept") {
		t.Errorf("read-only forced analyze: replaced=%v telemetry=%v analysis=%v", res.Replaced, res.TelemetryErr, res.AnalysisErr)
	}
	if after := countDerived(t, store, matchID); after != before {
		t.Errorf("read-only Force destroyed the previous analysis: %+v -> %+v", before, after)
	}
	if _, err := store.DB().Exec("PRAGMA query_only = 0"); err != nil {
		t.Fatal(err)
	}

	// Good replay: replaced exactly once; the seeded pending case, whose
	// player the real analysis does not flag, is closed with a reason.
	res, err = e.AnalyzeFile(ctx, syntheticReplay, true)
	if err != nil {
		t.Fatal(err)
	}
	if perr := res.PersistError(); perr != nil {
		t.Fatal(perr)
	}
	if !res.Replaced || res.ClearedEvents != 1 || res.ClearedScores != 1 || res.Stored.CasesClosed != 1 {
		t.Errorf("forced analyze: replaced=%v cleared=%d events/%d scores, closed=%d",
			res.Replaced, res.ClearedEvents, res.ClearedScores, res.Stored.CasesClosed)
	}
	if after := countDerived(t, store, matchID); after != (derivedCounts{0, 0, 1, 0}) {
		t.Errorf("after forced re-analysis: %+v (want seeded events/scores gone, case kept but not pending)", after)
	}
	rc, err := store.GetReviewCase(ctx, "RC-"+matchID+"-echovr:1001")
	if err != nil || rc.Status != model.CaseStatusClosed || rc.CloseReason == "" {
		t.Errorf("stale case = %+v, %v", rc, err)
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
