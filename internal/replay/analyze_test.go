package replay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
	"github.com/nevr-anticheat/nevr-anticheat/internal/testutil"
)

const syntheticReplay = "../../tests/fixtures/synthetic_session.echoreplay"

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.General.LogLevel = "error"
	walking := cfg.Detectors["MOV_006"]
	walking.Enabled = false // legacy fixture declares zero raw velocity while moving poses
	cfg.Detectors["MOV_006"] = walking
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
	// Without Force the stored match is refused at its first tick and
	// nothing of it is kept; the file is still read through (a later
	// session would be analyzed), so its corrupt line is reported.
	if _, err := e.AnalyzeFile(ctx, corrupt, false); err == nil || !strings.Contains(err.Error(), "reading echoreplay") {
		t.Errorf("non-forced analyze of a stored, corrupt replay: err = %v, want the parse error", err)
	}
	if after := countDerived(t, store, matchID); after != before {
		t.Errorf("non-forced analyze changed the previous analysis: %+v -> %+v", before, after)
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

// twoSessionReplay writes the synthetic fixture as a two-match recording:
// its second 60 lines carry session id second and start ten minutes later.
func twoSessionReplay(t *testing.T, second string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rematch.echoreplay")
	if _, _, err := testutil.SplitReplaySessions(syntheticReplay, path, second, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	return path
}

type storedTicks struct{ ticks, frames, minIndex, maxIndex int }

// tickRows reports what match_ticks and telemetry_frames hold for a match.
func tickRows(t *testing.T, store *sqlite.Store, matchID string) storedTicks {
	t.Helper()
	var s storedTicks
	if err := store.DB().QueryRow(`SELECT COUNT(*), COALESCE(MIN(frame_index), -1), COALESCE(MAX(frame_index), -1) FROM match_ticks WHERE match_id = ?`, matchID).
		Scan(&s.ticks, &s.minIndex, &s.maxIndex); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM telemetry_frames WHERE match_id = ?`, matchID).Scan(&s.frames); err != nil {
		t.Fatal(err)
	}
	return s
}

// Contract H: a recording whose session id changes mid-file holds two
// matches. Each is analyzed and stored under its own id (context, frames,
// raw ticks from frame index 0) instead of the second being appended to the
// first and dropped as duplicates of its ticks.
func TestAnalyzeFileAll_TwoSessions(t *testing.T) {
	e := newTestEngine(t)
	store := e.Store()
	ctx := context.Background()
	path := twoSessionReplay(t, "SYN-FIXTURE-002")

	results, err := e.AnalyzeFileAll(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want one per session", len(results))
	}
	wantStart := []time.Time{
		time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 1, 12, 10, 4, 20_000_000, time.UTC), // second half's first sample, shifted
	}
	total := 0
	for i, res := range results {
		id := fmt.Sprintf("SYN-FIXTURE-00%d", i+1)
		if res.AlreadyStored || res.Replaced || res.Result == nil || res.MatchCtx == nil ||
			res.MatchCtx.MatchID != id || res.Result.MatchID != id || res.Path != path {
			t.Fatalf("result %d: %+v", i, res)
		}
		if res.Frames != 240 || res.Result.FramesProcessed != 60 || res.Result.InvalidFrames != 0 {
			t.Errorf("%s: frames parsed=%d processed=%d invalid=%d", id, res.Frames, res.Result.FramesProcessed, res.Result.InvalidFrames)
		}
		if !res.RawStored || res.Telemetry != (sqlite.TelemetryStoreResult{Inserted: 240, TicksInserted: 60}) {
			t.Errorf("%s: telemetry %+v raw=%v (a second match's ticks must not collide with the first's)", id, res.Telemetry, res.RawStored)
		}
		if res.Diagnostics == nil || res.Diagnostics.SessionChanges != 1 || res.Diagnostics.FramesRejected != 0 {
			t.Errorf("%s: diagnostics %+v", id, res.Diagnostics)
		}
		if !res.MatchCtx.StartTime.Equal(wantStart[i]) || res.MatchCtx.Duration != 3953*time.Millisecond {
			t.Errorf("%s: start %v duration %v, want %v / 3.953s (its own first to last sample)", id, res.MatchCtx.StartTime, res.MatchCtx.Duration, wantStart[i])
		}
		if len(res.Summary.FramesByPlayer) != 4 || res.Summary.FramesByPlayer["echovr:1001"] != 60 ||
			res.Summary.FirstTimestamp != 0 || res.Summary.LastTimestamp <= 0 {
			t.Errorf("%s: summary %+v", id, res.Summary)
		}
		if perr := res.PersistError(); perr != nil || len(res.Warnings()) != 0 {
			t.Errorf("%s: persist %v warnings %v", id, perr, res.Warnings())
		}
		total += res.Telemetry.Inserted

		mc, err := store.GetMatchContext(ctx, id)
		if err != nil {
			t.Fatalf("%s: context not stored: %v", id, err)
		}
		if mc.MatchID != id || len(mc.PlayerIDs) != 4 || !mc.StartTime.Equal(wantStart[i]) || mc.ReplayFile != "rematch.echoreplay" {
			t.Errorf("%s: stored context %+v", id, mc)
		}
		if rows := tickRows(t, store, id); rows != (storedTicks{ticks: 60, frames: 240, minIndex: 0, maxIndex: 59}) {
			t.Errorf("%s: stored rows %+v, want 60 ticks at frame index 0..59 and 240 frames", id, rows)
		}
	}
	if total != 480 {
		t.Errorf("telemetry stored across both matches = %d, want the whole file (480)", total)
	}
	if results[0].MatchCtx == results[1].MatchCtx {
		t.Error("both matches share one context")
	}

	// Re-run without force: both refused, nothing written.
	results, err = e.AnalyzeFileAll(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("second run: %d results", len(results))
	}
	for i, res := range results {
		id := fmt.Sprintf("SYN-FIXTURE-00%d", i+1)
		if !res.AlreadyStored || res.Result != nil || res.RawStored || res.MatchCtx.MatchID != id || res.Diagnostics == nil {
			t.Errorf("second run %d: %+v", i, res)
		}
		if rows := tickRows(t, store, id); rows.ticks != 60 || rows.frames != 240 {
			t.Errorf("second run wrote rows for %s: %+v", id, rows)
		}
	}

	// With force: both replaced, their source data already present.
	results, err = e.AnalyzeFileAll(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("forced run: %d results", len(results))
	}
	for i, res := range results {
		if res.AlreadyStored || !res.Replaced || res.Telemetry != (sqlite.TelemetryStoreResult{Ignored: 240, TicksIgnored: 60}) {
			t.Errorf("forced run %d: replaced=%v telemetry=%+v", i, res.Replaced, res.Telemetry)
		}
	}
}

// The stored check is per match: with the first match already in the store
// and no force, it is refused and the second is still analyzed; force then
// replaces the first and re-analyzes the second.
func TestAnalyzeFileAll_StoredCheckPerMatch(t *testing.T) {
	e := newTestEngine(t)
	store := e.Store()
	ctx := context.Background()
	if _, err := e.AnalyzeFile(ctx, syntheticReplay, false); err != nil {
		t.Fatal(err)
	}
	path := twoSessionReplay(t, "SYN-FIXTURE-002")

	results, err := e.AnalyzeFileAll(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d", len(results))
	}
	if r := results[0]; !r.AlreadyStored || r.Result != nil || r.MatchCtx.MatchID != "SYN-FIXTURE-001" {
		t.Errorf("stored first match: %+v", r)
	}
	if r := results[1]; r.AlreadyStored || r.Replaced || r.Result == nil || r.MatchCtx.MatchID != "SYN-FIXTURE-002" ||
		r.Telemetry != (sqlite.TelemetryStoreResult{Inserted: 240, TicksInserted: 60}) {
		t.Errorf("second match after a stored first: %+v", r)
	}
	if rows := tickRows(t, store, "SYN-FIXTURE-001"); rows.ticks != 120 || rows.frames != 480 {
		t.Errorf("refused match was written to: %+v", rows)
	}
	if exists, err := store.HasMatch(ctx, "SYN-FIXTURE-002"); err != nil || !exists {
		t.Errorf("second match not stored: %v %v", exists, err)
	}

	results, err = e.AnalyzeFileAll(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || !results[0].Replaced || !results[1].Replaced ||
		results[0].Telemetry != (sqlite.TelemetryStoreResult{Ignored: 240, TicksIgnored: 60}) ||
		results[1].Telemetry != (sqlite.TelemetryStoreResult{Ignored: 240, TicksIgnored: 60}) {
		t.Errorf("forced run: %+v", results)
	}
}

// AnalyzeFile keeps its single-match contract over a two-match recording:
// the first match's result, the second (analyzed and stored all the same)
// in AdditionalMatches; a stored first match is ErrMatchAlreadyStored.
func TestAnalyzeFile_TwoSessionsAdditionalMatches(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	path := twoSessionReplay(t, "SYN-FIXTURE-002")

	res, err := e.AnalyzeFile(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.MatchCtx.MatchID != "SYN-FIXTURE-001" || len(res.AdditionalMatches) != 1 ||
		res.AdditionalMatches[0].MatchCtx.MatchID != "SYN-FIXTURE-002" || res.AdditionalMatches[0].Result == nil {
		t.Errorf("result %+v additional %+v", res, res.AdditionalMatches)
	}
	if exists, err := e.Store().HasMatch(ctx, "SYN-FIXTURE-002"); err != nil || !exists {
		t.Errorf("second match not stored: %v %v", exists, err)
	}
	// A single-session file carries no AdditionalMatches.
	single, err := e.AnalyzeFile(ctx, syntheticReplay, true)
	if err != nil || single.AdditionalMatches != nil {
		t.Errorf("single-session: %+v, %v", single, err)
	}
	_, err = e.AnalyzeFile(ctx, path, false)
	var stored *MatchStoredError
	if !errors.Is(err, ErrMatchAlreadyStored) || !errors.As(err, &stored) || stored.MatchID != "SYN-FIXTURE-001" {
		t.Errorf("stored first match: %v", err)
	}
}

// A hard failure part-way through the file (here a corrupt line inside the
// second session) is returned together with the matches finished before
// it: the first match is analyzed and stored, the second is not.
func TestAnalyzeFileAll_ErrorKeepsFinishedMatches(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	path := twoSessionReplay(t, "SYN-FIXTURE-002")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("2026/03/01 12:10:09.000\t" + strings.Repeat("x", 9*1024*1024) + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	results, err := e.AnalyzeFileAll(ctx, path, false)
	if err == nil || !strings.Contains(err.Error(), "reading echoreplay") {
		t.Fatalf("corrupt second session: err = %v, want the parse error", err)
	}
	if len(results) != 1 || results[0].MatchCtx.MatchID != "SYN-FIXTURE-001" || results[0].Result == nil || results[0].Diagnostics == nil {
		t.Fatalf("results before the failure = %+v", results)
	}
	if exists, err := e.Store().HasMatch(ctx, "SYN-FIXTURE-001"); err != nil || !exists {
		t.Errorf("first match not stored: %v %v", exists, err)
	}
	if exists, err := e.Store().HasMatchContext(ctx, "SYN-FIXTURE-002"); err != nil || exists {
		t.Errorf("unfinished second match has a context: %v %v", exists, err)
	}
	// The single-match wrapper reports the failure only.
	if res, err := e.AnalyzeFile(ctx, path, true); err == nil || res != nil {
		t.Errorf("AnalyzeFile over the corrupt file: %+v, %v", res, err)
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
